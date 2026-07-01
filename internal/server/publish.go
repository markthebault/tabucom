/*
This file coordinates validation, staging, metadata creation, and publication.
It accepts raw HTML, Markdown, or ZIP bodies and never executes uploaded code.
Local and S3 backends receive the same fully validated immutable deployment.
It depends on standard HTTP, MIME, filesystem, networking, crypto, and time APIs,
while archive.go, markdown.go, password.go, and storage helpers do focused work.
*/
package server

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var errEmptyBody = errors.New("request body is empty")

const idAllocationAttempts = 16

// Metadata is persisted with each deployment and returned to publishing clients.
// The file count and byte count describe expanded, servable content rather than
// the transport encoding of the upload.
type Metadata struct {
	ID        string            `json:"id"`
	CreatedAt time.Time         `json:"createdAt"`
	ExpiresAt time.Time         `json:"expiresAt"`
	Files     int               `json:"files"`
	Bytes     int64             `json:"bytes"`
	SPA       bool              `json:"spa"`
	Password  *passwordMetadata `json:"password,omitempty"`
}

// publishResponse is deliberately separate from persisted metadata so password
// hashes and cookie tokens can never enter the public API response.
type publishResponse struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	Files     int       `json:"files"`
	Bytes     int64     `json:"bytes"`
	SPA       bool      `json:"spa"`
	Protected bool      `json:"protected"`
	Password  string    `json:"password,omitempty"`
}

// publishOptions is the validated portion of a publish request that influences
// storage. Parsing it before staging avoids creating temporary directories for
// malformed requests.
type publishOptions struct {
	contentType      string
	spa              bool
	ttl              time.Duration
	prefix           string
	password         string
	generatePassword bool
}

// stagedContent reports the exact deployment size after transformation or ZIP
// expansion. It is used to create immutable metadata before commit.
type stagedContent struct {
	files int
	bytes int64
}

// rateBucket implements a fixed rate-limit window for one client and action.
type rateBucket struct {
	start time.Time
	count int
}

