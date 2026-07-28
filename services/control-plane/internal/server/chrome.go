package server

import (
	"bytes"
	"embed"
	"html/template"
	"net/http"
)

//go:embed templates
var templatesFS embed.FS

// Chrome is what templates/layout.html needs on every page. Pages with their
// own payload embed it; pages that are pure markup use it as the dot directly.
// Every field is json:"-" because Page embeds this and /api/state is a
// scripted surface that must not grow presentation fields.
type Chrome struct {
	Title     string `json:"-"`
	Nav       string `json:"-"` // sidebar key of the active page
	Narrow    bool   `json:"-"` // constrain the content column to a readable width
	HasEngine bool   `json:"-"` // no engine (--dashboard mode) means no config/deploy/csr links
	HasAuth   bool   `json:"-"` // no user store means no account/sign-out links
	HasDocker bool   `json:"-"` // no Docker means no log viewer
	Version   string `json:"-"` // the running build, shown in the sidebar
	NeedToken bool   `json:"-"` // /setup must ask for the one-time token
}

// version is the running build, or "dev" when the binary was not stamped.
func (s *Server) version() string {
	if s.opt.Version == "" {
		return "dev"
	}
	return s.opt.Version
}

// chrome fills in the parts of the layout that depend on how the server was
// configured rather than on which page is rendering.
func (s *Server) chrome(title, nav string) Chrome {
	return Chrome{
		Title:     title,
		Nav:       nav,
		HasEngine: s.opt.Engine != nil,
		HasAuth:   s.opt.Auth != nil,
		HasDocker: s.opt.Docker != nil,
		Version:   s.version(),
		NeedToken: s.opt.SetupToken.Value() != "",
	}
}

type navItem struct {
	Href   string
	Label  string
	Icon   string
	Active bool
}

func navitem(active, key, href, label, icon string) navItem {
	return navItem{Href: href, Label: label, Icon: icon, Active: active == key}
}

// parsePage parses one page template together with the shared layout. Each
// page is its own template set, so a page may override "topbar" or "scripts"
// without affecting the others.
func (a *assets) parsePage(name string) (*template.Template, error) {
	return template.New(name).Funcs(a.funcs()).ParseFS(templatesFS,
		"templates/layout.html", "templates/partials.html", "templates/"+name)
}

func (a *assets) funcs() template.FuncMap {
	f := template.FuncMap{
		"asset":   a.URL,
		"navitem": navitem,
	}
	for k, v := range tmplFuncs {
		f[k] = v
	}
	return f
}

// render executes a page's layout into a buffer first. ExecuteTemplate writes
// as it goes, so any error after the first byte - a nil dereference in a
// template action, a panel method returning an error - used to leave the
// browser with half a page, a 200, and nothing visible to say it failed.
// Buffering costs one page of memory and makes the failure a failure.
//
// no-store because every page carries the Access panel, and the Access panel is
// every lab password in cleartext (masked with a CSS class; the value is in the
// DOM). Without it these are heuristically cacheable and land in the browser's
// on-disk cache.
func (s *Server) render(w http.ResponseWriter, t *template.Template, layout string, data any) {
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, layout, data); err != nil {
		s.opt.Logger.Error("render page", "template", t.Name(), "err", err)
		http.Error(w, "page render failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// A full CSP is real work here: every page uses inline <script> and inline
	// onclick handlers, so nonces would have to thread through render and every
	// template. These three are one line each and cover most of the value -
	// nosniff stops content-type confusion, no-referrer keeps lab FQDNs out of
	// Referer, and frame-ancestors is the clickjacking half of a CSP.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	_, _ = buf.WriteTo(w)
}
