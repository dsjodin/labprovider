package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsjodin/labprovider/services/control-plane/internal/auth"
)

func authServer(t *testing.T) (http.Handler, *auth.Store) {
	t.Helper()
	store := &auth.Store{Path: filepath.Join(t.TempDir(), "users.json")}
	srv, err := New(Options{
		Auth:     store,
		Sessions: auth.NewSessions(time.Hour),
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv.Handler(), store
}

func post(t *testing.T, h http.Handler, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieName {
			return c
		}
	}
	t.Fatalf("no %s cookie in the response", auth.CookieName)
	return nil
}

// The whole point of the feature: nothing but the public paths answers without
// a session.
func TestUnauthenticatedRequestsAreRefused(t *testing.T) {
	h, store := authServer(t)
	if err := store.Create("operator", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/", "/config", "/deploy", "/csr", "/account"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusSeeOther {
			t.Errorf("GET %s = %d, want 303 to /login", path, rec.Code)
		}
		if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/login") {
			t.Errorf("GET %s redirected to %q, want /login", path, loc)
		}
	}
	// API callers get a status code, not an HTML redirect.
	for _, path := range []string{"/api/state", "/api/config", "/api/services", "/api/account"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s = %d, want 401", path, rec.Code)
		}
	}
	// healthz stays open so install.sh and Docker can probe it.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /healthz = %d, want 200", rec.Code)
	}
}

func TestLoginGrantsAccess(t *testing.T) {
	h, store := authServer(t)
	if err := store.Create("operator", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}

	if rec := post(t, h, "/api/login", `{"username":"operator","password":"wrong-password"}`, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("login with a wrong password = %d, want 401", rec.Code)
	}
	rec := post(t, h, "/api/login", `{"username":"operator","password":"correct-horse-battery"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("login = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	cookie := sessionCookie(t, rec)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie = %+v, want HttpOnly and SameSite=Lax", cookie)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/state with a session = %d, want 200", rec.Code)
	}

	// Signing out invalidates the same cookie.
	if rec := post(t, h, "/api/logout", "", cookie); rec.Code != http.StatusOK {
		t.Fatalf("logout = %d, want 200", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/state", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/state after logout = %d, want 401", rec.Code)
	}
}

func TestSetupIsOnlyReachableWhileTheStoreIsEmpty(t *testing.T) {
	h, store := authServer(t)

	// With no operator, everything redirects to /setup rather than /login.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/config", nil))
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/setup") {
		t.Errorf("GET /config with no operator redirected to %q, want /setup", loc)
	}

	if rec := post(t, h, "/api/setup", `{"username":"operator","password":"short"}`, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("setup with a short password = %d, want 400", rec.Code)
	}
	rec = post(t, h, "/api/setup", `{"username":"operator","password":"correct-horse-battery"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	cookie := sessionCookie(t, rec)

	if empty, _ := store.Empty(); empty {
		t.Fatal("setup did not persist the operator")
	}
	// A second setup call must not be able to add an account.
	if rec := post(t, h, "/api/setup", `{"username":"attacker","password":"correct-horse-battery"}`, nil); rec.Code != http.StatusConflict {
		t.Errorf("second setup = %d, want 409", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("the setup session does not work: %d", rec.Code)
	}
}

func TestPasswordChangeEndsOtherSessions(t *testing.T) {
	h, store := authServer(t)
	if err := store.Create("operator", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	first := sessionCookie(t, post(t, h, "/api/login", `{"username":"operator","password":"correct-horse-battery"}`, nil))
	second := sessionCookie(t, post(t, h, "/api/login", `{"username":"operator","password":"correct-horse-battery"}`, nil))

	rec := post(t, h, "/api/account/password", `{"current":"correct-horse-battery","new":"a-brand-new-password"}`, second)
	if rec.Code != http.StatusOK {
		t.Fatalf("password change = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	// The caller keeps working via the freshly issued cookie; the other session
	// was opened with the old password and must not survive.
	req := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	req.AddCookie(first)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusUnauthorized {
		t.Errorf("the other session survived a password change: %d", rec2.Code)
	}
	if !store.Verify("operator", "a-brand-new-password") {
		t.Error("the new password does not work")
	}
}

func TestAccountReportsTheSignedInOperator(t *testing.T) {
	h, store := authServer(t)
	if err := store.Create("operator", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, post(t, h, "/api/login", `{"username":"operator","password":"correct-horse-battery"}`, nil))

	req := httptest.NewRequest(http.MethodGet, "/api/account", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/account = %d", rec.Code)
	}
	var got struct {
		Username  string   `json:"username"`
		Operators []string `json:"operators"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Username != "operator" || len(got.Operators) != 1 {
		t.Errorf("account = %+v, want operator with one entry", got)
	}
}

// A nil Auth keeps the read-only dashboard deployment working unchanged.
func TestAuthDisabledLeavesRoutesOpen(t *testing.T) {
	srv, err := New(Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/state with auth disabled = %d, want 200", rec.Code)
	}
}

func TestSecureCookieFollowsTheScheme(t *testing.T) {
	h, store := authServer(t)
	if err := store.Create("operator", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	// Plain HTTP is the bootstrap window before the CA issues the control
	// plane's leaf; a Secure cookie there would never be sent back.
	if c := sessionCookie(t, post(t, h, "/api/login", `{"username":"operator","password":"correct-horse-battery"}`, nil)); c.Secure {
		t.Error("Secure was set on a plain-HTTP request")
	}

	req := httptest.NewRequest(http.MethodPost, "/api/login",
		strings.NewReader(`{"username":"operator","password":"correct-horse-battery"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if c := sessionCookie(t, rec); !c.Secure {
		t.Error("Secure was not set behind an HTTPS proxy")
	}
}

// html/template contextually escapes <script> bodies, so the sign-out helper
// added to the dashboard nav must survive rendering intact.
func TestDashboardRendersTheSignOutNav(t *testing.T) {
	h, store := authServer(t)
	if err := store.Create("operator", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, post(t, h, "/api/login", `{"username":"operator","password":"correct-horse-battery"}`, nil))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d", rec.Code)
	}
	for _, want := range []string{`href="/account"`, "function signOut", "/api/logout"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("the dashboard is missing %q", want)
		}
	}
}
