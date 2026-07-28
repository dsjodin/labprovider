package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/dsjodin/labprovider/services/control-plane/internal/envfile"
	"github.com/dsjodin/labprovider/services/control-plane/internal/fetch"
)

type depotFetchRequest struct {
	URL      string `json:"url"`
	Dest     string `json:"dest"`
	Username string `json:"username"`
	Password string `json:"password"`
	SHA256   string `json:"sha256"`
}

// depotDataDir reads DEPOT_DATA_DIR from the managed config. The fetcher writes
// only under it, so an unconfigured depot has nowhere to write and says so.
func (s *Server) depotDataDir() (string, error) {
	content, saved, err := s.opt.Engine.Store.Load()
	if err != nil {
		return "", err
	}
	if !saved {
		return "", fmt.Errorf("no configuration saved yet")
	}
	dir := envfile.Parse(content)["DEPOT_DATA_DIR"]
	if dir == "" {
		return "", fmt.Errorf("DEPOT_DATA_DIR is not configured")
	}
	return dir, nil
}

// depotDest resolves an operator-supplied relative path to an absolute one
// under dataDir, and is the whole security surface of the fetch endpoint: it is
// the only place a request decides where the control plane writes.
//
// The separator in the prefix comparison is not decoration - it is the bug 5.1
// found in dnssync.go, where a sibling directory shared a prefix with the one
// being checked.
func depotDest(dataDir, dest string) (string, error) {
	dest = strings.TrimSpace(dest)
	if dest == "" {
		return "", fmt.Errorf("destination is required")
	}
	if filepath.IsAbs(dest) || strings.HasPrefix(dest, "~") {
		return "", fmt.Errorf("destination must be relative to the depot data directory")
	}
	clean := filepath.Clean(dest)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("destination must stay inside the depot data directory")
	}
	full := filepath.Join(dataDir, clean)

	// Resolve the parent through any symlinks before trusting the prefix: a
	// symlinked subdirectory would otherwise pass a textual check and write
	// somewhere else entirely. The parent is resolved rather than the file,
	// which does not exist yet.
	parent := filepath.Dir(full)
	resolvedParent := parent
	if r, err := filepath.EvalSymlinks(parent); err == nil {
		resolvedParent = r
	} else if !os.IsNotExist(err) {
		return "", err
	}
	root := dataDir
	if r, err := filepath.EvalSymlinks(dataDir); err == nil {
		root = r
	}
	if resolvedParent != root && !strings.HasPrefix(resolvedParent, root+string(filepath.Separator)) {
		return "", fmt.Errorf("destination must stay inside the depot data directory")
	}
	return filepath.Join(resolvedParent, filepath.Base(full)), nil
}

func (s *Server) handleDepotFetch(w http.ResponseWriter, r *http.Request) {
	var req depotFetchRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	parsed, err := url.Parse(strings.TrimSpace(req.URL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("url must be an absolute http or https URL"))
		return
	}
	dataDir, err := s.depotDataDir()
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	dest, err := depotDest(dataDir, req.Dest)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	// Inline credentials move into the fields they belong in. Pasting
	// https://user:pass@depot.example.com/bundle.zip is the normal way to reach
	// a password-protected Broadcom mirror, and url.Parse keeps that userinfo in
	// URL.User where String() reproduces it verbatim - so it would otherwise be
	// logged on failure and polled back by the depot page every few seconds,
	// which is exactly what the contract below promises does not happen.
	if u := parsed.User; u != nil {
		if req.Username == "" {
			req.Username = u.Username()
		}
		if pw, ok := u.Password(); ok && req.Password == "" {
			req.Password = pw
		}
		parsed.User = nil
	}

	// Credentials are used for this transfer and dropped: never persisted to
	// the managed config, never logged, never echoed back by the status
	// endpoint.
	if err := s.fetcher.Start(fetch.Request{
		URL:      parsed.String(),
		Dest:     dest,
		Username: req.Username,
		Password: req.Password,
		SHA256:   strings.TrimSpace(req.SHA256),
	}); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, fetch.ErrBusy) {
			status = http.StatusConflict
		}
		writeErr(w, status, err)
		return
	}
	writeJSON(w, http.StatusAccepted, s.fetcher.Status())
}

func (s *Server) handleDepotFetchStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, s.fetcher.Status())
}

func (s *Server) handleDepotFetchCancel(w http.ResponseWriter, r *http.Request) {
	s.fetcher.Cancel()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "cancelling"})
}
