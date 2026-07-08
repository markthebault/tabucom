/*
This file tests direct HTML publication, metadata, TTLs, and request validation.
It verifies the returned URL through the public router and committed filesystem.
Rate-limit cases protect the process-local publisher admission policy.
It depends on shared HTTP test helpers and standard filesystem and time packages,
with deterministic clocks used where exact expiry behavior matters.
*/
package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestPublishHTMLAndServe exercises the complete direct-HTML lifecycle: immutable
// ID allocation, response metadata, storage commit, ordinary serving, and SPA
// fallback. The clock is fixed so retention assertions are exact.
func TestPublishHTMLAndServe(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	server := testServer(t, func(config *Config) {
		config.Now = func() time.Time { return now }
	})

	published, response := publish(t, server, "text/html; charset=utf-8", []byte("<h1>Hello</h1>"), "?spa=1")
	requireStatus(t, response, http.StatusCreated)

	if !validID(published.ID) {
		t.Fatalf("invalid deployment ID %q", published.ID)
	}
	if published.URL != "http://publisher.test/p/"+published.ID+"/" {
		t.Fatalf("URL=%q", published.URL)
	}
	if published.Files != 1 || !published.SPA {
		t.Fatalf("unexpected metadata: %+v", published)
	}
	if !published.CreatedAt.Equal(now) || !published.ExpiresAt.Equal(now.Add(deploymentTTL)) {
		t.Fatalf("unexpected timestamps: %+v", published)
	}

	// Verify the returned URL itself, not a separately reconstructed storage path.
	request := httptest.NewRequest(http.MethodGet, published.URL, nil)
	served := httptest.NewRecorder()
	server.ServeHTTP(served, request)
	if served.Code != http.StatusOK || !strings.Contains(served.Body.String(), "<h1>Hello</h1>") || !strings.Contains(served.Body.String(), copyHTMLMarker) {
		t.Fatalf("deployment status=%d body=%q", served.Code, served.Body.String())
	}
	raw := httptest.NewRecorder()
	server.ServeHTTP(raw, httptest.NewRequest(http.MethodGet, published.URL+"?raw=1", nil))
	if raw.Code != http.StatusOK || raw.Body.String() != "<h1>Hello</h1>" {
		t.Fatalf("raw deployment status=%d body=%q", raw.Code, raw.Body.String())
	}

	// Extensionless navigation falls back only because this publish enabled SPA.
	request = httptest.NewRequest(http.MethodGet, published.URL+"dashboard/settings", nil)
	served = httptest.NewRecorder()
	server.ServeHTTP(served, request)
	requireStatus(t, served, http.StatusOK)

	// The private manifest must exist for expiry checks, but serving tests below
	// ensure it cannot be fetched through the deployment URL.
	if _, err := os.Stat(filepath.Join(server.sites, published.ID, metadataName)); err != nil {
		t.Fatalf("metadata missing: %v", err)
	}
}

// TestPublishCustomTTL ensures a client-requested positive retention duration
// replaces, rather than extends, the configured default.
func TestPublishCustomTTL(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	server := testServer(t, func(config *Config) {
		config.Now = func() time.Time { return now }
	})

	published, response := publish(t, server, "text/html", []byte("<h1>Hello</h1>"), "?ttl=48h")
	requireStatus(t, response, http.StatusCreated)
	if !published.ExpiresAt.Equal(now.Add(48 * time.Hour)) {
		t.Fatalf("ExpiresAt=%s, want=%s", published.ExpiresAt, now.Add(48*time.Hour))
	}
}

func TestPublishPrefix(t *testing.T) {
	server := testServer(t, nil)
	published, response := publish(t, server, "text/html", []byte("<h1>Hello</h1>"), "?prefix=my-super-website")
	requireStatus(t, response, http.StatusCreated)

	if !strings.HasPrefix(published.ID, "my-super-website-") || !validID(published.ID) {
		t.Fatalf("invalid prefixed ID %q", published.ID)
	}
	if suffix := strings.TrimPrefix(published.ID, "my-super-website-"); len(suffix) != prefixedIDSuffixHexLength {
		t.Fatalf("suffix length=%d, want=%d", len(suffix), prefixedIDSuffixHexLength)
	}
	if published.URL != "http://publisher.test/p/"+published.ID+"/" {
		t.Fatalf("URL=%q", published.URL)
	}

	served := httptest.NewRecorder()
	server.ServeHTTP(served, httptest.NewRequest(http.MethodGet, published.URL, nil))
	if served.Code != http.StatusOK || !strings.Contains(served.Body.String(), "<h1>Hello</h1>") || !strings.Contains(served.Body.String(), copyHTMLMarker) {
		t.Fatalf("deployment status=%d body=%q", served.Code, served.Body.String())
	}
}

// TestPublishValidation checks that malformed requests fail with stable API codes
// before any deployment becomes visible.
func TestPublishValidation(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		query       string
		body        []byte
		status      int
		code        string
	}{
		{name: "unsupported type", contentType: "application/json", body: []byte("{}"), status: http.StatusUnsupportedMediaType, code: "unsupported_media_type"},
		{name: "invalid SPA", contentType: "text/html", query: "?spa=maybe", body: []byte("x"), status: http.StatusBadRequest, code: "invalid_spa"},
		{name: "zero TTL", contentType: "text/html", query: "?ttl=0", body: []byte("x"), status: http.StatusBadRequest, code: "invalid_ttl"},
		{name: "uppercase prefix", contentType: "text/html", query: "?prefix=My-Website", body: []byte("x"), status: http.StatusBadRequest, code: "invalid_prefix"},
		{name: "punctuated prefix", contentType: "text/html", query: "?prefix=my.website", body: []byte("x"), status: http.StatusBadRequest, code: "invalid_prefix"},
		{name: "dangling prefix hyphen", contentType: "text/html", query: "?prefix=my-website-", body: []byte("x"), status: http.StatusBadRequest, code: "invalid_prefix"},
		{name: "long prefix", contentType: "text/html", query: "?prefix=abcdefghijklmnopqrstuvwxyz0123456789abcdefghijk", body: []byte("x"), status: http.StatusBadRequest, code: "invalid_prefix"},
		{name: "empty body", contentType: "text/html", status: http.StatusBadRequest, code: "empty_body"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := testServer(t, nil)
			_, response := publish(t, server, test.contentType, test.body, test.query)
			requireStatus(t, response, test.status)
			if code := responseErrorCode(t, response); code != test.code {
				t.Fatalf("error code=%q, want=%q", code, test.code)
			}

			entries, err := os.ReadDir(server.sites)
			if err != nil {
				t.Fatalf("read sites directory: %v", err)
			}
			if len(entries) != 0 {
				t.Fatalf("failed request left storage entries: %v", entries)
			}
		})
	}
}

// TestPublishRateLimit verifies fixed-window enforcement and retry guidance.
func TestPublishRateLimit(t *testing.T) {
	server := testServer(t, func(config *Config) {
		config.RateLimitPerHour = 1
	})

	_, first := publish(t, server, "text/html", []byte("one"), "")
	requireStatus(t, first, http.StatusCreated)

	_, second := publish(t, server, "text/html", []byte("two"), "")
	requireStatus(t, second, http.StatusTooManyRequests)
	if second.Header().Get("Retry-After") == "" {
		t.Fatal("rate-limited response has no Retry-After header")
	}
	if code := responseErrorCode(t, second); code != "rate_limited" {
		t.Fatalf("error code=%q, want=rate_limited", code)
	}
}
