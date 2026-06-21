// Package server implements Tabucom's HTTP API and immutable static-site host.
//
// The package deliberately separates request routing from publication, archive
// extraction, storage, rendering, and configuration. Keeping those boundaries
// explicit makes the security properties of uploaded content easier to audit.
package server

import (
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Server owns the storage root and the small amount of mutable process state.
// Published deployments themselves are immutable after their staging directory
// is atomically renamed into the sites directory.
type Server struct {
	cfg   Config
	sites string
	s3    *s3Storage

	// stop and done coordinate the expiry worker without leaking a goroutine.
	// closeOnce makes Close safe for callers that share server ownership.
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once

	// Rate-limit state is process-local by design. The mutex covers both request
	// accounting and stale-bucket cleanup performed by Sweep.
	rateMu sync.Mutex
	rates  map[string]rateBucket
}

// New validates the complete configuration before creating storage or starting
// background work. An initial sweep prevents expired deployments from becoming
// visible during the first periodic-cleanup interval after a restart.
func New(cfg Config) (*Server, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	sites := filepath.Join(cfg.DataDir, "sites")
	if err := os.MkdirAll(sites, 0750); err != nil {
		return nil, err
	}

	s := &Server{
		cfg:   cfg,
		sites: sites,
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
		rates: make(map[string]rateBucket),
	}
	if cfg.S3Bucket != "" {
		var err error
		s.s3, err = newS3Storage(cfg)
		if err != nil {
			return nil, err
		}
	}
	if err := s.Sweep(); err != nil {
		// Cleanup failure should be observable, but it should not prevent healthy
		// deployments from being served while operators repair stale entries.
		slog.Warn("initial expiry sweep", "error", err)
	}
	go s.sweeper()
	return s, nil
}

// Close stops background cleanup and waits until the worker has exited.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		close(s.stop)
		<-s.done
	})
}

// sweeper serializes periodic expiry work through Sweep. A ticker is created in
// the worker so all of its resources have the same lifetime as the goroutine.
func (s *Server) sweeper() {
	defer close(s.done)
	ticker := time.NewTicker(s.cfg.SweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := s.Sweep(); err != nil {
				slog.Warn("expiry sweep", "error", err)
			}
		case <-s.stop:
			return
		}
	}
}

// ServeHTTP is intentionally limited to dispatch. Each endpoint's policy and
// mechanics live with that endpoint, keeping route precedence visible here.
// Fixed routes are matched by exact path; deployment recognition is the only
// prefix-based route. That distinction prevents documentation or API lookalikes
// from being accepted accidentally.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// A preview subdomain is a static origin, never an API origin. It must be
	// checked before API routes so uploaded pages cannot reach publisher routes
	// through their isolated host name.
	if id, requested, ok := s.wildcardSiteRequest(r); ok {
		s.serveSiteRequest(w, r, id, requested)
		return
	}

	switch {
	case r.URL.Path == "/healthz" && r.Method == http.MethodGet:
		s.health(w)
	case r.URL.Path == "/api/v1/publish" && r.Method == http.MethodPost:
		s.publish(w, r)
	case r.URL.Path == "/api/v1/publish":
		w.Header().Set("Allow", http.MethodPost)
		apiError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
	case isWebPath(r.URL.Path) && isReadMethod(r.Method):
		s.serveWebFile(w, r)
	case isWebPath(r.URL.Path):
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	default:
		s.serveFallbackRoute(w, r)
	}
}

// serveFallbackRoute handles deployment paths, the landing page, and the final
// 404. Separating it keeps the fixed API and discovery routes easy to scan.
// The landing page intentionally accepts GET only, unlike embedded discovery
// assets whose explicit routing policy also permits HEAD.
func (s *Server) serveFallbackRoute(w http.ResponseWriter, r *http.Request) {
	if id, requested, ok := s.pathSiteRequest(r); ok {
		s.serveSiteRequest(w, r, id, requested)
		return
	}
	if r.URL.Path == "/" && r.Method == http.MethodGet {
		s.landing(w, r)
		return
	}
	http.NotFound(w, r)
}

// serveSiteRequest permits POST only so protected deployments can accept the
// built-in password form. Unprotected deployments reject it after metadata load.
func (s *Server) serveSiteRequest(w http.ResponseWriter, r *http.Request, id, requested string) {
	if !isReadMethod(r.Method) && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, HEAD, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.serveSite(w, r, id, requested)
}

// health verifies actual write and delete operations in the deployment volume.
// Merely checking that the directory exists would miss read-only mounts and
// permission changes that make new publications impossible.
func (s *Server) health(w http.ResponseWriter) {
	if s.s3 != nil {
		if err := s.s3.health(); err != nil {
			apiError(w, http.StatusServiceUnavailable, "service_unavailable", "storage health check failed")
			return
		}
		jsonReply(w, http.StatusOK, map[string]any{"status": "ok", "storage": "s3", "ttlSeconds": int64(s.cfg.TTL.Seconds()), "publishLimit": s.cfg.RateLimitPerHour})
		return
	}
	file, err := os.CreateTemp(s.sites, ".health-")
	if err != nil {
		apiError(w, http.StatusServiceUnavailable, "service_unavailable", "storage is not writable")
		return
	}

	name := file.Name()
	if err := errors.Join(file.Close(), os.Remove(name)); err != nil {
		apiError(w, http.StatusServiceUnavailable, "service_unavailable", "storage health check failed")
		return
	}

	jsonReply(w, http.StatusOK, map[string]any{
		"status":       "ok",
		"storage":      "writable",
		"ttlSeconds":   int64(s.cfg.TTL.Seconds()),
		"publishLimit": s.cfg.RateLimitPerHour,
	})
}

// isReadMethod centralizes the static-resource method policy.
func isReadMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}
