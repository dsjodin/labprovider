package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dsjodin/labprovider/services/control-plane/internal/docker"
)

func restartServer(t *testing.T, d stubDocker) *Server {
	t.Helper()
	return testServer(t, Options{Engine: testEngine(t, ""), Docker: d})
}

func TestRestartRestartsEveryContainerInTheProject(t *testing.T) {
	var restarted []string
	srv := restartServer(t, stubDocker{
		restarted: &restarted,
		list: []docker.Container{
			{ID: "1", Name: "labprovider-netbox", State: "running", Project: "netbox"},
			{ID: "2", Name: "labprovider-netbox-postgres", State: "running", Project: "netbox"},
			{ID: "3", Name: "labprovider-step-ca", State: "running", Project: "step-ca"},
		},
	})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/services/netbox/restart", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	// Only netbox's project, and all of it: a stack is restarted whole or not
	// at all, and the CA is a different service.
	if len(restarted) != 2 || restarted[0] != "1" || restarted[1] != "2" {
		t.Errorf("restarted = %v, want the two netbox containers", restarted)
	}
	var body struct {
		Service   string   `json:"service"`
		Restarted []string `json:"restarted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Service != "netbox" || len(body.Restarted) != 2 {
		t.Errorf("body = %+v, want netbox with two containers", body)
	}
}

// ca maps to the step-ca Compose project, so the lookup has to go through
// projectOf rather than matching the registry name.
func TestRestartUsesTheComposeProjectName(t *testing.T) {
	var restarted []string
	srv := restartServer(t, stubDocker{
		restarted: &restarted,
		list:      []docker.Container{{ID: "9", Name: "labprovider-step-ca", State: "running", Project: "step-ca"}},
	})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/services/ca/restart", nil))
	if rec.Code != http.StatusOK || len(restarted) != 1 {
		t.Fatalf("status = %d, restarted = %v", rec.Code, restarted)
	}
}

func TestRestartRejectsUnknownAndUndeployedServices(t *testing.T) {
	srv := restartServer(t, stubDocker{list: []docker.Container{
		{ID: "1", Name: "labprovider-netbox", State: "running", Project: "netbox"},
	}})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/services/nope/restart", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown service status = %d, want 404", rec.Code)
	}

	// Registered but with no containers: nothing to restart, and saying so
	// beats reporting a successful no-op.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/services/mailpit/restart", nil))
	if rec.Code != http.StatusConflict {
		t.Errorf("undeployed service status = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "deploy it first") {
		t.Errorf("body = %s, want a pointer to deploying first", rec.Body)
	}
}
