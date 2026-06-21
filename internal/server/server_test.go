/*
This file contains shared fixtures for server package behavior tests.
It creates isolated production-shaped servers and routes synthetic HTTP requests.
Helpers decode public responses, inspect errors, and build in-memory ZIP archives.
It depends on standard archive, JSON, HTTP test, filesystem, and time packages,
and is reused by each focused test file in this package.
*/
package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// testServer creates a production Server with storage isolated to the current
// test. Tests mutate only the settings relevant to their scenario, keeping the
// remaining limits representative of real operation.
//
// A real background sweeper is started because lifecycle behavior is part of the
// Server contract. The long interval prevents periodic work from introducing
// timing into behavior tests; tests that need cleanup invoke Sweep directly.
func testServer(t *testing.T, mutate func(*Config)) *Server {
	t.Helper()

	config := DefaultConfig()
	config.DataDir = t.TempDir()
	config.SweepInterval = time.Hour
	if mutate != nil {
		mutate(&config)
	}

	server, err := New(config)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	// Cleanup also verifies that every test stops the background sweeper.
	t.Cleanup(server.Close)
	return server
}

// publish sends a request through the public HTTP router. It intentionally does
// not call the publish handler directly, so tests cover route selection and
// response serialization in addition to publication mechanics.
//
// The helper decodes success bodies only. Failure tests use responseErrorCode,
// making accidental success impossible to interpret as a valid zero-value result.
func publish(t *testing.T, server *Server, contentType string, body []byte, query string) (publishResponse, *httptest.ResponseRecorder) {
	t.Helper()

	request := httptest.NewRequest(
		http.MethodPost,
		"http://publisher.test/api/v1/publish"+query,
		bytes.NewReader(body),
	)
	request.Header.Set("Content-Type", contentType)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	var published publishResponse
	if response.Code == http.StatusCreated {
		if err := json.Unmarshal(response.Body.Bytes(), &published); err != nil {
			t.Fatalf("decode publish response: %v", err)
		}
	}
	return published, response
}

// requireStatus reports the complete response body when an endpoint returns an
// unexpected status. This makes table-driven failures diagnosable in one line.
func requireStatus(t *testing.T, response *httptest.ResponseRecorder, want int) {
	t.Helper()
	if response.Code != want {
		t.Fatalf("status=%d, want=%d, body=%s", response.Code, want, response.Body.String())
	}
}

// responseErrorCode extracts the stable API error code used by client tests.
// It deliberately ignores human wording, which may improve without breaking API clients.
func responseErrorCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var envelope apiErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error response: %v; body=%s", err, response.Body.String())
	}
	return envelope.Error.Code
}

// zipEntry describes one synthetic archive header. Mode is optional so ordinary
// files use archive/zip's default regular-file representation.
type zipEntry struct {
	name string
	body string
	mode os.FileMode
}

// zipBody constructs archives in memory and avoids committing generated fixtures.
// Store compression also keeps limit tests deterministic.
//
// Header order is preserved. This is important for conflict tests where a regular
// file must exist before a later entry attempts to use it as a parent directory.
func zipBody(t *testing.T, entries ...zipEntry) []byte {
	t.Helper()

	var body bytes.Buffer
	writer := zip.NewWriter(&body)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Store}
		if entry.mode != 0 {
			header.SetMode(entry.mode)
		}
		file, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatalf("create ZIP entry %q: %v", entry.name, err)
		}
		if _, err := io.WriteString(file, entry.body); err != nil {
			t.Fatalf("write ZIP entry %q: %v", entry.name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close ZIP: %v", err)
	}
	return body.Bytes()
}
