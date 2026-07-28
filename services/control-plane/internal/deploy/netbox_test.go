package deploy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// A token that NetBox still accepts is kept: reminting on every deploy would
// invalidate the credential the running dns-sync container holds.
func TestDNSSyncTokenIsReusedWhileNetBoxAcceptsIt(t *testing.T) {
	dir := t.TempDir()
	const stored = "nbt_old.tok"
	if err := os.WriteFile(filepath.Join(dir, "netbox.token"), []byte(stored), 0o600); err != nil {
		t.Fatal(err)
	}
	var (
		mu        sync.Mutex
		provision bool
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/api/users/tokens/provision/" {
			provision = true
		}
		if r.URL.Path == "/api/" && r.Header.Get("Authorization") == "Bearer "+stored {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	api := &netboxAPI{base: srv.URL, client: srv.Client()}
	rc := testRunCtx(map[string]string{"DNS_SYNC_SECRETS_DIR": dir})
	if err := provisionNetboxDNSSyncToken(context.Background(), rc, api); err != nil {
		t.Fatalf("provisionNetboxDNSSyncToken: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if provision {
		t.Error("a working stored token must not be reminted")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "netbox.token")); string(b) != stored {
		t.Errorf("token file = %q, want it unchanged", b)
	}
}

// A stored token NetBox rejects (rebuilt database, retired token) must be
// replaced, and the stale ones retired first.
func TestDNSSyncTokenIsRemintedWhenRejected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "netbox.token"), []byte("nbt_stale.tok"), 0o600); err != nil {
		t.Fatal(err)
	}
	var (
		mu      sync.Mutex
		deleted []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/api/users/tokens/" && r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `{"results":[{"id":11}]}`)
		case r.Method == http.MethodDelete:
			deleted = append(deleted, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/api/users/tokens/provision/":
			_, _ = io.WriteString(w, `{"id":12,"key":"new","token":"tok2"}`)
		case r.URL.Path == "/api/" && r.Header.Get("Authorization") == "Bearer nbt_new.tok2":
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer srv.Close()

	api := &netboxAPI{base: srv.URL, client: srv.Client()}
	rc := testRunCtx(map[string]string{"DNS_SYNC_SECRETS_DIR": dir})
	if err := provisionNetboxDNSSyncToken(context.Background(), rc, api); err != nil {
		t.Fatalf("provisionNetboxDNSSyncToken: %v", err)
	}

	tokenFile := filepath.Join(dir, "netbox.token")
	b, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "nbt_new.tok2" {
		t.Errorf("token file = %q, want the composite token", b)
	}
	fi, err := os.Stat(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("token file mode = %v, want 0600", fi.Mode().Perm())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(deleted) != 1 || deleted[0] != "/api/users/tokens/11/" {
		t.Errorf("retired tokens = %v, want the stale one deleted", deleted)
	}
}

// The freshly minted token is verified before it is written, so a token that
// does not work fails the deploy instead of landing in a file dns-sync reads.
func TestDNSSyncTokenFailsWhenTheFreshTokenIsRejected(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/users/tokens/" && r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `{"results":[]}`)
		case r.URL.Path == "/api/users/tokens/provision/":
			_, _ = io.WriteString(w, `{"id":12,"key":"new","token":"tok2"}`)
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer srv.Close()

	api := &netboxAPI{base: srv.URL, client: srv.Client()}
	rc := testRunCtx(map[string]string{"DNS_SYNC_SECRETS_DIR": dir})
	if err := provisionNetboxDNSSyncToken(context.Background(), rc, api); err == nil {
		t.Fatal("want an error when NetBox rejects the freshly provisioned token")
	}
	if _, err := os.Stat(filepath.Join(dir, "netbox.token")); !os.IsNotExist(err) {
		t.Error("a rejected token must not be written to disk")
	}
}

// Without a secrets directory there is nowhere to put the token; that is a
// skip with a notice, not a failed deploy.
func TestDNSSyncTokenSkippedWithoutASecretsDir(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	api := &netboxAPI{base: srv.URL, client: srv.Client()}
	if err := provisionNetboxDNSSyncToken(context.Background(), testRunCtx(nil), api); err != nil {
		t.Fatalf("provisionNetboxDNSSyncToken: %v", err)
	}
}

// The dashboard token is read-only twice over: provisioned with write_enabled
// false and patched to false afterwards, whatever the provision body honored.
func TestDashboardTokenIsProvisionedReadOnly(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("provisionNetboxDashboardToken chowns the secrets dir to 1000:1000; needs root")
	}
	dir := t.TempDir()
	var (
		mu       sync.Mutex
		patched  = map[string]bool{}
		provided map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/api/users/tokens/provision/":
			provided = decodeJSONBody(t, r)
			_, _ = io.WriteString(w, `{"id":21,"key":"dash","token":"tok"}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/users/tokens/21/":
			body := decodeJSONBody(t, r)
			patched["write_enabled_false"] = body["write_enabled"] == false
			_, _ = io.WriteString(w, `{"id":21}`)
		case r.URL.Path == "/api/ipam/prefixes/" && r.Header.Get("Authorization") == "Bearer nbt_dash.tok":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `{"results":[]}`)
		case r.Method == http.MethodPost:
			_, _ = io.WriteString(w, `{"id":5}`)
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer srv.Close()

	api := &netboxAPI{base: srv.URL, client: srv.Client()}
	rc := testRunCtx(map[string]string{"CONTROL_PLANE_SECRETS_DIR": dir})
	if err := provisionNetboxDashboardToken(context.Background(), rc, api); err != nil {
		t.Fatalf("provisionNetboxDashboardToken: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if provided["write_enabled"] != false {
		t.Errorf("provision write_enabled = %v, want false", provided["write_enabled"])
	}
	if !patched["write_enabled_false"] {
		t.Error("the minted token was not patched to write_enabled=false")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "netbox-readonly.token")); string(b) != "nbt_dash.tok" {
		t.Errorf("token file = %q", b)
	}
}
