package disk

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFetchReportsCapacityImmediatelyAndSizesAfterTheWalk(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "depot")
	if err := os.MkdirAll(filepath.Join(data, "PROD"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "PROD", "bundle"), make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	targets := []Target{
		{Service: "depot", Path: data},
		{Service: "netbox", Path: filepath.Join(root, "never-deployed")},
	}

	var r Reporter
	ov, err := r.Fetch(root, targets)
	if err != nil {
		t.Fatal(err)
	}
	// Capacity is available on the first call; sizes are still being measured.
	if ov.Filesystem.TotalBytes == 0 {
		t.Error("TotalBytes = 0, want the filesystem capacity")
	}
	if !ov.Measuring || len(ov.Dirs) != 0 {
		t.Errorf("first Fetch = %+v, want an in-flight measurement and no sizes", ov)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ov, err = r.Fetch(root, targets)
		if err != nil {
			t.Fatal(err)
		}
		if !ov.Measuring {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if ov.Measuring {
		t.Fatal("measurement never completed")
	}
	// A directory that does not exist is left out rather than reported as zero.
	if len(ov.Dirs) != 1 || ov.Dirs[0].Service != "depot" {
		t.Fatalf("Dirs = %+v, want just depot", ov.Dirs)
	}
	if ov.Dirs[0].Bytes != 4096 {
		t.Errorf("depot size = %d, want 4096", ov.Dirs[0].Bytes)
	}
	if ov.MeasuredAt == "" {
		t.Error("MeasuredAt is empty after a completed walk")
	}
}

func TestFetchFailsOnAMissingRoot(t *testing.T) {
	var r Reporter
	if _, err := r.Fetch(filepath.Join(t.TempDir(), "gone"), nil); err == nil {
		t.Error("Fetch on a missing root succeeded, want an error")
	}
}
