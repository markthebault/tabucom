package server

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const metadataName = ".site.json"

//go:embed web web/.well-known/agent.json
var embeddedWeb embed.FS

type Config struct {
	ListenAddr       string
	DataDir          string
	BaseURL          string
	PreviewDomain    string
	TTL              time.Duration
	SweepInterval    time.Duration
	MaxUploadBytes   int64
	MaxExpandedSize  int64
	MaxFiles         int
	RateLimitPerHour int
	Now              func() time.Time
}

func DefaultConfig() Config {
	return Config{ListenAddr: ":8080", DataDir: "./data", TTL: 720 * time.Hour, SweepInterval: time.Hour, MaxUploadBytes: 100 << 20, MaxExpandedSize: 500 << 20, MaxFiles: 10000, RateLimitPerHour: 60, Now: time.Now}
}

func ConfigFromEnv() (Config, error) {
	c := DefaultConfig()
	if v := os.Getenv("LISTEN_ADDR"); v != "" {
		c.ListenAddr = v
	}
	if v := os.Getenv("PORT"); v != "" {
		port, err := strconv.Atoi(v)
		if err != nil || port < 1 || port > 65535 {
			return c, fmt.Errorf("PORT must be an integer between 1 and 65535")
		}
		c.ListenAddr = ":" + v
	}
	if v := os.Getenv("DATA_DIR"); v != "" {
		c.DataDir = v
	}
	c.BaseURL = strings.TrimRight(os.Getenv("PUBLIC_API_URL"), "/")
	if c.BaseURL == "" {
		c.BaseURL = strings.TrimRight(os.Getenv("BASE_URL"), "/")
	}
	c.PreviewDomain = strings.ToLower(strings.TrimSuffix(os.Getenv("PREVIEW_DOMAIN"), "."))
	var err error
	if v := os.Getenv("TTL"); v != "" {
		c.TTL, err = time.ParseDuration(v)
		if err != nil {
			return c, fmt.Errorf("TTL: %w", err)
		}
	}
	if v := os.Getenv("SWEEP_INTERVAL"); v != "" {
		c.SweepInterval, err = time.ParseDuration(v)
		if err != nil {
			return c, fmt.Errorf("SWEEP_INTERVAL: %w", err)
		}
	}
	if v := os.Getenv("MAX_UPLOAD_BYTES"); v != "" {
		c.MaxUploadBytes, err = strconv.ParseInt(v, 10, 64)
		if err != nil {
			return c, fmt.Errorf("MAX_UPLOAD_BYTES: %w", err)
		}
	}
	if v := os.Getenv("MAX_EXPANDED_BYTES"); v != "" {
		c.MaxExpandedSize, err = strconv.ParseInt(v, 10, 64)
		if err != nil {
			return c, fmt.Errorf("MAX_EXPANDED_BYTES: %w", err)
		}
	}
	if v := os.Getenv("MAX_FILES"); v != "" {
		c.MaxFiles, err = strconv.Atoi(v)
		if err != nil {
			return c, fmt.Errorf("MAX_FILES: %w", err)
		}
	}
	if v := os.Getenv("RATE_LIMIT_PER_HOUR"); v != "" {
		c.RateLimitPerHour, err = strconv.Atoi(v)
		if err != nil {
			return c, fmt.Errorf("RATE_LIMIT_PER_HOUR: %w", err)
		}
	}
	return c, c.validate()
}

func (c Config) validate() error {
	if c.DataDir == "" || c.TTL <= 0 || c.SweepInterval <= 0 || c.MaxUploadBytes <= 0 || c.MaxExpandedSize <= 0 || c.MaxFiles <= 0 || c.RateLimitPerHour <= 0 {
		return errors.New("data directory, durations, and limits must be positive")
	}
	return nil
}

type Metadata struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	Files     int       `json:"files"`
	Bytes     int64     `json:"bytes"`
	SPA       bool      `json:"spa"`
}

type publishResponse struct {
	Metadata
	URL string `json:"url"`
}

type Server struct {
	cfg       Config
	sites     string
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
	rateMu    sync.Mutex
	rates     map[string]rateBucket
}

