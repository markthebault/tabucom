package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestExpirySweep proves that expiry is exact, storage is removed, and subsequent
// deployment requests return not found.
func TestExpirySweep(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	server := testServer(t, func(config *Config) {
		config.Now = func() time.Time { return now }
	})

	published, response := publish(t, server, "text/html", []byte("x"), "")
	requireStatus(t, response, http.StatusCreated)

	// Move beyond expiry before running the same cleanup method used by the worker.
	now = published.ExpiresAt.Add(time.Second)
	if err := server.Sweep(); err != nil {
		t.Fatalf("Sweep() error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(server.sites, published.ID)); !os.IsNotExist(err) {
		t.Fatalf("expired deployment remains: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, published.URL, nil)
	served := httptest.NewRecorder()
	server.ServeHTTP(served, request)
	requireStatus(t, served, http.StatusNotFound)
}

// TestServeCacheControlHonorsRemainingTTL checks both sub-second cache rounding
// and the exact instant at which a deployment stops being served.
func TestServeCacheControlHonorsRemainingTTL(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	server := testServer(t, func(config *Config) {
		config.Now = func() time.Time { return now }
	})
	published, response := publish(t, server, "text/html", []byte("x"), "")
	requireStatus(t, response, http.StatusCreated)

	// A positive duration below one second is served but never cached for a second.
	now = published.ExpiresAt.Add(-500 * time.Millisecond)
	request := httptest.NewRequest(http.MethodGet, published.URL, nil)
	served := httptest.NewRecorder()
	server.ServeHTTP(served, request)
	requireStatus(t, served, http.StatusOK)
	if cacheControl := served.Header().Get("Cache-Control"); cacheControl != "public, max-age=0, must-revalidate" {
		t.Fatalf("Cache-Control=%q", cacheControl)
	}

	// ExpiresAt is exclusive: equality is already expired.
	now = published.ExpiresAt
	served = httptest.NewRecorder()
	server.ServeHTTP(served, request)
	requireStatus(t, served, http.StatusNotFound)
}

// TestDeploymentServingPolicy verifies private metadata, traversal normalization,
// method restrictions, and the boundaries of SPA fallback.
func TestDeploymentServingPolicy(t *testing.T) {
	server := testServer(t, nil)
	published, response := publish(t, server, "text/html", []byte("home"), "?spa=true")
	requireStatus(t, response, http.StatusCreated)

	tests := []struct {
		name   string
		method string
		path   string
		status int
	}{
		{name: "private metadata", method: http.MethodGet, path: metadataName, status: http.StatusNotFound},
		{name: "normalized metadata traversal", method: http.MethodGet, path: "assets/../" + metadataName, status: http.StatusNotFound},
		{name: "missing asset has no SPA fallback", method: http.MethodGet, path: "app.js", status: http.StatusNotFound},
		{name: "HEAD navigation has no SPA fallback", method: http.MethodHead, path: "dashboard", status: http.StatusNotFound},
		{name: "POST forbidden", method: http.MethodPost, path: "", status: http.StatusMethodNotAllowed},
		{name: "GET navigation uses SPA fallback", method: http.MethodGet, path: "dashboard", status: http.StatusOK},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, published.URL+test.path, nil)
			served := httptest.NewRecorder()
			server.ServeHTTP(served, request)
			requireStatus(t, served, test.status)
		})
	}
}

// TestSweepRemovesStaleStages ensures cleanup distinguishes abandoned stages from
// an in-progress upload inside the one-hour safety window.
func TestSweepRemovesStaleStages(t *testing.T) {
	now := time.Date(2026, 1, 1, 2, 0, 0, 0, time.UTC)
	server := testServer(t, func(config *Config) {
		config.Now = func() time.Time { return now }
	})

	stale := filepath.Join(server.sites, ".staging-stale")
	recent := filepath.Join(server.sites, ".staging-recent")
	if err := os.Mkdir(stale, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(recent, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(stale, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}

	if err := server.Sweep(); err != nil {
		t.Fatalf("Sweep() error: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale stage remains: %v", err)
	}
	if _, err := os.Stat(recent); err != nil {
		t.Fatalf("recent stage was removed: %v", err)
	}
}
