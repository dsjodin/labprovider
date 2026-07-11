package deploy

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
)

// Compose executes the docker CLI (compose v2 and plain docker) in a service
// runtime directory, streaming combined output line-by-line into the deploy
// log. Exec keeps full behavior parity with the compose files (variable
// guards, profiles) at near-zero code cost versus the Docker SDK.
type Compose struct {
	Dir string
	Out func(line string)
}

func (c Compose) docker(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = c.Dir
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout // interleave, like a terminal
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("docker %v: %w", args, err)
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if c.Out != nil {
			c.Out(sc.Text())
		}
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("docker %v failed: %w", args, err)
	}
	return nil
}

func (c Compose) Up(ctx context.Context) error {
	return c.docker(ctx, "compose", "up", "-d")
}

// Down is tolerant like the bash `docker compose down || true`: a missing
// compose file or already-gone stack is not an error.
func (c Compose) Down(ctx context.Context) error {
	if _, err := os.Stat(c.Dir + "/docker-compose.yml"); os.IsNotExist(err) {
		return nil
	}
	if err := c.docker(ctx, "compose", "down"); err != nil && c.Out != nil {
		c.Out("compose down: " + err.Error() + " (continuing)")
	}
	return nil
}

func (c Compose) Pull(ctx context.Context) error {
	return c.docker(ctx, "compose", "pull")
}

// Build runs `docker build -t tag dir` (used for the locally built images).
func (c Compose) Build(ctx context.Context, tag, dir string) error {
	return c.docker(ctx, "build", "-t", tag, dir)
}

// RunRM runs a one-shot `docker run --rm` with the given args appended.
func (c Compose) RunRM(ctx context.Context, args ...string) error {
	return c.docker(ctx, append([]string{"run", "--rm"}, args...)...)
}