type rateBucket struct {
	start time.Time
	count int
}

func New(cfg Config) (*Server, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	sites := filepath.Join(cfg.DataDir, "sites")
	if err := os.MkdirAll(sites, 0750); err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, sites: sites, stop: make(chan struct{}), done: make(chan struct{}), rates: make(map[string]rateBucket)}
	if err := s.Sweep(); err != nil {
		slog.Warn("initial expiry sweep", "error", err)
	}
	go s.sweeper()
	return s, nil
}

func (s *Server) Close() { s.closeOnce.Do(func() { close(s.stop); <-s.done }) }

func (s *Server) sweeper() {
	defer close(s.done)
	t := time.NewTicker(s.cfg.SweepInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			if err := s.Sweep(); err != nil {
				slog.Warn("expiry sweep", "error", err)
			}
		case <-s.stop:
			return
		}
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Preview subdomains are isolated static origins, never API origins.
	if id, subpath, ok := s.wildcardSiteRequest(r); ok {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.serveSite(w, r, id, subpath)
		return
	}
	if r.URL.Path == "/healthz" && r.Method == http.MethodGet {
		s.health(w)
		return
	}
	if r.URL.Path == "/api/v1/publish" && r.Method == http.MethodPost {
		s.publish(w, r)
		return
	}
	if r.URL.Path == "/api/v1/publish" {
		w.Header().Set("Allow", http.MethodPost)
		apiError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	if (r.URL.Path == "/openapi.json" || r.URL.Path == "/llms.txt" || r.URL.Path == "/.well-known/agent.json") && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		s.serveWebFile(w, r)
		return
	}
	if id, subpath, ok := s.pathSiteRequest(r); ok {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.serveSite(w, r, id, subpath)
		return
	}
	if r.URL.Path == "/" && r.Method == http.MethodGet {
		s.landing(w, r)
		return
	}
	http.NotFound(w, r)
}

func (s *Server) health(w http.ResponseWriter) {
	f, err := os.CreateTemp(s.sites, ".health-")
	if err != nil {
		apiError(w, http.StatusServiceUnavailable, "service_unavailable", "storage is not writable")
		return
	}
	name := f.Name()
	closeErr := f.Close()
	removeErr := os.Remove(name)
	if closeErr != nil || removeErr != nil {
		apiError(w, http.StatusServiceUnavailable, "service_unavailable", "storage health check failed")
		return
	}
	jsonReply(w, http.StatusOK, map[string]any{
		"status":       "ok",
		"storage":      "writable",
		"ttlSeconds":   int64(s.cfg.TTL.Seconds()),
		"publishLimit": s.cfg.RateLimitPerHour,
	})
}

