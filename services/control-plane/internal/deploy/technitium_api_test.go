package deploy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

// On first bootstrap the admin account still has the first-boot admin/admin
// credentials; AdminToken must rotate it to TECHNITIUM_ADMIN_PASSWORD via
// changePassword with the CURRENT password as pass and the new one as newPass.
// Omitting newPass is the "Parameter 'newPass' missing" deploy failure.
func TestAdminToken_RotatesFirstBootPassword(t *testing.T) {
	const newPass = "S3cret-New!"
	var (
		mu        sync.Mutex
		rotated   bool
		gotChange url.Values
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch r.URL.Path {
		case "/api/user/login":
			mu.Lock()
			ok := q.Get("pass") == "admin" || (rotated && q.Get("pass") == newPass)
			mu.Unlock()
			if ok {
				_, _ = io.WriteString(w, `{"status":"ok","token":"tok"}`)
			} else {
				_, _ = io.WriteString(w, `{"status":"error","errorMessage":"invalid credentials"}`)
			}
		case "/api/user/changePassword":
			mu.Lock()
			gotChange = q
			rotated = true
			mu.Unlock()
			_, _ = io.WriteString(w, `{"status":"ok"}`)
		default:
			_, _ = io.WriteString(w, `{"status":"ok"}`)
		}
	}))
	defer srv.Close()

	api := technitiumAPI{base: srv.URL}
	rc := &RunCtx{Env: map[string]string{"TECHNITIUM_ADMIN_PASSWORD": newPass}, Log: func(string, ...any) {}}
	tok, err := api.AdminToken(context.Background(), rc)
	if err != nil {
		t.Fatalf("AdminToken: %v", err)
	}
	if tok != "tok" {
		t.Fatalf("token = %q, want tok", tok)
	}
	if got := gotChange.Get("newPass"); got != newPass {
		t.Errorf("changePassword newPass = %q, want %q", got, newPass)
	}
	if got := gotChange.Get("pass"); got != "admin" {
		t.Errorf("changePassword pass (current) = %q, want admin", got)
	}
}

// When the configured password already works (re-run after the first rotation),
// AdminToken must reuse it and not call changePassword.
func TestAdminToken_ReusesConfiguredPassword(t *testing.T) {
	const pass = "S3cret-New!"
	var (
		mu           sync.Mutex
		changeCalled bool
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/user/changePassword" {
			mu.Lock()
			changeCalled = true
			mu.Unlock()
		}
		if r.URL.Path == "/api/user/login" && r.URL.Query().Get("pass") == pass {
			_, _ = io.WriteString(w, `{"status":"ok","token":"tok"}`)
			return
		}
		_, _ = io.WriteString(w, `{"status":"error","errorMessage":"invalid credentials"}`)
	}))
	defer srv.Close()

	api := technitiumAPI{base: srv.URL}
	rc := &RunCtx{Env: map[string]string{"TECHNITIUM_ADMIN_PASSWORD": pass}, Log: func(string, ...any) {}}
	if _, err := api.AdminToken(context.Background(), rc); err != nil {
		t.Fatalf("AdminToken: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if changeCalled {
		t.Error("changePassword must not be called when the configured password already works")
	}
}
