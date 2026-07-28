// Package disk reports host filesystem capacity and per-service data
// directory sizes for the dashboard. In a lab the depot and SeaweedFS are what
// fill a disk, and today the first symptom is a deploy failing on ENOSPC.
package disk

import (
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// Target is one directory to measure, labelled with the service that owns it.
type Target struct {
	Service string
	Path    string
}

// Filesystem is the capacity of the filesystem holding a path.
type Filesystem struct {
	Path        string `json:"path"`
	TotalBytes  uint64 `json:"total_bytes"`
	FreeBytes   uint64 `json:"free_bytes"`
	UsedBytes   uint64 `json:"used_bytes"`
	UsedPercent int    `json:"used_percent"`
}

// Dir is one measured service directory.
type Dir struct {
	Service string `json:"service"`
	Path    string `json:"path"`
	Bytes   uint64 `json:"bytes"`
}

// Overview is the panel's payload. Measuring is true while the first (or a
// refreshed) directory walk is still running, so the page can say the sizes
// are on their way instead of showing zeroes as fact.
type Overview struct {
	Filesystem Filesystem `json:"filesystem"`
	Dirs       []Dir      `json:"dirs"`
	MeasuredAt string     `json:"measured_at,omitempty"`
	Measuring  bool       `json:"measuring"`
}

// walkTTL is how long a directory measurement is served before it is refreshed.
// Sizes on a lab host move in minutes, not seconds, and a depot holding VCF
// bundles is hundreds of thousands of inodes - too slow to walk on a page load.
const walkTTL = 5 * time.Minute

// Reporter answers the disk panel. Capacity comes from statfs on every call
// (microseconds); directory sizes come from a cached walk refreshed in the
// background, so a page load never waits on the filesystem.
type Reporter struct {
	mu        sync.Mutex
	dirs      []Dir
	measured  time.Time
	measuring bool
}

// Fetch returns capacity for root plus the last completed directory sizes,
// kicking off a refresh when they are stale. It never blocks on the walk, so it
// takes no context: there is nothing here for a caller to cancel.
func (r *Reporter) Fetch(root string, targets []Target) (Overview, error) {
	ov := Overview{}
	fsUsage, err := statfs(root)
	if err != nil {
		return ov, err
	}
	ov.Filesystem = fsUsage

	r.mu.Lock()
	ov.Dirs = r.dirs
	if !r.measured.IsZero() {
		ov.MeasuredAt = r.measured.UTC().Format(time.RFC3339)
	}
	stale := time.Since(r.measured) > walkTTL
	if stale && !r.measuring {
		r.measuring = true
		go r.refresh(targets)
	}
	ov.Measuring = r.measuring
	r.mu.Unlock()
	return ov, nil
}

// refresh walks every target and replaces the cache. It runs detached from the
// request that triggered it: a walk outliving one page load is the point.
func (r *Reporter) refresh(targets []Target) {
	dirs := make([]Dir, 0, len(targets))
	for _, t := range targets {
		if t.Path == "" {
			continue
		}
		size, err := dirSize(t.Path)
		if err != nil {
			continue // not deployed yet, or not readable; leave it off the list
		}
		dirs = append(dirs, Dir{Service: t.Service, Path: t.Path, Bytes: size})
	}
	r.mu.Lock()
	r.dirs = dirs
	r.measured = time.Now()
	r.measuring = false
	r.mu.Unlock()
}

// dirSize sums the apparent size of every regular file under path. Unreadable
// entries are skipped rather than failing the whole measurement.
func dirSize(path string) (uint64, error) {
	if _, err := os.Stat(path); err != nil {
		return 0, err
	}
	var total uint64
	err := filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		total += uint64(info.Size())
		return nil
	})
	return total, err
}

// statfs reports the capacity of the filesystem holding path.
// Capacity is statfs on the filesystem holding path, for callers outside the
// panel - the depot fetcher checks free space before starting a download that
// would otherwise fail forty minutes in.
func Capacity(path string) (Filesystem, error) { return statfs(path) }

func statfs(path string) (Filesystem, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return Filesystem{}, err
	}
	bsize := uint64(st.Bsize)
	total := st.Blocks * bsize
	// Bavail, not Bfree: the reserved-for-root blocks are not space labprovider
	// can use, so counting them would overstate what is left.
	free := st.Bavail * bsize
	used := total - free
	f := Filesystem{Path: path, TotalBytes: total, FreeBytes: free, UsedBytes: used}
	if total > 0 {
		f.UsedPercent = int(used * 100 / total)
	}
	return f, nil
}
