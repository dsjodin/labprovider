package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dsjodin/labprovider/services/control-plane/internal/access"
	"github.com/dsjodin/labprovider/services/control-plane/internal/deploy"
	"github.com/dsjodin/labprovider/services/control-plane/internal/disk"
	"github.com/dsjodin/labprovider/services/control-plane/internal/envfile"
)

const maxConfigBytes = 1 << 20 // an env file is a few KB; reject anything absurd

// maxJSONBytes bounds the small JSON request bodies. A deploy selection is a
// handful of service names and a depot fetch is a URL and a path.
const maxJSONBytes = 64 << 10

// registerControlPlane wires the config wizard and deploy engine routes when
// an engine is configured. Without one (the read-only --dashboard deployment)
// the dashboard keeps working and these routes simply do not exist.
func (s *Server) registerControlPlane(mux *http.ServeMux) {
	if s.opt.Engine == nil {
		return
	}
	mux.HandleFunc("GET /services", s.handleServicesPage)
	mux.HandleFunc("GET /service/{name}", s.handleServicePage)
	mux.HandleFunc("GET /config", func(w http.ResponseWriter, r *http.Request) {
		s.render(w, s.pages["wizard.html"], "layout", s.chrome("Configuration", "config"))
	})
	mux.HandleFunc("GET /deploy", func(w http.ResponseWriter, r *http.Request) {
		c := s.chrome("Deploy", "deploy")
		c.Narrow = true
		s.render(w, s.pages["deploy.html"], "layout", c)
	})
	mux.HandleFunc("GET /csr", func(w http.ResponseWriter, r *http.Request) {
		c := s.chrome("Sign CSR", "csr")
		c.Narrow = true
		s.render(w, s.pages["csr.html"], "layout", c)
	})
	mux.HandleFunc("POST /api/csr/sign", s.handleCSRSign)
	mux.HandleFunc("GET /api/ca/root", s.handleCARoot)
	mux.HandleFunc("GET /api/backup", s.handleBackup)
	mux.HandleFunc("GET /api/backup/contents", s.handleBackupContents)
	mux.HandleFunc("GET /api/config", s.handleConfigGet)
	mux.HandleFunc("POST /api/config/validate", s.handleConfigValidate)
	mux.HandleFunc("PUT /api/config", s.handleConfigPut)
	mux.HandleFunc("GET /api/seed", s.handleSeedGet)
	mux.HandleFunc("PUT /api/seed", s.handleSeedPut)
	mux.HandleFunc("GET /api/services", s.handleServices)
	mux.HandleFunc("POST /api/services/{name}/restart", s.handleServiceRestart)
	mux.HandleFunc("POST /api/depot/fetch", s.handleDepotFetch)
	mux.HandleFunc("GET /api/depot/fetch", s.handleDepotFetchStatus)
	mux.HandleFunc("POST /api/depot/fetch/cancel", s.handleDepotFetchCancel)
	mux.HandleFunc("POST /api/deploy", s.handleDeploy)
	mux.HandleFunc("POST /api/deploys/{id}/cancel", s.handleDeployCancel)
	mux.HandleFunc("GET /api/deploys/{id}/events", s.handleDeployEvents)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// handleConfigGet serves the managed config, or the shipped example when
// nothing has been uploaded yet, as a downloadable env file.
func (s *Server) handleConfigGet(w http.ResponseWriter, r *http.Request) {
	content, saved, err := s.opt.Engine.Store.Load()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	// The whole labprovider.env, which is every secret the lab has.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-labprovider-Config-Saved", strconv.FormatBool(saved))
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", `attachment; filename="labprovider.env"`)
	}
	_, _ = w.Write(content)
}

type validateResponse struct {
	Issues  []envfile.Issue `json:"issues"`
	Missing []string        `json:"missing_vars"`
	// MissingBlock is the missing entries with their example values and
	// comments, ready for the wizard to append to the textarea.
	MissingBlock string `json:"missing_block,omitempty"`
	Valid        bool   `json:"valid"`
}

