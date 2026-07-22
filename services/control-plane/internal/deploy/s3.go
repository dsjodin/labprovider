package deploy

import (
	"context"
	"net"
	"os"
	"time"
)

// S3 deploys SeaweedFS, the port of bootstrap/s3.sh.
type S3 struct{}

func (S3) Name() string   { return "s3" }
func (S3) Deps() []string { return nil }

func (s S3) Deploy(ctx context.Context, rc *RunCtx) error {
	if err := EnsureDir(rc.Workdir("s3"), 0o755, -1, -1); err != nil {
		return err
	}
	if err := EnsureDir(rc.Env["S3_DATA_DIR"], 0o755, -1, -1); err != nil {
		return err
	}
	if err := Render("docker-compose.s3.yml.tpl", rc.Env, rc.Workdir("s3")+"/docker-compose.yml", 0o644); err != nil {
		return err
	}
	c := rc.Compose("s3")
	if err := c.Down(ctx); err != nil {
		return err
	}
	if err := c.Up(ctx); err != nil {
		return err
	}
	rc.Log("Waiting for the S3 endpoint on port %s.", rc.Env["S3_PORT"])
	if err := WaitTCP(ctx, net.JoinHostPort("127.0.0.1", rc.Env["S3_PORT"]), 30, 2*time.Second); err != nil {
		return err
	}
	rc.Log("SeaweedFS S3 is ready on port %s. Data dir: %s", rc.Env["S3_PORT"], rc.Env["S3_DATA_DIR"])
	return nil
}

func (s S3) Remove(ctx context.Context, rc *RunCtx) error {
	c := rc.Compose("s3")
	if err := c.Down(ctx); err != nil {
		return err
	}
	if err := os.RemoveAll(rc.Workdir("s3")); err != nil {
		return err
	}
	rc.Log("Removed SeaweedFS containers and runtime files. Persistent data in %s was preserved.", rc.Env["S3_DATA_DIR"])
	return nil
}
