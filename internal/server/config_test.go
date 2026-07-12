/*
This file tests environment parsing, precedence, defaults, and validation.
It protects the operator-facing configuration contract used by cmd/tabucom.
S3 settings are checked as optional additions to the local-storage defaults.
It depends on Go's testing and time packages and the Config helpers,
with each test using isolated process environment values.
*/
package server

import (
	"reflect"
	"testing"
	"time"
)

// TestConfigFromEnv verifies precedence, normalization, default retention, and
// explicit duration overrides in one representative production configuration.
func TestConfigFromEnv(t *testing.T) {
	// PORT must override LISTEN_ADDR because platform runtimes commonly inject it.
	t.Setenv("LISTEN_ADDR", "127.0.0.1:9999")
	t.Setenv("PORT", "18080")
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("PUBLIC_API_URL", "https://publish.example.test/")
	t.Setenv("PREVIEW_DOMAIN", "Preview.Example.Test.")
	t.Setenv("TTL", "720h")
	t.Setenv("MAX_FILES", "25")
	t.Setenv("RATE_LIMIT_PER_HOUR", "7")
	t.Setenv("PUBLISH_API_KEYS", "first-key, second-key")
	t.Setenv("STATELESS_PUBLISH_TOKENS_ENABLED", "true")
	t.Setenv("STATELESS_TOKEN_SIGNING_SECRET", "12345678901234567890123456789012")
	t.Setenv("STATELESS_PUBLISH_TOKEN_TTL", "720h")

	config, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv() error: %v", err)
	}

	// 720h is the documented 30-day duration and should equal the default even
	// though it arrived through the environment.
	if config.ListenAddr != ":18080" ||
		config.BaseURL != "https://publish.example.test" ||
		config.PreviewDomain != "preview.example.test" ||
		config.TTL != deploymentTTL ||
		config.MaxFiles != 25 ||
		config.RateLimitPerHour != 7 ||
		!reflect.DeepEqual(config.PublishAPIKeys, []string{"first-key", "second-key"}) ||
		!config.StatelessPublishTokensEnabled ||
		config.StatelessPublishTokenTTL != 30*24*time.Hour ||
		config.StatelessTokenSigningSecret == "" {
		t.Fatalf("unexpected config: %+v", config)
	}

	// A client-independent retention override must remain supported.
	t.Setenv("TTL", "24h")
	config, err = ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv() with TTL override: %v", err)
	}
	if config.TTL != 24*time.Hour {
		t.Fatalf("TTL=%s, want=24h", config.TTL)
	}
}

func TestS3ConfigIsOptional(t *testing.T) {
	t.Setenv("S3_BUCKET", "")
	config, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.S3Bucket != "" {
		t.Fatalf("S3Bucket=%q", config.S3Bucket)
	}

	t.Setenv("S3_BUCKET", "deployments")
	t.Setenv("S3_ENDPOINT", "http://127.0.0.1:9000/")
	t.Setenv("S3_REGION", "auto")
	t.Setenv("S3_PREFIX", "/tabucom/")
	t.Setenv("S3_PATH_STYLE", "true")
	config, err = ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.S3Bucket != "deployments" || config.S3Endpoint != "http://127.0.0.1:9000" || config.S3Region != "auto" || config.S3Prefix != "tabucom" || !config.S3PathStyle {
		t.Fatalf("unexpected S3 config: %+v", config)
	}
}

// TestConfigFromEnvRejectsInvalidValues covers parse errors and values that parse
// successfully but would disable an operational resource bound.
func TestConfigFromEnvRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "non-numeric port", key: "PORT", value: "invalid"},
		{name: "out-of-range port", key: "PORT", value: "65536"},
		{name: "invalid duration", key: "TTL", value: "tomorrow"},
		{name: "zero duration", key: "SWEEP_INTERVAL", value: "0s"},
		{name: "invalid byte count", key: "MAX_UPLOAD_BYTES", value: "many"},
		{name: "negative expanded size", key: "MAX_EXPANDED_BYTES", value: "-1"},
		{name: "zero file count", key: "MAX_FILES", value: "0"},
		{name: "zero rate limit", key: "RATE_LIMIT_PER_HOUR", value: "0"},
		{name: "empty publish API key", key: "PUBLISH_API_KEYS", value: "first-key,,second-key"},
		{name: "duplicate publish API key", key: "PUBLISH_API_KEYS", value: "same-key,same-key"},
		{name: "invalid stateless token flag", key: "STATELESS_PUBLISH_TOKENS_ENABLED", value: "maybe"},
		{name: "invalid stateless token duration", key: "STATELESS_PUBLISH_TOKEN_TTL", value: "one-month"},
		{name: "zero stateless token duration", key: "STATELESS_PUBLISH_TOKEN_TTL", value: "0s"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Each subtest receives an independent environment snapshot from testing.
			t.Setenv(test.key, test.value)
			if _, err := ConfigFromEnv(); err == nil {
				t.Fatalf("ConfigFromEnv() accepted %s=%q", test.key, test.value)
			}
		})
	}
}

func TestConfigRejectsEnabledStatelessTokensWithoutSecret(t *testing.T) {
	t.Setenv("STATELESS_PUBLISH_TOKENS_ENABLED", "true")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("ConfigFromEnv() accepted enabled stateless tokens without a signing secret")
	}
}
