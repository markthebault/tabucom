package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const metadataName = ".site.json"

// pathSiteRequest recognizes only valid deployment IDs under /p/{id}/. Invalid
// IDs deliberately fall through to the normal 404 path instead of touching disk.
func (s *Server) pathSiteRequest(r *http.Request) (id, requested string, ok bool) {
	if !strings.HasPrefix(r.URL.Path, "/p/") {
		return "", "", false
	}

	remainder := strings.TrimPrefix(r.URL.Path, "/p/")
	parts := strings.SplitN(remainder, "/", 2)
	if !validID(parts[0]) {
		return "", "", false
	}
	if len(parts) == 2 {
		requested = parts[1]
	}
	return parts[0], requested, true
}

// wildcardSiteRequest maps {id}.{PreviewDomain} to an isolated deployment. A
// port is removed before suffix matching to support local development hosts.
func (s *Server) wildcardSiteRequest(r *http.Request) (id, requested string, ok bool) {
	if s.cfg.PreviewDomain == "" {
		return "", "", false
	}

	host := strings.ToLower(r.Host)
	if hostname, _, err := net.SplitHostPort(host); err == nil {
		host = hostname
	}

	suffix := "." + s.cfg.PreviewDomain
	if !strings.HasSuffix(host, suffix) {
		return "", "", false
	}
	id = strings.TrimSuffix(host, suffix)
	if !validID(id) {
		return "", "", false
	}

	return id, strings.TrimPrefix(r.URL.Path, "/"), true
}

// serveSite resolves a file only within an immutable deployment. Expired or
// unreadable metadata makes the entire deployment indistinguishable from absent.
func (s *Server) serveSite(w http.ResponseWriter, r *http.Request, id, requested string) {
	if s.s3 != nil {
		s.serveS3Site(w, r, id, requested)
		return
	}
	root := filepath.Join(s.sites, id)
	metadata, err := readMetadata(root)
	now := s.cfg.Now().UTC()
	if err != nil || !metadata.ExpiresAt.After(now) {
		http.NotFound(w, r)
		return
	}

	requested = normalizeSitePath(requested, r.URL.Path)
	if requested == metadataName || strings.HasPrefix(requested, "../") {
		// Metadata is server-private even though it shares the deployment tree.
		http.NotFound(w, r)
		return
	}

	file, err := resolveSiteFile(root, requested)
	if err != nil && metadata.SPA && r.Method == http.MethodGet && path.Ext(requested) == "" {
		// SPA fallback applies only to extensionless GET navigation. Missing assets
		// and HEAD requests retain ordinary not-found semantics.
		file, err = resolveSiteFile(root, "index.html")
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// These headers constrain browser capabilities without changing uploaded
	// document bytes. The short cache lifetime cannot outlive deployment expiry.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
	w.Header().Set("Cache-Control", deploymentCacheControl(metadata.ExpiresAt, now))
	http.ServeFile(w, r, file)
}

// normalizeSitePath applies URL-path semantics before converting to a host path.
// Prefixing with slash makes path.Clean collapse traversal attempts inside a
// virtual root rather than preserving leading .. components.
func normalizeSitePath(requested, requestPath string) string {
	requested = path.Clean("/" + requested)
	requested = strings.TrimPrefix(requested, "/")
	if requested == "" || strings.HasSuffix(requestPath, "/") {
		requested = path.Join(requested, "index.html")
	}
	return requested
}

// resolveSiteFile accepts a regular file or a directory containing index.html.
// Symlinks cannot enter deployments through ZIP extraction, and deployments are
// immutable after publication.
func resolveSiteFile(root, requested string) (string, error) {
	file := filepath.Join(root, filepath.FromSlash(requested))
	info, err := os.Stat(file)
	if err == nil && info.IsDir() {
		file = filepath.Join(file, "index.html")
		info, err = os.Stat(file)
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("deployment path is not a regular file")
	}
	return file, nil
}

// deploymentCacheControl caps browser caching at five minutes and never allows
// a cached object to cross the deployment's exact expiry boundary.
func deploymentCacheControl(expiresAt, now time.Time) string {
	remaining := expiresAt.Sub(now)
	if remaining <= 0 {
		return "no-store"
	}
	if remaining < time.Second {
		return "public, max-age=0, must-revalidate"
	}

	maxAge := int(remaining / time.Second)
	if maxAge > 300 {
		maxAge = 300
	}
	return "public, max-age=" + strconv.Itoa(maxAge) + ", must-revalidate"
}

// readMetadata loads the private manifest used for expiry and SPA behavior.
func readMetadata(root string) (Metadata, error) {
	var metadata Metadata
	data, err := os.ReadFile(filepath.Join(root, metadataName))
	if err != nil {
		return metadata, err
	}
	err = json.Unmarshal(data, &metadata)
	return metadata, err
}

// writeMetadata stores the manifest before atomic publication.
func writeMetadata(root string, metadata Metadata) error {
	data, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, metadataName), data, 0640)
}

