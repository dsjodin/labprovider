package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsjodin/labprovider/services/control-plane/internal/auth"
)

// setupServer is a control plane with no operator account yet - the state an
// install.sh run leaves behind, and the window this token exists to close.
func setupServer(t *testing.T) (*Server, *SetupToken) {
	t.Helper()
	dir := t.TempDir()
	token, err := NewSetupToken(filepath.Join(dir, "setup-token"))
	if err != nil {
		t.Fatal(err)
	}
	srv := testServer(t, Options{
		Auth:       &auth.Store{Path: filepath.Join(dir, "users.json")},
		Sessions:   auth.NewSessions(time.Hour),
		SetupToken: token,
	})
	return srv, token
}

func setup(t *testing.T, srv *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// Whoever reaches /setup first used to own a root-equivalent surface on the
// segment. Now they need the token install.sh printed.
func TestSetupRequiresTheToken(t *testing.T) {
	srv, token := setupServer(t)

	if rec := setup(t, srv, `{"username":"operator","password":"correct-horse-battery"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("setup with no token = %d, want 403", rec.Code)
	}
	if rec := setup(t, srv, `{"username":"operator","password":"correct-horse-battery","token":"guessed"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("setup with a wrong token = %d, want 403", rec.Code)
	}
	// The refusal must not leak the token.
	rec := setup(t, srv, `{"username":"operator","password":"correct-horse-battery","token":"guessed"}`)
	if strings.Contains(rec.Body.String(), token.Value()) {
		t.Error("the error message leaked the token")
	}

	rec = setup(t, srv, `{"username":"operator","password":"correct-horse-battery","token":"`+token.Value()+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup with the right token = %d: %s", rec.Code, rec.Body.String())
	}
}

// The token authorizes exactly one account, once: it is spent on use and the
// file is removed, so a token left on a terminal scrollback is not a credential
// to the next person who finds it.
func TestSetupTokenIsSpentOnce(t *testing.T) {
	srv, token := setupServer(t)
	path, value := token.Path, token.Value()

	if rec := setup(t, srv, `{"username":"operator","password":"correct-horse-battery","token":"`+value+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("first setup = %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the token file survived the setup it authorized: %v", err)
	}
	if token.Value() != "" {
		t.Error("the token is still live in memory")
	}
	// A second attempt is refused by the account check regardless of the token.
	if rec := setup(t, srv, `{"username":"other","password":"correct-horse-battery","token":"`+value+`"}`); rec.Code != http.StatusConflict {
		t.Errorf("second setup = %d, want 409", rec.Code)
	}
}

// A restart before the account is created must not invalidate the token the
// installer already printed.
func TestSetupTokenSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "setup-token")
	first, err := NewSetupToken(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewSetupToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if first.Value() != second.Value() {
		t.Errorf("a restart rotated the token: %q then %q", first.Value(), second.Value())
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("token file mode = %o, want 600", info.Mode().Perm())
	}
}

// The form only asks for what the server will check.
func TestSetupPageAsksForTheTokenOnlyWhenOneExists(t *testing.T) {
	srv, _ := setupServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/setup", nil))
	if !strings.Contains(rec.Body.String(), "Setup token") {
		t.Error("the setup form does not ask for the token")
	}

	// With the check disabled there is nothing to ask for.
	plain := testServer(t, Options{
		Auth:     &auth.Store{Path: filepath.Join(t.TempDir(), "users.json")},
		Sessions: auth.NewSessions(time.Hour),
	})
	rec = httptest.NewRecorder()
	plain.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/setup", nil))
	if strings.Contains(rec.Body.String(), "Setup token") {
		t.Error("the form asks for a token the server will not check")
	}
}

// Defense in depth on CSRF: Lax already blocks the classic form post, but the
// wizard's raw-text bodies are CORS-simple, so a browser-labelled cross-site
// request is refused outright.
func TestCrossSiteRequestsAreRejected(t *testing.T) {
	dir := t.TempDir()
	users := filepath.Join(dir, "users.json")
	if err := os.WriteFile(users, []byte(`{"users":[{"username":"operator","hash":"x"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewSessions(time.Hour)
	srv := testServer(t, Options{Auth: &auth.Store{Path: users}, Sessions: sessions})
	token, err := sessions.Create("operator")
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		site string
		want int
	}{
		{"", http.StatusOK},            // curl, older browsers
		{"same-origin", http.StatusOK}, // the wizard itself
		{"none", http.StatusOK},        // typed into the address bar
		{"cross-site", http.StatusForbidden},
		{"same-site", http.StatusForbidden},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/state", nil)
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
		if tc.site != "" {
			req.Header.Set("Sec-Fetch-Site", tc.site)
		}
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("Sec-Fetch-Site: %q = %d, want %d", tc.site, rec.Code, tc.want)
		}
	}
}

// Three headers that cost one line each; a full CSP would need nonces threaded
// through every inline script.
func TestPagesSetSecurityHeaders(t *testing.T) {
	srv := testServer(t, Options{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	want := map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
		"Content-Security-Policy": "frame-ancestors 'none'",
	}
	for header, value := range want {
		if got := rec.Header().Get(header); got != value {
			t.Errorf("%s = %q, want %q", header, got, value)
		}
	}
}

// The semaphore bounds CPU spent on bcrypt; the login path must still work
// under it, and a cancelled request must not consume a slot.
func TestConcurrentLoginsAreCapped(t *testing.T) {
	srv, token := setupServer(t)
	if rec := setup(t, srv, `{"username":"operator","password":"correct-horse-battery","token":"`+token.Value()+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("setup = %d: %s", rec.Code, rec.Body.String())
	}

	done := make(chan int, 12)
	for i := 0; i < 12; i++ {
		go func() {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/login",
				strings.NewReader(`{"username":"operator","password":"correct-horse-battery"}`))
			req.Header.Set("Content-Type", "application/json")
			srv.Handler().ServeHTTP(rec, req)
			done <- rec.Code
		}()
	}
	for i := 0; i < 12; i++ {
		select {
		case code := <-done:
			if code != http.StatusOK {
				t.Errorf("login under concurrency = %d, want 200", code)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("logins deadlocked under the semaphore")
		}
	}
	if len(srv.verifying) != 0 {
		t.Errorf("%d semaphore slots were never released", len(srv.verifying))
	}
}

func TestSetupTokenJSONShape(t *testing.T) {
	var req credentials
	if err := json.Unmarshal([]byte(`{"username":"a","password":"b","token":"c"}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.Token != "c" {
		t.Errorf("token = %q", req.Token)
	}
}
