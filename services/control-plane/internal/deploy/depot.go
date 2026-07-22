package deploy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Depot deploys the VCF offline depot (nginx), the port of
// bootstrap/depot.sh: PROD directory layout, step-ca cert, managed htpasswd
// (now APR1-MD5 in Go instead of apache2-utils), HTTP + HTTPS health waits.
type Depot struct{}

func (Depot) Name() string   { return "depot" }
func (Depot) Deps() []string { return []string{"ca"} }

func (d Depot) Deploy(ctx context.Context, rc *RunCtx) error {
	env := rc.Env
	runtime := rc.Workdir("depot")

	if env["DEPOT_HTTP_PORT"] == env["DEPOT_HTTPS_PORT"] {
		return fmt.Errorf("DEPOT_HTTP_PORT and DEPOT_HTTPS_PORT must be different")
	}
	for _, dir := range []string{env["DEPOT_DATA_DIR"], env["DEPOT_CERT_DIR"]} {
		if dir == runtime || strings.HasPrefix(dir, runtime+"/") {
			return fmt.Errorf("%s must not be inside %s so remove preserves depot content", dir, runtime)
		}
	}
	if err := requireCAReady(ctx, env); err != nil {
		return err
	}

	for _, dir := range []string{
		runtime, env["DEPOT_DIR"], env["DEPOT_DATA_DIR"], env["DEPOT_AUTH_DIR"],
		filepath.Join(env["DEPOT_DATA_DIR"], "PROD", "COMP"),
		filepath.Join(env["DEPOT_DATA_DIR"], "PROD", "metadata"),
		filepath.Join(env["DEPOT_DATA_DIR"], "PROD", "vsan", "hcl"),
	} {
		if err := EnsureDir(dir, 0o755, -1, -1); err != nil {
			return err
		}
	}
	if err := IssueCert(ctx, rc, env["DEPOT_FQDN"], env["DEPOT_CERT_DIR"], "depot"); err != nil {
		return err
	}

	// Managed htpasswd, regenerated from env on every deploy.
	line, err := htpasswdLine(env["DEPOT_BASIC_AUTH_USER"], env["DEPOT_BASIC_AUTH_PASSWORD"])
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(env["DEPOT_AUTH_DIR"], "htpasswd"), []byte(line), 0o644); err != nil {
		return err
	}

	if err := Render("depot-nginx.conf.tpl", env, runtime+"/nginx.conf", 0o644); err != nil {
		return err
	}
	if err := Render("docker-compose.depot.yml.tpl", env, runtime+"/docker-compose.yml", 0o644); err != nil {
		return err
	}
	cmp := rc.Compose("depot")
	if err := cmp.Down(ctx); err != nil {
		return err
	}
	if err := cmp.Up(ctx); err != nil {
		return err
	}

	rc.Log("Waiting for the depot HTTP endpoint on port %s.", env["DEPOT_HTTP_PORT"])
	if err := waitHTTPPinned(ctx, fmt.Sprintf("http://%s:%s/healthz", env["DEPOT_FQDN"], env["DEPOT_HTTP_PORT"]), 60, 2*time.Second); err != nil {
		return err
	}
	rc.Log("Waiting for the depot HTTPS endpoint on port %s.", env["DEPOT_HTTPS_PORT"])
	caRoot := filepath.Join(env["CA_DATA_DIR"], "certs", "root_ca.crt")
	httpsURL := fmt.Sprintf("https://%s:%s/healthz", env["DEPOT_FQDN"], env["DEPOT_HTTPS_PORT"])
	if err := WaitHTTPSPinned(ctx, httpsURL, caRoot, 60, 2*time.Second); err != nil {
		return err
	}
	rc.Log("Depot is ready on http://%s:%s and https://%s:%s.",
		env["DEPOT_FQDN"], env["DEPOT_HTTP_PORT"], env["DEPOT_FQDN"], env["DEPOT_HTTPS_PORT"])
	return nil
}

func (d Depot) Remove(ctx context.Context, rc *RunCtx) error {
	cmp := rc.Compose("depot")
	if err := cmp.Down(ctx); err != nil {
		return err
	}
	if err := os.RemoveAll(rc.Workdir("depot")); err != nil {
		return err
	}
	os.Remove(filepath.Join(rc.Env["DEPOT_AUTH_DIR"], "htpasswd"))
	rc.Log("Removed depot containers and runtime files. Persistent depot content in %s and certificates in %s were preserved.",
		rc.Env["DEPOT_DATA_DIR"], rc.Env["DEPOT_CERT_DIR"])
	return nil
}