func (s *Server) validateBody(r *http.Request) (validateResponse, []byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxConfigBytes+1))
	if err != nil {
		return validateResponse{}, nil, err
	}
	if len(body) > maxConfigBytes {
		return validateResponse{}, nil, fmt.Errorf("config too large")
	}
	resp := validateResponse{Issues: []envfile.Issue{}, Missing: []string{}}
	vars := envfile.Parse(body)
	if issues := envfile.ValidateAll(vars); issues != nil {
		resp.Issues = issues
	}
	if example, err := s.opt.Engine.Store.Example(); err == nil {
		if missing := envfile.MissingFromExample(body, example); missing != nil {
			resp.Missing = missing
			resp.MissingBlock = envfile.MissingBlock(body, example)
		}
	}
	resp.Valid = len(resp.Issues) == 0 && len(resp.Missing) == 0
	return resp, body, nil
}

func (s *Server) handleConfigValidate(w http.ResponseWriter, r *http.Request) {
	resp, _, err := s.validateBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleConfigPut validates and atomically saves the managed config. Missing
// variables block the save (the deploy engine would reject the file anyway);
// value-level issues are returned but do not block, so an operator can save
// incrementally while filling in secrets.
func (s *Server) handleConfigPut(w http.ResponseWriter, r *http.Request) {
	resp, body, err := s.validateBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(resp.Missing) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, resp)
		return
	}
	if err := s.opt.Engine.Store.Save(body); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// Let startup-bound components pick up the new config (e.g. the certsrv
	// listener binds/unbinds when VMSCA is toggled) without a restart.
	if s.opt.OnConfigSaved != nil {
		s.opt.OnConfigSaved()
	}
	writeJSON(w, http.StatusOK, resp)
}

// seedPath is the managed dns.seed location next to the managed config; the
// netbox and dns-sync deployers read the same path.
func (s *Server) seedPath() string {
	return filepath.Join(filepath.Dir(s.opt.Engine.Store.Path), "dns.seed")
}

// handleSeedGet serves the managed dns.seed (empty when none is saved).
func (s *Server) handleSeedGet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	b, err := os.ReadFile(s.seedPath())
	if err != nil && !os.IsNotExist(err) {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	_, _ = w.Write(b)
}

// handleSeedPut validates each record line (<fqdn> <ip[/cidr]>) and saves the
// file; an empty body deletes it (dns.seed is optional).
func (s *Server) handleSeedPut(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxConfigBytes+1))
	if err != nil || len(body) > maxConfigBytes {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("bad or oversized seed file"))
		return
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		_ = os.Remove(s.seedPath())
		writeJSON(w, http.StatusOK, map[string]any{"saved": false, "removed": true})
		return
	}
	if issues := validateSeed(body); len(issues) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"issues": issues})
		return
	}
	if err := os.WriteFile(s.seedPath(), body, 0o644); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"saved": true})
}

// handleCSRSign signs an uploaded PEM CSR with step-ca and returns the signed
// certificate (full chain). The requester keeps its private key; only the CSR
// crosses the wire.
func (s *Server) handleCSRSign(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxConfigBytes+1))
	if err != nil || len(body) > maxConfigBytes {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("bad or oversized CSR"))
		return
	}
	content, saved, err := s.opt.Engine.Store.Load()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if !saved {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("no configuration saved yet; save one in the config wizard first"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	crt, err := deploy.SignCSR(ctx, envfile.Parse(content), body)
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"cert": string(crt)})
}

// fqdn is the hostname the dashboard is reached at. The managed config is the
// source of truth - it is what the deploy engine renders Traefik's router rule
// from - and the operator can change it in the wizard without restarting the
// control plane, so it wins over the process environment. install.sh starts the
// container with no CONTROL_PLANE_FQDN at all, which leaves both empty until a
// configuration is saved; the header then shows no hostname rather than a wrong
// one.
func (s *Server) fqdn() string {
	if s.opt.Engine == nil {
		return s.opt.FQDN
	}
	content, saved, err := s.opt.Engine.Store.Load()
	if err != nil || !saved {
		return s.opt.FQDN
	}
	if v := envfile.Parse(content)["CONTROL_PLANE_FQDN"]; v != "" {
		return v
	}
	return s.opt.FQDN
}

