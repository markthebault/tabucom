package server

// These tests stay at the HTTP boundary because password protection spans
// publication, persisted metadata, routing, browser forms, cookies, and serving.
// Assertions are grouped by lifecycle rather than individual helper so a failure
// identifies which externally observable security guarantee was broken.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// publishWithPassword exercises publication through the public router instead
// of calling an internal handler. That keeps header parsing, response encoding,
// and storage commit behavior inside every password test.
func publishWithPassword(t *testing.T, server *Server, query, password string) (publishResponse, *httptest.ResponseRecorder) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "http://publisher.test/api/v1/publish"+query, bytes.NewReader([]byte("<h1>secret</h1>")))
	request.Header.Set("Content-Type", "text/html")
	// An empty value means the test is exercising generated or absent protection;
	// explicit empty-header validation is covered separately below.
	if password != "" {
		request.Header.Set("Tabucom-Password", password)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	// Failure responses have a different envelope, so decode only successful
	// publications and let each caller assert the actual status first.
	var published publishResponse
	if response.Code == http.StatusCreated {
		if err := json.Unmarshal(response.Body.Bytes(), &published); err != nil {
			t.Fatal(err)
		}
	}
	return published, response
}

// TestCustomPasswordLifecycle covers the complete browser flow: safe persistence,
// locked navigation, error handling, login, cookie policy, and SPA serving.
func TestCustomPasswordLifecycle(t *testing.T) {
	server := testServer(t, nil)
	published, response := publishWithPassword(t, server, "?spa=1", "correct horse")
	requireStatus(t, response, http.StatusCreated)
	if !published.Protected || published.Password != "correct horse" {
		t.Fatalf("unexpected response: %+v", published)
	}
	// Publishers receive the chosen password, but the private manifest must contain
	// only verifier material so a storage read cannot recover the plaintext.
	manifest, err := os.ReadFile(filepath.Join(server.sites, published.ID, metadataName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(manifest), published.Password) {
		t.Fatal("manifest contains the plaintext password")
	}

	// Initial GET renders the native dialog and explicitly prevents caching.
	unauthorized := httptest.NewRecorder()
	server.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, published.URL, nil))
	requireStatus(t, unauthorized, http.StatusUnauthorized)
	if unauthorized.Header().Get("Cache-Control") != "no-store" || !strings.Contains(unauthorized.Body.String(), `<dialog open`) || !strings.Contains(unauthorized.Body.String(), `name="password"`) {
		t.Fatalf("unexpected password form: headers=%v body=%q", unauthorized.Header(), unauthorized.Body.String())
	}
	// HEAD follows the same authorization policy but must never emit HTML bytes.
	head := httptest.NewRecorder()
	server.ServeHTTP(head, httptest.NewRequest(http.MethodHead, published.URL, nil))
	requireStatus(t, head, http.StatusUnauthorized)
	if head.Body.Len() != 0 {
		t.Fatal("HEAD password response included a body")
	}

	// A wrong password remains unauthorized and does not issue a cookie.
	wrong := passwordRequest(published.URL, "wrong password")
	wrongResponse := httptest.NewRecorder()
	server.ServeHTTP(wrongResponse, wrong)
	requireStatus(t, wrongResponse, http.StatusUnauthorized)

	// Successful authentication redirects back to the exact requested SPA route.
	login := passwordRequest(published.URL+"dashboard", published.Password)
	loginResponse := httptest.NewRecorder()
	server.ServeHTTP(loginResponse, login)
	requireStatus(t, loginResponse, http.StatusSeeOther)
	if loginResponse.Header().Get("Location") != "/p/"+published.ID+"/dashboard" {
		t.Fatalf("Location=%q", loginResponse.Header().Get("Location"))
	}
	// Path mode scopes the HttpOnly cookie to this deployment; SameSite=Lax keeps
	// ordinary link navigation working without sending it on cross-site subrequests.
	result := loginResponse.Result()
	cookies := result.Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].Path != "/p/"+published.ID+"/" {
		t.Fatalf("unexpected cookies: %+v", cookies)
	}

	// The issued cookie authorizes SPA fallback while protected content remains
	// private and unavailable to intermediary caches.
	served := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, published.URL+"dashboard", nil)
	request.AddCookie(cookies[0])
	server.ServeHTTP(served, request)
	requireStatus(t, served, http.StatusOK)
	if served.Body.String() != "<h1>secret</h1>" || served.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("unexpected protected response: headers=%v body=%q", served.Header(), served.Body.String())
	}
}

