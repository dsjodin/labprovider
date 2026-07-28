package deploy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dsjodin/labprovider/services/control-plane/internal/envfile"
)

type fakeService struct {
	name string
	deps []string
}

func (f fakeService) Name() string                          { return f.name }
func (f fakeService) Deps() []string                        { return f.deps }
func (f fakeService) Deploy(context.Context, *RunCtx) error { return nil }
func (f fakeService) Remove(context.Context, *RunCtx) error { return nil }

func testEngine(t *testing.T) *Engine {
	t.Helper()
	e := NewEngine(envfile.Store{}, &StateStore{Path: filepath.Join(t.TempDir(), "state.json")}, nil)
	e.Register(fakeService{name: "ca"})
	e.Register(fakeService{name: "technitium", deps: []string{"ca"}})
	e.Register(fakeService{name: "netbox", deps: []string{"ca"}})
	e.Register(fakeService{name: "dns-sync", deps: []string{"netbox", "technitium"}})
	return e
}

func TestResolveExpandsDeps(t *testing.T) {
	e := testEngine(t)
	got, err := e.Resolve([]string{"dns-sync"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ca", "technitium", "netbox", "dns-sync"}
	if !slices.Equal(got, want) {
		t.Errorf("Resolve = %v, want %v", got, want)
	}
}

// skipTestEnv is a config the schema recognizes; only the hashed variables matter.
func skipTestEnv() map[string]string {
	return map[string]string{
		"HOST_IP":       "10.0.0.10/24",
		"SEARCH_DOMAIN": "lab.local",
		"WORKDIR":       "/opt/labprovider",
		"CA_FQDN":       "ca.lab.local",
		"CA_PORT":       "9000",
	}
}

func TestSkipDeployedDeps(t *testing.T) {
	e := testEngine(t)
	env := skipTestEnv()
	caHash := envfile.ServiceHash(env, "ca")

	// Nothing deployed yet: selecting technitium pulls in ca.
	ordered, _ := e.Resolve([]string{"technitium"})
	got := e.skipDeployedDeps([]string{"technitium"}, ordered, nil, env)
	if !slices.Equal(got, []string{"ca", "technitium"}) {
		t.Errorf("fresh host: %v, want [ca technitium]", got)
	}

	// ca deployed ok: selecting technitium runs only technitium.
	if err := e.State.Record("ca", "deploy", "ok", caHash); err != nil {
		t.Fatal(err)
	}
	got = e.skipDeployedDeps([]string{"technitium"}, ordered, nil, env)
	if !slices.Equal(got, []string{"technitium"}) {
		t.Errorf("ca deployed: %v, want [technitium]", got)
	}

	// A failed dependency is re-run.
	if err := e.State.Record("ca", "deploy", "failed: boom", ""); err != nil {
		t.Fatal(err)
	}
	got = e.skipDeployedDeps([]string{"technitium"}, ordered, nil, env)
	if !slices.Equal(got, []string{"ca", "technitium"}) {
		t.Errorf("ca failed: %v, want [ca technitium]", got)
	}

	// Explicit selection always runs, deployed or not.
	if err := e.State.Record("ca", "deploy", "ok", caHash); err != nil {
		t.Fatal(err)
	}
	ordered, _ = e.Resolve([]string{"ca", "technitium"})
	got = e.skipDeployedDeps([]string{"ca", "technitium"}, ordered, nil, env)
	if !slices.Equal(got, []string{"ca", "technitium"}) {
		t.Errorf("explicit ca: %v, want [ca technitium]", got)
	}

	// "all" never skips.
	ordered, _ = e.Resolve([]string{"all"})
	got = e.skipDeployedDeps([]string{"all"}, ordered, nil, env)
	if len(got) != len(ordered) {
		t.Errorf("all: %v, want everything (%v)", got, ordered)
	}
}

// A dependency that is deployed and running is still redeployed when the
// configuration it was deployed from has changed since.
func TestSkipDeployedDepsRedeploysChangedConfig(t *testing.T) {
	e := testEngine(t)
	env := skipTestEnv()
	if err := e.State.Record("ca", "deploy", "ok", envfile.ServiceHash(env, "ca")); err != nil {
		t.Fatal(err)
	}
	ordered, _ := e.Resolve([]string{"technitium"})
	running := map[string]bool{"ca": true, "technitium": true}

	got := e.skipDeployedDeps([]string{"technitium"}, ordered, running, env)
	if !slices.Equal(got, []string{"technitium"}) {
		t.Fatalf("unchanged config: %v, want [technitium]", got)
	}

	env["CA_PORT"] = "9443"
	got = e.skipDeployedDeps([]string{"technitium"}, ordered, running, env)
	if !slices.Equal(got, []string{"ca", "technitium"}) {
		t.Errorf("changed CA_PORT: %v, want [ca technitium]", got)
	}

	// A variable no service in this hash depends on must not trigger a redeploy.
	env["CA_PORT"] = "9000"
	env["SAMBA_PORT"] = "445"
	got = e.skipDeployedDeps([]string{"technitium"}, ordered, running, env)
	if !slices.Equal(got, []string{"technitium"}) {
		t.Errorf("unrelated variable: %v, want [technitium]", got)
	}

	// State written before ConfigHash existed reads as changed: one redeploy.
	if err := e.State.Record("ca", "deploy", "ok", ""); err != nil {
		t.Fatal(err)
	}
	got = e.skipDeployedDeps([]string{"technitium"}, ordered, running, env)
	if !slices.Equal(got, []string{"ca", "technitium"}) {
		t.Errorf("legacy state entry: %v, want [ca technitium]", got)
	}
}

// blockingService blocks in Deploy until its context is cancelled, standing in
// for a hung upstream.
type blockingService struct {
	name    string
	started chan struct{}
}

func (b blockingService) Name() string   { return b.name }
func (b blockingService) Deps() []string { return nil }
func (b blockingService) Deploy(ctx context.Context, _ *RunCtx) error {
	close(b.started)
	<-ctx.Done()
	return ctx.Err()
}
func (b blockingService) Remove(context.Context, *RunCtx) error { return nil }

func TestCancelUnblocksAHungRun(t *testing.T) {
	e := NewEngine(envfile.Store{}, &StateStore{Path: filepath.Join(t.TempDir(), "state.json")}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc := blockingService{name: "stuck", started: make(chan struct{})}
	e.Register(svc)

	run := newRun(1, []string{"stuck"}, false)
	e.current = run
	e.runs[1] = run
	go e.execute(run, map[string]string{})

	<-svc.started
	if err := e.Cancel(1); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	for i := 0; i < 200 && !run.Done(); i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if !run.Done() {
		t.Fatal("run did not finish after cancel")
	}
	if got := run.Result(); got != "deploy-failed" {
		t.Errorf("Result = %q, want deploy-failed", got)
	}
	if err := e.Cancel(1); !errors.Is(err, ErrNotRunning) {
		t.Errorf("Cancel of a finished run = %v, want ErrNotRunning", err)
	}
}

func TestEmitClosesSubscribersThatFallBehind(t *testing.T) {
	run := newRun(1, []string{"ca"}, false)
	_, ch := run.Subscribe()
	// The channel buffers 256 events; one more than that must close it rather
	// than silently dropping lines out of the middle of the log.
	for i := 0; i < 300; i++ {
		run.emit(Event{Type: "log", Line: "line"})
	}
	drained := 0
	for range ch {
		drained++
	}
	if drained != 256 {
		t.Errorf("drained %d events before close, want 256", drained)
	}
	if replay, _ := run.Subscribe(); len(replay) != 300 {
		t.Errorf("replay has %d events, want 300", len(replay))
	}
}

// Every closed tab and every EventSource reconnect used to leave a channel and
// up to 256 buffered events alive for the rest of the run.
func TestUnsubscribeDropsTheChannel(t *testing.T) {
	run := newRun(1, []string{"ca"}, false)
	_, gone := run.Subscribe()
	_, live := run.Subscribe()
	run.Unsubscribe(gone)

	if _, ok := <-gone; ok {
		t.Error("the unsubscribed channel should be closed")
	}
	run.emit(Event{Type: "log", Line: "after"})
	if got := <-live; got.Line != "after" {
		t.Errorf("the remaining subscriber got %+v", got)
	}
	if n := len(run.subs); n != 1 {
		t.Errorf("run retains %d subscribers, want 1", n)
	}

	// A channel emit already closed for falling behind, and a second
	// Unsubscribe, must both be no-ops rather than a double close.
	run.Unsubscribe(gone)
	run.Unsubscribe(live)
	run.Unsubscribe(live)
}

func TestRunHistoryIsCapped(t *testing.T) {
	e := testEngine(t)
	for i := 1; i <= maxRunHistory+5; i++ {
		run := newRun(i, []string{"ca"}, false)
		run.finish("deploy-done")
		e.runs[i] = run
		e.pruneRuns()
	}
	if len(e.runs) != maxRunHistory {
		t.Errorf("kept %d runs, want %d", len(e.runs), maxRunHistory)
	}
	if e.Run(1) != nil {
		t.Error("oldest run was not pruned")
	}
	if e.Run(maxRunHistory+5) == nil {
		t.Error("newest run was pruned")
	}
}

// Docker is the source of truth: a dependency whose last deploy succeeded but
// whose containers are gone must be redeployed, not skipped.
func TestSkipDeployedDepsRespectsDockerTruth(t *testing.T) {
	e := testEngine(t)
	env := skipTestEnv()
	if err := e.State.Record("ca", "deploy", "ok", envfile.ServiceHash(env, "ca")); err != nil {
		t.Fatal(err)
	}
	ordered, _ := e.Resolve([]string{"technitium"})

	got := e.skipDeployedDeps([]string{"technitium"}, ordered, map[string]bool{"ca": true}, env)
	if !slices.Equal(got, []string{"technitium"}) {
		t.Errorf("ca running: %v, want [technitium]", got)
	}
	got = e.skipDeployedDeps([]string{"technitium"}, ordered, map[string]bool{}, env)
	if !slices.Equal(got, []string{"ca", "technitium"}) {
		t.Errorf("ca deployed but not running: %v, want [ca technitium]", got)
	}
	// A nil map means Docker could not be reached; fall back to the history.
	got = e.skipDeployedDeps([]string{"technitium"}, ordered, nil, env)
	if !slices.Equal(got, []string{"technitium"}) {
		t.Errorf("docker unreachable: %v, want [technitium]", got)
	}
}

// The deploy that runs must run with the secret that is on disk. Start's
// preparation - Load, MaterializeSecrets, Validate - used to run before the
// single-flight claim, and MaterializeSecrets is read-then-create with no
// locking of its own: two deploys arriving together on a first deploy both
// found the secret absent, both generated a value, and both wrote. Last write
// won on disk while the run that got the single-flight slot kept its own. For a
// Postgres password that bakes one value into the data directory at initdb time
// and leaves another on disk, locking the platform out of its own database.
func TestConcurrentStartsAgreeOnGeneratedSecrets(t *testing.T) {
	const secretVar = "CA_POSTGRES_PASSWORD"
	for i := 0; i < 50; i++ {
		dir := t.TempDir()
		cfg := filepath.Join(dir, "labprovider.env")
		if err := os.WriteFile(cfg, []byte("HOST_IP=\"192.168.12.121/24\"\n"+secretVar+"=\"\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		deployed := make(chan string, 8)
		e := NewEngine(envfile.Store{Path: cfg}, &StateStore{Path: filepath.Join(dir, "state.json")}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		e.Register(recordingService{name: "ca", key: secretVar, seen: deployed})

		// All callers enter Start at once; remove skips config validation, which
		// a two-variable file cannot pass, while still running the secrets path.
		var ready, done sync.WaitGroup
		ready.Add(1)
		for j := 0; j < 8; j++ {
			done.Add(1)
			go func() {
				defer done.Done()
				ready.Wait()
				_, _ = e.Start([]string{"ca"}, true, nil)
			}()
		}
		ready.Done()
		done.Wait()

		var ran string
		select {
		case ran = <-deployed:
		case <-time.After(5 * time.Second):
			t.Fatal("no run reached the service")
		}
		// Let the run finish before t.TempDir cleans up under it: execute
		// writes state.json after the service returns.
		waitDone(t, e)
		onDisk, err := os.ReadFile(filepath.Join(dir, "secrets", secretVar))
		if err != nil {
			t.Fatalf("no secret was generated: %v", err)
		}
		if got := strings.TrimSpace(string(onDisk)); got != ran {
			t.Fatalf("the run deployed %q while %q is on disk", ran, got)
		}
	}
}

// waitDone blocks until the engine is idle again.
func waitDone(t *testing.T, e *Engine) {
	t.Helper()
	for i := 0; i < 500; i++ {
		e.mu.Lock()
		run := e.current
		e.mu.Unlock()
		if run == nil || run.Done() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the run never finished")
}

// recordingService reports the value of one env variable the run handed it.
type recordingService struct {
	name string
	key  string
	seen chan string
}

func (r recordingService) Name() string   { return r.name }
func (r recordingService) Deps() []string { return nil }
func (r recordingService) Deploy(_ context.Context, rc *RunCtx) error {
	r.seen <- rc.Env[r.key]
	return nil
}
func (r recordingService) Remove(_ context.Context, rc *RunCtx) error {
	r.seen <- rc.Env[r.key]
	return nil
}
