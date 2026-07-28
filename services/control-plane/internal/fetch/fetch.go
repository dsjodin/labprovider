// Package fetch downloads a file into the depot from a URL the operator
// supplies. VCF bundles are tens of gigabytes, so this is a server-side
// transfer rather than a browser upload: the browser never holds the bytes, a
// closed tab does not abort it, and a dropped connection resumes instead of
// starting over.
//
// One transfer at a time, deliberately: the deploy engine is single-flight and
// a download must not be pushed through it - an hour-long fetch would block
// every deploy for an hour - but two concurrent multi-gigabyte writes to the
// same disk help nobody either.
package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dsjodin/labprovider/services/control-plane/internal/disk"
)

// Stages a transfer moves through, reported to the UI.
const (
	StageIdle        = "idle"
	StageDownloading = "downloading"
	StageVerifying   = "verifying"
	StageDone        = "done"
	StageFailed      = "failed"
)

// partSuffix marks an incomplete transfer. A half-written bundle that SDDC
// Manager can see is worse than no bundle, so the final name appears only after
// the transfer (and its checksum, when given) is good.
const partSuffix = ".part"

// freeSpaceMargin is left over after a transfer that declares its size. A depot
// filled to the last byte breaks the next deploy instead of this download.
const freeSpaceMargin = 1 << 30 // 1 GiB

var ErrBusy = fmt.Errorf("a depot transfer is already running")

// Request is one transfer. Dest must be an absolute path the caller has already
// confined to the depot; this package does not know where the depot is.
type Request struct {
	URL      string
	Dest     string
	Username string
	Password string
	SHA256   string
}

// Status is the whole progress model: one transfer, a byte count, and how it
// ended. There is no log to stream, so the page polls this instead of holding
// an event stream open.
type Status struct {
	Active   bool   `json:"active"`
	Stage    string `json:"stage"`
	URL      string `json:"url,omitempty"`
	Dest     string `json:"dest,omitempty"`
	Received int64  `json:"received"`
	Total    int64  `json:"total"` // 0 when the server sends no Content-Length
	Resumed  int64  `json:"resumed"`
	Started  string `json:"started,omitempty"`
	Finished string `json:"finished,omitempty"`
	Error    string `json:"error,omitempty"`
}

type job struct {
	req      Request
	total    atomic.Int64
	received atomic.Int64
	resumed  atomic.Int64
	stage    atomic.Value // string
	started  time.Time
	cancel   context.CancelFunc
}

// Fetcher runs at most one transfer. The zero value is usable.
type Fetcher struct {
	Client *http.Client
	Now    func() time.Time
	Logger *slog.Logger

	mu   sync.Mutex
	cur  *job
	last Status
}

func (f *Fetcher) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

func (f *Fetcher) logger() *slog.Logger {
	if f.Logger != nil {
		return f.Logger
	}
	return slog.Default()
}

// client uses the system trust pool, not the labprovider root: the source of a
// VCF bundle is on the internet, not in the lab.
func (f *Fetcher) client() *http.Client {
	if f.Client != nil {
		return f.Client
	}
	return &http.Client{
		// No overall timeout: this is a multi-hour transfer by design. The
		// bounded parts are connect and response-header time.
		Transport: &http.Transport{
			ResponseHeaderTimeout: 60 * time.Second,
			TLSHandshakeTimeout:   30 * time.Second,
			Proxy:                 http.ProxyFromEnvironment,
		},
	}
}

