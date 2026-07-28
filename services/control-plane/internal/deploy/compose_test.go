package deploy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// The project name must not follow the directory. NetBox composes from
// NETBOX_DIR, which the operator sets: before this was pinned, pointing it at
// /srv/netbox-data renamed the project to "netbox-data", and readiness, the
// dashboard, and reset.sh all look for "netbox".
func TestComposeArgsPinTheProjectName(t *testing.T) {
	tests := []struct {
		name    string
		compose Compose
		want    string
	}{
		{"basename by default", Compose{Dir: "/opt/labprovider/technitium"}, "technitium"},
		{"explicit project wins", Compose{Dir: "/srv/netbox-data", Project: "netbox"}, "netbox"},
		{"explicit project with a matching dir", Compose{Dir: "/opt/labprovider/netbox", Project: "netbox"}, "netbox"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := tc.compose.composeArgs("up", "-d")
			if !slices.Equal(args, []string{"compose", "-p", tc.want, "up", "-d"}) {
				t.Errorf("composeArgs = %v, want project %q", args, tc.want)
			}
		})
	}
}

// A single output line past the scanner's 1 MiB limit used to stop the read
// loop with nothing draining the pipe: docker blocked writing, cmd.Wait blocked
// behind it, and the run hung until maxRunDuration - an hour of ErrBusy for
// every other deploy. The stand-in docker on PATH emits exactly that.
func TestDockerDrainsAfterAnOverlongLine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell stand-in for docker")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nawk 'BEGIN { while (i++ < 2100000) printf \"x\" ; print \"\" ; print \"done\" }'\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var mu sync.Mutex
	var lines []string
	c := Compose{Dir: dir, Out: func(line string) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, line)
	}}

	done := make(chan error, 1)
	go func() { done <- c.docker(context.Background(), "compose", "up", "-d") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("docker returned %v, want nil", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("docker blocked on an overlong line instead of draining")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(lines) == 0 || !strings.HasPrefix(lines[len(lines)-1], "output truncated:") {
		t.Errorf("truncation was not reported: %q", lines)
	}
}

// The cancellation arm is the part of a retry loop that is easy to get wrong,
// and getting it wrong means a cancelled deploy keeps polling for the rest of
// its budget. It is now written once.
func TestRetryStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := retry(ctx, 100, 10*time.Millisecond, "the thing", func(context.Context) error {
		calls++
		if calls == 2 {
			cancel()
		}
		return errors.New("not yet")
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("retry after cancel = %v, want context.Canceled", err)
	}
	if calls != 2 {
		t.Errorf("fn called %d times after cancel, want 2", calls)
	}
}

func TestRetrySucceedsAndReportsTheLastFailure(t *testing.T) {
	calls := 0
	err := retry(context.Background(), 5, time.Millisecond, "the thing", func(context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("not yet")
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Errorf("retry = %v after %d calls, want nil after 3", err, calls)
	}

	err = retry(context.Background(), 3, time.Millisecond, "the thing", func(context.Context) error {
		return errors.New("still down")
	})
	if err == nil || !strings.Contains(err.Error(), "the thing did not become ready") ||
		!strings.Contains(err.Error(), "still down") {
		t.Errorf("exhausted retry = %v, want it to name the thing and the last failure", err)
	}
}

// An attempt that consumes the whole context leaves both arms of the wait
// ready, and the answer used to depend on which one select picked: the caller
// got a bare "context deadline exceeded" as often as the message naming what
// failed. There is nothing left to wait for after the last attempt.
func TestRetryReportsTheFailureWhenTheContextIsAlreadyDone(t *testing.T) {
	for i := 0; i < 50; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
		err := retry(ctx, 1, time.Millisecond, "the thing", func(ctx context.Context) error {
			<-ctx.Done()
			return errors.New("still down")
		})
		cancel()
		if err == nil || !strings.Contains(err.Error(), "the thing did not become ready") {
			t.Fatalf("retry = %v, want it to name the thing", err)
		}
	}
}