// collectAccess builds the Access panel from the managed config: the deployed
// web UIs with their lab credentials, plus whether the root CA is downloadable.
// It needs the deploy engine (which owns the config); the read-only dashboard
// deployment renders the panel as not configured.
func (s *Server) collectAccess() AccessPanel {
	p := AccessPanel{}
	if s.opt.Engine == nil {
		p.Status = disabled("deploy engine not enabled")
		return p
	}
	content, saved, err := s.opt.Engine.Store.Load()
	if err != nil {
		p.Status = unavailable(err)
		return p
	}
	if !saved {
		p.Status = disabled("no configuration saved yet")
		return p
	}
	env := envfile.Parse(content)
	p.Entries = access.Build(env)
	if path := rootCAPath(env); path != "" {
		if _, err := os.Stat(path); err == nil {
			p.CAReady = true
		}
	}
	p.Status = ok()
	return p
}

// collectDisk reports the capacity of the filesystem holding WORKDIR plus the
// size of each service's data directory. Both come from the local host, so like
// the Access panel it needs the deploy engine only for the managed config.
func (s *Server) collectDisk() DiskPanel {
	p := DiskPanel{}
	if s.opt.Engine == nil {
		p.Status = disabled("deploy engine not enabled")
		return p
	}
	content, saved, err := s.opt.Engine.Store.Load()
	if err != nil {
		p.Status = unavailable(err)
		return p
	}
	if !saved {
		p.Status = disabled("no configuration saved yet")
		return p
	}
	env := envfile.Parse(content)
	root := env["WORKDIR"]
	if root == "" {
		p.Status = disabled("WORKDIR not set")
		return p
	}
	ov, err := s.disk.Fetch(root, s.diskTargets(env))
	if err != nil {
		p.Status = unavailable(err)
		return p
	}
	p.Overview = ov
	p.Status = ok()
	return p
}

// diskTargets is the per-service data directory list, in registry order, from
// the same serviceMeta join the Services panel uses. A service the operator has
// not deployed simply has no directory and drops out of the measurement.
func (s *Server) diskTargets(env map[string]string) []disk.Target {
	var targets []disk.Target
	for _, svc := range s.opt.Engine.Services() {
		meta, ok := serviceMeta[svc.Name()]
		if !ok || meta.dirKey == "" {
			continue
		}
		if dir := env[meta.dirKey]; dir != "" {
			targets = append(targets, disk.Target{Service: svc.Name(), Path: dir})
		}
	}
	return targets
}

// rootCAPath is the step-ca root certificate location, derived from CA_DATA_DIR
// the same way the MSCA chain loader is. Empty when CA_DATA_DIR is unset.
func rootCAPath(env map[string]string) string {
	dir := env["CA_DATA_DIR"]
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "certs", "root_ca.crt")
}

// handleCARoot serves the step-ca root certificate as a download so lab hosts
// (Windows or Linux) can add it to their trust store and treat step-ca-issued
// certificates as valid.
func (s *Server) handleCARoot(w http.ResponseWriter, r *http.Request) {
	content, saved, err := s.opt.Engine.Store.Load()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if !saved {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("no configuration saved yet"))
		return
	}
	path := rootCAPath(envfile.Parse(content))
	if path == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("CA_DATA_DIR not set"))
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("root CA not found; deploy the CA first"))
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", `attachment; filename="root_ca.crt"`)
	_, _ = w.Write(b)
}

func validateSeed(content []byte) []string {
	var issues []string
	for i, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			issues = append(issues, fmt.Sprintf("line %d: expected <fqdn> <ip> or <fqdn> <ip/cidr>", i+1))
			continue
		}
		value := fields[1]
		if strings.Contains(value, "/") {
			if _, err := netip.ParsePrefix(value); err != nil {
				issues = append(issues, fmt.Sprintf("line %d: invalid CIDR %q", i+1, value))
			}
		} else if _, err := netip.ParseAddr(value); err != nil {
			issues = append(issues, fmt.Sprintf("line %d: invalid IP %q", i+1, value))
		}
	}
	return issues
}

// foundationServices are the base infrastructure the deploy UI pre-selects and
// which must be deployed and up before any other service can be deployed. The
// order matches the intended deploy order (deps make it deterministic anyway).
var foundationServices = []string{"ca", "technitium", "traefik", "netbox", "dns-sync"}

