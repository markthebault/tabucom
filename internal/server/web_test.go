/*
This file tests health responses and every embedded public discovery document.
It verifies routing, method policy, origin substitution, and required guide copy.
The password template remains private because web.go exposes only allowlisted paths.
It depends on shared isolated server fixtures and standard HTTP test and string APIs,
with table-driven cases covering the complete embedded web surface.
*/
package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHealthAndDiscoveryDocuments verifies every embedded public document, origin
// substitution, storage health semantics, and critical landing-page copy.
func TestHealthAndDiscoveryDocuments(t *testing.T) {
	server := testServer(t, nil)
	tests := []struct {
		url      string
		required []string
		absent   []string
	}{
		{
			url:      "http://docs.test/healthz",
			required: []string{`"status":"ok"`, `"storage":"writable"`},
		},
		{
			url: "http://docs.test/",
			required: []string{
				"Publish a front end. Get a URL.",
				"HTML",
				"Markdown",
				"immutable URL",
				`href="/agents"`,
			},
			absent: []string{"<nav", "<pre", "<table", "POST</span>"},
		},
		{
			url: "http://docs.test/agents",
			required: []string{
				"POST",
				"HTML",
				"Markdown",
				"ZIP",
				"ttl",
				"30 days",
				"Static builds only",
				"http://docs.test/api/v1/publish",
			},
		},
		{url: "http://docs.test/openapi.json"},
		{url: "http://docs.test/llms.txt"},
		{url: "http://docs.test/.well-known/agent.json"},
	}

	for _, test := range tests {
		t.Run(test.url, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.url, nil)
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			requireStatus(t, response, http.StatusOK)

			body := response.Body.String()
			if strings.Contains(body, "{{ORIGIN}}") {
				t.Fatalf("origin placeholder remains in %s", test.url)
			}
			for _, required := range test.required {
				if !strings.Contains(body, required) {
					t.Errorf("response is missing %q", required)
				}
			}
			for _, absent := range test.absent {
				if strings.Contains(body, absent) {
					t.Errorf("response unexpectedly contains %q", absent)
				}
			}
		})
	}
}

// TestDiscoveryHEADAndMethodPolicy confirms embedded assets support metadata-only
// reads while rejecting mutation methods with an explicit Allow header.
func TestDiscoveryHEADAndMethodPolicy(t *testing.T) {
	server := testServer(t, nil)

	head := httptest.NewRequest(http.MethodHead, "http://docs.test/openapi.json", nil)
	headResponse := httptest.NewRecorder()
	server.ServeHTTP(headResponse, head)
	requireStatus(t, headResponse, http.StatusOK)
	if headResponse.Body.Len() != 0 {
		t.Fatalf("HEAD returned a body: %q", headResponse.Body.String())
	}
	if headResponse.Header().Get("Content-Length") == "" {
		t.Fatal("HEAD response has no Content-Length")
	}

	post := httptest.NewRequest(http.MethodPost, "http://docs.test/openapi.json", nil)
	postResponse := httptest.NewRecorder()
	server.ServeHTTP(postResponse, post)
	requireStatus(t, postResponse, http.StatusMethodNotAllowed)
	if allow := postResponse.Header().Get("Allow"); allow != "GET, HEAD" {
		t.Fatalf("Allow=%q, want GET, HEAD", allow)
	}
}

// TestPublishRouteMethodPolicy keeps API failures machine-readable and advertises
// the only supported method.
func TestPublishRouteMethodPolicy(t *testing.T) {
	server := testServer(t, nil)
	request := httptest.NewRequest(http.MethodGet, "http://docs.test/api/v1/publish", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	requireStatus(t, response, http.StatusMethodNotAllowed)
	if allow := response.Header().Get("Allow"); allow != http.MethodPost {
		t.Fatalf("Allow=%q, want POST", allow)
	}
	if code := responseErrorCode(t, response); code != "method_not_allowed" {
		t.Fatalf("error code=%q, want=method_not_allowed", code)
	}
}
