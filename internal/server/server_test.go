package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testServer(t *testing.T, mutate func(*Config)) *Server {
	t.Helper()
	c := DefaultConfig()
	c.DataDir = t.TempDir()
	c.SweepInterval = time.Hour
	if mutate != nil {
		mutate(&c)
	}
	s, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestConfigFromEnv(t *testing.T) {
	t.Setenv("PORT", "18080")
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("PUBLIC_API_URL", "https://publish.example.test/")
	t.Setenv("PREVIEW_DOMAIN", "Preview.Example.Test.")
	t.Setenv("TTL", "24h")
	t.Setenv("MAX_FILES", "25")
	t.Setenv("RATE_LIMIT_PER_HOUR", "7")
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != ":18080" || cfg.BaseURL != "https://publish.example.test" || cfg.PreviewDomain != "preview.example.test" || cfg.TTL != 24*time.Hour || cfg.MaxFiles != 25 || cfg.RateLimitPerHour != 7 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	t.Setenv("PORT", "invalid")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("invalid PORT was accepted")
	}
}

func publish(t *testing.T, s *Server, contentType string, body []byte, query string) (publishResponse, *httptest.ResponseRecorder) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "http://publisher.test/api/v1/publish"+query, bytes.NewReader(body))
	r.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	var got publishResponse
	if w.Code == http.StatusCreated {
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
	}
	return got, w
}

func TestPublishHTMLAndServe(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	s := testServer(t, func(c *Config) { c.Now = func() time.Time { return now }; c.TTL = 2 * time.Hour })
	got, w := publish(t, s, "text/html; charset=utf-8", []byte("<h1>Hello</h1>"), "?spa=1")
	if w.Code != 201 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !validID(got.ID) || got.URL != "http://publisher.test/p/"+got.ID+"/" || got.Files != 1 || !got.SPA {
		t.Fatalf("bad response: %+v", got)
	}
	if !got.CreatedAt.Equal(now) || !got.ExpiresAt.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("bad timestamps: %+v", got)
	}
	r := httptest.NewRequest(http.MethodGet, "http://publisher.test/p/"+got.ID+"/", nil)
	out := httptest.NewRecorder()
	s.ServeHTTP(out, r)
	if out.Code != 200 || out.Body.String() != "<h1>Hello</h1>" {
		t.Fatalf("serve status=%d body=%q", out.Code, out.Body.String())
	}
	// Extensionless routes fall back only for SPA publishes.
	r = httptest.NewRequest(http.MethodGet, "http://publisher.test/p/"+got.ID+"/dashboard/settings", nil)
	out = httptest.NewRecorder()
	s.ServeHTTP(out, r)
	if out.Code != 200 {
		t.Fatalf("SPA fallback status=%d", out.Code)
	}
	if _, err := os.Stat(filepath.Join(s.sites, got.ID, metadataName)); err != nil {
		t.Fatal("metadata missing:", err)
	}
}

func TestMarkdownEscapesActiveContent(t *testing.T) {
	s := testServer(t, nil)
	got, w := publish(t, s, "text/markdown", []byte("# Title\n\n<script>alert(1)</script> [safe](https://example.com) [bad](javascript:alert)\n\n| A | B |\n| --- | --- |\n| one | two |\n\n```\n<b>x</b>\n```"), "")
	if w.Code != 201 {
		t.Fatalf("status=%d: %s", w.Code, w.Body.String())
	}
	b, err := os.ReadFile(filepath.Join(s.sites, got.ID, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if strings.Contains(text, "<script>") || strings.Contains(text, "<b>x</b>") || strings.Contains(text, `href="javascript:`) || !strings.Contains(text, "&lt;script&gt;") || !strings.Contains(text, `href="https://example.com"`) || !strings.Contains(text, "<table>") {
		t.Fatalf("unsafe/incomplete rendering: %s", text)
	}
}

func TestMarkdownRendersTablesAndSafeLinks(t *testing.T) {
	s := testServer(t, nil)
	source := []byte("| Name | Value |\n| --- | --- |\n| docs | [safe](https://example.com) |\n\n[unsafe](javascript:alert(1))")
	got, w := publish(t, s, "text/markdown", source, "")
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d: %s", w.Code, w.Body.String())
	}
	b, err := os.ReadFile(filepath.Join(s.sites, got.ID, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if !strings.Contains(text, "<table>") || !strings.Contains(text, `href="https://example.com"`) {
		t.Fatalf("expected GFM-style table and safe link: %s", text)
	}
	if strings.Contains(text, `href="javascript:`) {
		t.Fatalf("unsafe link was activated: %s", text)
	}
}

type zipEntry struct {
	name, body string
	mode       os.FileMode
}

func zipBody(t *testing.T, entries ...zipEntry) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Store}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		w, err := z.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, e.body)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestPublishZIPAssetsAndWildcardServing(t *testing.T) {
	s := testServer(t, func(c *Config) { c.PreviewDomain = "preview.test" })
	body := zipBody(t, zipEntry{"index.html", "home", 0}, zipEntry{"assets/app.js", "ok", 0})
	got, w := publish(t, s, "application/zip", body, "")
	if w.Code != 201 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if got.Files != 2 || got.Bytes != 6 || got.URL != "http://"+got.ID+".preview.test/" {
		t.Fatalf("bad response: %+v", got)
	}
	r := httptest.NewRequest(http.MethodGet, "http://"+got.ID+".preview.test/assets/app.js", nil)
	r.Host = got.ID + ".preview.test"
	out := httptest.NewRecorder()
	s.ServeHTTP(out, r)
	if out.Code != 200 || out.Body.String() != "ok" {
		t.Fatalf("wildcard serve %d %q", out.Code, out.Body.String())
	}
	r = httptest.NewRequest(http.MethodPost, "http://"+got.ID+".preview.test/api/v1/publish", strings.NewReader("x"))
	r.Host = got.ID + ".preview.test"
	out = httptest.NewRecorder()
	s.ServeHTTP(out, r)
	if out.Code != 405 {
		t.Fatalf("preview host API isolation status=%d", out.Code)
	}
}

func TestZIPRejections(t *testing.T) {
	tests := []struct {
		name    string
		entries []zipEntry
		code    string
	}{
		{"traversal", []zipEntry{{"../index.html", "x", 0}}, "invalid_archive"},
		{"absolute", []zipEntry{{"/index.html", "x", 0}}, "invalid_archive"},
		{"windows absolute", []zipEntry{{"C:/index.html", "x", 0}}, "invalid_archive"},
		{"backslash traversal", []zipEntry{{"..\\index.html", "x", 0}}, "invalid_archive"},
		{"duplicate normalized", []zipEntry{{"index.html", "x", 0}, {"a/../index.html", "y", 0}}, "invalid_archive"},
		{"symlink", []zipEntry{{"index.html", "target", os.ModeSymlink | 0777}}, "invalid_archive"},
		{"missing index", []zipEntry{{"dist/index.html", "x", 0}}, "missing_index"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testServer(t, nil)
			_, w := publish(t, s, "application/zip", zipBody(t, tt.entries...), "")
			if w.Code != 400 {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			var e struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &e)
			if e.Error.Code != tt.code {
				t.Fatalf("code=%q body=%s", e.Error.Code, w.Body.String())
			}
		})
	}
}