// publish coordinates validation, staging, and atomic commit. No deployment path
// is visible until every input check and metadata write has succeeded.
func (s *Server) publish(w http.ResponseWriter, r *http.Request) {
	if err := s.requirePublishToken(r); err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer realm="tabucom-publish"`)
		apiError(w, http.StatusUnauthorized, "unauthorized", "valid publish bearer token required")
		return
	}

	if retryAfter, allowed := s.allowPublish(r); !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
		apiError(w, http.StatusTooManyRequests, "rate_limited", "publish rate limit exceeded")
		return
	}

	options, err := s.parsePublishOptions(r)
	if err != nil {
		writePublishError(w, err)
		return
	}

	// Content-Length is an early rejection only. MaxBytesReader remains the
	// authoritative bound because clients may omit or misreport this header.
	if r.ContentLength > s.cfg.MaxUploadBytes {
		apiError(w, http.StatusRequestEntityTooLarge, "upload_too_large", "request body exceeds upload limit")
		return
	}

	id, err := s.allocateID(options.prefix)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "internal_error", "could not allocate site ID")
		return
	}

	stage, err := os.MkdirTemp(s.sites, ".staging-")
	if err != nil {
		apiError(w, http.StatusInternalServerError, "internal_error", "could not stage site")
		return
	}
	// Rename removes the stage path on success; RemoveAll cleans every failure.
	defer os.RemoveAll(stage)

	content, err := s.stageContent(w, r, stage, options.contentType)
	if err != nil {
		writePublishError(w, err)
		return
	}
	if err := requireRootIndex(stage); err != nil {
		writePublishError(w, err)
		return
	}

	now := s.cfg.Now().UTC()
	password := options.password
	if options.generatePassword {
		password, err = randomSecret(18)
		if err != nil {
			apiError(w, http.StatusInternalServerError, "internal_error", "could not generate password")
			return
		}
	}
	metadata := Metadata{
		ID:        id,
		CreatedAt: now,
		ExpiresAt: now.Add(options.ttl),
		Files:     content.files,
		Bytes:     content.bytes,
		SPA:       options.spa,
	}
	if password != "" {
		metadata.Password, err = newPasswordMetadata(password)
		if err != nil {
			apiError(w, http.StatusInternalServerError, "internal_error", "could not protect site")
			return
		}
	}
	if err := writeMetadata(stage, metadata); err != nil {
		apiError(w, http.StatusInternalServerError, "internal_error", "could not write metadata")
		return
	}

	var commitErr error
	if s.s3 != nil {
		commitErr = s.s3.commit(stage, id)
	} else {
		// The staging directory and destination share a parent, so Rename provides
		// atomic visibility: readers observe either no deployment or the complete one.
		commitErr = os.Rename(stage, filepath.Join(s.sites, id))
	}
	if commitErr != nil {
		apiError(w, http.StatusInternalServerError, "internal_error", "could not commit site")
		return
	}

	jsonReply(w, http.StatusCreated, publishResponse{
		ID: metadata.ID, URL: s.siteURL(r, id), CreatedAt: metadata.CreatedAt,
		ExpiresAt: metadata.ExpiresAt, Files: metadata.Files, Bytes: metadata.Bytes,
		SPA: metadata.SPA, Protected: metadata.Password != nil, Password: password,
	})
}

// parsePublishOptions accepts only the three documented upload formats and the
// deliberately small query-string surface.
func (s *Server) parsePublishOptions(r *http.Request) (publishOptions, error) {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !supportedContentType(contentType) {
		return publishOptions{}, &publishError{
			status:  http.StatusUnsupportedMediaType,
			code:    "unsupported_media_type",
			message: "Content-Type must be text/html, text/markdown, or application/zip",
		}
	}

	spa, err := parseBool(r.URL.Query().Get("spa"))
	if err != nil {
		return publishOptions{}, &publishError{http.StatusBadRequest, "invalid_spa", "spa must be 1, 0, true, or false"}
	}
	ttl, err := parseTTL(r.URL.Query().Get("ttl"), s.cfg.TTL)
	if err != nil {
		return publishOptions{}, &publishError{http.StatusBadRequest, "invalid_ttl", "ttl must be a positive duration such as 72h or 90m"}
	}
	prefix := r.URL.Query().Get("prefix")
	if prefix != "" && !validPrefix(prefix) {
		return publishOptions{}, &publishError{http.StatusBadRequest, "invalid_prefix", "prefix must be 1 to 46 lowercase letters, numbers, or hyphens, starting and ending with a letter or number"}
	}
	generatePassword, err := parseBool(r.URL.Query().Get("generatePassword"))
	if err != nil {
		return publishOptions{}, &publishError{http.StatusBadRequest, "invalid_generate_password", "generatePassword must be 1, 0, true, or false"}
	}
	passwordValues, passwordSet := r.Header[http.CanonicalHeaderKey("Tabucom-Password")]
	password := r.Header.Get("Tabucom-Password")
	if generatePassword && passwordSet {
		return publishOptions{}, &publishError{http.StatusBadRequest, "invalid_password", "Tabucom-Password and generatePassword cannot be used together"}
	}
	if passwordSet && (len(passwordValues) != 1 || !validPassword(password)) {
		return publishOptions{}, &publishError{http.StatusBadRequest, "invalid_password", "password must be 8 to 128 printable ASCII characters"}
	}

	return publishOptions{contentType: contentType, spa: spa, ttl: ttl, prefix: prefix, password: password, generatePassword: generatePassword}, nil
}

func validPassword(password string) bool {
	if len(password) < 8 || len(password) > 128 {
		return false
	}
	for _, character := range []byte(password) {
		if character < 0x20 || character > 0x7e {
			return false
		}
	}
	return true
}

func randomSecret(bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func (s *Server) allocateID(prefix string) (string, error) {
	for range idAllocationAttempts {
		id, err := randomID()
		if prefix != "" {
			id, err = randomPrefixedID(prefix)
		}
		if err != nil {
			return "", err
		}
		exists, err := s.siteExists(id)
		if err != nil {
			return "", err
		}
		if !exists {
			return id, nil
		}
	}
	return "", errors.New("could not allocate unique site ID")
}

func (s *Server) siteExists(id string) (bool, error) {
	if s.s3 != nil {
		return s.s3.siteExists(id)
	}
	_, err := os.Stat(filepath.Join(s.sites, id))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// supportedContentType is kept separate so media-type policy has one definition.
func supportedContentType(contentType string) bool {
	switch contentType {
	case "text/html", "text/markdown", "application/zip":
		return true
	default:
		return false
	}
}

// stageContent converts each accepted transport into a directory containing
// already-built static files. It never invokes an interpreter, build tool, or
// package manager on uploaded content.
func (s *Server) stageContent(w http.ResponseWriter, r *http.Request, stage, contentType string) (stagedContent, error) {
	switch contentType {
	case "text/html":
		size, err := streamRequestBody(w, r, filepath.Join(stage, "index.html"), s.cfg.MaxUploadBytes)
		return stagedContent{files: 1, bytes: size}, err

	case "text/markdown":
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadBytes))
		if err != nil {
			return stagedContent{}, err
		}
		if len(body) == 0 {
			return stagedContent{}, errEmptyBody
		}
		rendered := renderMarkdown(body)
		if int64(len(rendered)) > s.cfg.MaxExpandedSize {
			return stagedContent{}, newPublishError(http.StatusRequestEntityTooLarge, "upload_too_large", "rendered content exceeds expanded size limit")
		}
		if err := os.WriteFile(filepath.Join(stage, "index.html"), rendered, 0640); err != nil {
			return stagedContent{}, err
		}
		return stagedContent{files: 1, bytes: int64(len(rendered))}, nil

	case "application/zip":
		archivePath := filepath.Join(stage, ".upload.zip")
		if _, err := streamRequestBody(w, r, archivePath, s.cfg.MaxUploadBytes); err != nil {
			return stagedContent{}, err
		}
		defer os.Remove(archivePath)
		files, size, err := s.extractZip(stage, archivePath)
		return stagedContent{files: files, bytes: size}, err
	}

	// parsePublishOptions makes this unreachable; retain a defensive error so a
	// future content type cannot accidentally bypass staging.
	return stagedContent{}, newPublishError(http.StatusUnsupportedMediaType, "unsupported_media_type", "unsupported content type")
}

// requireRootIndex enforces the static-site entry-point contract for all upload
// forms, including archives that contain an index only in a nested directory.
func requireRootIndex(stage string) error {
	info, err := os.Stat(filepath.Join(stage, "index.html"))
	if err != nil || !info.Mode().IsRegular() {
		return newPublishError(http.StatusBadRequest, "missing_index", "site must contain index.html at its root")
	}
	return nil
}

// streamRequestBody writes through MaxBytesReader and O_EXCL. A partial file is
// safe because it exists only under a staging directory that will be removed.
func streamRequestBody(w http.ResponseWriter, r *http.Request, target string, maxBytes int64) (int64, error) {
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0640)
	if err != nil {
		return 0, err
	}

	written, copyErr := io.Copy(output, http.MaxBytesReader(w, r.Body, maxBytes))
	closeErr := output.Close()
	if copyErr != nil || closeErr != nil {
		return written, errors.Join(copyErr, closeErr)
	}
	if written == 0 {
		return 0, errEmptyBody
	}
	return written, nil
}

// allowPublish applies a process-local fixed window using the remote socket
// address. Proxy trust is intentionally not inferred from spoofable headers.
func (s *Server) allowPublish(r *http.Request) (time.Duration, bool) {
	return s.allowRate("publish:"+networkPeer(r), time.Hour, s.cfg.RateLimitPerHour)
}

func (s *Server) allowPasswordAttempt(r *http.Request, id string) (time.Duration, bool) {
	return s.allowRate("password:"+id+":"+networkPeer(r), time.Minute, 10)
}

func networkPeer(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if host == "" {
		host = "unknown"
	}
	return host
}

func (s *Server) allowRate(key string, window time.Duration, limit int) (time.Duration, bool) {
	now := s.cfg.Now()
	s.rateMu.Lock()
	defer s.rateMu.Unlock()

	bucket := s.rates[key]
	if bucket.start.IsZero() || now.Sub(bucket.start) >= window {
		s.rates[key] = rateBucket{start: now, count: 1}
		return 0, true
	}
	if bucket.count >= limit {
		return window - now.Sub(bucket.start), false
	}

	bucket.count++
	s.rates[key] = bucket
	return 0, true
}

// parseBool accepts the documented URL-friendly boolean forms.
func parseBool(value string) (bool, error) {
	switch strings.ToLower(value) {
	case "", "0", "false":
		return false, nil
	case "1", "true":
		return true, nil
	default:
		return false, errors.New("invalid boolean")
	}
}

// parseTTL uses the configured retention only when the client omits a TTL.
func parseTTL(value string, fallback time.Duration) (time.Duration, error) {
	if value == "" {
		return fallback, nil
	}
	ttl, err := time.ParseDuration(value)
	if err != nil || ttl <= 0 {
		return 0, errors.New("invalid ttl")
	}
	return ttl, nil
}

// publishError carries the stable client-facing status and error code while
// satisfying error for uniform staging failure handling.
type publishError struct {
	status  int
	code    string
	message string
}

func (e *publishError) Error() string { return e.message }

// newPublishError keeps error construction readable at validation sites.
func newPublishError(status int, code, message string) error {
	return &publishError{status: status, code: code, message: message}
}

// writePublishError maps expected client failures without disclosing filesystem
// or archive internals. Unexpected failures are logged with server-side detail.
func writePublishError(w http.ResponseWriter, err error) {
	var clientError *publishError
	var maxBytesError *http.MaxBytesError

	switch {
	case errors.As(err, &maxBytesError):
		apiError(w, http.StatusRequestEntityTooLarge, "upload_too_large", "request body exceeds upload limit")
	case errors.Is(err, errEmptyBody):
		apiError(w, http.StatusBadRequest, "empty_body", "request body is empty")
	case errors.As(err, &clientError):
		apiError(w, clientError.status, clientError.code, clientError.message)
	default:
		slog.Error("publish", "error", err)
		apiError(w, http.StatusInternalServerError, "internal_error", "could not publish site")
	}
}