// TestGeneratedPasswordAndValidation locks the two publish modes and their trust-
// boundary validation into stable API behavior.
func TestGeneratedPasswordAndValidation(t *testing.T) {
	server := testServer(t, nil)
	published, response := publishWithPassword(t, server, "?generatePassword=1", "")
	requireStatus(t, response, http.StatusCreated)
	if !published.Protected || len(published.Password) != 24 {
		t.Fatalf("unexpected generated password response: %+v", published)
	}

	// Each malformed input must fail before a deployment becomes visible and must
	// return the documented machine-readable error code.
	tests := []struct {
		name, query, password, code string
	}{
		{name: "short", password: "short", code: "invalid_password"},
		{name: "non printable", password: "password\n", code: "invalid_password"},
		{name: "both modes", query: "?generatePassword=1", password: "password", code: "invalid_password"},
		{name: "invalid generation flag", query: "?generatePassword=maybe", code: "invalid_generate_password"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, response := publishWithPassword(t, testServer(t, nil), test.query, test.password)
			requireStatus(t, response, http.StatusBadRequest)
			if got := responseErrorCode(t, response); got != test.code {
				t.Fatalf("code=%q, want=%q", got, test.code)
			}
		})
	}

	// Header presence matters: an explicitly empty custom password is invalid,
	// while an omitted header means an ordinary unprotected publication.
	emptyRequest := httptest.NewRequest(http.MethodPost, "http://publisher.test/api/v1/publish", strings.NewReader("x"))
	emptyRequest.Header.Set("Content-Type", "text/html")
	emptyRequest.Header.Set("Tabucom-Password", "")
	emptyResponse := httptest.NewRecorder()
	server.ServeHTTP(emptyResponse, emptyRequest)
	requireStatus(t, emptyResponse, http.StatusBadRequest)
	if got := responseErrorCode(t, emptyResponse); got != "invalid_password" {
		t.Fatalf("empty password code=%q", got)
	}
}

// TestPasswordAttemptLimit proves the eleventh submission in one fixed minute is
// rejected before password hashing and tells the caller when to retry.
func TestPasswordAttemptLimit(t *testing.T) {
	server := testServer(t, nil)
	published, response := publishWithPassword(t, server, "", "correct password")
	requireStatus(t, response, http.StatusCreated)
	// The first ten attempts are processed normally, even though they are wrong.
	for attempt := 0; attempt < 10; attempt++ {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, passwordRequest(published.URL, "wrong password"))
		requireStatus(t, response, http.StatusUnauthorized)
	}
	// The next attempt is throttled rather than performing another Argon2id check.
	limited := httptest.NewRecorder()
	server.ServeHTTP(limited, passwordRequest(published.URL, "wrong password"))
	requireStatus(t, limited, http.StatusTooManyRequests)
	if limited.Header().Get("Retry-After") == "" {
		t.Fatal("limited login has no Retry-After")
	}
}

// TestWildcardPasswordCookie verifies that origin isolation changes cookie scope:
// the deployment owns its host, so Path=/ is both necessary and minimal.
func TestWildcardPasswordCookie(t *testing.T) {
	server := testServer(t, func(config *Config) {
		config.BaseURL = "https://publisher.test"
		config.PreviewDomain = "preview.test"
	})
	published, response := publishWithPassword(t, server, "", "correct password")
	requireStatus(t, response, http.StatusCreated)
	// The configured HTTPS public origin must also produce a Secure cookie even
	// though httptest itself does not terminate TLS.
	request := passwordRequest(published.URL, published.Password)
	request.Host = published.ID + ".preview.test"
	login := httptest.NewRecorder()
	server.ServeHTTP(login, request)
	requireStatus(t, login, http.StatusSeeOther)
	cookies := login.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Path != "/" || !cookies[0].Secure {
		t.Fatalf("unexpected wildcard cookie: %+v", cookies)
	}
}

// passwordRequest reproduces the browser's native form encoding so tests cover
// ParseForm behavior rather than constructing internal authorization state.
func passwordRequest(target, password string) *http.Request {
	form := url.Values{"password": {password}}.Encode()
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return request
}
