package server

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/dsjodin/labprovider/services/control-plane/internal/config"
	"github.com/dsjodin/labprovider/services/control-plane/internal/deploy"
	"github.com/dsjodin/labprovider/services/control-plane/internal/envfile"
)

// Six hand-maintained tables have to agree with the deploy registry, and until
// this test nothing checked that they did. Two had already drifted, both when
// Harbor was added, and both failed silently: Harbor's logs were invisible in
// the UI and its containers survived a reset.sh that reported a clean host.
//
// Every exception below is a deliberate one with the reason attached. A new
// service that belongs in a table but is not in it fails here, at the cost of
// one line, rather than in a lab.
func registry(t *testing.T) []string {
	t.Helper()
	engine := deploy.NewEngine(envfile.Store{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	deploy.RegisterAll(engine)
	names := make([]string, 0, len(engine.Services()))
	for _, svc := range engine.Services() {
		names = append(names, svc.Name())
	}
	if len(names) == 0 {
		t.Fatal("the registry is empty")
	}
	return names
}

// repoFile reads a file from the repository root, which is four levels above
// this package.
func repoFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "..", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestEveryServiceHasADashboardRow(t *testing.T) {
	for _, name := range registry(t) {
		if _, ok := serviceMeta[name]; !ok {
			t.Errorf("service %q has no serviceMeta entry, so its dashboard row has no FQDN or disk target", name)
		}
	}
	for name := range serviceMeta {
		if !contains(registry(t), name) {
			t.Errorf("serviceMeta lists %q, which no deployer registers", name)
		}
	}
}

// foundationServices gates every other deploy, so a name here that no deployer
// registers would make foundationReady permanently false and POST /api/deploy
// reject every non-foundation service with 409.
func TestFoundationServicesAreRegistered(t *testing.T) {
	names := registry(t)
	for _, name := range foundationServices {
		if !contains(names, name) {
			t.Errorf("foundationServices lists %q, which no deployer registers", name)
		}
	}
}

// composeProject only lists the services whose stack directory differs from
// their registry name, so the check is that every override names something real
// and that no override is stale.
func TestComposeProjectOverridesAreRegistered(t *testing.T) {
	names := registry(t)
	for service, project := range composeProject {
		if !contains(names, service) {
			t.Errorf("composeProject maps %q, which no deployer registers", service)
		}
		if service == project {
			t.Errorf("composeProject maps %q to itself; that is projectOf's default, drop the entry", service)
		}
	}
}

// A service missing from the filters is invisible in the container table, in
// the log viewer's picker, and to the Recent errors panel - while its dashboard
// row keeps working, because collectServices deliberately lists unfiltered. So
// the failure is silent and partial, which is why it went unnoticed for Harbor.
func TestContainerFiltersCoverEveryService(t *testing.T) {
	os.Unsetenv("CONTROL_PLANE_CONTAINER_FILTERS")
	filters := config.Load().ContainerFilters
	example := repoFile(t, filepath.Join("config", "labprovider.env.example"))
	exampleFilters := envfile.Parse([]byte(example))["CONTROL_PLANE_CONTAINER_FILTERS"]

	for _, name := range registry(t) {
		project := projectOf(name)
		if !matchesFilter(project, filters) {
			t.Errorf("no CONTROL_PLANE_CONTAINER_FILTERS default matches %q (project %q)", name, project)
		}
		if !matchesFilter(project, strings.Split(exampleFilters, ",")) {
			t.Errorf("no CONTROL_PLANE_CONTAINER_FILTERS entry in labprovider.env.example matches %q (project %q)", name, project)
		}
	}
	// The two defaults are copies of each other and drift the same way.
	if got := strings.Join(filters, ","); got != exampleFilters {
		t.Errorf("the filter default in config.go and the one in the example have diverged:\n go: %s\nenv: %s", got, exampleFilters)
	}
}

// reset.sh removes one compose project per service. A missing name leaves the
// stack running while the script reports a clean host, and the next install
// hits a port collision instead of a fresh slate.
func TestResetScriptRemovesEveryProject(t *testing.T) {
	script := repoFile(t, "reset.sh")
	m := regexp.MustCompile(`(?s)LABPROVIDER_PROJECTS=\((.*?)\)`).FindStringSubmatch(script)
	if m == nil {
		t.Fatal("LABPROVIDER_PROJECTS array not found in reset.sh; this test parses it by shape")
	}
	listed := strings.Fields(m[1])

	for _, name := range registry(t) {
		project := projectOf(name)
		if !contains(listed, project) {
			t.Errorf("reset.sh LABPROVIDER_PROJECTS is missing %q (service %q)", project, name)
		}
	}
	projects := map[string]bool{}
	for _, name := range registry(t) {
		projects[projectOf(name)] = true
	}
	for _, project := range listed {
		if !projects[project] {
			t.Errorf("reset.sh removes project %q, which no registered service creates", project)
		}
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func matchesFilter(project string, filters []string) bool {
	for _, f := range filters {
		if f = strings.TrimSpace(f); f != "" && strings.Contains(project, f) {
			return true
		}
	}
	return false
}