func isFoundation(name string) bool {
	for _, f := range foundationServices {
		if f == name {
			return true
		}
	}
	return false
}

// composeProject maps a registry service to the Compose project its stack runs
// under, which is the basename of the directory holding its compose file. Only
// the services whose directory differs from their registry name are listed.
var composeProject = map[string]string{
	"ca":   "step-ca",
	"sftp": "sftpgo",
}

func projectOf(service string) string {
	if p, ok := composeProject[service]; ok {
		return p
	}
	return service
}

// liveProjects returns the Compose projects with at least one running
// container, and whether Docker answered at all. Docker is the source of truth
// for what is running; state.json is advisory history, so a service whose
// container died an hour ago must not still read as ready. When Docker is
// unreachable the caller falls back to the recorded history rather than
// declaring the whole platform down.
func (s *Server) liveProjects(ctx context.Context) (map[string]bool, bool) {
	if s.opt.Docker == nil {
		return nil, false
	}
	pctx, cancel := s.panelCtx(ctx)
	defer cancel()
	// No name filter: CONTROL_PLANE_CONTAINER_FILTERS omits several services
	// (traefik among them), and filtering here would report them as down.
	containers, err := s.opt.Docker.List(pctx, nil, s.opt.Now())
	if err != nil {
		s.opt.Logger.Warn("docker list for readiness", "err", err)
		return nil, false
	}
	live := map[string]bool{}
	for _, c := range containers {
		if c.State == "running" && c.Project != "" {
			live[c.Project] = true
		}
	}
	return live, true
}

// ready reports whether a service both deployed successfully and is running
// now. The recorded result alone is not enough: the engine records "ok" after
// its readiness gate passes, which says nothing about an hour later.
func ready(state deploy.State, live map[string]bool, dockerOK bool, name string) bool {
	st, ok := state.Services[name]
	if !ok || st.LastAction != "deploy" || st.Result != "ok" {
		return false
	}
	if !dockerOK {
		return true
	}
	return live[projectOf(name)]
}

// runningServices translates the live Compose projects into the engine's
// service names, so a dependency whose containers died is redeployed instead
// of skipped. A nil result tells the engine Docker had nothing to say.
func runningServices(engine *deploy.Engine, live map[string]bool, dockerOK bool) map[string]bool {
	if !dockerOK {
		return nil
	}
	running := map[string]bool{}
	for _, svc := range engine.Services() {
		if live[projectOf(svc.Name())] {
			running[svc.Name()] = true
		}
	}
	return running
}

func foundationReady(state deploy.State, live map[string]bool, dockerOK bool) bool {
	for _, name := range foundationServices {
		if !ready(state, live, dockerOK, name) {
			return false
		}
	}
	return true
}

type serviceInfo struct {
	Name       string   `json:"name"`
	Deps       []string `json:"deps"`
	Core       bool     `json:"core"`
	Ready      bool     `json:"ready"`
	Running    bool     `json:"running"`
	LastAction string   `json:"last_action,omitempty"`
	LastResult string   `json:"last_result,omitempty"`
	LastAt     string   `json:"last_at,omitempty"`
}

