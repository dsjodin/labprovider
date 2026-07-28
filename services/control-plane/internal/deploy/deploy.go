// Package deploy is the control plane's deploy engine: a static registry of
// services with explicit dependencies, executed sequentially in dependency
// order, streaming progress events to SSE subscribers. Each deployer is a Go
// port of its bootstrap/*.sh module with identical data-preservation
// semantics.
package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dsjodin/labprovider/services/control-plane/internal/envfile"
)

// Service is one deployable unit. Deploy and Remove must be idempotent.
type Service interface {
	Name() string
	Deps() []string
	Deploy(ctx context.Context, rc *RunCtx) error
	Remove(ctx context.Context, rc *RunCtx) error
}

// RunCtx carries everything a deployer needs for one run.
type RunCtx struct {
	Env map[string]string // parsed labprovider.env plus derived fields
	Log func(format string, args ...any)
	eng *Engine
	svc string
}

// Workdir returns ${WORKDIR}/<sub>.
func (rc *RunCtx) Workdir(sub string) string {
	return filepath.Join(rc.Env["WORKDIR"], sub)
}

// Compose returns a compose runner rooted at ${WORKDIR}/<sub> whose output
// streams into the deploy log.
func (rc *RunCtx) Compose(sub string) Compose {
	return Compose{Dir: rc.Workdir(sub), Out: func(line string) { rc.Log("%s", line) }}
}

// Engine owns the registry, the single-flight deploy loop, and state.
type Engine struct {
	Store  envfile.Store
	State  *StateStore
	Logger *slog.Logger

	// ListenAddr is the address this control plane is actually serving on, so
	// the Traefik deployer routes to the port that is listening rather than to
	// whatever the managed config happens to say. install.sh owns the value;
	// see the CONTROL_PLANE_ADDR note in the example config.
	ListenAddr string

	services []Service // registration order IS the --all deploy order

	// prep serializes Start's preparation - Store.Load, MaterializeSecrets,
	// Validate - which runs before the single-flight claim below and is
	// read-then-create with no locking of its own. Two POST /api/deploy calls
	// arriving together on a first deploy would both find <secrets>/X absent,
	// both generate a value, and both write: last write wins on disk while each
	// caller's env map holds its own. The loser then hits ErrBusy and returns,
	// but the winner may be running with the value the loser overwrote - and for
	// a Postgres password that means initdb bakes in one value while the file on
	// disk says another, locking the platform out of its own database on the
	// next deploy. The window is narrow; the recovery is deleting a data
	// directory.
	prep sync.Mutex

	mu      sync.Mutex
	nextID  int
	current *Run         // nil when idle
	runs    map[int]*Run // by id, for SSE replay
}

func NewEngine(store envfile.Store, state *StateStore, logger *slog.Logger) *Engine {
	return &Engine{Store: store, State: state, Logger: logger, runs: map[int]*Run{}}
}

// Register appends a service; call in dependency order (deps first).
func (e *Engine) Register(s Service) {
	e.services = append(e.services, s)
}

// Services returns the registry in deploy order.
func (e *Engine) Services() []Service {
	return e.services
}

func (e *Engine) find(name string) Service {
	for _, s := range e.services {
		if s.Name() == name {
			return s
		}
	}
	return nil
}

// Resolve expands the selection with transitive dependencies and returns it
// in registry (dependency) order. "all" selects everything.
func (e *Engine) Resolve(selection []string) ([]string, error) {
	want := map[string]bool{}
	if slices.Contains(selection, "all") {
		for _, s := range e.services {
			want[s.Name()] = true
		}
	} else {
		var addDeps func(name string) error
		addDeps = func(name string) error {
			s := e.find(name)
			if s == nil {
				return fmt.Errorf("unknown service: %s", name)
			}
			if want[name] {
				return nil
			}
			want[name] = true
			for _, dep := range s.Deps() {
				if err := addDeps(dep); err != nil {
					return err
				}
			}
			return nil
		}
		for _, name := range selection {
			if err := addDeps(name); err != nil {
				return nil, err
			}
		}
	}

	var ordered []string
	for _, s := range e.services {
		if want[s.Name()] {
			ordered = append(ordered, s.Name())
		}
	}
	return ordered, nil
}

