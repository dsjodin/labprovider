package deploy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// Harbor deploys the CNCF container registry, for the VKS and vSphere
// image-pull workflows: projects, robot accounts, image-pull secrets, and
// replication. It is the heaviest service labprovider hosts - eight containers
// before the optional scanner - and the only one whose compose file it does not
// write.
//
// # Why the compose file is generated
//
// Harbor ships goharbor/prepare, which reads harbor.yml and generates the
// compose file plus a per-component configuration tree. Hand-writing all of
// that would mean owning the internals of eight containers across every version
// bump; rendering one template and running one throwaway container is smaller,
// and it is the deployment path Harbor actually supports.
//
// The cost, stated because it breaks a property every other service here has:
// the golden test pins harbor.yml and the override, not the compose file that
// is actually run. If that trade stops being worth it, registry:3 behind
// Traefik is half a day and covers the disconnected-VKS case on its own.
//
// What covers the gap: the harbor-prepare CI job runs the real prepare for the
// pinned HARBOR_PREPARE_IMAGE and checks bindLoopbackPublish still matches its
// output, so a version bump that changes the generated file's shape fails there
// rather than on the operator's host mid-deploy.
type Harbor struct{}

func (Harbor) Name() string   { return "harbor" }
func (Harbor) Deps() []string { return []string{"ca", "traefik"} }

func (h Harbor) Deploy(ctx context.Context, rc *RunCtx) error {
	env := rc.Env
	runtime := rc.Workdir("harbor")

	// HARBOR_DATA_DIR holds the registry blobs and the PostgreSQL data
	// directory: pushed images and the account database live here, so it must
	// survive a remove.
	if err := requireOutsideRuntime(env["HARBOR_DATA_DIR"], runtime, "HARBOR_DATA_DIR", "the registry blobs and database"); err != nil {
		return err
	}
	if err := requireCAReady(ctx, env); err != nil {
		return err
	}
	for _, dir := range []string{runtime, env["HARBOR_DATA_DIR"]} {
		if err := EnsureDir(dir, 0o755, -1, -1); err != nil {
			return err
		}
	}

	if err := Render("harbor.yml.tpl", env, filepath.Join(runtime, "harbor.yml"), 0o600); err != nil {
		return err
	}
	if err := Render("docker-compose.harbor-override.yml.tpl", env, filepath.Join(runtime, "docker-compose.override.yml"), 0o644); err != nil {
		return err
	}

	cmp := rc.Compose("harbor")
	// Stop the previous stack before regenerating its compose file: prepare
	// overwrites docker-compose.yml, and `compose down` against a rewritten file
	// can miss containers the old one declared.
	if err := cmp.Down(ctx); err != nil {
		return err
	}

	if err := h.prepare(ctx, rc, cmp, runtime); err != nil {
		return err
	}
	if err := bindLoopbackPublish(filepath.Join(runtime, "docker-compose.yml"), env["HARBOR_HTTP_PORT"]); err != nil {
		return err
	}

	if err := cmp.Up(ctx); err != nil {
		return err
	}

	// Harbor initializes PostgreSQL and runs its migrations on first start, so
	// the budget is minutes rather than the usual seconds.
	url := fmt.Sprintf("http://127.0.0.1:%s/api/v2.0/ping", env["HARBOR_HTTP_PORT"])
	rc.Log("Waiting for the Harbor API on port %s (first start migrates its database; this takes a few minutes).", env["HARBOR_HTTP_PORT"])
	if err := waitHTTPPinned(ctx, url, 90, 5*time.Second); err != nil {
		return err
	}

	rc.Log("Harbor is ready at https://%s (admin / HARBOR_ADMIN_PASSWORD).", env["HARBOR_FQDN"])
	rc.Log("docker login %s needs the step-ca root in the client's trust store; download it from the dashboard's Access panel.", env["HARBOR_FQDN"])
	return nil
}