func (s *Server) landing(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(embeddedWeb, "web/index.html")
	if err != nil {
		http.Error(w, "documentation unavailable", http.StatusInternalServerError)
		return
	}
	b = bytes.ReplaceAll(b, []byte("{{ORIGIN}}"), []byte(html.EscapeString(s.requestBase(r))))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

func (s *Server) serveWebFile(w http.ResponseWriter, r *http.Request) {
	name := "web" + r.URL.Path
	b, err := fs.ReadFile(embeddedWeb, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	b = bytes.ReplaceAll(b, []byte("{{ORIGIN}}"), []byte(s.requestBase(r)))
	if typ := mime.TypeByExtension(path.Ext(name)); typ != "" {
		w.Header().Set("Content-Type", typ)
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	if r.Method == http.MethodGet {
		_, _ = w.Write(b)
	}
}

func (s *Server) publish(w http.ResponseWriter, r *http.Request) {
	if retry, ok := s.allowPublish(r); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
		apiError(w, http.StatusTooManyRequests, "rate_limited", "publish rate limit exceeded")
		return
	}
	ct, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		apiError(w, 415, "unsupported_media_type", "Content-Type must be text/html, text/markdown, or application/zip")
		return
	}
	if ct != "text/html" && ct != "text/markdown" && ct != "application/zip" {
		apiError(w, 415, "unsupported_media_type", "Content-Type must be text/html, text/markdown, or application/zip")
		return
	}
	spa, err := parseBool(r.URL.Query().Get("spa"))
	if err != nil {
		apiError(w, 400, "invalid_spa", "spa must be 1, 0, true, or false")
		return
	}
	if r.ContentLength > s.cfg.MaxUploadBytes {
		apiError(w, 413, "upload_too_large", "request body exceeds upload limit")
		return
	}
	id, err := randomID()
	if err != nil {
		apiError(w, 500, "internal_error", "could not allocate site ID")
		return
	}
	stage, err := os.MkdirTemp(s.sites, ".staging-")
	if err != nil {
		apiError(w, 500, "internal_error", "could not stage site")
		return
	}
	defer os.RemoveAll(stage)
	var files int
	var size int64
	switch ct {
	case "text/html":
		size, err = streamRequestBody(w, r, filepath.Join(stage, "index.html"), s.cfg.MaxUploadBytes)
		files = 1
	case "text/markdown":
		body, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadBytes))
		if readErr != nil {
			err = readErr
			break
		}
		if len(body) == 0 {
			err = errEmptyBody
			break
		}
		rendered := renderMarkdown(body)
		if int64(len(rendered)) > s.cfg.MaxExpandedSize {
			err = &publishError{http.StatusRequestEntityTooLarge, "upload_too_large", "rendered content exceeds expanded size limit"}
			break
		}
		err = os.WriteFile(filepath.Join(stage, "index.html"), rendered, 0640)
		files, size = 1, int64(len(rendered))
	case "application/zip":
		archivePath := filepath.Join(stage, ".upload.zip")
		_, err = streamRequestBody(w, r, archivePath, s.cfg.MaxUploadBytes)
		if err == nil {
			files, size, err = s.extractZip(stage, archivePath)
		}
		_ = os.Remove(archivePath)
	}
	if err != nil {
		var pe *publishError
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			apiError(w, http.StatusRequestEntityTooLarge, "upload_too_large", "request body exceeds upload limit")
		} else if errors.Is(err, errEmptyBody) {
			apiError(w, http.StatusBadRequest, "empty_body", "request body is empty")
		} else if errors.As(err, &pe) {
			apiError(w, pe.status, pe.code, pe.message)
		} else {
			slog.Error("publish", "error", err)
			apiError(w, 500, "internal_error", "could not publish site")
		}
		return
	}
	if _, err := os.Stat(filepath.Join(stage, "index.html")); err != nil {
		apiError(w, 400, "missing_index", "site must contain index.html at its root")
		return
	}
	now := s.cfg.Now().UTC()
	meta := Metadata{ID: id, CreatedAt: now, ExpiresAt: now.Add(s.cfg.TTL), Files: files, Bytes: size, SPA: spa}
	metaBytes, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(stage, metadataName), metaBytes, 0640); err != nil {
		apiError(w, 500, "internal_error", "could not write metadata")
		return
	}
	if err := os.Rename(stage, filepath.Join(s.sites, id)); err != nil {
		apiError(w, 500, "internal_error", "could not commit site")
		return
	}
	jsonReply(w, http.StatusCreated, publishResponse{Metadata: meta, URL: s.siteURL(r, id)})
}

var errEmptyBody = errors.New("request body is empty")

func streamRequestBody(w http.ResponseWriter, r *http.Request, target string, maxBytes int64) (int64, error) {
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0640)
	if err != nil {
		return 0, err
	}
	n, copyErr := io.Copy(out, http.MaxBytesReader(w, r.Body, maxBytes))
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil {
		return n, errors.Join(copyErr, closeErr)
	}
	if n == 0 {
		return 0, errEmptyBody
	}
	return n, nil
}

func (s *Server) allowPublish(r *http.Request) (time.Duration, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if host == "" {
		host = "unknown"
	}
	now := s.cfg.Now()
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	b := s.rates[host]
	if b.start.IsZero() || now.Sub(b.start) >= time.Hour {
		s.rates[host] = rateBucket{start: now, count: 1}
		return 0, true
	}
	if b.count >= s.cfg.RateLimitPerHour {
		return time.Hour - now.Sub(b.start), false
	}
	b.count++
	s.rates[host] = b
	return 0, true
}

type publishError struct {
	status        int
	code, message string
}

