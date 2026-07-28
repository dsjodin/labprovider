package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsjodin/labprovider/services/dns-sync/internal/model"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func mustParse(t *testing.T, s string) []model.Record {
	t.Helper()
	recs, err := parseBuiltinRecords(s)
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

func TestParseBuiltinRecordsSeparators(t *testing.T) {
	recs, err := parseBuiltinRecords("a.lab=10.0.0.1,b.lab=10.0.0.2\nc.lab=10.0.0.3")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 {
		t.Fatalf("got %d records, want 3", len(recs))
	}
}

func TestCurrentBuiltinsLiveFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "builtin-records")
	writeFile := func(t *testing.T, s string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	s := &sourceWithBuiltins{
		static: mustParse(t, "seed.lab=10.0.0.9"),
		file:   file,
		logger: discardLogger(),
	}

	// Missing file: fall back to the static set.
	if got := s.currentBuiltins(); len(got) != 1 || got[0].Name != "seed.lab." {
		t.Fatalf("missing file: got %v, want static seed.lab", got)
	}

	// Live file: picked up on the next read.
	writeFile(t, "mail.lab=10.0.0.1\nlldap.lab=10.0.0.2\n")
	if got := s.currentBuiltins(); len(got) != 2 {
		t.Fatalf("live file: got %d records, want 2", len(got))
	}

	// A service removed from the file drops out of the set.
	writeFile(t, "mail.lab=10.0.0.1\n")
	if got := s.currentBuiltins(); len(got) != 1 || got[0].Name != "mail.lab." {
		t.Fatalf("after removal: got %v, want only mail.lab", got)
	}

	// Parse error keeps the last good set instead of returning empty, so the
	// reconciler never full-syncs away every service record on a transient fault.
	writeFile(t, "not a valid record")
	if got := s.currentBuiltins(); len(got) != 1 || got[0].Name != "mail.lab." {
		t.Fatalf("parse error: got %v, want last-good mail.lab", got)
	}

	// Empty file also keeps the last good set.
	writeFile(t, "")
	if got := s.currentBuiltins(); len(got) != 1 || got[0].Name != "mail.lab." {
		t.Fatalf("empty file: got %v, want last-good mail.lab", got)
	}
}
