package deploy

import (
	"context"
	"net"
	"os"
	"time"
)

// Rsyslog deploys the containerized syslog collector. The image is built
// locally from an embedded Dockerfile; the rendered config is the complete
// /etc/rsyslog.conf (imudp/imtcp inputs, per-host dynafile output). The
// config is checked with rsyslogd -N1 before the stack starts, matching the
// bash module.
type Rsyslog struct{}

func (Rsyslog) Name() string   { return "rsyslog" }
func (Rsyslog) Deps() []string { return nil }

func (r Rsyslog) Deploy(ctx context.Context, rc *RunCtx) error {
	if err := EnsureDir(rc.Workdir("rsyslog"), 0o755, -1, -1); err != nil {
		return err
	}
	if err := EnsureDir(rc.Env["SYSLOG_LOG_DIR"], 0o755, -1, -1); err != nil {
		return err
	}
	if err := Render("rsyslog.conf.tpl", rc.Env, rc.Workdir("rsyslog")+"/rsyslog.conf", 0o644); err != nil {
		return err
	}
	if err := Render("rsyslog.Dockerfile", rc.Env, rc.Workdir("rsyslog")+"/build/Dockerfile", 0o644); err != nil {
		return err
	}
	if err := Render("docker-compose.rsyslog.yml.tpl", rc.Env, rc.Workdir("rsyslog")+"/docker-compose.yml", 0o644); err != nil {
		return err
	}
	cmp := rc.Compose("rsyslog")
	rc.Log("Building the rsyslog image %s.", rc.Env["RSYSLOG_IMAGE"])
	if err := cmp.Build(ctx, rc.Env["RSYSLOG_IMAGE"], rc.Workdir("rsyslog")+"/build"); err != nil {
		return err
	}
	tagBuiltVersion(ctx, rc, cmp, rc.Env["RSYSLOG_IMAGE"], "rsyslog")
	rc.Log("Validating the rendered rsyslog configuration (rsyslogd -N1).")
	if err := cmp.RunRM(ctx,
		"-v", rc.Workdir("rsyslog")+"/rsyslog.conf:/etc/rsyslog.conf:ro",
		rc.Env["RSYSLOG_IMAGE"], "rsyslogd", "-N1", "-f", "/etc/rsyslog.conf"); err != nil {
		return err
	}
	if err := cmp.Down(ctx); err != nil {
		return err
	}
	if err := cmp.Up(ctx); err != nil {
		return err
	}
	rc.Log("Waiting for the syslog TCP listener on port %s.", rc.Env["SYSLOG_PORT"])
	if err := WaitTCP(ctx, net.JoinHostPort("127.0.0.1", rc.Env["SYSLOG_PORT"]), 15, 2*time.Second); err != nil {
		return err
	}
	rc.Log("rsyslog is ready on UDP/TCP %s. Logs land under %s.", rc.Env["SYSLOG_PORT"], rc.Env["SYSLOG_LOG_DIR"])
	return nil
}

func (r Rsyslog) Remove(ctx context.Context, rc *RunCtx) error {
	cmp := rc.Compose("rsyslog")
	if err := cmp.Down(ctx); err != nil {
		return err
	}
	if err := os.RemoveAll(rc.Workdir("rsyslog")); err != nil {
		return err
	}
	rc.Log("Removed the rsyslog container and runtime files. Collected logs in %s were preserved.", rc.Env["SYSLOG_LOG_DIR"])
	return nil
}
