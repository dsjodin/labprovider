package server

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsjodin/labprovider/services/control-plane/internal/auth"
	"github.com/dsjodin/labprovider/services/control-plane/internal/deploy"
	"github.com/dsjodin/labprovider/services/control-plane/internal/envfile"
)

// backupServer builds a control plane over a host layout with everything worth
// keeping: config, generated secrets, dns.seed, accounts, and CA key material.
func backupServer(t *testing.T) (*Server, *http.Cookie) {
	t.Helper()
	dir := t.TempDir()
	caDir := filepath.Join(dir, "step-ca")
	cpDir := filepath.Join(dir, "control-plane")
	for _, d := range []string{filepath.Join(cpDir, "secrets"), filepath.Join(caDir, "secrets")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, content string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	cfg := filepath.Join(cpDir, "labprovider.env")
	write(cfg, "CA_DATA_DIR=\""+caDir+"\"\n", 0o600)
	write(filepath.Join(cpDir, "secrets", "CA_POSTGRES_PASSWORD"), "s3cret\n", 0o600)
	write(filepath.Join(cpDir, "dns.seed"), "vc01.sddc.lab 10.0.0.5\n", 0o644)
	write(filepath.Join(caDir, "secrets", "root_ca_key"), "PRIVATE KEY\n", 0o600)
	users := filepath.Join(cpDir, "users.json")
	write(users, `{"users":[{"username":"operator","hash":"x"}]}`, 0o600)

	engine := deploy.NewEngine(
		envfile.Store{Path: cfg},
		&deploy.StateStore{Path: filepath.Join(cpDir, "state.json")},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	deploy.RegisterAll(engine)
	// The archive is every secret in the lab, so the routes sit behind the
	// operator session like everything else; the test carries one.
	store := &auth.Store{Path: users}
	sessions := auth.NewSessions(time.Hour)
	srv := testServer(t, Options{Engine: engine, Auth: store, Sessions: sessions})
	token, err := sessions.Create("operator")
	if err != nil {
		t.Fatal(err)
	}
	return srv, &http.Cookie{Name: auth.CookieName, Value: token}
}

// getAs issues an authenticated GET.
func getAs(t *testing.T, srv *Server, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestBackupCarriesTheIrreplaceableState(t *testing.T) {
	srv, cookie := backupServer(t)
	rec := getAs(t, srv, "/api/backup", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/backup = %d: %s", rec.Code, rec.Body.String())
	}
	// Unauthenticated it must not hand over the archive at all.
	if anon := getAs(t, srv, "/api/backup", nil); anon.Code != http.StatusUnauthorized {
		t.Errorf("anonymous GET /api/backup = %d, want 401", anon.Code)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "labprovider-backup-") {
		t.Errorf("Content-Disposition = %q", cd)
	}

	gz, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	modes := map[string]os.FileMode{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("the archive is truncated or corrupt: %v", err)
		}
		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		got[hdr.Name] = string(b)
		modes[hdr.Name] = os.FileMode(hdr.Mode).Perm()
	}

	want := map[string]string{
		"labprovider.env":              "CA_DATA_DIR=\"",
		"secrets/CA_POSTGRES_PASSWORD": "s3cret",
		"dns.seed":                     "vc01.sddc.lab",
		"users.json":                   "operator",
		"step-ca/secrets/root_ca_key":  "PRIVATE KEY",
	}
	for name, contains := range want {
		if !strings.Contains(got[name], contains) {
			t.Errorf("archive entry %q = %q, want it to contain %q", name, got[name], contains)
		}
	}
	// A restore that widens the mode on a key or a secret would be a quiet
	// downgrade, so the mode travels with the file.
	for _, name := range []string{"secrets/CA_POSTGRES_PASSWORD", "step-ca/secrets/root_ca_key", "users.json"} {
		if modes[name] != 0o600 {
			t.Errorf("%s archived with mode %o, want 600", name, modes[name])
		}
	}
}

// The listing exists so an operator can see what the button covers before
// relying on it, and carries the tar equivalent for anyone who would rather
// cron it.
func TestBackupContentsListsWhatIsCovered(t *testing.T) {
	srv, cookie := backupServer(t)
	rec := getAs(t, srv, "/api/backup/contents", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/backup/contents = %d", rec.Code)
	}
	var resp struct {
		Entries []struct{ Name, Path, Why string }
		Bytes   int64
		Tar     string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	// config, secrets, dns.seed, users.json, step-ca
	if len(resp.Entries) != 5 {
		t.Errorf("listed %d entries, want 5: %+v", len(resp.Entries), resp.Entries)
	}
	if resp.Bytes == 0 {
		t.Error("reported a zero-byte backup set")
	}
	if !strings.HasPrefix(resp.Tar, "tar czf ") {
		t.Errorf("tar equivalent = %q", resp.Tar)
	}
	for _, e := range resp.Entries {
		if e.Why == "" {
			t.Errorf("entry %q does not say why it is worth keeping", e.Name)
		}
	}
}

// A lab that has not saved a configuration has nothing to back up, and saying
// so beats handing over an empty archive that looks like a backup.
func TestBackupRefusesBeforeAnythingIsSaved(t *testing.T) {
	dir := t.TempDir()
	engine := deploy.NewEngine(
		envfile.Store{Path: filepath.Join(dir, "absent.env")},
		&deploy.StateStore{Path: filepath.Join(dir, "state.json")},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	srv := testServer(t, Options{Engine: engine})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/backup", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("GET /api/backup with no config = %d, want 400", rec.Code)
	}
}
