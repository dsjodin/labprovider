package server

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dsjodin/labprovider/services/control-plane/internal/envfile"
)

// The irreplaceable state is small and known. Everything else on the host is
// either regenerable (runtime directories, rendered compose files) or service
// data the operator already knows they have (Postgres volumes, registry blobs,
// depot bundles) - those are large, and a dashboard button that streams them
// would be a worse tool than rsync.
//
// What is in here cannot be recreated from anything: lose the CA key material
// and every certificate the lab issued has to be reissued and re-trusted; lose
// the generated secrets and the Postgres data directories they were initialized
// with are unopenable.
//
// maxBackupBytes bounds the walk. The CA directory is kilobytes; anything near
// this limit means CA_DATA_DIR points somewhere unexpected, and truncating a
// backup silently is the one failure that would only be discovered during a
// restore.
const maxBackupBytes = 64 << 20

// backupTarget is one thing worth keeping, and why.
type backupTarget struct {
	// path is absolute on the host; name is where it lands in the archive.
	path, name, why string
}

// backupTargets resolves what to archive from the managed config. Missing
// entries are skipped rather than failing: a lab that has not deployed the CA
// yet still has a config and accounts worth backing up.
func (s *Server) backupTargets() ([]backupTarget, error) {
	content, saved, err := s.opt.Engine.Store.Load()
	if err != nil {
		return nil, err
	}
	if !saved {
		return nil, fmt.Errorf("no configuration saved yet")
	}
	env := envfile.Parse(content)
	cpDir := filepath.Dir(s.opt.Engine.Store.Path)

	targets := []backupTarget{
		{s.opt.Engine.Store.Path, "labprovider.env", "the configuration"},
		{filepath.Join(cpDir, "secrets"), "secrets", "generated secrets, baked into Postgres at initdb time"},
		{filepath.Join(cpDir, "dns.seed"), "dns.seed", "external DNS records"},
	}
	if s.opt.Auth != nil {
		targets = append(targets, backupTarget{s.opt.Auth.Path, "users.json", "operator accounts"})
	}
	if ca := env["CA_DATA_DIR"]; ca != "" {
		targets = append(targets, backupTarget{ca, "step-ca", "the CA key material"})
	}

	var found []backupTarget
	for _, t := range targets {
		if t.path == "" {
			continue
		}
		if _, err := os.Stat(t.path); err == nil {
			found = append(found, t)
		}
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("nothing to back up yet")
	}
	return found, nil
}

// handleBackup streams a gzipped tar of the irreplaceable state.
//
// Streamed rather than assembled in memory, so the response starts immediately
// - but that means a mid-stream error cannot become an error status, since the
// 200 is already sent. The size is bounded up front and the walk errors are
// checked before anything is written for exactly that reason; a failure after
// the first byte truncates the archive, so it is logged and the tar is left
// unterminated, which every extractor reports as a corrupt archive rather than
// silently accepting a short backup.
func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	targets, err := s.backupTargets()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	total, err := backupSize(targets)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if total > maxBackupBytes {
		writeErr(w, http.StatusUnprocessableEntity, fmt.Errorf(
			"the backup set is %d MiB, past the %d MiB limit; check that CA_DATA_DIR is not pointed at service data",
			total>>20, maxBackupBytes>>20))
		return
	}

	name := fmt.Sprintf("labprovider-backup-%s.tar.gz", s.opt.Now().UTC().Format("2006-01-02-1504"))
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")

	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	for _, t := range targets {
		if err := addToArchive(tw, t); err != nil {
			s.opt.Logger.Error("backup failed mid-stream; the archive is truncated",
				"path", t.path, "err", err)
			return
		}
	}
	if err := tw.Close(); err != nil {
		s.opt.Logger.Error("backup failed closing the tar", "err", err)
		return
	}
	if err := gz.Close(); err != nil {
		s.opt.Logger.Error("backup failed closing the gzip stream", "err", err)
	}
}

func backupSize(targets []backupTarget) (int64, error) {
	var total int64
	for _, t := range targets {
		err := filepath.WalkDir(t.path, func(_ string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			total += info.Size()
			return nil
		})
		if err != nil {
			return 0, err
		}
	}
	return total, nil
}

// addToArchive writes one file or directory tree under t.name. Symlinks are
// skipped: the CA directory has none, and following one out of the tree would
// put something unexpected in an archive the operator will later extract as
// root.
func addToArchive(tw *tar.Writer, t backupTarget) error {
	root := filepath.Clean(t.path)
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name := t.name
		if rel != "." {
			name = t.name + "/" + filepath.ToSlash(rel)
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = name
		// Times and ownership are the host's; the mode matters because these
		// files are 0600 for a reason and a restore should keep them that way.
		hdr.Uname, hdr.Gname = "", ""
		hdr.ModTime = hdr.ModTime.UTC().Truncate(time.Second)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
}

// backupContents describes what the button will hand over, so the operator can
// see what is and is not covered before relying on it.
func (s *Server) handleBackupContents(w http.ResponseWriter, r *http.Request) {
	targets, err := s.backupTargets()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	type entry struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Why  string `json:"why"`
	}
	out := make([]entry, 0, len(targets))
	var paths []string
	for _, t := range targets {
		out = append(out, entry{t.name, t.path, t.why})
		paths = append(paths, t.path)
	}
	size, _ := backupSize(targets)
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": out,
		"bytes":   size,
		// The equivalent command, for operators who would rather cron it than
		// click it. This is the whole backup story in one line.
		"tar": "tar czf labprovider-backup.tar.gz " + strings.Join(paths, " "),
	})
}
