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
				"Available publish options",
				"prefix",
				"password",
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
		{
			url: "http://docs.test/llms.txt",
			required: []string{
				"publish with the defaults",
				"future publishes can set `ttl`, `prefix`, `spa`, or password protection",
			},
		},
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
			for _, stale := range []string{"tabucom.vps", "mark-thebault", "TABUCOM_ORIGIN", "publish.tools.company"} {
				if strings.Contains(body, stale) {
					t.Fatalf("response contains stale origin guidance %q", stale)
				}
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

func TestStatelessTokenInstructionsOnlyWhenEnabled(t *testing.T) {
	disabled := testServer(t, nil)
	disabledHome := getBody(t, disabled, "http://docs.test/")
	disabledLLMS := getBody(t, disabled, "http://docs.test/llms.txt")
	if strings.Contains(disabledHome, "Generate Publish Token") || strings.Contains(disabledLLMS, "TABUCOM_PUBLISH_TOKEN") {
		t.Fatal("disabled docs contain stateless token instructions")
	}
	if !strings.Contains(disabledLLMS, "Publishing requires no authentication") {
		t.Fatal("disabled llms.txt no longer keeps open-publish guidance")
	}
	if !strings.Contains(disabledHome, `fetch("/llms.txt"`) {
		t.Fatal("copy-instructions button no longer copies llms.txt")
	}

	enabled := testServer(t, func(config *Config) {
		config.StatelessPublishTokensEnabled = true
		config.StatelessTokenSigningSecret = testSigningSecret
	})
	enabledHome := getBody(t, enabled, "http://docs.test/")
	enabledLLMS := getBody(t, enabled, "http://docs.test/llms.txt")
	for _, required := range []string{"Generate Publish Token", "Token TTL: 1 hour", "This token lets your LLM publish a document"} {
		if !strings.Contains(enabledHome, required) {
			t.Fatalf("enabled home is missing %q", required)
		}
	}
	for _, required := range []string{"First generate a publish token", "TABUCOM_PUBLISH_TOKEN", "Authorization: Bearer $TABUCOM_PUBLISH_TOKEN"} {
		if !strings.Contains(enabledLLMS, required) {
			t.Fatalf("enabled llms.txt is missing %q", required)
		}
	}
	if strings.Contains(enabledLLMS, "Publishing requires no authentication") {
		t.Fatal("enabled llms.txt contains contradictory open-publish guidance")
	}
}

func getBody(t *testing.T, server *Server, url string) string {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, url, nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	requireStatus(t, response, http.StatusOK)
	return response.Body.String()
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