// Start begins a transfer, or returns ErrBusy when one is already running.
func (f *Fetcher) Start(req Request) error {
	if req.URL == "" || req.Dest == "" {
		return fmt.Errorf("url and destination are both required")
	}
	if req.SHA256 != "" {
		if _, err := hex.DecodeString(req.SHA256); err != nil || len(req.SHA256) != 64 {
			return fmt.Errorf("sha256 must be 64 hex characters")
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cur != nil {
		return ErrBusy
	}
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{req: req, started: f.now(), cancel: cancel}
	j.stage.Store(StageDownloading)
	f.cur = j
	go f.run(ctx, j)
	return nil
}

// Cancel stops the running transfer. The partial file is kept, so the next
// attempt resumes rather than starting over.
func (f *Fetcher) Cancel() {
	f.mu.Lock()
	j := f.cur
	f.mu.Unlock()
	if j != nil {
		j.cancel()
	}
}

// Status reports the running transfer, or the last one to finish.
func (f *Fetcher) Status() Status {
	f.mu.Lock()
	j, last := f.cur, f.last
	f.mu.Unlock()
	if j == nil {
		if last.Stage == "" {
			last.Stage = StageIdle
		}
		return last
	}
	stage, _ := j.stage.Load().(string)
	return Status{
		Active:   true,
		Stage:    stage,
		URL:      j.req.URL,
		Dest:     j.req.Dest,
		Received: j.received.Load(),
		Total:    j.total.Load(),
		Resumed:  j.resumed.Load(),
		Started:  j.started.UTC().Format(time.RFC3339),
	}
}

func (f *Fetcher) run(ctx context.Context, j *job) {
	err := f.transfer(ctx, j)

	status := Status{
		Stage:    StageDone,
		URL:      j.req.URL,
		Dest:     j.req.Dest,
		Received: j.received.Load(),
		Total:    j.total.Load(),
		Resumed:  j.resumed.Load(),
		Started:  j.started.UTC().Format(time.RFC3339),
		Finished: f.now().UTC().Format(time.RFC3339),
	}
	if err != nil {
		status.Stage = StageFailed
		status.Error = err.Error()
		f.logger().Error("depot fetch failed", "url", j.req.URL, "dest", j.req.Dest, "err", err)
	} else {
		f.logger().Info("depot fetch complete", "dest", j.req.Dest, "bytes", status.Received)
	}

	f.mu.Lock()
	f.cur = nil
	f.last = status
	f.mu.Unlock()
}

func (f *Fetcher) transfer(ctx context.Context, j *job) error {
	part := j.req.Dest + partSuffix
	var offset int64
	if info, err := os.Stat(part); err == nil && info.Mode().IsRegular() {
		offset = info.Size()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, j.req.URL, nil)
	if err != nil {
		return err
	}
	if j.req.Username != "" || j.req.Password != "" {
		req.SetBasicAuth(j.req.Username, j.req.Password)
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}

	resp, err := f.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// The server ignored the Range header (or there was none): whatever is
		// on disk is not a prefix of this response, so start over.
		offset = 0
	case http.StatusPartialContent:
	default:
		return fmt.Errorf("%s responded %s", j.req.URL, resp.Status)
	}

	j.resumed.Store(offset)
	j.received.Store(offset)
	if resp.ContentLength > 0 {
		j.total.Store(offset + resp.ContentLength)
	}

	if total := j.total.Load(); total > 0 {
		if err := checkFree(filepath.Dir(part), total-offset); err != nil {
			return err
		}
	}

	flags := os.O_CREATE | os.O_WRONLY
	if offset > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	out, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, &counter{r: resp.Body, n: &j.received}); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}

	if j.req.SHA256 != "" {
		j.stage.Store(StageVerifying)
		sum, err := sha256File(ctx, part)
		if err != nil {
			return err
		}
		if !strings.EqualFold(sum, j.req.SHA256) {
			// Left as .part on purpose: the bad bytes are visible for
			// inspection and are not mistaken for a usable bundle.
			return fmt.Errorf("sha256 mismatch: got %s, expected %s", sum, j.req.SHA256)
		}
	}
	return os.Rename(part, j.req.Dest)
}

// checkFree fails a transfer that cannot fit before it writes anything, rather
// than in its fiftieth minute.
func checkFree(dir string, need int64) error {
	fs, err := disk.Capacity(dir)
	if err != nil {
		return nil // capacity is advisory; never fail a transfer over statfs
	}
	if fs.FreeBytes < uint64(need)+freeSpaceMargin {
		return fmt.Errorf("not enough free space on %s: %d bytes free, %d needed plus a 1 GiB margin",
			fs.Path, fs.FreeBytes, need)
	}
	return nil
}

func sha256File(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, &ctxReader{ctx: ctx, r: f}); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// counter reports progress while io.Copy runs.
type counter struct {
	r io.Reader
	n *atomic.Int64
}

func (c *counter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// ctxReader makes the checksum pass cancellable; a 40 GiB re-read is long
// enough that an operator who cancels expects it to stop.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
