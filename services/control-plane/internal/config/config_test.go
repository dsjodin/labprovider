package config

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// captureLogs points the default logger at a buffer for the duration of a test,
// so the warnings these fallbacks now emit can be asserted on.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestReadTokenPrefersTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("  from-the-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_TOKEN_FILE", path)
	t.Setenv("TEST_TOKEN", "from-the-env")

	if got := readToken("TEST_TOKEN_FILE", "TEST_TOKEN"); got != "from-the-file" {
		t.Errorf("readToken = %q, want the file's contents trimmed", got)
	}
}

// An unreadable token file used to render as "not configured", which is the
// message for "you never set this up" - not for "labprovider cannot read the
// file you pointed it at".
func TestReadTokenSaysWhyItFellBack(t *testing.T) {
	logs := captureLogs(t)
	t.Setenv("TEST_TOKEN_FILE", filepath.Join(t.TempDir(), "absent"))
	t.Setenv("TEST_TOKEN", "from-the-env")

	if got := readToken("TEST_TOKEN_FILE", "TEST_TOKEN"); got != "from-the-env" {
		t.Errorf("readToken = %q, want the env fallback", got)
	}
	if !strings.Contains(logs.String(), "TEST_TOKEN_FILE") {
		t.Errorf("the unreadable file was not reported: %s", logs)
	}
	// The value itself must never reach the log.
	if strings.Contains(logs.String(), "from-the-env") {
		t.Error("the token value was logged")
	}
}

func TestEnvIntAndDurationReportMalformedValues(t *testing.T) {
	logs := captureLogs(t)

	t.Setenv("TEST_INT", "200s")
	if got := envInt("TEST_INT", 200); got != 200 {
		t.Errorf("envInt = %d, want the default 200", got)
	}
	t.Setenv("TEST_DURATION", "12")
	if got := envDuration("TEST_DURATION", time.Hour); got != time.Hour {
		t.Errorf("envDuration = %v, want the default 1h", got)
	}
	for _, want := range []string{"TEST_INT", "TEST_DURATION"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("%s was replaced by its default with no warning: %s", want, logs)
		}
	}

	// A well-formed value is used and says nothing.
	quiet := captureLogs(t)
	t.Setenv("TEST_INT", "50")
	t.Setenv("TEST_DURATION", "90s")
	if got := envInt("TEST_INT", 200); got != 50 {
		t.Errorf("envInt = %d, want 50", got)
	}
	if got := envDuration("TEST_DURATION", time.Hour); got != 90*time.Second {
		t.Errorf("envDuration = %v, want 90s", got)
	}
	if quiet.Len() != 0 {
		t.Errorf("a valid value logged something: %s", quiet)
	}
}
