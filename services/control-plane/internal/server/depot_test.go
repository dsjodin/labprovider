package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsjodin/labprovider/services/control-plane/internal/deploy"
)

// depotDest is the only place a request decides where the control plane writes,
// so it is tested as the hostile-input parser it is.
func TestDepotDestConfinesToDataDir(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "depot")
	if err := os.MkdirAll(filepath.Join(dataDir, "PROD", "COMP"), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ dest, want string }{
		{"bundle.tar", filepath.Join(dataDir, "bundle.tar")},
		{"PROD/COMP/bundle.tar", filepath.Join(dataDir, "PROD/COMP/bundle.tar")},
		{"./PROD/COMP/../COMP/b.tar", filepath.Join(dataDir, "PROD/COMP/b.tar")},
	} {
		got, err := depotDest(dataDir, tc.dest)
		if err != nil {
			t.Errorf("depotDest(%q) error: %v", tc.dest, err)
			continue
		}
		if got != tc.want {
			t.Errorf("depotDest(%q) = %q, want %q", tc.dest, got, tc.want)
		}
	}

	for _, dest := range []string{
		"", "   ",
		"/etc/passwd",
		"~/bundle.tar",
		"../outside.tar",
		"PROD/../../outside.tar",
		"..",
	} {
		if got, err := depotDest(dataDir, dest); err == nil {
			t.Errorf("depotDest(%q) = %q, want an error", dest, got)
		}
	}
}

// A symlinked subdirectory passes a textual prefix check and writes somewhere
// else entirely, so the parent is resolved before the prefix is trusted.
func TestDepotDestRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "depot")
	outside := filepath.Join(root, "outside")
	for _, d := range []string{dataDir, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(dataDir, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	got, err := depotDest(dataDir, "escape/bundle.tar")
	if err == nil {
		t.Errorf("depotDest through a symlink = %q, want an error", got)
	}
}

// A sibling directory sharing a prefix must not pass. This is the bug 5.1 found
// in dnssync.go, in a place where it would be a write outside the depot.
func TestDepotDestRejectsPrefixSibling(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "depot")
	sibling := root + "/depotX"
	for _, d := range []string{dataDir, sibling} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := depotDest(dataDir, "../depotX/bundle.tar"); err == nil {
		t.Errorf("depotDest into a prefix sibling = %q, want an error", got)
	}
}

func TestDepotDestErrorsMentionTheDepot(t *testing.T) {
	_, err := depotDest(t.TempDir(), "/etc/passwd")
	if err == nil || !strings.Contains(err.Error(), "depot data directory") {
		t.Errorf("error = %v, want it to name the depot data directory", err)
	}
}

// depotServer wires an engine that knows the depot, pointed at a temp data
// directory.
func depotServer(t *testing.T) (*Server, string) {
	t.Helper()
	dataDir := t.TempDir()
	cfg := "DEPOT_FQDN=\"depot.sddc.lab\"\nDEPOT_DATA_DIR=\"" + dataDir + "\"\n"
	return testServer(t, Options{Engine: testEngine(t, cfg, deploy.Depot{})}), dataDir
}

func postJSON(t *testing.T, srv *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestDepotFetchDownloadsIntoTheDepot(t *testing.T) {
	const body = "a bundle"
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	defer source.Close()

	srv, dataDir := depotServer(t)
	rec := postJSON(t, srv, "/api/depot/fetch",
		`{"url":"`+source.URL+`","dest":"PROD/COMP/bundle.tar"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body.String())
	}

	dest := filepath.Join(dataDir, "PROD/COMP/bundle.tar")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(dest); err == nil {
			if string(b) != body {
				t.Fatalf("content = %q", b)
			}
			// The status endpoint reports the finished transfer and never the
			// credentials it used.
			status := renderJSON(t, srv, "/api/depot/fetch")
			if !strings.Contains(status, `"stage":"done"`) {
				t.Errorf("status = %s", status)
			}
			if strings.Contains(status, "password") {
				t.Error("the status endpoint must not echo credentials")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the file never arrived")
}

// Inline credentials are the normal way to reach a password-protected Broadcom
// mirror, and url.Parse keeps them in the URL where String() reproduces them -
// so the status endpoint used to hand them straight back to whoever polled it.
func TestDepotFetchStripsInlineCredentials(t *testing.T) {
	var gotUser, gotPass string
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, _ = r.BasicAuth()
		_, _ = io.WriteString(w, "a bundle")
	}))
	defer source.Close()

	withCreds := strings.Replace(source.URL, "http://", "http://depotuser:hunter2@", 1)
	srv, dataDir := depotServer(t)
	rec := postJSON(t, srv, "/api/depot/fetch", `{"url":"`+withCreds+`","dest":"bundle.tar"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body.String())
	}
	// The 202 body is the same Status the page polls.
	if strings.Contains(rec.Body.String(), "hunter2") || strings.Contains(rec.Body.String(), "depotuser") {
		t.Errorf("the accepted response echoed the inline credentials: %s", rec.Body.String())
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.ReadFile(filepath.Join(dataDir, "bundle.tar")); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Moved into the fields, not dropped: the transfer still authenticates.
	if gotUser != "depotuser" || gotPass != "hunter2" {
		t.Errorf("upstream saw user=%q pass=%q, want the inline credentials forwarded as Basic auth", gotUser, gotPass)
	}
	if status := renderJSON(t, srv, "/api/depot/fetch"); strings.Contains(status, "hunter2") {
		t.Errorf("the status endpoint echoed the password: %s", status)
	}
}

func renderJSON(t *testing.T, srv *Server, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Body.String()
}

func TestDepotFetchRejectsBadRequests(t *testing.T) {
	srv, _ := depotServer(t)
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"escape", `{"url":"https://example.invalid/b","dest":"../../etc/cron.d/x"}`, http.StatusBadRequest},
		{"absolute dest", `{"url":"https://example.invalid/b","dest":"/etc/cron.d/x"}`, http.StatusBadRequest},
		{"file scheme", `{"url":"file:///etc/passwd","dest":"b.tar"}`, http.StatusBadRequest},
		{"no scheme", `{"url":"example.invalid/b","dest":"b.tar"}`, http.StatusBadRequest},
		{"bad checksum", `{"url":"https://example.invalid/b","dest":"b.tar","sha256":"xyz"}`, http.StatusBadRequest},
	} {
		if got := postJSON(t, srv, "/api/depot/fetch", tc.body).Code; got != tc.want {
			t.Errorf("%s: status = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// The fetch form belongs to the depot alone: nothing else has anywhere to put a
// downloaded bundle.
func TestDepotFetchFormOnlyOnTheDepotPage(t *testing.T) {
	srv, _ := depotServer(t)
	if !strings.Contains(renderPath(t, srv, "/service/depot"), "Fetch into the depot") {
		t.Error("the depot page should offer the fetch form")
	}
	if strings.Contains(renderPath(t, srv, "/service/netbox"), "Fetch into the depot") {
		t.Error("only the depot page should offer the fetch form")
	}
}
