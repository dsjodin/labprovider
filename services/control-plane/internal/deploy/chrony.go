package deploy

import (
	"context"
	"os"
	"time"
)

// Chrony deploys the containerized NTP server. The image is built locally
// from an embedded Dockerfile (no registry needed) and runs with host
// networking plus only the SYS_TIME capability so chronyd can discipline the
// host clock. Disabling systemd-timesyncd is one-time host prep in install.sh.
type Chrony struct{}

func (Chrony) Name() string   { return "chrony" }
func (Chrony) Deps() []string { return nil }

func (c Chrony) Deploy(ctx context.Context, rc *RunCtx) error {
	if err := EnsureDir(rc.Workdir("chrony"), 0o755, -1, -1); err != nil {
		return err
	}
	if err := EnsureDir(rc.Env["CHRONY_DIR"], 0o755, -1, -1); err != nil {
		return err
	}
	if err := Render("chrony.conf.tpl", rc.Env, rc.Workdir("chrony")+"/chrony.conf", 0o644); err != nil {
		return err
	}
	if err := Render("chrony.Dockerfile", rc.Env, rc.Workdir("chrony")+"/build/Dockerfile", 0o644); err != nil {
		return err
	}
	if err := Render("docker-compose.chrony.yml.tpl", rc.Env, rc.Workdir("chrony")+"/docker-compose.yml", 0o644); err != nil {
		return err
	}
	cmp := rc.Compose("chrony")
	rc.Log("Building the chrony image %s.", rc.Env["CHRONY_IMAGE"])
	if err := cmp.Build(ctx, rc.Env["CHRONY_IMAGE"], rc.Workdir("chrony")+"/build"); err != nil {
		return err
	}
	tagBuiltVersion(ctx, rc, cmp, rc.Env["CHRONY_IMAGE"], "chrony")
	if err := cmp.Down(ctx); err != nil {
		return err
	}
	if err := cmp.Up(ctx); err != nil {
		return err
	}
	// chronyc tracking succeeds once chronyd is up and answering; sources may
	// still be settling, which is fine for readiness.
	rc.Log("Verifying chronyd answers (chronyc tracking).")
	var err error
	for attempt := 0; attempt < 15; attempt++ {
		if err = cmp.docker(ctx, "compose", "exec", "-T", "chrony", "chronyc", "tracking"); err == nil {
			rc.Log("Chrony is ready on UDP 123.")
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return err
}

func (c Chrony) Remove(ctx context.Context, rc *RunCtx) error {
	cmp := rc.Compose("chrony")
	if err := cmp.Down(ctx); err != nil {
		return err
	}
	if err := os.RemoveAll(rc.Workdir("chrony")); err != nil {
		return err
	}
	rc.Log("Removed the chrony container and runtime files. Drift data in %s was preserved.", rc.Env["CHRONY_DIR"])
	return nil
}
