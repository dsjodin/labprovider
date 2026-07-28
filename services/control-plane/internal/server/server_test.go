package server

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dsjodin/labprovider/services/control-plane/internal/certs"
	"github.com/dsjodin/labprovider/services/control-plane/internal/dns"
	"github.com/dsjodin/labprovider/services/control-plane/internal/docker"
	"github.com/dsjodin/labprovider/services/control-plane/internal/ipam"
)

type stubCerts struct {
	out []certs.Cert
	err error
}

func (s stubCerts) List(context.Context) ([]certs.Cert, error) { return s.out, s.err }

type stubDNS struct {
	out dns.Overview
	err error
}

func (s stubDNS) Fetch(context.Context) (dns.Overview, error) { return s.out, s.err }

type stubIPAM struct {
	out ipam.Overview
	err error
}

func (s stubIPAM) Fetch(context.Context) (ipam.Overview, error) { return s.out, s.err }

type stubDocker struct {
	list       []docker.Container
	listErr    error
	lines      []string
	restartErr error
	restarted  *[]string // recorded container IDs, when the test cares
}

func (s stubDocker) List(context.Context, []string, time.Time) ([]docker.Container, error) {
	return s.list, s.listErr
}
func (s stubDocker) LogLines(context.Context, string, int) ([]string, error) {
	return s.lines, nil
}
func (s stubDocker) Restart(_ context.Context, id string) error {
	if s.restartErr != nil {
		return s.restartErr
	}
	if s.restarted != nil {
		*s.restarted = append(*s.restarted, id)
	}
	return nil
}

func testServer(t *testing.T, opt Options) *Server {
	t.Helper()
	opt.Now = func() time.Time { return time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC) }
	opt.WarnDays = 30
	srv, err := New(opt)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

// All sources up: every panel is ok and the errors panel picks up a JSON
// ERROR line tailed from a running container.
func TestCollect_AllUp(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	srv := testServer(t, Options{
		Certs: stubCerts{out: []certs.Cert{{CommonName: "ca.sddc.lab", NotAfter: now.Add(100 * 24 * time.Hour)}}},
		DNS:   stubDNS{out: dns.Overview{Zones: []dns.ZoneInfo{{Name: "sddc.lab", RecordCount: 3}}, TLSReachable: true}},
		IPAM:  stubIPAM{out: ipam.Overview{PrefixCount: 2, IPCount: 5, DNSNames: []string{"a.sddc.lab"}}},
		Docker: stubDocker{
			list:  []docker.Container{{ID: "x", Name: "dns-sync", State: "running"}},
			lines: []string{`{"level":"ERROR","msg":"reconcile failed"}`},
		},
	})

	page := srv.collect(context.Background())
	for name, st := range map[string]Status{
		"certs": page.Certs.Status, "dns": page.DNS.Status, "ipam": page.IPAM.Status,
		"services": page.Services.Status, "errors": page.Errors.Status,
	} {
		if !st.OK() {
			t.Errorf("%s panel not ok: %+v", name, st)
		}
	}
	if len(page.Errors.Entries) != 1 || page.Errors.Entries[0].Message != "reconcile failed" {
		t.Errorf("errors panel entries: %+v", page.Errors.Entries)
	}
	if page.Certs.Summary.ActiveOK != 1 {
		t.Errorf("certs summary: %+v", page.Certs.Summary)
	}
}

// One source down must not affect the others; its panel is unavailable.
func TestCollect_Isolation(t *testing.T) {
	srv := testServer(t, Options{
		Certs:  stubCerts{err: errors.New("connect stepca postgres failed")},
		DNS:    stubDNS{out: dns.Overview{TLSReachable: true}},
		IPAM:   stubIPAM{err: errors.New("netbox 500")},
		Docker: stubDocker{listErr: errors.New("dial /var/run/docker.sock: no such file")},
	})

	page := srv.collect(context.Background())

	if !page.Certs.Status.Unavailable() || !strings.Contains(page.Certs.Status.Error, "postgres") {
		t.Errorf("certs should be unavailable with error, got %+v", page.Certs.Status)
	}
	if !page.DNS.Status.OK() {
		t.Errorf("dns should stay ok despite other failures, got %+v", page.DNS.Status)
	}
	if !page.IPAM.Status.Unavailable() {
		t.Errorf("ipam should be unavailable, got %+v", page.IPAM.Status)
	}
	// Docker failure degrades both services and errors together.
	if !page.Services.Status.Unavailable() || !page.Errors.Status.Unavailable() {
		t.Errorf("docker panels should both be unavailable: svc=%+v err=%+v",
			page.Services.Status, page.Errors.Status)
	}
}

