package deploy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The host resolv.conf is normally a symlink into /run/systemd/resolve/. If the
// rewrite followed it, the deploy would overwrite systemd-resolved's own file
// and the removal path could never restore it.
func TestWriteResolvConfReplacesTheSymlinkInsteadOfFollowingIt(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "stub-resolv.conf")
	const stub = "nameserver 127.0.0.53\n"
	if err := os.WriteFile(target, []byte(stub), 0o644); err != nil {
		t.Fatal(err)
	}
	resolv := filepath.Join(dir, "resolv.conf")
	if err := os.Symlink(target, resolv); err != nil {
		t.Fatal(err)
	}

	if err := writeTechnitiumResolvConf(dir, "lab.example.com"); err != nil {
		t.Fatalf("writeTechnitiumResolvConf: %v", err)
	}

	fi, err := os.Lstat(resolv)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Error("resolv.conf is still a symlink")
	}
	got, err := os.ReadFile(resolv)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{technitiumResolvMarker, "nameserver 127.0.0.1", "search lab.example.com"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("resolv.conf missing %q:\n%s", want, got)
		}
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != stub {
		t.Errorf("the symlink target was rewritten: %q (err %v)", b, err)
	}
}

// Removal restores systemd-resolved only for a file labprovider wrote. An
// operator's hand-written resolv.conf carries no marker and must survive.
func TestRestoreHostResolverLeavesAForeignFileAlone(t *testing.T) {
	dir := t.TempDir()
	hostEtc = func() string { return dir }
	t.Cleanup(func() { hostEtc = hostEtcDefault })

	resolv := filepath.Join(dir, "resolv.conf")
	const custom = "nameserver 10.0.0.1\nsearch corp.example\n"
	if err := os.WriteFile(resolv, []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}

	restoreHostResolver(&RunCtx{Env: map[string]string{}, Log: func(string, ...any) {}})

	got, err := os.ReadFile(resolv)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != custom {
		t.Errorf("resolv.conf = %q, want it untouched (%q)", got, custom)
	}
}

func TestRestoreHostResolverRelinksWhatItWrote(t *testing.T) {
	dir := t.TempDir()
	hostEtc = func() string { return dir }
	t.Cleanup(func() { hostEtc = hostEtcDefault })
	stubSystemdResolved(t, dir)

	if err := writeTechnitiumResolvConf(dir, "lab.example.com"); err != nil {
		t.Fatal(err)
	}

	restoreHostResolver(&RunCtx{Env: map[string]string{}, Log: func(string, ...any) {}})

	link, err := os.Readlink(filepath.Join(dir, "resolv.conf"))
	if err != nil {
		t.Fatalf("resolv.conf is not a symlink after restore: %v", err)
	}
	if link != systemdResolvConf {
		t.Errorf("symlink target = %q", link)
	}
}

// The whole point of the backup: a host that does not run systemd-resolved gets
// back the file it had, not a guess about what it had.
func TestRestoreHostResolverPutsBackTheOriginal(t *testing.T) {
	dir := t.TempDir()
	hostEtc = func() string { return dir }
	t.Cleanup(func() { hostEtc = hostEtcDefault })

	resolv := filepath.Join(dir, "resolv.conf")
	const original = "nameserver 10.0.0.1\nsearch corp.example\n"
	if err := os.WriteFile(resolv, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := writeTechnitiumResolvConf(dir, "lab.example.com"); err != nil {
		t.Fatal(err)
	}
	// A redeploy must not overwrite the backup with labprovider's own rewrite.
	if err := writeTechnitiumResolvConf(dir, "lab.example.com"); err != nil {
		t.Fatal(err)
	}

	restoreHostResolver(&RunCtx{Env: map[string]string{}, Log: func(string, ...any) {}})

	got, err := os.ReadFile(resolv)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("resolv.conf = %q, want the original %q", got, original)
	}
	if _, err := os.Stat(filepath.Join(dir, resolvBackup)); !os.IsNotExist(err) {
		t.Error("the backup should be consumed by the restore")
	}
}

// Without a backup and without systemd-resolved, the old code replaced a
// working resolv.conf with a symlink to a path that does not exist - and logged
// that it had restored the resolver. Leaving it alone and saying so is the only
// safe move.
func TestRestoreHostResolverLeavesTheFileWhenThereIsNothingToRestoreTo(t *testing.T) {
	dir := t.TempDir()
	hostEtc = func() string { return dir }
	t.Cleanup(func() { hostEtc = hostEtcDefault })
	systemdResolvConf = filepath.Join(dir, "absent", "resolv.conf")
	t.Cleanup(func() { systemdResolvConf = "/run/systemd/resolve/resolv.conf" })

	if err := writeTechnitiumResolvConf(dir, "lab.example.com"); err != nil {
		t.Fatal(err)
	}
	var logged []string
	restoreHostResolver(&RunCtx{Env: map[string]string{}, Log: func(f string, a ...any) {
		logged = append(logged, fmt.Sprintf(f, a...))
	}})

	resolv := filepath.Join(dir, "resolv.conf")
	fi, err := os.Lstat(resolv)
	if err != nil {
		t.Fatalf("resolv.conf is gone: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("resolv.conf was replaced with a symlink to a file that does not exist")
	}
	if !strings.Contains(strings.Join(logged, "\n"), "does not run systemd-resolved") {
		t.Errorf("the operator was not told to fix it manually: %q", logged)
	}
}

// stubSystemdResolved points systemdResolvConf at a real file so a test can
// exercise the fallback on a machine that may not run systemd-resolved.
func stubSystemdResolved(t *testing.T, dir string) {
	t.Helper()
	stub := filepath.Join(dir, "systemd-resolv.conf")
	if err := os.WriteFile(stub, []byte("nameserver 127.0.0.53\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	systemdResolvConf = stub
	t.Cleanup(func() { systemdResolvConf = "/run/systemd/resolve/resolv.conf" })
}
