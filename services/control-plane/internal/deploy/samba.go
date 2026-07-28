package deploy

import (
	"context"
	"net"
	"os"
	"time"
)

// Samba deploys an SMB/CIFS file share. The image is built locally from an
// embedded Dockerfile (no registry needed); the entrypoint provisions the
// single share user from the compose environment on every start, and the
// rendered smb.conf defines one share. Raw TCP on 445, so no Traefik.
type Samba struct{}

func (Samba) Name() string   { return "samba" }
func (Samba) Deps() []string { return nil }

func (s Samba) Deploy(ctx context.Context, rc *RunCtx) error {
	env := rc.Env
	if err := EnsureDir(rc.Workdir("samba"), 0o755, -1, -1); err != nil {
		return err
	}
	if err := EnsureDir(env["SAMBA_SHARE_DIR"], 0o755, 1000, 1000); err != nil {
		return err
	}
	if err := Render("smb.conf.tpl", env, rc.Workdir("samba")+"/smb.conf", 0o644); err != nil {
		return err
	}
	if err := Render("samba.Dockerfile", env, rc.Workdir("samba")+"/build/Dockerfile", 0o644); err != nil {
		return err
	}
	if err := Render("samba-entrypoint.sh", env, rc.Workdir("samba")+"/build/entrypoint.sh", 0o755); err != nil {
		return err
	}
	if err := Render("docker-compose.samba.yml.tpl", env, rc.Workdir("samba")+"/docker-compose.yml", 0o644); err != nil {
		return err
	}
	cmp := rc.Compose("samba")
	rc.Log("Building the samba image %s.", env["SAMBA_IMAGE"])
	if err := cmp.Build(ctx, env["SAMBA_IMAGE"], rc.Workdir("samba")+"/build"); err != nil {
		return err
	}
	tagBuiltVersion(ctx, rc, cmp, env["SAMBA_IMAGE"], "samba")
	if err := cmp.Down(ctx); err != nil {
		return err
	}
	if err := cmp.Up(ctx); err != nil {
		return err
	}
	rc.Log("Waiting for the SMB listener on port %s.", env["SAMBA_PORT"])
	if err := WaitTCP(ctx, net.JoinHostPort("127.0.0.1", env["SAMBA_PORT"]), 30, 2*time.Second); err != nil {
		return err
	}
	rc.Log("Samba is ready: //%s/%s (or //%s/%s once dns-sync publishes the name) (user %s). Share dir: %s.",
		env["SAMBA_FQDN"], env["SAMBA_SHARE_NAME"], env["HOST_IPV4"], env["SAMBA_SHARE_NAME"],
		env["SAMBA_USER"], env["SAMBA_SHARE_DIR"])
	return nil
}

func (s Samba) Remove(ctx context.Context, rc *RunCtx) error {
	cmp := rc.Compose("samba")
	if err := cmp.Down(ctx); err != nil {
		return err
	}
	if err := os.RemoveAll(rc.Workdir("samba")); err != nil {
		return err
	}
	rc.Log("Removed the samba container and runtime files. Share content in %s was preserved.", rc.Env["SAMBA_SHARE_DIR"])
	return nil
}