func (e *publishError) Error() string   { return e.message }
func invalidZip(code, msg string) error { return &publishError{400, code, msg} }

func (s *Server) extractZip(dst, archivePath string) (int, int64, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return 0, 0, invalidZip("invalid_archive", "body is not a valid ZIP archive")
	}
	defer zr.Close()
	seen := map[string]bool{}
	files := 0
	var total int64
	for _, f := range zr.File {
		name := strings.ReplaceAll(f.Name, "\\", "/")
		clean := path.Clean(name)
		windowsAbsolute := len(name) >= 3 && ((name[0] >= 'A' && name[0] <= 'Z') || (name[0] >= 'a' && name[0] <= 'z')) && name[1] == ':' && name[2] == '/'
		if name == "" || strings.HasPrefix(name, "/") || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) || windowsAbsolute || strings.ContainsRune(name, 0) {
			return 0, 0, invalidZip("invalid_archive", "ZIP contains an unsafe path")
		}
		if strings.Count(clean, "/") > 50 {
			return 0, 0, invalidZip("invalid_archive", "ZIP path nesting is too deep")
		}
		isDir := strings.HasSuffix(name, "/")
		key := strings.TrimSuffix(clean, "/")
		if seen[key] {
			return 0, 0, invalidZip("invalid_archive", "ZIP contains duplicate normalized paths")
		}
		seen[key] = true
		mode := f.Mode()
		if mode&os.ModeSymlink != 0 || mode&os.ModeType != 0 && !mode.IsDir() {
			return 0, 0, invalidZip("invalid_archive", "ZIP links and special files are not allowed")
		}
		target := filepath.Join(dst, filepath.FromSlash(clean))
		if isDir || mode.IsDir() {
			if err := os.MkdirAll(target, 0750); err != nil {
				return 0, total, err
			}
			continue
		}
		files++
		if files > s.cfg.MaxFiles {
			return 0, 0, &publishError{413, "too_many_files", "ZIP exceeds file count limit"}
		}
		if f.UncompressedSize64 > uint64(s.cfg.MaxExpandedSize) || total > s.cfg.MaxExpandedSize-int64(f.UncompressedSize64) {
			return 0, 0, &publishError{413, "upload_too_large", "ZIP exceeds expanded size limit"}
		}
		if err := os.MkdirAll(filepath.Dir(target), 0750); err != nil {
			return 0, total, err
		}
		rc, err := f.Open()
		if err != nil {
			return 0, total, invalidZip("invalid_archive", "ZIP entry cannot be read")
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0640)
		if err != nil {
			rc.Close()
			return 0, total, err
		}
		n, cpErr := io.Copy(out, io.LimitReader(rc, s.cfg.MaxExpandedSize-total+1))
		closeErr := out.Close()
		rc.Close()
		if cpErr != nil || closeErr != nil {
			return 0, total, errors.Join(cpErr, closeErr)
		}
		total += n
		if total > s.cfg.MaxExpandedSize {
			return 0, 0, &publishError{413, "upload_too_large", "ZIP exceeds expanded size limit"}
		}
	}
	return files, total, nil
}

func (s *Server) pathSiteRequest(r *http.Request) (string, string, bool) {
	if strings.HasPrefix(r.URL.Path, "/p/") {
		rest := strings.TrimPrefix(r.URL.Path, "/p/")
		parts := strings.SplitN(rest, "/", 2)
		if validID(parts[0]) {
			sub := ""
			if len(parts) == 2 {
				sub = parts[1]
			}
			return parts[0], sub, true
		}
	}
	return "", "", false
}