func (s *Server) handleServices(w http.ResponseWriter, r *http.Request) {
	var state deploy.State
	if s.opt.Engine.State != nil {
		state = s.opt.Engine.State.Snapshot()
	}
	live, dockerOK := s.liveProjects(r.Context())
	var out []serviceInfo
	for _, svc := range s.opt.Engine.Services() {
		name := svc.Name()
		info := serviceInfo{
			Name:    name,
			Deps:    svc.Deps(),
			Core:    isFoundation(name),
			Ready:   ready(state, live, dockerOK, name),
			Running: live[projectOf(name)],
		}
		if st, ok := state.Services[name]; ok {
			info.LastAction = st.LastAction
			info.LastResult = st.Result
			info.LastAt = st.At.Format("2006-01-02 15:04:05 UTC")
		}
		out = append(out, info)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleServicesPage renders every service as a card, full width. The dashboard
// carries a summary of the same data; this is where the whole list lives, so
// the dashboard does not have to grow a row per service forever.
func (s *Server) handleServicesPage(w http.ResponseWriter, r *http.Request) {
	panel, _ := s.collectServices(r.Context(), s.opt.Now())
	s.render(w, s.pages["services.html"], "layout", ServicesPage{
		Chrome:   s.chrome("Services", "services"),
		Services: panel,
	})
}

// handleServiceRestart restarts the containers of one service's Compose
// project. It is deliberately not a redeploy: nothing is rendered, no
// certificate is issued, and the containers come back on the compose file
// already on disk. The lever an operator reaches for when a container is wedged
// used to be a full deploy run.
func (s *Server) handleServiceRestart(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	known := false
	for _, svc := range s.opt.Engine.Services() {
		if svc.Name() == name {
			known = true
			break
		}
	}
	if !known {
		writeErr(w, http.StatusNotFound, fmt.Errorf("unknown service: %s", name))
		return
	}
	if s.opt.Docker == nil {
		writeErr(w, http.StatusServiceUnavailable, fmt.Errorf("Docker is not reachable from the control plane"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()

	containers, err := s.opt.Docker.List(ctx, nil, s.opt.Now())
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	project := projectOf(name)
	var restarted []string
	for _, c := range containers {
		if c.Project != project {
			continue
		}
		if err := s.opt.Docker.Restart(ctx, c.ID); err != nil {
			writeErr(w, http.StatusInternalServerError, fmt.Errorf("restart %s: %w", c.Name, err))
			return
		}
		restarted = append(restarted, c.Name)
	}
	if len(restarted) == 0 {
		writeErr(w, http.StatusConflict, fmt.Errorf("%s has no containers to restart; deploy it first", name))
		return
	}
	s.opt.Logger.Info("restarted service", "service", name, "containers", restarted)
	writeJSON(w, http.StatusOK, map[string]any{"service": name, "restarted": restarted})
}

type deployRequest struct {
	Services []string `json:"services"`
	Remove   bool     `json:"remove"`
}

func (s *Server) handleDeploy(w http.ResponseWriter, r *http.Request) {
	var req deployRequest
	// Services is an uncapped []string: without a limit a multi-megabyte array
	// is decoded into memory before Resolve rejects the first unknown name.
	// Every other body-reading handler here already bounds its input.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(req.Services) == 0 {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("no services selected"))
		return
	}
	// Enforce the two-phase flow the deploy UI presents: no non-foundation
	// service may be deployed until the whole foundation is up. Removes are
	// exempt (you can always tear a service down).
	live, dockerOK := s.liveProjects(r.Context())
	if !req.Remove && s.opt.Engine.State != nil && !foundationReady(s.opt.Engine.State.Snapshot(), live, dockerOK) {
		for _, name := range req.Services {
			if !isFoundation(name) {
				writeErr(w, http.StatusConflict, fmt.Errorf("deploy the foundation services (%s) before %s", strings.Join(foundationServices, ", "), name))
				return
			}
		}
	}
	id, err := s.opt.Engine.Start(req.Services, req.Remove, runningServices(s.opt.Engine, live, dockerOK))
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, deploy.ErrBusy) {
			status = http.StatusConflict
		}
		writeErr(w, status, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int{"id": id})
}

func (s *Server) handleDeployCancel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("bad deploy id"))
		return
	}
	if err := s.opt.Engine.Cancel(id); err != nil {
		status := http.StatusNotFound
		if errors.Is(err, deploy.ErrNotRunning) {
			status = http.StatusConflict
		}
		writeErr(w, status, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "cancelling"})
}

// handleDeployEvents streams a run's progress as SSE, replaying buffered
// events first so late subscribers get the full log.
func (s *Server) handleDeployEvents(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("bad deploy id"))
		return
	}
	run := s.opt.Engine.Run(id)
	if run == nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("no such deploy"))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	send := func(ev deploy.Event) {
		b, _ := json.Marshal(ev)
		fmt.Fprintf(w, "data: %s\n\n", b)
	}
	replay, live := run.Subscribe()
	if live != nil {
		defer run.Unsubscribe(live)
	}
	for _, ev := range replay {
		send(ev)
	}
	flusher.Flush()
	if live == nil {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-live:
			if !ok {
				return
			}
			send(ev)
			flusher.Flush()
		}
	}
}