// Sweep removes expired deployments, invalid deployment directories, abandoned
// staging directories, and rate-limit buckets older than their fixed window.
func (s *Server) Sweep() error {
	if s.s3 != nil {
		s.removeExpiredRateBuckets(s.cfg.Now())
		return s.s3.sweep(s.cfg.Now().UTC())
	}
	entries, err := os.ReadDir(s.sites)
	if err != nil {
		return err
	}

	now := s.cfg.Now()
	s.removeExpiredRateBuckets(now)

	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".staging-") {
			s.removeStaleStage(entry, now)
			continue
		}
		if !entry.IsDir() || !validID(entry.Name()) {
			continue
		}

		root := filepath.Join(s.sites, entry.Name())
		metadata, metadataErr := readMetadata(root)
		if metadataErr != nil || !metadata.ExpiresAt.After(now) {
			// Invalid metadata fails closed because retention cannot be established.
			if removeErr := os.RemoveAll(root); removeErr != nil {
				err = errors.Join(err, removeErr)
			}
		}
	}

	return err
}

// removeExpiredRateBuckets bounds process memory independently of publication
// traffic by sharing the expiry sweep cadence.
func (s *Server) removeExpiredRateBuckets(now time.Time) {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()

	for client, bucket := range s.rates {
		if now.Sub(bucket.start) >= time.Hour {
			delete(s.rates, client)
		}
	}
}

// removeStaleStage removes only stages older than one hour. A live large upload
// is therefore not raced by ordinary periodic cleanup.
func (s *Server) removeStaleStage(entry os.DirEntry, now time.Time) {
	info, err := entry.Info()
	if err == nil && now.Sub(info.ModTime()) > time.Hour {
		_ = os.RemoveAll(filepath.Join(s.sites, entry.Name()))
	}
}

// requestBase returns the configured public origin or derives one from the
// request. Only exact http and https forwarded values are accepted.
func (s *Server) requestBase(r *http.Request) string {
	if s.cfg.BaseURL != "" {
		return s.cfg.BaseURL
	}

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := r.Header.Get("X-Forwarded-Proto"); forwarded == "http" || forwarded == "https" {
		scheme = forwarded
	}
	return scheme + "://" + r.Host
}

// siteURL constructs the immutable URL form selected by configuration.
func (s *Server) siteURL(r *http.Request, id string) string {
	if s.cfg.PreviewDomain == "" {
		return s.requestBase(r) + "/p/" + id + "/"
	}

	scheme := "https"
	if strings.HasPrefix(s.requestBase(r), "http://") {
		scheme = "http"
	}
	return scheme + "://" + id + "." + s.cfg.PreviewDomain + "/"
}

// randomID produces a lowercase RFC 4122 version-4 UUID without adding a runtime
// dependency. UUID shape is also the allowlist for deployment directory names.
func randomID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	bytes[6] = bytes[6]&0x0f | 0x40
	bytes[8] = bytes[8]&0x3f | 0x80

	encoded := hex.EncodeToString(bytes[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}

// validID accepts exactly the lowercase UUID representation generated above.
func validID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if character != '-' {
				return false
			}
			continue
		}
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}
