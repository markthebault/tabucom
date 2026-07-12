/*
This file defines Tabucom's complete runtime configuration contract.
It provides defaults, reads supported environment variables, and validates them.
The executable consumes Config while server components use its explicit limits.
It depends only on standard environment, conversion, error, and time packages,
keeping operational configuration independent from storage implementations.
*/
package server

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	deploymentTTL            = 30 * 24 * time.Hour
	statelessPublishTokenTTL = time.Hour
)

// Config contains server behavior, storage settings, and operational limits.
// Fields remain explicit instead of accepting arbitrary environment keys so a
// deployment has a small, reviewable configuration surface.
type Config struct {
	// ListenAddr is the address passed to http.Server by the executable.
	ListenAddr string
	// DataDir is the parent of the private deployment storage directory.
	DataDir string
	// BaseURL overrides request-derived public API URLs when set.
	BaseURL string
	// PreviewDomain enables isolated wildcard deployment origins when set.
	PreviewDomain string
	S3Bucket      string
	S3Endpoint    string
	S3Region      string
	S3Prefix      string
	S3PathStyle   bool

	// TTL is the default retention used when a client omits the ttl query value.
	TTL time.Duration
	// SweepInterval controls how often expired and abandoned data is removed.
	SweepInterval time.Duration

	// MaxUploadBytes limits compressed archives and direct text request bodies.
	MaxUploadBytes int64
	// MaxExpandedSize limits ZIP expansion and rendered Markdown output.
	MaxExpandedSize int64
	// MaxFiles limits all ZIP entries, including directories.
	MaxFiles int
	// RateLimitPerHour limits publications per remote address in this process.
	RateLimitPerHour int
	// PublishAPIKeys optionally requires one configured X-API-Key value to publish.
	PublishAPIKeys []string
	// StatelessPublishTokensEnabled requires signed bearer tokens on publishing.
	StatelessPublishTokensEnabled bool
	// StatelessTokenSigningSecret signs stateless publish tokens when enabled.
	StatelessTokenSigningSecret string
	// StatelessPublishTokenTTL controls how long generated publish tokens remain valid.
	StatelessPublishTokenTTL time.Duration

	// Now is injectable for deterministic expiry and rate-limit tests.
	Now func() time.Time
}

// DefaultConfig returns conservative production defaults. Callers may override
// them directly in tests or through ConfigFromEnv in the executable.
func DefaultConfig() Config {
	return Config{
		ListenAddr:               ":8080",
		DataDir:                  "./data",
		S3Region:                 "us-east-1",
		TTL:                      deploymentTTL,
		SweepInterval:            time.Hour,
		MaxUploadBytes:           100 << 20,
		MaxExpandedSize:          500 << 20,
		MaxFiles:                 10000,
		RateLimitPerHour:         60,
		StatelessPublishTokenTTL: statelessPublishTokenTTL,
		Now:                      time.Now,
	}
}

