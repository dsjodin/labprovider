package fetch

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// waitDone polls until the transfer finishes, so tests do not depend on timing.
func waitDone(t *testing.T, f *Fetcher) Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s := f.Status()
		if !s.Active && s.Stage != StageIdle {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("transfer did not finish")
	return Status{}
}

// rangeServer serves body, honouring a Range request the way a depot does.
func rangeServer(body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := 0
		if h := r.Header.Get("Range"); strings.HasPrefix(h, "bytes=") {
			n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(h, "bytes="), "-"))
			if err == nil && n < len(body) {
				start = n
			}
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)-start))
		if start > 0 {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(body)-1, len(body)))
			w.WriteHeader(http.StatusPartialContent)
		}
		_, _ = io.WriteString(w, body[start:])
	}))
}

func TestFetchWritesDestinationOnlyOnSuccess(t *testing.T) {
	const body = "a VCF bundle, allegedly"
	srv := rangeServer(body)
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "bundle.tar")
	f := &Fetcher{Logger: quiet()}
	if err := f.Start(Request{URL: srv.URL, Dest: dest}); err != nil {
		t.Fatal(err)
	}
	got := waitDone(t, f)
	if got.Stage != StageDone || got.Error != "" {
		t.Fatalf("status = %+v", got)
	}
	if b, err := os.ReadFile(dest); err != nil || string(b) != body {
		t.Errorf("dest content = %q, %v", b, err)
	}
	if _, err := os.Stat(dest + partSuffix); !os.IsNotExist(err) {
		t.Error("the .part file should be gone after a successful transfer")
	}
}

// The whole argument for a server-side fetch: an interrupted 40 GiB transfer
// must not start over.
func TestFetchResumesFromPartial(t *testing.T) {
	const body = "0123456789abcdefghij"
	srv := rangeServer(body)
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "bundle.tar")
	if err := os.WriteFile(dest+partSuffix, []byte(body[:8]), 0o644); err != nil {
		t.Fatal(err)
	}

	f := &Fetcher{Logger: quiet()}
	if err := f.Start(Request{URL: srv.URL, Dest: dest}); err != nil {
		t.Fatal(err)
	}
	got := waitDone(t, f)
	if got.Stage != StageDone {
		t.Fatalf("status = %+v", got)
	}
	if got.Resumed != 8 {
		t.Errorf("resumed = %d, want 8", got.Resumed)
	}
	if got.Received != int64(len(body)) {
		t.Errorf("received = %d, want %d", got.Received, len(body))
	}
	if b, _ := os.ReadFile(dest); string(b) != body {
		t.Errorf("dest content = %q, want the whole body", b)
	}
}

// A server that ignores Range answers 200 with the whole file; the bytes on
// disk are then not a prefix of the response, so the transfer starts over
// rather than concatenating two copies.
func TestFetchRestartsWhenServerIgnoresRange(t *testing.T) {
	const body = "whole file every time"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "bundle.tar")
	if err := os.WriteFile(dest+partSuffix, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &Fetcher{Logger: quiet()}
	if err := f.Start(Request{URL: srv.URL, Dest: dest}); err != nil {
		t.Fatal(err)
	}
	if got := waitDone(t, f); got.Stage != StageDone {
		t.Fatalf("status = %+v", got)
	}
	if b, _ := os.ReadFile(dest); string(b) != body {
		t.Errorf("dest content = %q, want exactly the body", b)
	}
}

func TestFetchVerifiesChecksum(t *testing.T) {
	const body = "bundle bytes"
	srv := rangeServer(body)
	defer srv.Close()
	sum := sha256.Sum256([]byte(body))

	t.Run("match", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "bundle.tar")
		f := &Fetcher{Logger: quiet()}
		if err := f.Start(Request{URL: srv.URL, Dest: dest, SHA256: hex.EncodeToString(sum[:])}); err != nil {
			t.Fatal(err)
		}
		if got := waitDone(t, f); got.Stage != StageDone {
			t.Fatalf("status = %+v", got)
		}
	})

	t.Run("mismatch keeps the part file", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "bundle.tar")
		f := &Fetcher{Logger: quiet()}
		wrong := strings.Repeat("ab", 32)
		if err := f.Start(Request{URL: srv.URL, Dest: dest, SHA256: wrong}); err != nil {
			t.Fatal(err)
		}
		got := waitDone(t, f)
		if got.Stage != StageFailed || !strings.Contains(got.Error, "sha256 mismatch") {
			t.Fatalf("status = %+v", got)
		}
		if _, err := os.Stat(dest); !os.IsNotExist(err) {
			t.Error("a bundle that failed its checksum must not appear under its final name")
		}
		if _, err := os.Stat(dest + partSuffix); err != nil {
			t.Error("the bad bytes should remain as .part for inspection")
		}
	})
}

func TestFetchRejectsBadRequests(t *testing.T) {
	f := &Fetcher{Logger: quiet()}
	for _, req := range []Request{
		{URL: "", Dest: "/tmp/x"},
		{URL: "https://example.invalid/x", Dest: ""},
		{URL: "https://example.invalid/x", Dest: "/tmp/x", SHA256: "nothex"},
	} {
		if err := f.Start(req); err == nil {
			t.Errorf("Start(%+v) = nil, want an error", req)
		}
	}
}

func TestFetchIsSingleFlight(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		_, _ = io.WriteString(w, "done")
	}))
	defer srv.Close()

	dir := t.TempDir()
	f := &Fetcher{Logger: quiet()}
	if err := f.Start(Request{URL: srv.URL, Dest: filepath.Join(dir, "one")}); err != nil {
		t.Fatal(err)
	}
	if err := f.Start(Request{URL: srv.URL, Dest: filepath.Join(dir, "two")}); err != ErrBusy {
		t.Errorf("second Start = %v, want ErrBusy", err)
	}
	close(release)
	waitDone(t, f)
}

func TestFetchCancelStopsTheTransfer(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-block
	}))
	defer srv.Close()
	defer close(block)

	dest := filepath.Join(t.TempDir(), "bundle.tar")
	f := &Fetcher{Logger: quiet()}
	if err := f.Start(Request{URL: srv.URL, Dest: dest}); err != nil {
		t.Fatal(err)
	}
	f.Cancel()
	got := waitDone(t, f)
	if got.Stage != StageFailed {
		t.Errorf("status = %+v, want a failed transfer", got)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("a cancelled transfer must not produce a final file")
	}
}
