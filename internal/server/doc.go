// Package server implements Tabucom's temporary, immutable static hosting service.
//
// # Scope
//
// The package has one intentionally narrow job:
//
//   - accept raw HTML, Markdown, or an already-built static ZIP archive;
//   - transform that input into a directory of static files;
//   - publish the directory atomically under a random deployment ID;
//   - serve those files until the deployment's recorded expiry time; and
//   - remove expired or incomplete data without executing uploaded code.
//
// It is not a build service. No request path invokes a shell, package manager,
// compiler, template engine, interpreter, or user-supplied executable. A ZIP is
// treated as a collection of bytes and paths, not as a source tree to inspect or
// build. Preserving that boundary is the most important package invariant.
//
// # Component boundaries
//
// Files are organized by responsibility so security-sensitive flows remain small:
//
//   - config.go defines supported settings, environment parsing, and validation.
//   - server.go owns lifecycle, background cleanup, routing, and storage health.
//   - publish.go validates publish requests and coordinates atomic publication.
//   - archive.go performs defensive ZIP validation and bounded extraction.
//   - site.go resolves deployment URLs, serves files, and enforces expiry.
//   - markdown.go renders a deliberately limited, escaped Markdown subset.
//   - web.go serves embedded human and agent discovery documentation.
//   - response.go owns the stable JSON success and error response conventions.
//
// Tests mirror those boundaries. server_test.go contains only shared fixtures;
// focused behavior tests live beside the responsibility they exercise. This
// layout reduces the amount of unrelated context required to review a change.
//
// # Storage model
//
// New creates this private layout beneath Config.DataDir:
//
//	sites/
//	  .staging-<random>/
//	  <deployment-uuid>/
//	    .site.json
//	    index.html
//	    <other static files>
//
// A staging directory is created beneath sites so its final rename stays on the
// same filesystem. The publish handler writes all content and metadata into that
// staging directory first. Only a successful os.Rename makes the deployment ID
// visible. Consequently, readers cannot observe a deployment with missing assets
// or metadata merely because an upload failed partway through.
//
// The deferred staging cleanup is intentionally retained after a successful
// rename. At that point the old staging path no longer exists, so cleanup is a
// harmless no-op. On every earlier return it removes partial files and directories.
//
// Published directories are immutable through the HTTP API. There is no update,
// overwrite, rename, or delete endpoint. An ID collision makes rename fail rather
// than replacing a deployment. UUID validation also prevents request paths from
// selecting arbitrary names beneath the storage root.
//
// The .site.json manifest is server-private. It records the ID, creation time,
// exact expiry, expanded file count, expanded byte count, SPA behavior, and
// optional password hash, salt, and cookie token. It is written before commit and
// explicitly blocked by static path resolution. Plaintext passwords are omitted.
//
// Deployments with missing or malformed manifests fail closed: they are not served
// and are eligible for removal during Sweep because their retention cannot be
// established reliably.
//
// # Publish request lifecycle
//
// ServeHTTP routes POST /api/v1/publish to the publication coordinator. The flow
// proceeds in the following order:
//
//  1. Account for the request in the process-local fixed rate-limit window.
//  2. Parse and allowlist the Content-Type media type.
//  3. Parse optional SPA, TTL, and password-protection settings.
//  4. Reject a known oversized Content-Length as an early optimization.
//  5. Allocate a cryptographically random version-4 UUID.
//  6. Create an unservable staging directory under sites.
//  7. Bound and stage the selected HTML, Markdown, or ZIP representation.
//  8. Require a regular index.html at the staging root.
//  9. Write immutable metadata with an absolute UTC expiry instant.
//  10. Atomically rename the complete stage to its deployment UUID.
//  11. Return the URL, expiry, protection state, and password when protected.
//
// Parsing Content-Length is not the upload security boundary. Requests may omit it
// or send a misleading value. Every body is therefore read through MaxBytesReader,
// and ZIP extraction applies a second independent expanded-size limit.
//
// Raw HTML is copied verbatim to index.html. Markdown is transformed only by the
// local safe renderer. ZIP input is expanded as static data. None of these paths
// infer a framework or inspect files for executable build instructions.
//
// The default expiry is thirty days. A positive client ttl replaces that default
// for the deployment. The persisted absolute expiry, not a process timer, is the
// source of truth across process restarts.
//
// The API response copies only public metadata fields, so the password hash,
// salt, and cookie token in the private manifest cannot be serialized publicly.
//
// # ZIP threat model
//
// ZIP headers are attacker-controlled. Archive extraction therefore assumes that
// names, modes, declared sizes, entry counts, and ordering may all be malicious.
// Validation and extraction enforce these rules before publication:
//
//   - empty names are rejected;
//   - NUL-containing names are rejected;
//   - leading-slash absolute paths are rejected;
//   - Windows drive-absolute paths are rejected;
//   - backslashes are normalized before path validation;
//   - cleaned . and .. paths are rejected;
//   - paths escaping through a leading ../ segment are rejected;
//   - path nesting deeper than the configured hard ceiling is rejected;
//   - duplicate names are detected after normalization;
//   - directories count toward the archive entry limit;
//   - symlinks and all non-directory special file modes are rejected;
//   - file-versus-directory conflicts fail closed;
//   - declared uncompressed sizes are checked before extraction;
//   - streamed bytes are checked independently of declared sizes; and
//   - index.html must be a regular file at the archive root.
//
// Normalized duplicate detection matters because distinct raw names can refer to
// the same destination. For example, index.html and assets/../index.html must not
// be allowed to race or overwrite one another. O_EXCL on extracted files supplies
// a second filesystem-level defense against destination collisions.
//
// Symlinks are never materialized from uploads. This prevents a later file entry
// from following a link outside the staging root. Devices, sockets, named pipes,
// and other special modes are likewise outside the static-file data model.
//
// Both declared and observed expanded sizes are bounded. Header checks reject
// obviously oversized entries without writing them. LimitReader then permits at
// most one byte beyond the remaining budget, making understated header sizes
// detectable without allowing unbounded expansion.
//
// Entry limits include directories because a directory-only archive can still
// consume substantial CPU, memory, and filesystem metadata. File and byte counts
// returned to clients describe regular expanded files and their contents, while
// the resource limit accounts for every archive entry processed.
//
// Structural archive failures use a stable invalid_archive client code without
// exposing host filesystem details. Resource-limit failures distinguish oversized
// content and excessive entry counts for actionable client behavior.
//
// # Markdown safety model
//
// Markdown support is intentionally smaller than CommonMark. The renderer handles
// ATX headings, paragraphs, unordered lists, fenced code, simple pipe tables, and
// a restricted inline-link form. Unsupported syntax remains ordinary text.
//
// Source text is escaped before it enters generated HTML. Raw tags, scripts, and
// fenced code cannot become active markup. Link labels and href values are escaped
// separately. Links activate only for http, https, mailto, root-relative,
// dot-relative, parent-relative, and fragment forms.
//
// Whitespace and quote characters invalidate a link destination. Active or opaque
// schemes such as javascript and data are emitted as escaped source syntax rather
// than href attributes. Generated links also carry nofollow and noreferrer.
//
// The renderer closes open list and code constructs at end of input. This creates
// well-formed output without trying to repair source as trusted markup.
//
// # Static serving
//
// Deployment requests use either /p/{id}/ in path mode or {id}.PreviewDomain in
// wildcard mode. Wildcard routing is evaluated first so a preview origin can never
// act as an alternative origin for publisher or discovery APIs.
//
// Static origins accept only GET and HEAD. A request first loads metadata and
// compares ExpiresAt with the injected clock. Equality is expired: a deployment is
// visible only while ExpiresAt is strictly after the current instant.
//
// URL paths are cleaned with slash semantics before conversion to filesystem paths.
// Empty paths and paths ending in slash resolve to index.html. Directories may also
// resolve to a child index.html. Only regular files are served.
//
// SPA fallback is narrow by design. It applies only to GET requests for missing,
// extensionless paths on deployments published with spa enabled. Missing assets,
// HEAD navigation, and explicit file extensions preserve normal 404 behavior.
//
// Responses add nosniff, no-referrer, and a restrictive Permissions-Policy. The
// cache lifetime is capped at five minutes and shortened to the exact remaining
// deployment lifetime. Positive sub-second lifetimes use max-age=0 with mandatory
// revalidation; expired content is never passed to ServeFile.
//
// # Public URL construction
//
// Config.BaseURL is authoritative when present. Otherwise requestBase derives the
// scheme and host from the request. TLS implies https. X-Forwarded-Proto affects
// scheme only when its complete value is exactly http or https.
//
// Path mode appends /p/{id}/ to the API base. Wildcard mode preserves the derived
// scheme and combines the deployment ID with the configured preview domain. Every
// successful publish response includes that URL, the matching absolute expiry,
// and its password when protection was requested.
//
// # Cleanup and concurrency
//
// New performs an initial Sweep and then starts one ticker-driven cleanup worker.
// Close uses sync.Once and waits for the worker, so shared ownership cannot close
// channels twice or leak the goroutine during tests and graceful shutdown.
//
// Sweep removes deployments whose manifests are invalid or expired. It also
// removes staging directories older than one hour, leaving recent stages alone to
// avoid racing a live large upload. Removal errors are joined so one bad directory
// does not prevent cleanup attempts for later entries.
//
// Publication rate buckets share a mutex between request accounting and cleanup.
// Buckets older than one fixed one-hour window are removed during Sweep, bounding
// memory even when client addresses do not return.
//
// The rate limiter keys the socket remote address and does not trust forwarded
// address headers. Deployments that require proxy-aware client identity should add
// an explicit trusted-proxy policy rather than accepting spoofable headers.
//
// # Health and failure policy
//
// GET /healthz creates, closes, and removes a temporary file in the sites directory.
// This verifies the mounted storage is currently writable instead of merely
// checking that a directory exists. A failure returns service_unavailable.
//
// Expected client failures use stable HTTP statuses and machine-readable error
// codes. Unexpected publication failures are logged internally and returned as a
// generic internal_error so filesystem paths and implementation details are not
// disclosed.
//
// Discovery assets are embedded into the binary and exposed through an explicit
// route allowlist. Adding a file beneath web does not publish it automatically.
// HTML origin substitution escapes request-derived values before insertion.
//
// # Change discipline
//
// Changes to publication or serving behavior should preserve these review rules:
//
//   - never introduce uploaded-code execution or build commands;
//   - keep staging and final deployment directories on one filesystem;
//   - retain independent compressed and expanded resource bounds;
//   - validate archive paths after slash normalization;
//   - keep deployment metadata private and immutable;
//   - use the persisted expiry as the serving and cleanup authority;
//   - verify successful uploads by fetching the returned deployment URL;
//   - cover both successful and failing security paths in tests;
//   - keep docs, OpenAPI, discovery metadata, and routes synchronized; and
//   - avoid generated archives, mounted data, and secrets in the repository.
//
// A new upload format belongs in parsePublishOptions and stageContent, with a
// bounded transformation into static files. A new static URL form belongs in route
// recognition and must reuse serveSiteRequest so the GET/HEAD policy is unchanged.
// A new discovery document belongs in the embedded web allowlist and its route
// method tests.
//
// Comments in implementation files focus on invariants and non-obvious decisions;
// this package overview centralizes the broader architecture so individual methods
// remain readable. Behavior is ultimately enforced by focused tests, go vet, and
// end-to-end publication checks rather than documentation alone.
package server
