package server

import (
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
		config.RateLimitPerHour != 7 {
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