// Nil providers render as "not configured", never as errors.
func TestCollect_NotConfigured(t *testing.T) {
	srv := testServer(t, Options{})
	page := srv.collect(context.Background())
	for name, st := range map[string]Status{
		"certs": page.Certs.Status, "dns": page.DNS.Status, "ipam": page.IPAM.Status,
		"services": page.Services.Status, "errors": page.Errors.Status,
	} {
		if !st.Disabled() {
			t.Errorf("%s should be disabled, got %+v", name, st)
		}
	}
}

// The displayed hostname comes from the managed config, which is what Traefik
// routes on. install.sh sets no CONTROL_PLANE_FQDN, so an unconfigured control
// plane shows none rather than a guess.
func TestCollect_FQDNComesFromManagedConfig(t *testing.T) {
	srv := testServer(t, Options{
		FQDN:   "dashboard.from.env",
		Engine: testEngine(t, "CONTROL_PLANE_FQDN=\"dashboard.lab.io\"\n"),
	})
	if got := srv.collect(context.Background()).FQDN; got != "dashboard.lab.io" {
		t.Errorf("FQDN = %q, want dashboard.lab.io", got)
	}

	// No saved config: fall back to the process environment.
	srv = testServer(t, Options{FQDN: "dashboard.from.env"})
	if got := srv.collect(context.Background()).FQDN; got != "dashboard.from.env" {
		t.Errorf("FQDN = %q, want dashboard.from.env", got)
	}

	// Neither set: empty, and the header renders without a hostname.
	srv = testServer(t, Options{})
	if got := srv.collect(context.Background()).FQDN; got != "" {
		t.Errorf("FQDN = %q, want empty", got)
	}
	if body := renderPath(t, srv, "/"); strings.Contains(body, "dashboard.sddc.lab") {
		t.Error("dashboard shows a guessed hostname")
	}
}

// The HTML page and JSON API render for a mixed up/down/disabled state.
func TestHandlers_Render(t *testing.T) {
	srv := testServer(t, Options{
		DNS:  stubDNS{out: dns.Overview{Zones: []dns.ZoneInfo{{Name: "sddc.lab"}}, TLSReachable: true}},
		IPAM: stubIPAM{err: errors.New("down")},
	})
	h := srv.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "labprovider") {
		t.Fatalf("index render: code=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Source unavailable") {
		t.Errorf("expected unavailable IPAM panel in HTML")
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	var page Page
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("json api: %v", err)
	}
	if !page.DNS.Status.OK() || !page.IPAM.Status.Unavailable() {
		t.Errorf("json state mismatch: dns=%+v ipam=%+v", page.DNS.Status, page.IPAM.Status)
	}
}

// Every page renders the Access panel, which is every lab password in
// cleartext - masked with a CSS class, so the value is in the DOM - and
// /api/state serializes the same panel to JSON. Without no-store both are
// heuristically cacheable and land in the browser's on-disk cache.
func TestSecretBearingResponsesAreNotCacheable(t *testing.T) {
	srv := testServer(t, Options{})
	for _, path := range []string{"/", "/api/state"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("GET %s Cache-Control = %q, want no-store", path, got)
		}
	}
}

// ExecuteTemplate writes as it goes, so an error partway through used to leave
// the browser with half a page and a 200. The template here writes a chunk and
// then calls a method that does not exist on the data.
func TestRenderFailsLoudlyInsteadOfTruncating(t *testing.T) {
	srv := testServer(t, Options{})
	tmpl := template.Must(template.New("boom").Parse(
		`{{define "layout"}}<html><body>first bytes{{.Missing.Field}}</body></html>{{end}}`))

	rec := httptest.NewRecorder()
	srv.render(rec, tmpl, "layout", struct{}{})

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "first bytes") {
		t.Errorf("a partial page reached the client: %q", rec.Body.String())
	}
}

// Without a stamped version there is no way - from the UI, the API, or docker
// inspect - to tell which commit is running, which makes "did my fix deploy?"
// unanswerable and bug reports unattributable.
func TestVersionIsReportedAndDefaultsToDev(t *testing.T) {
	for _, tc := range []struct{ set, want string }{
		{"", "dev"},
		{"v0.2.0-3-gabc1234-dirty", "v0.2.0-3-gabc1234-dirty"},
	} {
		srv := testServer(t, Options{Version: tc.set})

		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if !strings.Contains(rec.Body.String(), `"version":"`+tc.want+`"`) {
			t.Errorf("healthz with Version=%q = %s", tc.set, rec.Body.String())
		}
		if got := srv.chrome("Dashboard", "dashboard").Version; got != tc.want {
			t.Errorf("chrome version = %q, want %q", got, tc.want)
		}
	}
}
