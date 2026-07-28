package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dsjodin/labprovider/services/control-plane/internal/docker"
)

func logsServer(t *testing.T) *Server {
	t.Helper()
	return testServer(t, Options{
		Docker: stubDocker{
			list: []docker.Container{
				{ID: "2", Name: "labprovider-netbox", State: "exited"},
				{ID: "1", Name: "labprovider-technitium", State: "running"},
			},
			lines: []string{"first line", "second line"},
		},
	})
}

func TestLogsPageListsContainers(t *testing.T) {
	body := renderPath(t, logsServer(t), "/logs")
	for _, want := range []string{
		"labprovider-netbox", "labprovider-technitium",
		"(exited)", // a container that just died is the one worth reading
		"last 2000",
		"</html>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("logs page missing %q", want)
		}
	}
	// Sorted by name, so netbox precedes technitium regardless of Docker's order.
	if strings.Index(body, "labprovider-netbox") > strings.Index(body, "labprovider-technitium") {
		t.Error("containers should be listed in name order")
	}
}

func TestLogsAPIReturnsLines(t *testing.T) {
	rec := httptest.NewRecorder()
	logsServer(t).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/logs/labprovider-netbox?tail=50", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got struct {
		Container string   `json:"container"`
		Tail      int      `json:"tail"`
		Lines     []string `json:"lines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Container != "labprovider-netbox" || got.Tail != 50 || len(got.Lines) != 2 {
		t.Errorf("got %+v", got)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("log responses must not be cached")
	}
}

// The cap is the point of the endpoint taking a tail at all: an unbounded tail
// reads a whole log file into the control plane.
func TestLogsAPICapsTail(t *testing.T) {
	rec := httptest.NewRecorder()
	logsServer(t).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/logs/labprovider-netbox?tail=999999", nil))
	var got struct {
		Tail int `json:"tail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Tail != maxLogTail {
		t.Errorf("tail = %d, want it capped at %d", got.Tail, maxLogTail)
	}
}

// Resolving by name means the endpoint cannot be pointed at a container the
// dashboard does not display.
func TestLogsAPIRejectsUnknownContainer(t *testing.T) {
	rec := httptest.NewRecorder()
	logsServer(t).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/logs/some-other-container", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestLogsAPITextFormatDownloads(t *testing.T) {
	rec := httptest.NewRecorder()
	logsServer(t).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/logs/labprovider-netbox?format=text", nil))
	if got := rec.Body.String(); got != "first line\nsecond line\n" {
		t.Errorf("body = %q", got)
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "labprovider-netbox.log") {
		t.Errorf("Content-Disposition = %q", rec.Header().Get("Content-Disposition"))
	}
}

// Without Docker the page renders "unavailable" rather than failing, like every
// other panel.
func TestLogsPageWithoutDocker(t *testing.T) {
	body := renderPath(t, testServer(t, Options{}), "/logs")
	if !strings.Contains(body, "Not configured") {
		t.Error("logs page should degrade to a status line")
	}
}
