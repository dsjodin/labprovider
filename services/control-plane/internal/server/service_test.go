package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dsjodin/labprovider/services/control-plane/internal/docker"
)

const netboxConfig = "NETBOX_FQDN=\"netbox.sddc.lab\"\nNETBOX_DIR=\"/opt/labprovider/netbox\"\n" +
	"NETBOX_SUPERUSER_NAME=\"admin\"\nNETBOX_SUPERUSER_PASSWORD=\"hunter2\"\n" +
	"NETBOX_POSTGRES_PASSWORD=\"\"\nHOST_IP=\"10.0.0.5/24\"\n"

func TestServicePageRendersConfigAccessAndLogs(t *testing.T) {
	engine := testEngine(t, netboxConfig)
	srv := testServer(t, Options{
		Engine: engine,
		Docker: stubDocker{
			list:  []docker.Container{{ID: "1", Name: "labprovider-netbox", State: "running", Project: "netbox", Uptime: "2h"}},
			lines: []string{"netbox started", "listening on 8080"},
		},
	})

	body := renderPath(t, srv, "/service/netbox")
	for _, want := range []string{
		"https://netbox.sddc.lab",           // address from the row
		"/opt/labprovider/netbox",           // data directory
		"NETBOX_FQDN",                       // a variable the service is deployed from
		"HOST_IP",                           // and the common ones
		"labprovider-netbox",                // its container
		"listening on 8080",                 // its recent output
		"generated, stored on disk",         // empty generated secret, not "not set"
		`onclick="runService(this, false)"`, // deploy action
	} {
		if !strings.Contains(body, want) {
			t.Errorf("service page missing %q", want)
		}
	}

	// A secret renders masked rather than as plain text in a table cell.
	if !strings.Contains(body, `<span class="val mono masked">hunter2</span>`) {
		t.Error("password should render masked")
	}
	// Variables belonging to another service do not appear.
	if strings.Contains(body, "CA_POSTGRES_PASSWORD") {
		t.Error("service page shows another service's variables")
	}
}

func TestServicePageUnknownServiceIs404(t *testing.T) {
	srv := testServer(t, Options{Engine: testEngine(t, "")})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/service/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /service/nope = %d, want 404", rec.Code)
	}
}

// Docker being unreachable must not blank the page: the configuration and the
// deploy history are local, and they are what an operator debugging a broken
// lab came for.
func TestServicePageWithoutDocker(t *testing.T) {
	srv := testServer(t, Options{Engine: testEngine(t, netboxConfig)})
	body := renderPath(t, srv, "/service/netbox")
	for _, want := range []string{"NETBOX_FQDN", "not deployed", "</html>"} {
		if !strings.Contains(body, want) {
			t.Errorf("service page missing %q", want)
		}
	}
}