func (s *Server) wildcardSiteRequest(r *http.Request) (string, string, bool) {
	if s.cfg.PreviewDomain == "" {
		return "", "", false
	}
	host := strings.ToLower(r.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	suffix := "." + s.cfg.PreviewDomain
	if !strings.HasSuffix(host, suffix) {
		return "", "", false
	}
	id := strings.TrimSuffix(host, suffix)
	if !validID(id) {
		return "", "", false
	}
	return id, strings.TrimPrefix(r.URL.Path, "/"), true
}

func (s *Server) serveSite(w http.ResponseWriter, r *http.Request, id, requested string) {
	root := filepath.Join(s.sites, id)
	meta, err := readMetadata(root)
	if err != nil || !meta.ExpiresAt.After(s.cfg.Now()) {
		http.NotFound(w, r)
		return
	}
	requested = path.Clean("/" + requested)
	requested = strings.TrimPrefix(requested, "/")
	if requested == "" || strings.HasSuffix(r.URL.Path, "/") {
		requested = path.Join(requested, "index.html")
	}
	if requested == metadataName || strings.HasPrefix(requested, "../") {
		http.NotFound(w, r)
		return
	}
	file := filepath.Join(root, filepath.FromSlash(requested))
	info, err := os.Stat(file)
	if err == nil && info.IsDir() {
		file = filepath.Join(file, "index.html")
		info, err = os.Stat(file)
	}
	if err != nil && meta.SPA && r.Method == http.MethodGet && path.Ext(requested) == "" {
		file = filepath.Join(root, "index.html")
		info, err = os.Stat(file)
	}
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
	w.Header().Set("Cache-Control", "public, max-age=300")
	http.ServeFile(w, r, file)
}

func readMetadata(root string) (Metadata, error) {
	var m Metadata
	b, e := os.ReadFile(filepath.Join(root, metadataName))
	if e == nil {
		e = json.Unmarshal(b, &m)
	}
	return m, e
}

func (s *Server) Sweep() error {
	entries, err := os.ReadDir(s.sites)
	if err != nil {
		return err
	}
	now := s.cfg.Now()
	s.rateMu.Lock()
	for ip, b := range s.rates {
		if now.Sub(b.start) >= time.Hour {
			delete(s.rates, ip)
		}
	}
	s.rateMu.Unlock()
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".staging-") {
			info, x := e.Info()
			if x == nil && now.Sub(info.ModTime()) > time.Hour {
				_ = os.RemoveAll(filepath.Join(s.sites, e.Name()))
			}
			continue
		}
		if !e.IsDir() || !validID(e.Name()) {
			continue
		}
		m, x := readMetadata(filepath.Join(s.sites, e.Name()))
		if x != nil || !m.ExpiresAt.After(now) {
			if x := os.RemoveAll(filepath.Join(s.sites, e.Name())); x != nil {
				err = errors.Join(err, x)
			}
		}
	}
	return err
}

