// Package server collects current state from each upstream and renders it as an
// HTML page or JSON. Every panel is fetched under its own timeout and its
// errors are isolated: a dead or unconfigured source renders as "unavailable"
// or "not configured" and never blanks the page or fails the request.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/dsjodin/labprovider/services/control-plane/internal/auth"
	"github.com/dsjodin/labprovider/services/control-plane/internal/certs"
	"github.com/dsjodin/labprovider/services/control-plane/internal/deploy"
	"github.com/dsjodin/labprovider/services/control-plane/internal/disk"
	"github.com/dsjodin/labprovider/services/control-plane/internal/dns"
	"github.com/dsjodin/labprovider/services/control-plane/internal/docker"
	"github.com/dsjodin/labprovider/services/control-plane/internal/fetch"
	"github.com/dsjodin/labprovider/services/control-plane/internal/ipam"
	"github.com/dsjodin/labprovider/services/control-plane/internal/logs"
)

// Provider interfaces let the server run against real clients or test stubs.
type (
	CertProvider interface {
		List(ctx context.Context) ([]certs.Cert, error)
	}
	DNSProvider interface {
		Fetch(ctx context.Context) (dns.Overview, error)
	}
	IPAMProvider interface {
		Fetch(ctx context.Context) (ipam.Overview, error)
	}
	DockerProvider interface {
		List(ctx context.Context, nameFilters []string, now time.Time) ([]docker.Container, error)
		LogLines(ctx context.Context, id string, tail int) ([]string, error)
		Restart(ctx context.Context, id string) error
	}
)

type Options struct {
	// FQDN is the fallback dashboard hostname, from the process environment.
	// The managed config wins over it; see Server.fqdn.
	FQDN string
	// Version identifies the running build. install.sh passes git describe into
	// the image; "dev" is what an unstamped local build reports.
	Version          string
	WarnDays         int
	LogTail          int
	ContainerFilters []string
	Timeout          time.Duration
	MaxErrorLines    int

	Certs  CertProvider
	DNS    DNSProvider
	IPAM   IPAMProvider
	Docker DockerProvider

	// Engine enables the config wizard and deploy routes; nil (the read-only
	// --dashboard deployment) leaves the dashboard-only surface.
	Engine *deploy.Engine

	// Auth and Sessions enable the operator login. Both nil leaves every route
	// open, which is only defensible for the read-only dashboard deployment.
	Auth     *auth.Store
	Sessions *auth.Sessions

	// SetupToken gates the first-run /setup window. Nil disables the check.
	SetupToken *SetupToken

	// OnConfigSaved, when set, is invoked after the managed config is saved via
	// the wizard, so startup-bound components (the certsrv listener) can
	// reconcile to the new config without a control-plane restart.
	OnConfigSaved func()

	Logger *slog.Logger
	Now    func() time.Time // injectable clock; defaults to time.Now
}

type Server struct {
	opt    Options
	assets *assets
	pages  map[string]*template.Template // by template file name

	// disk caches the per-service directory walk across page loads; see
	// internal/disk for why it is not fetched inline.
	disk disk.Reporter

	// fetcher runs the depot's one-at-a-time URL transfers. Deliberately not
	// the deploy engine: an hour-long download must not hold single-flight.
	fetcher fetch.Fetcher

	// verifying caps concurrent password verifications. One bcrypt comparison
	// at cost 10 takes ~68ms, which throttles a serial attacker to ~15 guesses
	// a second on its own - but it does not bound concurrency, and N parallel
	// login attempts are N x 68ms of CPU on a box that is also running eighteen
	// containers. Four at a time keeps a login flood from starving the deploy
	// engine and the dashboard.
	verifying chan struct{}
}

// maxConcurrentVerifies is the width of the verifying semaphore.
const maxConcurrentVerifies = 4

func New(opt Options) (*Server, error) {
	if opt.Logger == nil {
		opt.Logger = slog.Default()
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Timeout <= 0 {
		opt.Timeout = 5 * time.Second
	}
	if opt.MaxErrorLines <= 0 {
		opt.MaxErrorLines = 50
	}
	a, err := newAssets()
	if err != nil {
		return nil, err
	}
	pages := map[string]*template.Template{}
	for _, name := range []string{"dashboard.html", "services.html", "service.html", "logs.html", "wizard.html", "deploy.html", "csr.html", "login.html", "account.html"} {
		t, err := a.parsePage(name)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		pages[name] = t
	}
	s := &Server{opt: opt, assets: a, pages: pages,
		verifying: make(chan struct{}, maxConcurrentVerifies)}
	s.fetcher.Logger = opt.Logger
	s.fetcher.Now = opt.Now
	return s, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// The version is here because /healthz is the one endpoint reachable
		// without a session: "which commit is running" has to be answerable
		// before you can answer "did my fix deploy?".
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.version()})
	})
	mux.HandleFunc("GET /static/", s.assets.handler())
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("GET /logs", s.handleLogsPage)
	mux.HandleFunc("GET /api/logs/{container}", s.handleLogs)
	mux.HandleFunc("GET /", s.handleIndex)
	s.registerControlPlane(mux)
	if s.opt.Auth == nil {
		return mux
	}
	s.registerAuth(mux)
	return s.requireAuth(mux)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	page := s.collect(r.Context())
	page.Chrome = s.chrome("Dashboard", "dashboard")
	s.render(w, s.pages["dashboard.html"], "layout", page)
}

