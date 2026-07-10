/*
This file tests optional stateless publish token behavior.
It covers the disabled default, missing credentials, bad signatures, expiry,
and the successful bearer-token publish path through the public router.
The tests use package helpers so token checks stay close to real HTTP handling.
It depends on standard HTTP test APIs and deterministic time only.
*/
package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const testSigningSecret = "12345678901234567890123456789012"

func TestPublishTokenUsesConfiguredTTL(t *testing.T) {
	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	server := testServer(t, func(config *Config) {
		config.Now = func() time.Time { return now }
		config.StatelessPublishTokensEnabled = true
		config.StatelessTokenSigningSecret = testSigningSecret
		config.StatelessPublishTokenTTL = 30 * 24 * time.Hour
	})
	request := httptest.NewRequest(http.MethodPost, "http://publisher.test/api/v1/publish-token", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	requireStatus(t, response, http.StatusCreated)

	var generated tokenResponse
	if err := json.Unmarshal(response.Body.Bytes(), &generated); err != nil {
		t.Fatalf("decode token response: %v", err)
	}
	if generated.TTLSeconds != int64((30*24*time.Hour).Seconds()) || !generated.ExpiresAt.Equal(now.Add(30*24*time.Hour)) {
		t.Fatalf("unexpected token response: %+v", generated)
	}
}

func TestStatelessPublishTokensDisabledKeepsPublishOpen(t *testing.T) {
	server := testServer(t, nil)

	_, response := publish(t, server, "text/html", []byte("open"), "")
	requireStatus(t, response, http.StatusCreated)
}

func TestStatelessPublishTokensRejectMissingInvalidAndExpired(t *testing.T) {
	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	server := testServer(t, func(config *Config) {
		config.Now = func() time.Time { return now }
		config.StatelessPublishTokensEnabled = true
		config.StatelessTokenSigningSecret = testSigningSecret
	})
	expired, _, err := server.newPublishToken(now.Add(-2*time.Hour), time.Hour)
	if err != nil {
		t.Fatalf("newPublishToken() error: %v", err)
	}

	tests := []struct {
		name   string
		header string
	}{
		{name: "missing"},
		{name: "invalid", header: "Bearer not-a-token"},
		{name: "expired", header: "Bearer " + expired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := publishWithAuthorization(t, server, test.header)
			requireStatus(t, response, http.StatusUnauthorized)
			if code := responseErrorCode(t, response); code != "unauthorized" {
				t.Fatalf("error code=%q, want=unauthorized", code)
			}
		})
	}
}

func TestStatelessPublishTokensAcceptValidPublishToken(t *testing.T) {
	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	server := testServer(t, func(config *Config) {
		config.Now = func() time.Time { return now }
		config.StatelessPublishTokensEnabled = true
		config.StatelessTokenSigningSecret = testSigningSecret
	})
	token, _, err := server.newPublishToken(now, time.Hour)
	if err != nil {
		t.Fatalf("newPublishToken() error: %v", err)
	}

	response := publishWithAuthorization(t, server, "Bearer "+token)
	requireStatus(t, response, http.StatusCreated)
}

func publishWithAuthorization(t *testing.T, server *Server, authorization string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "http://publisher.test/api/v1/publish", bytes.NewReader([]byte("x")))
	request.Header.Set("Content-Type", "text/html")
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}