func (s *Server) requestBase(r *http.Request) string {
	if s.cfg.BaseURL != "" {
		return s.cfg.BaseURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if v := r.Header.Get("X-Forwarded-Proto"); v == "http" || v == "https" {
		scheme = v
	}
	return scheme + "://" + r.Host
}
func (s *Server) siteURL(r *http.Request, id string) string {
	if s.cfg.PreviewDomain != "" {
		scheme := "https"
		if strings.HasPrefix(s.requestBase(r), "http://") {
			scheme = "http"
		}
		return scheme + "://" + id + "." + s.cfg.PreviewDomain + "/"
	}
	return s.requestBase(r) + "/p/" + id + "/"
}
func randomID() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	x := hex.EncodeToString(b[:])
	return x[0:8] + "-" + x[8:12] + "-" + x[12:16] + "-" + x[16:20] + "-" + x[20:], nil
}
func validID(v string) bool {
	if len(v) != 36 {
		return false
	}
	for i, c := range v {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}
func parseBool(v string) (bool, error) {
	switch strings.ToLower(v) {
	case "", "0", "false":
		return false, nil
	case "1", "true":
		return true, nil
	default:
		return false, errors.New("invalid boolean")
	}
}

// renderMarkdown deliberately implements a small, safe subset. All source text is
// escaped, so pasted HTML and script are displayed rather than executed.
func renderMarkdown(src []byte) []byte {
	lines := strings.Split(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n")
	var b strings.Builder
	b.WriteString(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Preview</title><style>body{font:16px/1.6 system-ui;max-width:52rem;margin:3rem auto;padding:0 1rem}pre{overflow:auto;padding:1rem;background:#f4f4f5}code{background:#f4f4f5;padding:.15em .3em}</style></head><body>`)
	inCode := false
	inList := false
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(line, "```") {
			if inList {
				b.WriteString("</ul>")
				inList = false
			}
			if inCode {
				b.WriteString("</code></pre>")
			} else {
				b.WriteString("<pre><code>")
			}
			inCode = !inCode
			continue
		}
		if inCode {
			b.WriteString(html.EscapeString(line))
			b.WriteByte('\n')
			continue
		}
		t := strings.TrimSpace(line)
		if strings.Contains(t, "|") && i+1 < len(lines) && markdownTableSeparator(lines[i+1]) {
			if inList {
				b.WriteString("</ul>")
				inList = false
			}
			head := markdownCells(t)
			b.WriteString("<table><thead><tr>")
			for _, c := range head {
				b.WriteString("<th>" + markdownInline(c) + "</th>")
			}
			b.WriteString("</tr></thead><tbody>")
			i += 2
			for ; i < len(lines) && strings.Contains(lines[i], "|") && strings.TrimSpace(lines[i]) != ""; i++ {
				b.WriteString("<tr>")
				for _, c := range markdownCells(lines[i]) {
					b.WriteString("<td>" + markdownInline(c) + "</td>")
				}
				b.WriteString("</tr>")
			}
			i--
			b.WriteString("</tbody></table>")
			continue
		}
		if strings.HasPrefix(t, "- ") {
			if !inList {
				b.WriteString("<ul>")
				inList = true
			}
			b.WriteString("<li>" + markdownInline(strings.TrimSpace(t[2:])) + "</li>")
			continue
		}
		if inList {
			b.WriteString("</ul>")
			inList = false
		}
		if t == "" {
			continue
		}
		n := 0
		for n < len(t) && n < 6 && t[n] == '#' {
			n++
		}
		if n > 0 && len(t) > n && t[n] == ' ' {
			fmt.Fprintf(&b, "<h%d>%s</h%d>", n, markdownInline(strings.TrimSpace(t[n:])), n)
		} else {
			b.WriteString("<p>" + markdownInline(t) + "</p>")
		}
	}
	if inCode {
		b.WriteString("</code></pre>")
	}
	if inList {
		b.WriteString("</ul>")
	}
	b.WriteString("</body></html>")
	return []byte(b.String())
}

func markdownInline(v string) string {
	var b strings.Builder
	for len(v) > 0 {
		open := strings.IndexByte(v, '[')
		if open < 0 {
			b.WriteString(html.EscapeString(v))
			break
		}
		closeLabel := strings.Index(v[open+1:], "](")
		if closeLabel < 0 {
			b.WriteString(html.EscapeString(v))
			break
		}
		closeLabel += open + 1
		closeURL := strings.IndexByte(v[closeLabel+2:], ')')
		if closeURL < 0 {
			b.WriteString(html.EscapeString(v))
			break
		}
		closeURL += closeLabel + 2
		b.WriteString(html.EscapeString(v[:open]))
		label, urlText := v[open+1:closeLabel], v[closeLabel+2:closeURL]
		lower := strings.ToLower(urlText)
		safe := !strings.ContainsAny(urlText, " \t\r\n\"") && (strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "mailto:") || strings.HasPrefix(urlText, "/") || strings.HasPrefix(urlText, "./") || strings.HasPrefix(urlText, "../") || strings.HasPrefix(urlText, "#"))
		if safe {
			b.WriteString(`<a rel="nofollow noreferrer" href="` + html.EscapeString(urlText) + `">` + html.EscapeString(label) + `</a>`)
		} else {
			b.WriteString(html.EscapeString(v[open : closeURL+1]))
		}
		v = v[closeURL+1:]
	}
	return b.String()
}
func markdownCells(v string) []string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "|")
	v = strings.TrimSuffix(v, "|")
	parts := strings.Split(v, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}
func markdownTableSeparator(v string) bool {
	cells := markdownCells(v)
	if len(cells) == 0 {
		return false
	}
	for _, c := range cells {
		c = strings.Trim(c, " :")
		if len(c) < 3 || strings.Trim(c, "-") != "" {
			return false
		}
	}
	return true
}

func jsonReply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func apiError(w http.ResponseWriter, status int, code, message string) {
	jsonReply(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
