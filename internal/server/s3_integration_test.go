package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// TestRustFSLifecycle is opt-in because it requires a real S3-compatible server.
// Run it with S3_TEST_ENDPOINT=http://127.0.0.1:19000 and AWS credentials set.
func TestRustFSLifecycle(t *testing.T) {
	endpoint := os.Getenv("S3_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("S3_TEST_ENDPOINT is not set")
	}

	bucket := "tabucom-test"
	config := DefaultConfig()
	config.DataDir = t.TempDir()
	config.S3Bucket = bucket
	config.S3Endpoint = endpoint
	config.S3PathStyle = true
	config.SweepInterval = time.Hour

	storage, err := newS3Storage(config)
	if err != nil {
		t.Fatal(err)
	}
	_, err = storage.client.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	if err != nil {
		// ponytail: a shared developer bucket may already exist; the lifecycle
		// operations below are the compatibility check that matters.
		t.Logf("CreateBucket: %v", err)
	}

	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	config.Now = func() time.Time { return now }
	server, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	health := httptest.NewRecorder()
	server.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "http://publisher.test/healthz", nil))
	requireStatus(t, health, http.StatusOK)

	published, response := publish(t, server, "application/zip", zipBody(t,
		zipEntry{name: "index.html", body: "<h1>RustFS</h1>"},
		zipEntry{name: "assets/app.js", body: "console.log('ok')"},
	), "?ttl=1h")
	requireStatus(t, response, http.StatusCreated)
	request := httptest.NewRequest(http.MethodGet, published.URL, nil)
	served := httptest.NewRecorder()
	server.ServeHTTP(served, request)
	if served.Code != http.StatusOK || served.Body.String() != "<h1>RustFS</h1>" {
		t.Fatalf("served status=%d body=%q", served.Code, served.Body.String())
	}
	asset := httptest.NewRecorder()
	server.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, published.URL+"assets/app.js", nil))
	if asset.Code != http.StatusOK || asset.Body.String() != "console.log('ok')" {
		t.Fatalf("asset status=%d body=%q", asset.Code, asset.Body.String())
	}

	protected, protectedResponse := publishWithPassword(t, server, "", "rustfs-password")
	requireStatus(t, protectedResponse, http.StatusCreated)
	locked := httptest.NewRecorder()
	server.ServeHTTP(locked, httptest.NewRequest(http.MethodGet, protected.URL, nil))
	requireStatus(t, locked, http.StatusUnauthorized)
	login := httptest.NewRecorder()
	server.ServeHTTP(login, passwordRequest(protected.URL, protected.Password))
	requireStatus(t, login, http.StatusSeeOther)
	unlockedRequest := httptest.NewRequest(http.MethodGet, protected.URL, nil)
	unlockedRequest.AddCookie(login.Result().Cookies()[0])
	unlocked := httptest.NewRecorder()
	server.ServeHTTP(unlocked, unlockedRequest)
	requireStatus(t, unlocked, http.StatusOK)

	now = published.ExpiresAt
	if err := server.Sweep(); err != nil {
		t.Fatal(err)
	}
	served = httptest.NewRecorder()
	server.ServeHTTP(served, request)
	requireStatus(t, served, http.StatusNotFound)
}