// Start validates the request and launches a deploy (or removal) in the
// background. It returns the run ID, or an error when a deploy is already in
// flight (single-flight) or validation fails.
//
// Explicitly selected services always run (idempotent redeploy). A dependency
// that was only pulled in by expansion is skipped when it last deployed
// successfully and Docker still reports it up - selecting technitium after ca
// is already up deploys just technitium. running carries that Docker view; nil
// means Docker was unreachable, so the recorded history decides on its own. In
// that case, if the history is stale, the dependent deployer's own readiness
// gate fails with a pointed "deploy <dep> first" error rather than silently
// misbehaving.
func (e *Engine) Start(selection []string, remove bool, running map[string]bool) (int, error) {
	e.prep.Lock()
	defer e.prep.Unlock()

	ordered, err := e.Resolve(selection)
	if err != nil {
		return 0, err
	}
	content, ok, err := e.Store.Load()
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, fmt.Errorf("no configuration uploaded yet; save one in the config wizard first")
	}
	env := envfile.Parse(content)
	if example, err := e.Store.Example(); err == nil {
		if missing := envfile.MissingFromExample(content, example); len(missing) > 0 {
			return 0, fmt.Errorf("config is outdated; missing variables: %v", missing)
		}
	}
	ipv4, network, err := envfile.DeriveHostIP(env["HOST_IP"])
	if err != nil {
		return 0, err
	}
	env["HOST_IPV4"] = ipv4
	env["HOST_NETWORK_CIDR"] = network

	// Secrets are materialized against the full resolved set, before the skip
	// decision: a generated value is part of a service's config hash, so a
	// dependency must be compared against the value it will actually be
	// deployed with, not against the empty variable in the managed file.
	secretsDir := e.secretsDir(env)
	generated, err := envfile.MaterializeSecrets(env, ordered, secretsDir)
	if err != nil {
		return 0, err
	}

	var skipped []string
	if remove {
		slices.Reverse(ordered)
	} else {
		kept := e.skipDeployedDeps(selection, ordered, running, env)
		for _, name := range ordered {
			if !slices.Contains(kept, name) {
				skipped = append(skipped, name)
			}
		}
		ordered = kept
		if issues := envfile.Validate(env, ordered); len(issues) > 0 {
			return 0, fmt.Errorf("config validation failed: %v", issues)
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.current != nil && !e.current.Done() {
		return 0, ErrBusy
	}
	e.nextID++
	run := newRun(e.nextID, ordered, remove)
	run.Skipped = skipped
	e.current = run
	e.runs[run.ID] = run
	e.pruneRuns()
	if len(generated) > 0 {
		run.emit(Event{Type: "log", Line: fmt.Sprintf("Generated missing secrets under %s: %s", secretsDir, strings.Join(generated, ", "))})
	}

	go e.execute(run, env)
	return run.ID, nil
}

// secretsDir holds the values MaterializeSecrets generates, alongside the
// dashboard's auto-provisioned NetBox and Technitium tokens. It sits next to
// the managed config rather than under WORKDIR: WORKDIR is runtime state that
// Remove deletes, and a regenerated PostgreSQL password would no longer match
// the data directory that deliberately survived the removal.
func (e *Engine) secretsDir(env map[string]string) string {
	if v := env["CONTROL_PLANE_SECRETS_DIR"]; v != "" {
		return v
	}
	return filepath.Join(filepath.Dir(e.Store.Path), "secrets")
}

var ErrBusy = fmt.Errorf("a deploy is already running")

// maxRunHistory bounds the retained runs available for SSE replay.
const maxRunHistory = 20

// pruneRuns drops the oldest runs past maxRunHistory. Each Run holds every log
// line it emitted, so an unpruned map grows without bound on a long-lived
// control plane. IDs increase monotonically, so the lowest are the oldest.
// Callers hold e.mu.
func (e *Engine) pruneRuns() {
	for len(e.runs) > maxRunHistory {
		oldest := 0
		for id := range e.runs {
			if oldest == 0 || id < oldest {
				oldest = id
			}
		}
		delete(e.runs, oldest)
	}
}

// maxRunDuration is the ceiling for one run. NetBox's first start alone can
// take ten minutes, so this is deliberately generous: it is a backstop against
// a wedged engine, not a performance budget.
const maxRunDuration = 60 * time.Minute

// ErrNotRunning is returned when a cancel targets a finished run.
var ErrNotRunning = fmt.Errorf("run is not running")

// Cancel stops a running deploy. The current step's context is cancelled, so
// the engine unblocks and reports the run as failed.
func (e *Engine) Cancel(id int) error {
	e.mu.Lock()
	run := e.runs[id]
	e.mu.Unlock()
	if run == nil {
		return fmt.Errorf("no such deploy")
	}
	if run.Done() {
		return ErrNotRunning
	}
	run.emit(Event{Type: "log", Line: "Cancel requested by the operator."})
	run.cancelRun()
	return nil
}

// skipDeployedDeps drops services that were added only by dependency
// expansion and whose last recorded deploy succeeded. "all" selects
// everything explicitly, so nothing is skipped there.
//
// A dependency has to clear three gates to be skipped:
//
//   - it last deployed successfully (state.json, advisory history);
//   - Docker reports it running. running, when non-nil, is that view; a nil map
//     means Docker could not be reached, in which case the history is all there
//     is. Docker is the source of truth, so a service whose containers died is
//     redeployed rather than skipped;
//   - its configuration has not changed since. Without this a deploy of netbox
//     after an edit to DNS_FORWARDER would leave technitium running the old
//     value, with nothing in the UI saying so. An entry recorded before
//     ConfigHash existed compares unequal and costs one redeploy.
func (e *Engine) skipDeployedDeps(selection, ordered []string, running map[string]bool, env map[string]string) []string {
	if slices.Contains(selection, "all") || e.State == nil {
		return ordered
	}
	state := e.State.Snapshot()
	var out []string
	for _, name := range ordered {
		if slices.Contains(selection, name) {
			out = append(out, name)
			continue
		}
		st, ok := state.Services[name]
		deployed := ok && st.LastAction == "deploy" && st.Result == "ok"
		unchanged := ok && st.ConfigHash == envfile.ServiceHash(env, name)
		if deployed && unchanged && (running == nil || running[name]) {
			continue // dependency already up on this config; its consumer's gate re-verifies
		}
		out = append(out, name)
	}
	return out
}

// Run returns a run by ID for SSE subscription.
func (e *Engine) Run(id int) *Run {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.runs[id]
}

func (e *Engine) execute(run *Run, env map[string]string) {
	// A run is bounded two ways: the operator can cancel it, and it cannot
	// outlive maxRunDuration. Without either, one hung upstream blocks every
	// later deploy with ErrBusy until the control plane restarts.
	ctx, cancel := context.WithTimeout(context.Background(), maxRunDuration)
	defer cancel()
	run.setCancel(cancel)

	verb := "deploy"
	if run.Remove {
		verb = "remove"
	}
	if len(run.Skipped) > 0 {
		run.emit(Event{Type: "log", Line: fmt.Sprintf("Skipping dependencies already deployed from this configuration: %s (tick them explicitly to redeploy).", strings.Join(run.Skipped, ", "))})
	}

	failed := false
	for _, name := range run.Services {
		svc := e.find(name)
		run.emit(Event{Type: "step-start", Service: name})
		rc := &RunCtx{
			Env: env,
			Log: func(format string, args ...any) {
				run.emit(Event{Type: "log", Service: name, Line: fmt.Sprintf(format, args...)})
			},
			eng: e,
			svc: name,
		}
		var err error
		if run.Remove {
			err = svc.Remove(ctx, rc)
		} else {
			err = svc.Deploy(ctx, rc)
		}
		if err != nil {
			if ctx.Err() != nil {
				err = fmt.Errorf("%s: %w", ctx.Err(), err)
			}
			run.emit(Event{Type: "step-failed", Service: name, Line: err.Error()})
			// No hash on a failure: the field means "the config this service is
			// successfully running", and a half-applied deploy is not that.
			e.recordResult(name, verb, "failed: "+err.Error(), "")
			failed = true
			break
		}
		// A deployer that ignores ctx must not let the run continue past a
		// cancel; stop before starting the next service.
		if ctx.Err() != nil {
			run.emit(Event{Type: "step-failed", Service: name, Line: ctx.Err().Error()})
			failed = true
			break
		}
		run.emit(Event{Type: "step-done", Service: name})
		hash := ""
		if !run.Remove {
			hash = envfile.ServiceHash(env, name)
		}
		e.recordResult(name, verb, "ok", hash)
	}

	if failed {
		run.finish("deploy-failed")
	} else {
		run.finish("deploy-done")
	}
}

func (e *Engine) recordResult(service, verb, result, configHash string) {
	if e.State == nil {
		return
	}
	if err := e.State.Record(service, verb, result, configHash); err != nil {
		e.Logger.Warn("record deploy state", "service", service, "err", err)
	}
}

// requireOutsideRuntime rejects a persistent directory that sits inside the
// service's runtime workdir. Remove deletes that workdir wholesale, so nesting
// data under it would silently turn "remove the service" into "delete the
// operator's data" - the one contract every deployer has to keep.
func requireOutsideRuntime(dir, runtime, varName, preserves string) error {
	if dir == runtime || strings.HasPrefix(dir, runtime+"/") {
		return fmt.Errorf("%s (%s) must not be inside %s so remove preserves %s", varName, dir, runtime, preserves)
	}
	return nil
}

// EnsureDir creates a directory with the given mode and optional uid/gid
// ownership (-1 skips chown), the Go equivalent of install -d + chown.
func EnsureDir(path string, mode os.FileMode, uid, gid int) error {
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	if uid >= 0 {
		if err := os.Chown(path, uid, gid); err != nil {
			return err
		}
	}
	return nil
}
