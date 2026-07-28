package deploy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func decodeJSONBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var out map[string]any
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("bad request body %q: %v", b, err)
	}
	return out
}

func testRunCtx(env map[string]string) *RunCtx {
	if env == nil {
		env = map[string]string{}
	}
	return &RunCtx{Env: env, Log: func(string, ...any) {}}
}

// NetBox 4.6 returns key and token separately; the usable credential is the
// composite "nbt_<key>.<token>", not either half on its own.
func TestProvisionTokenBuildsAV2BearerHeader(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/users/tokens/provision/" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		_, _ = io.WriteString(w, `{"id":7,"key":"abc","token":"xyz"}`)
	}))
	defer srv.Close()

	api := &netboxAPI{base: srv.URL, client: srv.Client()}
	header, composite, id, err := api.provisionToken(context.Background(), "admin", "pw", "labprovider dashboard", false)
	if err != nil {
		t.Fatalf("provisionToken: %v", err)
	}
	if header != "Bearer nbt_abc.xyz" {
		t.Errorf("header = %q", header)
	}
	if composite != "nbt_abc.xyz" {
		t.Errorf("composite = %q", composite)
	}
	if id != 7 {
		t.Errorf("id = %d, want 7", id)
	}
	if got["username"] != "admin" || got["password"] != "pw" || got["description"] != "labprovider dashboard" {
		t.Errorf("request body = %v", got)
	}
	if got["write_enabled"] != false {
		t.Errorf("write_enabled = %v, want false", got["write_enabled"])
	}
}

// Pre-4.6 responses carry no token field, and the key alone is the credential.
func TestProvisionTokenFallsBackToTheLegacyHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":3,"key":"legacykey"}`)
	}))
	defer srv.Close()

	api := &netboxAPI{base: srv.URL, client: srv.Client()}
	header, composite, _, err := api.provisionToken(context.Background(), "admin", "pw", "", true)
	if err != nil {
		t.Fatalf("provisionToken: %v", err)
	}
	if header != "Token legacykey" || composite != "legacykey" {
		t.Errorf("header = %q, composite = %q", header, composite)
	}
}

func TestProvisionTokenWithoutAKeyFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":3}`)
	}))
	defer srv.Close()

	api := &netboxAPI{base: srv.URL, client: srv.Client()}
	if _, _, _, err := api.provisionToken(context.Background(), "admin", "pw", "", true); err == nil {
		t.Fatal("want an error when NetBox returns no key")
	}
}

// Redeploys must not accumulate live credentials: every token carrying the
// description is deleted before a new one is minted.
func TestRetireTokensByDescriptionDeletesEveryMatch(t *testing.T) {
	var (
		mu      sync.Mutex
		deleted []string
		listQ   string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			listQ = r.URL.RawQuery
			_, _ = io.WriteString(w, `{"results":[{"id":4},{"id":9}]}`)
		case http.MethodDelete:
			deleted = append(deleted, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	api := &netboxAPI{base: srv.URL, client: srv.Client()}
	api.retireTokensByDescription(context.Background(), testRunCtx(nil), "labprovider dns-sync", "dns-sync")

	mu.Lock()
	defer mu.Unlock()
	if listQ != "description=labprovider+dns-sync" {
		t.Errorf("list query = %q", listQ)
	}
	if len(deleted) != 2 || deleted[0] != "/api/users/tokens/4/" || deleted[1] != "/api/users/tokens/9/" {
		t.Errorf("deleted = %v", deleted)
	}
}

// A NetBox error must surface as an error rather than an empty result: the
// deployers act on what these calls return.
func TestRequestFailsOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"detail":"nope"}`)
	}))
	defer srv.Close()

	api := &netboxAPI{base: srv.URL, client: srv.Client()}
	if _, err := api.getObjectID(context.Background(), "/api/users/tokens/", "description=x"); err == nil {
		t.Fatal("want an error on HTTP 403")
	}
}
