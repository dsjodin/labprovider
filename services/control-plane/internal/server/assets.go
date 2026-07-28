package server

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

//go:embed static
var staticFS embed.FS

// assets maps a request path to embedded file contents. The served path
// carries a content hash (app.<hash>.css) so an upgraded control plane never
// serves a stale stylesheet from the browser cache, and the response can be
// marked immutable.
type assets struct {
	files map[string][]byte // served path -> contents
	urls  map[string]string // logical name -> served path
}

func newAssets() (*assets, error) {
	a := &assets{files: map[string][]byte{}, urls: map[string]string{}}
	entries, err := fs.ReadDir(staticFS, "static")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		body, err := staticFS.ReadFile("static/" + e.Name())
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(body)
		ext := path.Ext(e.Name())
		served := "/static/" + strings.TrimSuffix(e.Name(), ext) + "." + hex.EncodeToString(sum[:])[:12] + ext
		a.files[served] = body
		a.urls[e.Name()] = served
	}
	return a, nil
}

// URL returns the hashed path for an embedded asset, or "" if it does not
// exist. Templates call it through the "asset" func.
func (a *assets) URL(name string) string { return a.urls[name] }

func (a *assets) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, ok := a.files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if ct := mime.TypeByExtension(path.Ext(r.URL.Path)); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		_, _ = w.Write(body)
	}
}
