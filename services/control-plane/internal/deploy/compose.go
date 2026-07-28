package deploy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Compose executes the docker CLI (compose v2 and plain docker) in a service
// runtime directory, streaming combined output line-by-line into the deploy
// log. Exec keeps full behavior parity with the compose files (variable
// guards, profiles) at near-zero code cost versus the Docker SDK.
type Compose struct {
	Dir string
	// Project pins the Compose project name. Empty means the basename of Dir,
	// which is what Compose would have inferred anyway - so every stack whose
	// workdir is named after its service keeps the name it already has. NetBox
	// sets it explicitly: its directory is NETBOX_DIR, an operator-set path, and
	// an operator who moves it must not silently rename the project that
	// readiness, the dashboard, and reset.sh all look for.
	Project string
	Out     func(line string)
}

// composeArgs prefixes a compose subcommand with the pinned project name, so
// the project can never be inferred from a directory the operator renamed.
func (c Compose) composeArgs(args ...string) []string {
	project := c.Project
	if project == "" {
		project = filepath.Base(c.Dir)
	}
	return append([]string{"compose", "-p", project}, args...)
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
	// A line over the buffer limit (a compose pull progress burst, a container
	// logging a serialized blob) stops the scanner. Nothing would then drain the
	// pipe, docker would block writing into it, and cmd.Wait would block behind
	// that until maxRunDuration - an hour in which the single-flight engine
	// refuses every other deploy. Stop parsing, keep draining, say so.
	if err := sc.Err(); err != nil {
		if c.Out != nil {
			c.Out("output truncated: " + err.Error())
		}
		_, _ = io.Copy(io.Discard, stdout)
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("docker %v failed: %w", args, err)
	}
	return nil
}

func (c Compose) Up(ctx context.Context) error {
	return c.docker(ctx, c.composeArgs("up", "-d")...)
}

// Down is tolerant like the bash `docker compose down || true`: a missing
// compose file or already-gone stack is not an error.
func (c Compose) Down(ctx context.Context) error {
	if _, err := os.Stat(c.Dir + "/docker-compose.yml"); os.IsNotExist(err) {
		return nil
	}
	if err := c.docker(ctx, c.composeArgs("down")...); err != nil && c.Out != nil {
		c.Out("compose down: " + err.Error() + " (continuing)")
	}
	return nil
}

func (c Compose) Pull(ctx context.Context) error {
	return c.docker(ctx, c.composeArgs("pull")...)
}

// Build runs `docker build -t tag dir` (used for the locally built images).
func (c Compose) Build(ctx context.Context, tag, dir string) error {
	return c.docker(ctx, "build", "-t", tag, dir)
}

// Tag adds an additional tag to an existing local image.
func (c Compose) Tag(ctx context.Context, src, dst string) error {
	return c.docker(ctx, "tag", src, dst)
}

// Output runs docker with the given args and returns trimmed stdout, for
// reading a value (not streaming logs) out of a container.
func (c Compose) Output(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = c.Dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("docker %v failed: %w", args, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// RunRM runs a one-shot `docker run --rm` with the given args appended.
func (c Compose) RunRM(ctx context.Context, args ...string) error {
	return c.docker(ctx, append([]string{"run", "--rm"}, args...)...)
}

// Exec runs `docker compose exec -T <args>` against a running service,
// streaming output into the deploy log. -T disables the TTY so it works
// non-interactively.
func (c Compose) Exec(ctx context.Context, args ...string) error {
	return c.docker(ctx, c.composeArgs(append([]string{"exec", "-T"}, args...)...)...)
}
