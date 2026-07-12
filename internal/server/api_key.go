/*
This file implements optional operator-managed API keys for publishing.
Keys are read once from configuration and apply only to the publish endpoint.
They are intended for small self-hosted installations with a known publisher.
The comparison hashes both values before a constant-time equality check so a
request cannot learn a configured key from early string-comparison exits.
It depends only on standard cryptography, HTTP, and string helper packages.
*/
package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
)

// apiKeysFromEnv parses the comma-separated operator allow-list. Whitespace
// around entries is ignored, while empty or duplicate entries fail startup so a
// typo never silently changes the intended access policy.
func apiKeysFromEnv(value string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}

	keys := make([]string, 0, strings.Count(value, ",")+1)
	seen := make(map[string]struct{}, cap(keys))
	for _, value := range strings.Split(value, ",") {
		key := strings.TrimSpace(value)
		if key == "" {
			return nil, errors.New("PUBLISH_API_KEYS must not contain empty keys")
		}
		if _, exists := seen[key]; exists {
			return nil, errors.New("PUBLISH_API_KEYS must not contain duplicate keys")
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	return keys, nil
}

// requirePublishAPIKey accepts every request when no keys are configured. Once
// enabled, exactly one configured value must be sent in X-API-Key; query-string
// credentials are deliberately unsupported to keep keys out of URLs and logs.
func (s *Server) requirePublishAPIKey(r *http.Request) bool {
	if len(s.cfg.PublishAPIKeys) == 0 {
		return true
	}

	presented := sha256.Sum256([]byte(r.Header.Get("X-API-Key")))
	matched := 0
	for _, configured := range s.cfg.PublishAPIKeys {
		expected := sha256.Sum256([]byte(configured))
		matched |= subtle.ConstantTimeCompare(presented[:], expected[:])
	}
	return matched == 1
}
