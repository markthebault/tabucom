# Agent guide

This repository implements an internal, no-sign-in temporary static publisher. Preserve these invariants:

- `POST /api/v1/publish` accepts raw HTML, Markdown, or a ZIP of already-built static files.
- The server never executes uploaded code or runs package-manager/build commands.
- Every deployment is immutable and expires exactly 30 days after successful publication.
- ZIPs require `index.html` at their root and must be extracted defensively.
- Path mode serves `/p/{id}/`; optional wildcard mode returns a deployment subdomain.
- Agents must return both `url` and `expiresAt` after publishing.

## Durable commands

Run the service:

```sh
go run ./cmd/here-now-alt
```

Format, test, and statically check changes:

```sh
gofmt -w ./cmd ./internal
go test ./...
go vet ./...
```

Build and smoke-test the container:

```sh
docker build -t temporary-publisher .
docker run --rm -d --name temporary-publisher-test -p 8080:8080 \
  -e PUBLIC_API_URL=http://localhost:8080 \
  -v publisher-test-data:/data temporary-publisher
curl -fsS http://localhost:8080/healthz
docker stop temporary-publisher-test
```

Smoke-test all input forms against a running server:

```sh
printf '<!doctype html><title>smoke</title><h1>ok</h1>' > /tmp/site.html
printf '# smoke\n\nMarkdown works.\n' > /tmp/site.md
curl -fsS -X POST http://localhost:8080/api/v1/publish -H 'Content-Type: text/html' --data-binary @/tmp/site.html
curl -fsS -X POST http://localhost:8080/api/v1/publish -H 'Content-Type: text/markdown' --data-binary @/tmp/site.md
mkdir -p /tmp/static-site && cp /tmp/site.html /tmp/static-site/index.html
(cd /tmp/static-site && zip -qr /tmp/static-site.zip .)
curl -fsS -X POST 'http://localhost:8080/api/v1/publish?spa=1' -H 'Content-Type: application/zip' --data-binary @/tmp/static-site.zip
```

Validate embedded web JSON after editing it:

```sh
python3 -m json.tool internal/server/web/openapi.json >/dev/null
python3 -m json.tool internal/server/web/.well-known/agent.json >/dev/null
```

Run the isolated discovery check with a local model after starting the service and an LM Studio model identified as `codex-local`:

```sh
BASE_URL=http://127.0.0.1:8080 CODEX_MODEL=codex-local ./scripts/codex-agent-test.sh
```

## Change checklist

1. Keep landing-page examples, OpenAPI, `llms.txt`, discovery metadata, README, and implemented routes synchronized.
2. Add tests for success and failure paths, especially traversal, symlinks, duplicate paths, archive expansion limits, atomic visibility, SPA fallback, and expiry.
3. Verify the returned deployment URL itself, not only the upload response.
4. Do not commit generated archives, test data, secrets, or mounted volume contents.
