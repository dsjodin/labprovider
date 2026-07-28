package deploy

import (
	"context"
	"fmt"
	"os"
	"time"
)

// Mailpit deploys Mailpit, an SMTP sink with a web UI for inspecting mail that
// lab services (vCenter, SDDC Manager, Aria) send. SMTP is published on the
// host so those services can point at it; the web UI is fronted by Traefik.
type Mailpit struct{}

func (Mailpit) Name() string   { return "mailpit" }
func (Mailpit) Deps() []string { return nil }

func (m Mailpit) Deploy(ctx context.Context, rc *RunCtx) error {
	env := rc.Env
	if err := EnsureDir(rc.Workdir("mailpit"), 0o755, -1, -1); err != nil {
		return err
	}
	if err := EnsureDir(env["MAILPIT_DATA_DIR"], 0o755, -1, -1); err != nil {
		return err
	}
	if err := Render("docker-compose.mailpit.yml.tpl", env, rc.Workdir("mailpit")+"/docker-compose.yml", 0o644); err != nil {
		return err
	}
	cmp := rc.Compose("mailpit")
	if err := cmp.Down(ctx); err != nil {
		return err
	}
	if err := cmp.Up(ctx); err != nil {
		return err
	}

	// The web UI serves plain HTTP on the loopback-published port; Traefik
	// fronts it publicly at https://<MAILPIT_FQDN>.
	url := fmt.Sprintf("http://%s:%s/api/v1/info", env["MAILPIT_FQDN"], env["MAILPIT_UI_PORT"])
	rc.Log("Waiting for the Mailpit UI at %s.", url)
	if err := waitHTTPPinned(ctx, url, 45, 2*time.Second); err != nil {
		return err
	}
	rc.Log("Mailpit is ready: UI at https://%s. Point service SMTP at %s:%s.",
		env["MAILPIT_FQDN"], env["HOST_IPV4"], env["MAILPIT_SMTP_PORT"])
	return nil
}

func (m Mailpit) Remove(ctx context.Context, rc *RunCtx) error {
	cmp := rc.Compose("mailpit")
	if err := cmp.Down(ctx); err != nil {
		return err
	}
	if err := os.RemoveAll(rc.Workdir("mailpit")); err != nil {
		return err
	}
	rc.Log("Removed Mailpit containers and runtime files. Persistent data in %s was preserved.", rc.Env["MAILPIT_DATA_DIR"])
	return nil
}
