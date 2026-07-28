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

	"github.com/dsjodin/labprovider/services/control-plane/internal/deploy"
	"github.com/dsjodin/labprovider/services/control-plane/internal/docker"
	"github.com/dsjodin/labprovider/services/control-plane/internal/envfile"
)

func readinessEngine(t *testing.T) *deploy.Engine {
	t.Helper()
	e := deploy.NewEngine(
		envfile.Store{Path: filepath.Join(t.TempDir(), "labprovider.env")},
		&deploy.StateStore{Path: filepath.Join(t.TempDir(), "state.json")},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	e.Register(deploy.CA{})
	e.Register(deploy.Technitium{})
	e.Register(deploy.Traefik{})
	e.Register(deploy.Netbox{})
	e.Register(deploy.DNSSync{})
	for _, name := range []string{"ca", "technitium", "traefik", "netbox", "dns-sync"} {
		if err := e.State.Record(name, "deploy", "ok", ""); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func servicesJSON(t *testing.T, srv *Server) map[string]serviceInfo {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/services", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/services = %d", rec.Code)
	}
	var list []serviceInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	out := map[string]serviceInfo{}
	for _, s := range list {
		out[s.Name] = s
	}
	return out
}

// state.json is advisory history. A service that deployed successfully and then
// died must not still read as ready - that is the disagreement between the
// deploy page and the Services panel the review calls out.
func TestReadyReflectsDockerNotJustHistory(t *testing.T) {
	engine := readinessEngine(t)

	// The CA's compose project is step-ca, not ca: the project is the directory
	// name, and getting that mapping wrong would report the service as down.
	running := stubDocker{list: []docker.Container{
		{Name: "step-ca-step-ca-1", Project: "step-ca", State: "running"},
		{Name: "technitium-technitium-1", Project: "technitium", State: "running"},
		{Name: "traefik-traefik-1", Project: "traefik", State: "running"},
		{Name: "netbox-netbox-1", Project: "netbox", State: "running"},
		{Name: "dns-sync-dns-sync-1", Project: "dns-sync", State: "running"},
	}}
	srv := testServer(t, Options{Engine: engine, Docker: running,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	got := servicesJSON(t, srv)
	for _, name := range []string{"ca", "technitium", "traefik", "netbox", "dns-sync"} {
		if !got[name].Ready || !got[name].Running {
			t.Errorf("%s = %+v, want ready and running", name, got[name])
		}
	}

	// Same recorded history, but the CA container exited an hour ago.
	dead := running
	dead.list = append([]docker.Container{
		{Name: "step-ca-step-ca-1", Project: "step-ca", State: "exited"},
	}, running.list[1:]...)
	srv = testServer(t, Options{Engine: engine, Docker: dead,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	got = servicesJSON(t, srv)
	if got["ca"].Ready || got["ca"].Running {
		t.Errorf("ca = %+v, want neither ready nor running after its container exited", got["ca"])
	}
	if !got["technitium"].Ready {
		t.Error("technitium lost its ready state even though its container is up")
	}
}

// With Docker unreachable the recorded history is all there is; refusing every
// deploy would be worse than trusting it.
func TestReadyFallsBackWhenDockerIsUnreachable(t *testing.T) {
	engine := readinessEngine(t)
	srv := testServer(t, Options{Engine: engine,
		Docker: stubDocker{listErr: io.ErrUnexpectedEOF},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	got := servicesJSON(t, srv)
	if !got["ca"].Ready {
		t.Errorf("ca = %+v, want ready from history when Docker is unreachable", got["ca"])
	}
	if got["ca"].Running {
		t.Error("Running must stay false when Docker said nothing")
	}
}

// The two-phase flow: no non-foundation deploy until the foundation is up, and
// "up" now means running, not merely recorded.
func TestDeployIsBlockedWhenAFoundationContainerIsDown(t *testing.T) {
	engine := readinessEngine(t)
	srv := testServer(t, Options{Engine: engine,
		Docker: stubDocker{list: []docker.Container{
			{Name: "step-ca-step-ca-1", Project: "step-ca", State: "exited"},
			{Name: "technitium-technitium-1", Project: "technitium", State: "running"},
			{Name: "traefik-traefik-1", Project: "traefik", State: "running"},
			{Name: "netbox-netbox-1", Project: "netbox", State: "running"},
			{Name: "dns-sync-dns-sync-1", Project: "dns-sync", State: "running"},
		}},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})

	req := httptest.NewRequest(http.MethodPost, "/api/deploy", strings.NewReader(`{"services":["keycloak"]}`))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Errorf("deploy with a dead foundation service = %d, want 409 (%s)", rec.Code, rec.Body)
	}

	// Positive control: with the same recorded history and every container up,
	// the foundation gate lets the request through. It then fails later, on the
	// missing config, which is a different status - so the 409 above really came
	// from the gate.
	srv = testServer(t, Options{Engine: engine,
		Docker: stubDocker{list: []docker.Container{
			{Name: "step-ca-step-ca-1", Project: "step-ca", State: "running"},
			{Name: "technitium-technitium-1", Project: "technitium", State: "running"},
			{Name: "traefik-traefik-1", Project: "traefik", State: "running"},
			{Name: "netbox-netbox-1", Project: "netbox", State: "running"},
			{Name: "dns-sync-dns-sync-1", Project: "dns-sync", State: "running"},
		}},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	req = httptest.NewRequest(http.MethodPost, "/api/deploy", strings.NewReader(`{"services":["keycloak"]}`))
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code == http.StatusConflict {
		t.Errorf("deploy with a healthy foundation was still blocked by the gate (%s)", rec.Body)
	}
}
