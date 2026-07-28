package server

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsjodin/labprovider/services/control-plane/internal/deploy"
	"github.com/dsjodin/labprovider/services/control-plane/internal/docker"
	"github.com/dsjodin/labprovider/services/control-plane/internal/envfile"
)

func testEngine(t *testing.T, config string, extra ...deploy.Service) *deploy.Engine {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "labprovider.env")
	if err := os.WriteFile(cfg, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	engine := deploy.NewEngine(
		envfile.Store{Path: cfg, ExamplePath: filepath.Join(dir, "example.env")},
		&deploy.StateStore{Path: filepath.Join(dir, "state.json")},
		slog.New(slog.NewTextHandler(os.Stderr, nil)),
	)
	engine.Register(deploy.CA{})
	engine.Register(deploy.Netbox{})
	engine.Register(deploy.Mailpit{})
	for _, svc := range extra {
		engine.Register(svc)
	}
	return engine
}

func TestServiceRowsJoinRegistryConfigAndDocker(t *testing.T) {
	engine := testEngine(t, "CA_FQDN=\"ca.sddc.lab\"\nCA_DATA_DIR=\"/opt/labprovider/step-ca\"\nNETBOX_FQDN=\"netbox.sddc.lab\"\nNETBOX_DIR=\"/opt/labprovider/netbox\"\n")
	if err := engine.State.Record("ca", "deploy", "ok", ""); err != nil {
		t.Fatal(err)
	}
	if err := engine.State.Record("netbox", "deploy", "ok", ""); err != nil {
		t.Fatal(err)
	}
	srv := testServer(t, Options{Engine: engine})

	rows := srv.serviceRows([]docker.Container{
		{ID: "1", Name: "labprovider-step-ca", State: "running", Project: "step-ca"},
		{ID: "2", Name: "labprovider-netbox", State: "running", Project: "netbox"},
		{ID: "3", Name: "labprovider-netbox-postgres", State: "exited", Project: "netbox"},
	})

	if len(rows) != 3 {
		t.Fatalf("rows = %d, want one per registered service", len(rows))
	}
	byName := map[string]ServiceRow{}
	for _, r := range rows {
		byName[r.Name] = r
	}

	// ca runs under the "step-ca" compose project, not its registry name.
	ca := byName["ca"]
	if ca.State != stateRunning {
		t.Errorf("ca state = %q, want running", ca.State)
	}
	if ca.URL != "https://ca.sddc.lab" || ca.DataDir != "/opt/labprovider/step-ca" {
		t.Errorf("ca address/data = %q %q", ca.URL, ca.DataDir)
	}
	if !ca.Core {
		t.Error("ca should be marked core")
	}

	// One of NetBox's two containers is down.
	if got := byName["netbox"].State; got != stateDegraded {
		t.Errorf("netbox state = %q, want degraded", got)
	}
	if got := len(byName["netbox"].Containers); got != 2 {
		t.Errorf("netbox containers = %d, want 2", got)
	}

	// Never deployed, no containers, and no FQDN configured.
	mailpit := byName["mailpit"]
	if mailpit.State != stateNotDeployed {
		t.Errorf("mailpit state = %q, want not deployed", mailpit.State)
	}
	if mailpit.LastAt != "" || mailpit.URL != "" {
		t.Errorf("mailpit should have no history and no URL: %+v", mailpit)
	}
}

// A service that deployed successfully but whose containers are gone is
// "stopped", not "not deployed": state.json alone cannot tell the two apart,
// which is the whole point of the join.
func TestServiceRowStoppedVersusNeverDeployed(t *testing.T) {
	engine := testEngine(t, "")
	if err := engine.State.Record("netbox", "deploy", "ok", ""); err != nil {
		t.Fatal(err)
	}
	srv := testServer(t, Options{Engine: engine})

	byName := map[string]ServiceRow{}
	for _, r := range srv.serviceRows(nil) {
		byName[r.Name] = r
	}
	if got := byName["netbox"].State; got != stateStopped {
		t.Errorf("netbox state = %q, want stopped", got)
	}
	if got := byName["mailpit"].State; got != stateNotDeployed {
		t.Errorf("mailpit state = %q, want not deployed", got)
	}
}