// prepare runs goharbor/prepare, which reads harbor.yml from /input and writes
// docker-compose.yml plus common/config/** into the runtime directory.
//
// Every mount here is one of the four paths prepare writes to, and all four are
// in Harbor's own install script: /input for harbor.yml, /compose_location for
// docker-compose.yml, /config for the per-component config tree, and /data for
// the generated secrets. Without the last two, prepare writes them inside the
// throwaway container and compose fails on the first missing env_file.
//
// The /hostfs mount is Harbor's documented invocation: prepare creates the data
// volume's subdirectories on the host and reads certificate paths through it.
// It is a wide mount, and it is not an escalation - the control plane is
// already root with the Docker socket, which can mount anything at all.
func (h Harbor) prepare(ctx context.Context, rc *RunCtx, cmp Compose, runtime string) error {
	image := rc.Env["HARBOR_PREPARE_IMAGE"]
	// The generated compose file names ./common/config/** relative to the
	// project directory, so this path is fixed by prepare's output, not chosen.
	configDir := harborConfigDir(runtime)
	if err := EnsureDir(configDir, 0o755, -1, -1); err != nil {
		return err
	}
	rc.Log("Generating the Harbor compose file with %s.", image)
	if err := cmp.RunRM(ctx, prepareArgs(rc.Env, runtime)...); err != nil {
		return fmt.Errorf("harbor prepare: %w", err)
	}
	if _, err := os.Stat(filepath.Join(runtime, "docker-compose.yml")); err != nil {
		return fmt.Errorf("harbor prepare produced no docker-compose.yml in %s: %w", runtime, err)
	}
	// The compose file's env_file entries point here. Checking one of them turns
	// a prepare that wrote its config inside the container into an error naming
	// the cause, instead of a compose failure on a missing env file.
	if _, err := os.Stat(filepath.Join(configDir, "core", "env")); err != nil {
		return fmt.Errorf("harbor prepare wrote no config tree to %s; the compose file it generated cannot start: %w", configDir, err)
	}
	return nil
}

func harborConfigDir(runtime string) string { return filepath.Join(runtime, "common", "config") }

func prepareArgs(env map[string]string, runtime string) []string {
	args := []string{
		"-v", runtime + ":/input",
		"-v", runtime + ":/compose_location",
		"-v", harborConfigDir(runtime) + ":/config",
		"-v", env["HARBOR_DATA_DIR"] + ":/data",
		"-v", "/:/hostfs",
		env["HARBOR_PREPARE_IMAGE"], "prepare",
	}
	if env["HARBOR_TRIVY_ENABLE"] == "true" {
		// Only prepare decides whether the scanner is in the compose file; the
		// trivy block in harbor.yml configures it but does not enable it.
		args = append(args, "--with-trivy")
	}
	return args
}

// publishRE matches the port publish prepare writes for Harbor's nginx, with or
// without quotes: `- 8085:8080`.
var publishRE = regexp.MustCompile(`(?m)^(\s*-\s*)"?(\d+):8080"?\s*$`)

// bindLoopbackPublish rewrites Harbor's published HTTP port to bind 127.0.0.1.
// Harbor's own configuration takes a port number and no address, so its
// cleartext registry and login endpoints would otherwise be published on every
// interface - the defect Tier 1.1 fixed for the other admin surfaces. Traefik
// reaches Harbor over the proxy network, so nothing needs the host port except
// the readiness probe.
//
// A generated file is a moving target, so a miss is an error rather than a
// silent pass: the alternative is exposing the port and telling nobody.
func bindLoopbackPublish(composePath, port string) error {
	content, err := os.ReadFile(composePath)
	if err != nil {
		return err
	}
	replaced := false
	out := publishRE.ReplaceAllFunc(content, func(m []byte) []byte {
		sub := publishRE.FindSubmatch(m)
		if string(sub[2]) != port {
			return m
		}
		replaced = true
		return append(append([]byte{}, sub[1]...), []byte(`"127.0.0.1:`+port+`:8080"`)...)
	})
	if !replaced {
		return fmt.Errorf("harbor: no %s:8080 port publish found in %s; the generated compose file changed shape, "+
			"and applying it unchanged would expose Harbor's cleartext port on every interface", port, composePath)
	}
	return os.WriteFile(composePath, out, 0o644)
}

func (h Harbor) Remove(ctx context.Context, rc *RunCtx) error {
	cmp := rc.Compose("harbor")
	if err := cmp.Down(ctx); err != nil {
		return err
	}
	if err := os.RemoveAll(rc.Workdir("harbor")); err != nil {
		return err
	}
	rc.Log("Removed Harbor containers and runtime files. The registry blobs and database in %s were preserved.",
		rc.Env["HARBOR_DATA_DIR"])
	return nil
}
