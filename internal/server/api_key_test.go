/*
This file tests optional publish API key admission behavior and discovery text.
It verifies that the allow-list protects writes while public read routes remain
unchanged, which is important for temporary links shared with visitors.
The tests use the normal HTTP router rather than calling publish directly.
They depend on shared server fixtures plus standard HTTP and byte packages.
*/
package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPublishAPIKeysRequireConfiguredHeaderForPublishing(t *testing.T) {
	server := testServer(t, func(config *Config) {
		config.PublishAPIKeys = []string{"primary-key", "rotated-key"}
	})

	for _, test := range []struct {
		name string
		key  string
		want int
	}{
		{name: "missing", want: http.StatusUnauthorized},
		{name: "wrong", key: "not-a-key", want: http.StatusUnauthorized},
		{name: "first allowed key", key: "primary-key", want: http.StatusCreated},
		{name: "second allowed key", key: "rotated-key", want: http.StatusCreated},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "http://publisher.test/api/v1/publish", bytes.NewReader([]byte("<h1>ok</h1>")))
			request.Header.Set("Content-Type", "text/html")
			if test.key != "" {
				request.Header.Set("X-API-Key", test.key)
			}
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			requireStatus(t, response, test.want)
		})
	}

	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://publisher.test/healthz", nil))
	requireStatus(t, response, http.StatusOK)
}

func TestPublishAPIKeyAndStatelessTokenAreBothRequired(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	server := testServer(t, func(config *Config) {
		config.Now = func() time.Time { return now }
		config.PublishAPIKeys = []string{"primary-key"}
		config.StatelessPublishTokensEnabled = true
		config.StatelessTokenSigningSecret = testSigningSecret
	})
	token, _, err := server.newPublishToken(now, time.Hour)
	if err != nil {
		t.Fatalf("newPublishToken() error: %v", err)
	}

	for _, test := range []struct {
		name          string
		apiKey        string
		authorization string
		want          int
	}{
		{name: "API key only", apiKey: "primary-key", want: http.StatusUnauthorized},
		{name: "token only", authorization: "Bearer " + token, want: http.StatusUnauthorized},
		{name: "both credentials", apiKey: "primary-key", authorization: "Bearer " + token, want: http.StatusCreated},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "http://publisher.test/api/v1/publish", bytes.NewReader([]byte("<h1>ok</h1>")))
			request.Header.Set("Content-Type", "text/html")
			if test.apiKey != "" {
				request.Header.Set("X-API-Key", test.apiKey)
			}
			if test.authorization != "" {
				request.Header.Set("Authorization", test.authorization)
			}
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			requireStatus(t, response, test.want)
		})
	}
}

func TestPublishAPIKeyDiscoveryInstructionsAppearOnlyWhenEnabled(t *testing.T) {
	disabled := testServer(t, nil)
	if body := getBody(t, disabled, "http://publisher.test/llms.txt"); strings.Contains(body, "TABUCOM_PUBLISH_API_KEY") {
		t.Fatal("open instance advertises API key instructions")
	}

	enabled := testServer(t, func(config *Config) {
		config.PublishAPIKeys = []string{"primary-key"}
	})
	for _, url := range []string{"http://publisher.test/llms.txt", "http://publisher.test/.well-known/agent.json"} {
		body := getBody(t, enabled, url)
		if !strings.Contains(body, "TABUCOM_PUBLISH_API_KEY") || !strings.Contains(body, "X-API-Key") {
			t.Fatalf("%s does not advertise API key authentication: %q", url, body)
		}
	}
}