func TestZIPLimitsAndUploadLimit(t *testing.T) {
	s := testServer(t, func(c *Config) { c.MaxFiles = 1; c.MaxExpandedSize = 5; c.MaxUploadBytes = 1024 })
	_, w := publish(t, s, "application/zip", zipBody(t, zipEntry{"index.html", "x", 0}, zipEntry{"x.txt", "x", 0}), "")
	if w.Code != 413 {
		t.Fatalf("file limit status=%d %s", w.Code, w.Body.String())
	}
	_, w = publish(t, s, "application/zip", zipBody(t, zipEntry{"index.html", "123456", 0}), "")
	if w.Code != 413 {
		t.Fatalf("size limit status=%d %s", w.Code, w.Body.String())
	}
	s2 := testServer(t, func(c *Config) { c.MaxUploadBytes = 3 })
	_, w = publish(t, s2, "text/html", []byte("1234"), "")
	if w.Code != 413 {
		t.Fatalf("upload limit status=%d %s", w.Code, w.Body.String())
	}
	entries, err := os.ReadDir(s2.sites)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed upload left visible or staged content: %v", entries)
	}
}

func TestExpirySweep(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := testServer(t, func(c *Config) { c.Now = func() time.Time { return now }; c.TTL = time.Hour })
	got, w := publish(t, s, "text/html", []byte("x"), "")
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	now = now.Add(2 * time.Hour)
	if err := s.Sweep(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.sites, got.ID)); !os.IsNotExist(err) {
		t.Fatalf("site remains: %v", err)
	}
	r := httptest.NewRequest(http.MethodGet, "http://x/p/"+got.ID+"/", nil)
	out := httptest.NewRecorder()
	s.ServeHTTP(out, r)
	if out.Code != 404 {
		t.Fatalf("expired serve status=%d", out.Code)
	}
}

func TestAPIValidationHealthAndDocs(t *testing.T) {
	s := testServer(t, nil)
	for _, tc := range []struct {
		ct, q  string
		body   []byte
		status int
		code   string
	}{{"application/json", "", []byte("{}"), 415, "unsupported_media_type"}, {"text/html", "?spa=maybe", []byte("x"), 400, "invalid_spa"}, {"text/html", "", nil, 400, "empty_body"}} {
		_, w := publish(t, s, tc.ct, tc.body, tc.q)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) {
			t.Fatalf("got %d %s", w.Code, w.Body.String())
		}
	}
	for _, url := range []string{"http://docs.test/healthz", "http://docs.test/", "http://docs.test/openapi.json", "http://docs.test/llms.txt", "http://docs.test/.well-known/agent.json"} {
		r := httptest.NewRequest(http.MethodGet, url, nil)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Errorf("%s status=%d", url, w.Code)
		}
		if strings.Contains(w.Body.String(), "{{ORIGIN}}") {
			t.Errorf("placeholder remains in %s", url)
		}
		if url == "http://docs.test/healthz" && !strings.Contains(w.Body.String(), `"storage":"writable"`) {
			t.Errorf("health response does not verify storage: %s", w.Body.String())
		}
		if url == "http://docs.test/" {
			for _, required := range []string{"POST", "HTML", "Markdown", "ZIP", "30 days", "Static builds only", "http://docs.test/api/v1/publish"} {
				if !strings.Contains(w.Body.String(), required) {
					t.Errorf("homepage is missing agent-critical text %q", required)
				}
			}
		}
	}
}

func TestPublishRateLimit(t *testing.T) {
	s := testServer(t, func(c *Config) { c.RateLimitPerHour = 1 })
	_, w := publish(t, s, "text/html", []byte("one"), "")
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	_, w = publish(t, s, "text/html", []byte("two"), "")
	if w.Code != 429 || w.Header().Get("Retry-After") == "" || !strings.Contains(w.Body.String(), "rate_limited") {
		t.Fatalf("status=%d headers=%v body=%s", w.Code, w.Header(), w.Body.String())
	}
}
