package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

const (
	// Argon2id parameters follow the OWASP minimum profile. Keeping them fixed
	// also ensures deployments remain verifiable after a process restart.
	passwordMemory  = 19 * 1024
	passwordTime    = 2
	passwordThreads = 1
	passwordKeyLen  = 32
)

// passwordMetadata is private deployment state. Salt and Hash verify the human
// password; Token is the random bearer value issued only after verification.
// The plaintext password is returned to the publisher but never persisted.
type passwordMetadata struct {
	Salt  string `json:"salt"`
	Hash  string `json:"hash"`
	Token string `json:"token"`
}

// newPasswordMetadata creates independent verifier and session material for one
// immutable deployment. Randomness failures abort publication rather than
// creating a protected site with predictable credentials.
func newPasswordMetadata(password string) (*passwordMetadata, error) {
	// A unique salt prevents equal passwords producing equal stored hashes.
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	token, err := randomSecret(32)
	if err != nil {
		return nil, err
	}
	// The token avoids running Argon2id for every asset request after login.
	hash := argon2.IDKey([]byte(password), salt, passwordTime, passwordMemory, passwordThreads, passwordKeyLen)
	return &passwordMetadata{
		Salt:  base64.RawURLEncoding.EncodeToString(salt),
		Hash:  base64.RawURLEncoding.EncodeToString(hash),
		Token: token,
	}, nil
}

// matches reconstructs the persisted verifier and compares it in constant time.
// Malformed metadata fails closed so damaged deployments cannot bypass login.
func (metadata *passwordMetadata) matches(password string) bool {
	salt, err := base64.RawURLEncoding.DecodeString(metadata.Salt)
	if err != nil || len(salt) != 16 {
		return false
	}
	want, err := base64.RawURLEncoding.DecodeString(metadata.Hash)
	if err != nil || len(want) != passwordKeyLen {
		return false
	}
	// Argon2id is intentionally expensive; submission rate limiting happens
	// before this call to bound online guessing and resource consumption.
	got := argon2.IDKey([]byte(password), salt, passwordTime, passwordMemory, passwordThreads, passwordKeyLen)
	return subtle.ConstantTimeCompare(got, want) == 1
}

// authorizeSite gates both local and S3 content before path resolution. It
// returns true only when the caller may continue to static-file serving; every
// response generated here returns false to stop the serving pipeline.
func (s *Server) authorizeSite(w http.ResponseWriter, r *http.Request, id string, metadata Metadata) bool {
	protection := metadata.Password
	if protection == nil {
		return true
	}
	// Generated tokens are 32 random bytes encoded as 43 base64url characters.
	// Checking the shape prevents malformed empty metadata authorizing an empty cookie.
	if len(protection.Token) == 43 {
		if cookie, err := r.Cookie(siteCookieName(id)); err == nil &&
			subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(protection.Token)) == 1 {
			return true
		}
	}
	if r.Method != http.MethodPost {
		// Ordinary navigation receives the form without attempting password work.
		passwordForm(w, r, false, http.StatusUnauthorized)
		return false
	}

	retryAfter, allowed := s.allowPasswordAttempt(r, id)
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
		passwordForm(w, r, true, http.StatusTooManyRequests)
		return false
	}
	// Bound form parsing independently of the much larger publish body limit.
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	if err := r.ParseForm(); err != nil || !protection.matches(r.Form.Get("password")) {
		passwordForm(w, r, true, http.StatusUnauthorized)
		return false
	}

	now := s.cfg.Now().UTC()
	remaining := metadata.ExpiresAt.Sub(now)
	maxAge := int((remaining + time.Second - 1) / time.Second)
	if maxAge < 1 {
		maxAge = 1
	}
	// The cookie is host-only because Domain is omitted. HttpOnly prevents
	// uploaded JavaScript from reading the bearer token, while the narrow path
	// keeps path-mode deployments from sending it to unrelated deployments.
	http.SetCookie(w, &http.Cookie{
		Name: siteCookieName(id), Value: protection.Token, Path: siteCookiePath(r, id),
		Expires: metadata.ExpiresAt, MaxAge: maxAge, HttpOnly: true,
		Secure: strings.HasPrefix(s.requestBase(r), "https://"), SameSite: http.SameSiteLaxMode,
	})
	// Authentication responses and protected files must not enter shared caches.
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, r.URL.RequestURI(), http.StatusSeeOther)
	return false
}

// siteCookieName isolates cookie namespaces even if paths are normalized or a
// browser retains cookies from several deployments on the same host.
func siteCookieName(id string) string {
	return "tabucom_auth_" + strings.ReplaceAll(id, "-", "")
}

// siteCookiePath uses the deployment prefix in path mode and the host root for
// wildcard mode, where the deployment already owns an isolated origin.
func siteCookiePath(r *http.Request, id string) string {
	root := "/p/" + id
	if r.URL.Path == root {
		return root
	}
	if strings.HasPrefix(r.URL.Path, root+"/") {
		return root + "/"
	}
	return "/"
}

// The template is embedded into the binary with the other web assets and parsed
// once during package initialization. A syntax error therefore fails at startup.
var passwordPage = template.Must(template.ParseFS(embeddedWeb, "web/password-dialog.html.tmpl"))

// passwordForm renders initial, incorrect-password, and throttled states through
// one template. HEAD preserves status and headers without producing a body.
func passwordForm(w http.ResponseWriter, r *http.Request, invalid bool, status int) {
	// Inline styles are the only active page resource; scripts and external
	// resources remain blocked by the restrictive content security policy.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		// User input is never interpolated: only these fixed server messages reach
		// the template, and html/template escapes them defensively.
		message := ""
		if status == http.StatusTooManyRequests {
			message = "Too many attempts. Wait a moment, then try again."
		} else if invalid {
			message = "That password didn't work. Check it and try again."
		}
		_ = passwordPage.ExecuteTemplate(w, "password-dialog.html.tmpl", message)
	}
}
