package deploy

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsjodin/labprovider/services/control-plane/internal/envfile"
)

// bindLoopbackPublish is a regex over a file goharbor/prepare generates, and
// harbor.go is honest that the golden test pins harbor.yml and the override
// rather than the compose file that is actually run. The consequence is that a
// HARBOR_PREPARE_IMAGE bump that changes the generated file's shape fails at
// deploy time - on the operator's lab host, minutes into an eight-container
// start, on a change CI called green.
//
// This runs the real prepare for the pinned version and asserts the rewrite
// still works on its actual output, so that bump fails in CI instead.
//
// Opt-in rather than skip-if-no-Docker: it pulls a few hundred megabytes and
// writes through a /:/hostfs mount, which is not something an unsuspecting
// `go test ./...` should do. CI sets the variable.
// takeOwnership hands back what prepare wrote. prepare runs as root inside the
// container, so the compose file and the whole tree around it come back
// root:root - and this test does not run as root. bindLoopbackPublish rewrites
// that file in place and cannot open it for writing, then t.TempDir's RemoveAll
// cannot unlink it either. The deploy never hits this because the control plane
// is root on the lab host. A container is the only thing here that is.
func takeOwnership(ctx context.Context, image string, dirs ...string) error {
	for _, dir := range dirs {
		if err := (Compose{}).RunRM(ctx, "--entrypoint", "chown", "-v", dir+":/target",
			image, "-R", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "/target"); err != nil {
			return fmt.Errorf("%s: %w", dir, err)
		}
	}
	return nil
}

func TestHarborPrepareOutputIsStillRewritable(t *testing.T) {
	if os.Getenv("LABPROVIDER_HARBOR_PREPARE_TEST") != "1" {
		t.Skip("set LABPROVIDER_HARBOR_PREPARE_TEST=1 to run the real goharbor/prepare")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("the test was requested but docker is not on PATH: %v", err)
	}

	example, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "config", "labprovider.env.example"))
	if err != nil {
		t.Fatal(err)
	}
	env := envfile.Parse(example)

	// The pinned version is the thing under test; everything else is a value
	// prepare needs to produce a complete file.
	if env["HARBOR_VERSION"] == "" || env["HARBOR_PREPARE_IMAGE"] == "" {
		t.Fatal("HARBOR_VERSION and HARBOR_PREPARE_IMAGE must be set in the example")
	}
	runtime := t.TempDir()
	env["HARBOR_DATA_DIR"] = filepath.Join(t.TempDir(), "data")
	env["HARBOR_DB_PASSWORD"] = "prepare-test-db-password"
	env["HARBOR_ADMIN_PASSWORD"] = "prepare-test-admin-password"

	if err := EnsureDir(env["HARBOR_DATA_DIR"], 0o755, -1, -1); err != nil {
		t.Fatal(err)
	}
	if err := Render("harbor.yml.tpl", env, filepath.Join(runtime, "harbor.yml"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir(harborConfigDir(runtime), 0o755, -1, -1); err != nil {
		t.Fatal(err)
	}

	// Ten minutes covers the image pull on a cold runner.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var logs strings.Builder
	cmp := Compose{Dir: runtime, Out: func(line string) { logs.WriteString(line + "\n") }}

	// Registered before the run, so a prepare that fails halfway still leaves
	// two directories t.TempDir can remove. Its RemoveAll was registered by the
	// t.TempDir calls above and cleanups run last-registered-first, so this one
	// goes first.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := takeOwnership(ctx, env["HARBOR_PREPARE_IMAGE"], runtime, env["HARBOR_DATA_DIR"]); err != nil {
			t.Logf("cleanup: %v", err)
		}
	})

	if err := cmp.RunRM(ctx, prepareArgs(env, runtime)...); err != nil {
		t.Fatalf("goharbor/prepare failed: %v\n%s", err, logs.String())
	}
	if err := takeOwnership(ctx, env["HARBOR_PREPARE_IMAGE"], runtime, env["HARBOR_DATA_DIR"]); err != nil {
		t.Fatalf("could not take back ownership of what prepare wrote: %v", err)
	}

	generated := filepath.Join(runtime, "docker-compose.yml")
	before, err := os.ReadFile(generated)
	if err != nil {
		t.Fatalf("prepare produced no compose file: %v\n%s", err, logs.String())
	}

	// The failure this exists to catch: prepare changed shape and the regex no
	// longer finds the publish line it must rewrite.
	if err := bindLoopbackPublish(generated, env["HARBOR_HTTP_PORT"]); err != nil {
		t.Fatalf("bindLoopbackPublish no longer matches %s output.\n"+
			"This is the deploy-time failure a HARBOR_PREPARE_IMAGE bump would cause, caught in CI:\n%v\n\n"+
			"generated compose file:\n%s", env["HARBOR_PREPARE_IMAGE"], err, before)
	}

	after, err := os.ReadFile(generated)
	if err != nil {
		t.Fatal(err)
	}
	want := `- "127.0.0.1:` + env["HARBOR_HTTP_PORT"] + `:8080"`
	if !strings.Contains(string(after), want) {
		t.Errorf("the rewrite did not bind the cleartext port to the loopback (want %s):\n%s", want, after)
	}
	// Harbor's cleartext port must not be published on every interface, which
	// is the whole reason the rewrite exists.
	for _, line := range strings.Split(string(after), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "-") && strings.Contains(trimmed, ":8080") &&
			!strings.Contains(trimmed, "127.0.0.1") {
			t.Errorf("a publish line still exposes 8080 on every interface: %q", trimmed)
		}
	}
}
