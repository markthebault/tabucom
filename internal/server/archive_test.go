/*
This file tests successful and hostile ZIP publication scenarios.
It verifies archive limits, traversal defenses, atomic cleanup, and serving.
Tests exercise requests through the complete Server HTTP routing boundary.
It depends on server_test.go helpers and synthetic ZIP fixtures,
plus standard HTTP, filesystem, string, and testing packages.
*/
package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestPublishZIPAssetsAndWildcardServing verifies successful archive expansion,
// expanded metadata accounting, the returned wildcard URL, asset serving, and
// preview-origin API isolation.
func TestPublishZIPAssetsAndWildcardServing(t *testing.T) {
	server := testServer(t, func(config *Config) {
		config.PreviewDomain = "preview.test"
	})
	body := zipBody(t,
		zipEntry{name: "index.html", body: "home"},
		zipEntry{name: "assets/app.js", body: "ok"},
	)

	published, response := publish(t, server, "application/zip", body, "")
	requireStatus(t, response, http.StatusCreated)
	if published.Files != 2 || published.Bytes != 6 {
		t.Fatalf("unexpected expanded metadata: %+v", published)
	}
	if published.URL != "http://"+published.ID+".preview.test/" {
		t.Fatalf("URL=%q", published.URL)
	}

	// Fetch an asset through the exact origin returned to the publishing client.
	request := httptest.NewRequest(http.MethodGet, published.URL+"assets/app.js", nil)
	request.Host = published.ID + ".preview.test"
	served := httptest.NewRecorder()
	server.ServeHTTP(served, request)
	if served.Code != http.StatusOK || served.Body.String() != "ok" {
		t.Fatalf("wildcard asset status=%d body=%q", served.Code, served.Body.String())
	}

	// A deployment subdomain must never become an alternate API entry point.
	request = httptest.NewRequest(http.MethodPost, published.URL+"api/v1/publish", strings.NewReader("x"))
	request.Host = published.ID + ".preview.test"
	served = httptest.NewRecorder()
	server.ServeHTTP(served, request)
	requireStatus(t, served, http.StatusMethodNotAllowed)
}

// TestZIPRejections covers normalized traversal, absolute paths, duplicate names,
// links, filesystem conflicts, excessive depth, and the root-index contract.
func TestZIPRejections(t *testing.T) {
	deepPath := strings.Repeat("level/", maxArchivePathDepth+1) + "index.html"
	tests := []struct {
		name    string
		entries []zipEntry
		code    string
	}{
		{name: "traversal", entries: []zipEntry{{name: "../index.html", body: "x"}}, code: "invalid_archive"},
		{name: "absolute", entries: []zipEntry{{name: "/index.html", body: "x"}}, code: "invalid_archive"},
		{name: "Windows absolute", entries: []zipEntry{{name: "C:/index.html", body: "x"}}, code: "invalid_archive"},
		{name: "backslash traversal", entries: []zipEntry{{name: "..\\index.html", body: "x"}}, code: "invalid_archive"},
		{name: "duplicate normalized", entries: []zipEntry{{name: "index.html", body: "x"}, {name: "a/../index.html", body: "y"}}, code: "invalid_archive"},
		{name: "symlink", entries: []zipEntry{{name: "index.html", body: "target", mode: os.ModeSymlink | 0777}}, code: "invalid_archive"},
		{name: "conflicting paths", entries: []zipEntry{{name: "index.html", body: "x"}, {name: "assets", body: "file"}, {name: "assets/app.js", body: "x"}}, code: "invalid_archive"},
		{name: "excessive depth", entries: []zipEntry{{name: deepPath, body: "x"}}, code: "invalid_archive"},
		{name: "missing root index", entries: []zipEntry{{name: "dist/index.html", body: "x"}}, code: "missing_index"},
		{name: "index is directory", entries: []zipEntry{{name: "index.html/", mode: os.ModeDir}}, code: "missing_index"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := testServer(t, nil)
			_, response := publish(t, server, "application/zip", zipBody(t, test.entries...), "")
			requireStatus(t, response, http.StatusBadRequest)
			if code := responseErrorCode(t, response); code != test.code {
				t.Fatalf("error code=%q, want=%q", code, test.code)
			}

			// Failed extraction must leave neither a visible deployment nor a stage.
			entries, err := os.ReadDir(server.sites)
			if err != nil {
				t.Fatalf("read sites directory: %v", err)
			}
			if len(entries) != 0 {
				t.Fatalf("rejected ZIP left storage entries: %v", entries)
			}
		})
	}
}

// TestZIPAndUploadLimits distinguishes compressed request size, expanded content
// size, and archive entry-count limits. Direct HTML shares the request-size limit.
func TestZIPAndUploadLimits(t *testing.T) {
	t.Run("file count", func(t *testing.T) {
		server := testServer(t, func(config *Config) {
			config.MaxFiles = 1
			config.MaxUploadBytes = 1024
		})
		body := zipBody(t,
			zipEntry{name: "index.html", body: "x"},
			zipEntry{name: "x.txt", body: "x"},
		)
		_, response := publish(t, server, "application/zip", body, "")
		requireStatus(t, response, http.StatusRequestEntityTooLarge)
		if code := responseErrorCode(t, response); code != "too_many_files" {
			t.Fatalf("error code=%q, want=too_many_files", code)
		}
	})

	t.Run("expanded size", func(t *testing.T) {
		server := testServer(t, func(config *Config) {
			config.MaxExpandedSize = 5
			config.MaxUploadBytes = 1024
		})
		body := zipBody(t, zipEntry{name: "index.html", body: "123456"})
		_, response := publish(t, server, "application/zip", body, "")
		requireStatus(t, response, http.StatusRequestEntityTooLarge)
	})

	t.Run("direct upload size and atomicity", func(t *testing.T) {
		server := testServer(t, func(config *Config) {
			config.MaxUploadBytes = 3
		})
		_, response := publish(t, server, "text/html", []byte("1234"), "")
		requireStatus(t, response, http.StatusRequestEntityTooLarge)

		entries, err := os.ReadDir(server.sites)
		if err != nil {
			t.Fatalf("read sites directory: %v", err)
		}
		if len(entries) != 0 {
			t.Fatalf("oversized upload left storage entries: %v", entries)
		}
	})

	t.Run("directories count as entries", func(t *testing.T) {
		server := testServer(t, func(config *Config) {
			config.MaxFiles = 2
			config.MaxUploadBytes = 2048
		})
		body := zipBody(t,
			zipEntry{name: "assets/", mode: os.ModeDir},
			zipEntry{name: "index.html", body: "x"},
			zipEntry{name: "assets/app.js", body: "ok"},
		)
		_, response := publish(t, server, "application/zip", body, "")
		requireStatus(t, response, http.StatusRequestEntityTooLarge)
		if code := responseErrorCode(t, response); code != "too_many_files" {
			t.Fatalf("error code=%q, want=too_many_files", code)
		}
	})
}

// TestInvalidZIPBody ensures transport type alone cannot make arbitrary bytes an
// archive and that the structural failure uses the documented archive code.
func TestInvalidZIPBody(t *testing.T) {
	server := testServer(t, nil)
	_, response := publish(t, server, "application/zip", []byte("not a ZIP"), "")
	requireStatus(t, response, http.StatusBadRequest)
	if code := responseErrorCode(t, response); code != "invalid_archive" {
		t.Fatalf("error code=%q, want=invalid_archive", code)
	}
}