// handleState serializes the whole page, Access panel included, so it carries
// every lab password in cleartext. no-store keeps it out of the disk cache.
func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	page := s.collect(r.Context())
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(page)
}

// collect fetches every panel concurrently, each under its own timeout, and
// returns the assembled page. A panic or error in one fetch cannot affect
// another.
func (s *Server) collect(ctx context.Context) Page {
	now := s.opt.Now()
	page := Page{
		FQDN:        s.fqdn(),
		GeneratedAt: now.UTC().Format(time.RFC3339),
	}

	// Access and Disk are built from the local managed config and the local
	// filesystem (no upstream fetch, and the directory walk is cached), so they
	// run synchronously rather than as timed panel goroutines.
	page.Access = s.collectAccess()
	page.Disk = s.collectDisk()

	var wg sync.WaitGroup
	wg.Add(4)

	go func() {
		defer wg.Done()
		page.Certs = s.collectCerts(ctx, now)
	}()
	go func() {
		defer wg.Done()
		page.DNS = s.collectDNS(ctx)
	}()
	go func() {
		defer wg.Done()
		page.IPAM = s.collectIPAM(ctx)
	}()
	go func() {
		defer wg.Done()
		page.Services, page.Errors = s.collectDocker(ctx, now)
	}()

	wg.Wait()
	return page
}

func (s *Server) panelCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.opt.Timeout)
}

func (s *Server) collectCerts(ctx context.Context, now time.Time) CertsPanel {
	p := CertsPanel{}
	if s.opt.Certs == nil {
		p.Status = disabled("CONTROL_PLANE_STEPCA_DSN not set")
		return p
	}
	pctx, cancel := s.panelCtx(ctx)
	defer cancel()
	raw, err := s.opt.Certs.List(pctx)
	if err != nil {
		p.Status = unavailable(err)
		return p
	}
	p.Summary = certs.Summarize(raw, now, s.opt.WarnDays)
	p.Status = ok()
	return p
}

func (s *Server) collectDNS(ctx context.Context) DNSPanel {
	p := DNSPanel{}
	if s.opt.DNS == nil {
		p.Status = disabled("CONTROL_PLANE_TECHNITIUM_URL/token not set")
		return p
	}
	pctx, cancel := s.panelCtx(ctx)
	defer cancel()
	ov, err := s.opt.DNS.Fetch(pctx)
	if err != nil {
		p.Status = unavailable(err)
		return p
	}
	p.Overview = ov
	p.Status = ok()
	return p
}

func (s *Server) collectIPAM(ctx context.Context) IPAMPanel {
	p := IPAMPanel{}
	if s.opt.IPAM == nil {
		p.Status = disabled("CONTROL_PLANE_NETBOX_URL/token not set")
		return p
	}
	pctx, cancel := s.panelCtx(ctx)
	defer cancel()
	ov, err := s.opt.IPAM.Fetch(pctx)
	if err != nil {
		p.Status = unavailable(err)
		return p
	}
	p.Overview = ov
	p.Status = ok()
	return p
}

// collectServices lists containers once and joins them into service rows. The
// returned slice is the displayed (filtered) container set, which the errors
// panel tails.
func (s *Server) collectServices(ctx context.Context, now time.Time) (ServicesPanel, []docker.Container) {
	svc := ServicesPanel{}
	if s.opt.Docker == nil {
		svc.Status = disabled("CONTROL_PLANE_DOCKER_HOST not available")
		return svc, nil
	}

	pctx, cancel := s.panelCtx(ctx)
	defer cancel()
	// Unfiltered: the service rows judge a service by the containers Docker
	// reports for its Compose project, and a service the operator left out of
	// CONTROL_PLANE_CONTAINER_FILTERS would otherwise read as stopped while it
	// is running. The filters still decide what is displayed.
	all, err := s.opt.Docker.List(pctx, nil, now)
	if err != nil {
		svc.Status = unavailable(err)
		return svc, nil
	}
	var containers []docker.Container
	for _, c := range all {
		if docker.MatchName(c.Name, c.Project, s.opt.ContainerFilters) {
			containers = append(containers, c)
		}
	}
	svc.Containers = containers
	svc.Services = s.serviceRows(all)
	svc.Unmanaged = s.unmanagedContainers(containers, svc.Services)
	svc.Status = ok()
	return svc, containers
}

// collectDocker builds both the services panel and the recent-errors panel from
// one container listing so a single Docker failure degrades both together.
func (s *Server) collectDocker(ctx context.Context, now time.Time) (ServicesPanel, ErrorsPanel) {
	svc, containers := s.collectServices(ctx, now)
	errp := ErrorsPanel{}
	if !svc.Status.OK() {
		errp.Status = svc.Status
		return svc, errp
	}

	// Errors panel: tail each running container's log under its own short
	// budget and stop once MaxErrorLines is reached.
	for _, c := range containers {
		if c.State != "running" {
			continue
		}
		if len(errp.Entries) >= s.opt.MaxErrorLines {
			break
		}
		lctx, lcancel := context.WithTimeout(ctx, s.opt.Timeout)
		lines, lerr := s.opt.Docker.LogLines(lctx, c.ID, s.opt.LogTail)
		lcancel()
		if lerr != nil {
			s.opt.Logger.Warn("tail logs", "container", c.Name, "err", lerr)
			continue
		}
		for _, e := range logs.Extract(c.Name, lines) {
			errp.Entries = append(errp.Entries, e)
			if len(errp.Entries) >= s.opt.MaxErrorLines {
				break
			}
		}
	}
	errp.Status = ok()
	return svc, errp
}