// ConfigFromEnv overlays supported environment variables on the defaults and
// validates the complete result. Environment values are normalized here so the
// rest of the package can rely on stable URL and domain forms.
func ConfigFromEnv() (Config, error) {
	cfg := DefaultConfig()

	if value := os.Getenv("LISTEN_ADDR"); value != "" {
		cfg.ListenAddr = value
	}
	if value := os.Getenv("PORT"); value != "" {
		port, err := strconv.Atoi(value)
		if err != nil || port < 1 || port > 65535 {
			return cfg, errors.New("PORT must be an integer between 1 and 65535")
		}
		// PORT intentionally wins over LISTEN_ADDR for common platform hosts.
		cfg.ListenAddr = ":" + value
	}
	if value := os.Getenv("DATA_DIR"); value != "" {
		cfg.DataDir = value
	}

	// PUBLIC_API_URL is the documented name. BASE_URL remains a compatibility
	// fallback for existing installations.
	cfg.BaseURL = strings.TrimRight(os.Getenv("PUBLIC_API_URL"), "/")
	if cfg.BaseURL == "" {
		cfg.BaseURL = strings.TrimRight(os.Getenv("BASE_URL"), "/")
	}
	cfg.PreviewDomain = strings.ToLower(strings.TrimSuffix(os.Getenv("PREVIEW_DOMAIN"), "."))
	cfg.S3Bucket = strings.TrimSpace(os.Getenv("S3_BUCKET"))
	cfg.S3Endpoint = strings.TrimRight(strings.TrimSpace(os.Getenv("S3_ENDPOINT")), "/")
	cfg.S3Region = strings.TrimSpace(os.Getenv("S3_REGION"))
	if cfg.S3Region == "" {
		cfg.S3Region = "us-east-1"
	}
	cfg.S3Prefix = strings.Trim(strings.TrimSpace(os.Getenv("S3_PREFIX")), "/")
	if value := os.Getenv("S3_PATH_STYLE"); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return cfg, errors.New("S3_PATH_STYLE must be true or false")
		}
		cfg.S3PathStyle = parsed
	}

	var err error
	if cfg.TTL, err = durationFromEnv("TTL", cfg.TTL); err != nil {
		return cfg, err
	}
	if cfg.SweepInterval, err = durationFromEnv("SWEEP_INTERVAL", cfg.SweepInterval); err != nil {
		return cfg, err
	}
	if cfg.MaxUploadBytes, err = int64FromEnv("MAX_UPLOAD_BYTES", cfg.MaxUploadBytes); err != nil {
		return cfg, err
	}
	if cfg.MaxExpandedSize, err = int64FromEnv("MAX_EXPANDED_BYTES", cfg.MaxExpandedSize); err != nil {
		return cfg, err
	}
	if cfg.MaxFiles, err = intFromEnv("MAX_FILES", cfg.MaxFiles); err != nil {
		return cfg, err
	}
	if cfg.RateLimitPerHour, err = intFromEnv("RATE_LIMIT_PER_HOUR", cfg.RateLimitPerHour); err != nil {
		return cfg, err
	}
	if cfg.PublishAPIKeys, err = apiKeysFromEnv(os.Getenv("PUBLISH_API_KEYS")); err != nil {
		return cfg, err
	}
	if cfg.StatelessPublishTokenTTL, err = durationFromEnv("STATELESS_PUBLISH_TOKEN_TTL", cfg.StatelessPublishTokenTTL); err != nil {
		return cfg, err
	}
	if value := os.Getenv("STATELESS_PUBLISH_TOKENS_ENABLED"); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return cfg, errors.New("STATELESS_PUBLISH_TOKENS_ENABLED must be true or false")
		}
		cfg.StatelessPublishTokensEnabled = parsed
	}
	cfg.StatelessTokenSigningSecret = os.Getenv("STATELESS_TOKEN_SIGNING_SECRET")

	return cfg, cfg.validate()
}

// durationFromEnv parses one optional duration while preserving its setting name
// in errors, which makes deployment failures actionable.
func durationFromEnv(name string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback, fmt.Errorf("%s: %w", name, err)
	}
	return parsed, nil
}

// int64FromEnv parses byte-oriented limits without narrowing them to int.
func int64FromEnv(name string, fallback int64) (int64, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback, fmt.Errorf("%s: %w", name, err)
	}
	return parsed, nil
}

// intFromEnv parses count-oriented limits using the platform's native int size.
func intFromEnv(name string, fallback int) (int, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback, fmt.Errorf("%s: %w", name, err)
	}
	return parsed, nil
}

// validate rejects values that would disable retention or resource bounds. The
// service never supports unlimited uploads, expansion, entry counts, or TTLs.
func (cfg Config) validate() error {
	if cfg.DataDir == "" || cfg.TTL <= 0 || cfg.SweepInterval <= 0 || cfg.StatelessPublishTokenTTL <= 0 ||
		cfg.MaxUploadBytes <= 0 || cfg.MaxExpandedSize <= 0 ||
		cfg.MaxFiles <= 0 || cfg.RateLimitPerHour <= 0 {
		return errors.New("data directory, durations, and limits must be positive")
	}
	if cfg.S3Bucket != "" && cfg.S3Region == "" {
		return errors.New("S3_REGION must not be empty when S3_BUCKET is set")
	}
	if cfg.StatelessPublishTokensEnabled && len(cfg.StatelessTokenSigningSecret) < 32 {
		return errors.New("STATELESS_TOKEN_SIGNING_SECRET must be at least 32 bytes when stateless publish tokens are enabled")
	}
	return nil
}