func renderPath(t *testing.T, srv *Server, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", path, rec.Code)
	}
	return rec.Body.String()
}

// The dashboard's Services panel is a summary: counts, and cards only for what
// needs attention. Everything running is one line, not a row per service.
// Execute errors are only logged, so assert on the rendered body.
func TestDashboardServicesPanelIsASummary(t *testing.T) {
	engine := testEngine(t, "NETBOX_FQDN=\"netbox.sddc.lab\"\nNETBOX_DIR=\"/opt/labprovider/netbox\"\n")
	srv := testServer(t, Options{
		Engine: engine,
		Docker: stubDocker{list: []docker.Container{
			{ID: "1", Name: "labprovider-netbox", State: "running", Project: "netbox", Uptime: "2h"},
		}},
	})

	body := renderPath(t, srv, "/")
	for _, want := range []string{"Nothing needs attention", "View all 3 services", "not deployed", "</html>"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard body missing %q", want)
		}
	}
}

// A degraded service is the case the summary exists for: it renders in full,
// on the dashboard, without the operator opening anything.
func TestDashboardShowsDegradedService(t *testing.T) {
	engine := testEngine(t, "NETBOX_FQDN=\"netbox.sddc.lab\"\nNETBOX_DIR=\"/opt/labprovider/netbox\"\n")
	srv := testServer(t, Options{
		Engine: engine,
		Docker: stubDocker{list: []docker.Container{
			{ID: "1", Name: "labprovider-netbox", State: "running", Project: "netbox"},
			{ID: "2", Name: "labprovider-netbox-postgres", State: "exited", Project: "netbox"},
		}},
	})

	body := renderPath(t, srv, "/")
	for _, want := range []string{"degraded", `href="/service/netbox"`, "/opt/labprovider/netbox"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard body missing %q", want)
		}
	}
}

// The services page carries the full list the dashboard no longer does.
func TestServicesPageRendersEveryService(t *testing.T) {
	engine := testEngine(t, "NETBOX_FQDN=\"netbox.sddc.lab\"\nNETBOX_DIR=\"/opt/labprovider/netbox\"\n")
	srv := testServer(t, Options{
		Engine: engine,
		Docker: stubDocker{list: []docker.Container{
			{ID: "1", Name: "labprovider-netbox", State: "running", Project: "netbox", Uptime: "2h"},
		}},
	})

	body := renderPath(t, srv, "/services")
	for _, want := range []string{
		"https://netbox.sddc.lab", "/opt/labprovider/netbox",
		`href="/service/ca"`, `href="/service/netbox"`, `href="/service/mailpit"`,
		"running", "not deployed", "</html>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("services page missing %q", want)
		}
	}
}

// The rows are built from the unfiltered listing, so a service left out of
// CONTROL_PLANE_CONTAINER_FILTERS still reads as running; the filters only
// decide what the container tables show.
func TestCollectDockerFiltersDisplayNotServiceState(t *testing.T) {
	engine := testEngine(t, "")
	srv := testServer(t, Options{
		Engine:           engine,
		ContainerFilters: []string{"netbox"},
		Docker: stubDocker{list: []docker.Container{
			{ID: "1", Name: "labprovider-step-ca", State: "running", Project: "step-ca"},
			{ID: "2", Name: "labprovider-netbox", State: "running", Project: "netbox"},
			{ID: "3", Name: "unrelated-thing", State: "running", Project: "other"},
		}},
	})

	panel, _ := srv.collectDocker(context.Background(), time.Now())
	byName := map[string]ServiceRow{}
	for _, r := range panel.Services {
		byName[r.Name] = r
	}
	if got := byName["ca"].State; got != stateRunning {
		t.Errorf("ca state = %q; the filter must not decide service state", got)
	}
	if len(panel.Containers) != 1 || panel.Containers[0].Name != "labprovider-netbox" {
		t.Errorf("displayed containers = %+v, want only the filtered one", panel.Containers)
	}
	if len(panel.Unmanaged) != 0 {
		t.Errorf("unmanaged = %+v, want none (the stray container is filtered out)", panel.Unmanaged)
	}
}
