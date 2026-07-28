package server

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"

	"github.com/dsjodin/labprovider/services/control-plane/internal/docker"
)

// Tail sizes the viewer offers. maxLogTail is enforced server-side: an
// unbounded tail against a chatty container reads its whole log file into the
// control plane, which is a memory read an operator did not ask for.
const (
	defaultLogTail = 200
	maxLogTail     = 2000
)

// LogsPage is the container picker; the lines themselves are fetched by the
// page, because a tail the operator changes should not cost a page render.
type LogsPage struct {
	Chrome
	Status     Status
	Containers []docker.Container
	Selected   string
	Tail       int
	TailSizes  []int
}

// MaxTail lets the page state the cap without duplicating the constant.
func (LogsPage) MaxTail() int { return maxLogTail }

func (s *Server) handleLogsPage(w http.ResponseWriter, r *http.Request) {
	page := LogsPage{
		Chrome:    s.chrome("Logs", "logs"),
		Selected:  r.URL.Query().Get("container"),
		Tail:      defaultLogTail,
		TailSizes: []int{100, 200, 500, 2000},
	}
	containers, status := s.logContainers(r)
	page.Containers, page.Status = containers, status
	if page.Selected == "" && len(containers) > 0 {
		page.Selected = containers[0].Name
	}
	s.render(w, s.pages["logs.html"], "layout", page)
}

// logContainers lists every container the filters display, running or not: a
// container that just died is the one whose log is worth reading.
func (s *Server) logContainers(r *http.Request) ([]docker.Container, Status) {
	if s.opt.Docker == nil {
		return nil, disabled("CONTROL_PLANE_DOCKER_HOST not available")
	}
	ctx, cancel := s.panelCtx(r.Context())
	defer cancel()
	all, err := s.opt.Docker.List(ctx, nil, s.opt.Now())
	if err != nil {
		return nil, unavailable(err)
	}
	var out []docker.Container
	for _, c := range all {
		if docker.MatchName(c.Name, c.Project, s.opt.ContainerFilters) {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, ok()
}

// handleLogs tails one container by name. By name rather than by ID because
// that is what the operator sees everywhere else in the UI, and because
// resolving it here means the endpoint cannot be pointed at a container the
// dashboard does not display.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if s.opt.Docker == nil {
		writeErr(w, http.StatusServiceUnavailable, fmt.Errorf("Docker is not reachable from the control plane"))
		return
	}
	name := r.PathValue("container")
	tail := defaultLogTail
	if v := r.URL.Query().Get("tail"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("tail must be a positive integer"))
			return
		}
		tail = min(n, maxLogTail)
	}

	containers, status := s.logContainers(r)
	if !status.OK() {
		writeErr(w, http.StatusServiceUnavailable, fmt.Errorf("%s", status.Error))
		return
	}
	var id string
	for _, c := range containers {
		if c.Name == name {
			id = c.ID
			break
		}
	}
	if id == "" {
		writeErr(w, http.StatusNotFound, fmt.Errorf("unknown container: %s", name))
		return
	}

	ctx, cancel := s.panelCtx(r.Context())
	defer cancel()
	lines, err := s.opt.Docker.LogLines(ctx, id, tail)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Query().Get("format") == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.log"`)
		for _, ln := range lines {
			_, _ = w.Write([]byte(ln + "\n"))
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"container": name, "tail": tail, "lines": lines})
}
