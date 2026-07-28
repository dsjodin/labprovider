package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/dsjodin/labprovider/services/control-plane/internal/auth"
)

// publicPaths bypass the session check. Everything else - the dashboard, the
// wizard, the deploy engine, CSR signing - requires a login, because the
// control plane runs as root with the Docker socket mounted and its config
// endpoint returns every password in the lab in cleartext.
var publicPaths = map[string]bool{
	"/healthz":    true,
	"/login":      true,
	"/setup":      true,
	"/api/login":  true,
	"/api/logout": true,
	"/api/setup":  true,
}

func (s *Server) registerAuth(mux *http.ServeMux) {
	mux.HandleFunc("GET /login", s.handleLoginPage)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /setup", s.handleLoginPage)
	mux.HandleFunc("POST /api/setup", s.handleSetup)
	mux.HandleFunc("GET /account", func(w http.ResponseWriter, r *http.Request) {
		c := s.chrome("Account", "account")
		c.Narrow = true
		s.render(w, s.pages["account.html"], "layout", c)
	})
	mux.HandleFunc("GET /api/account", s.handleAccountGet)
	mux.HandleFunc("POST /api/account/password", s.handlePasswordChange)
}

// requireAuth gates every non-public path on a valid session. With no operator
// configured yet it sends browsers to /setup instead, so a fresh install is
// reachable exactly once, to create the first account.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /static/ is the embedded stylesheet and nothing else; the login
		// page needs it before there is a session.
		if publicPaths[r.URL.Path] || strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		if err := checkSameSite(r); err != nil {
			writeErr(w, http.StatusForbidden, err)
			return
		}
		empty, err := s.opt.Auth.Empty()
		if err != nil {
			s.opt.Logger.Error("read user store", "err", err)
			writeErr(w, http.StatusInternalServerError, fmt.Errorf("user store unreadable"))
			return
		}
		if empty {
			s.denyAuth(w, r, "/setup", "no operator account exists yet; open /setup")
			return
		}
		cookie, _ := r.Cookie(auth.CookieName)
		if cookie == nil {
			s.denyAuth(w, r, "/login", "authentication required")
			return
		}
		if _, ok := s.opt.Sessions.User(cookie.Value); !ok {
			s.denyAuth(w, r, "/login", "session expired")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// checkSameSite rejects a cross-site request that a browser labelled as such.
//
// Defense in depth on CSRF, not the primary defense: the session cookie is
// SameSite=Lax, which already keeps cross-site POSTs from carrying it, and the
// JSON endpoints are additionally protected by CORS on anything but a simple
// content type. The gap Lax alone leaves is PUT /api/config and POST
// /api/config/validate, which the wizard sends as raw text bodies - a
// CORS-simple content type. If Lax ever fails (an old browser, a same-site
// subdomain the operator also runs), those two are the exposed pair.
//
// An absent header is allowed: it means a client that does not send Fetch
// Metadata, which includes curl and every older browser, and rejecting those
// would break the API for no gain. "none" is a user-initiated navigation.
func checkSameSite(r *http.Request) error {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
		return nil
	default:
		return fmt.Errorf("cross-site request rejected")
	}
}

// denyAuth answers an API caller with 401 and a browser with a redirect.
func (s *Server) denyAuth(w http.ResponseWriter, r *http.Request, dest, msg string) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeErr(w, http.StatusUnauthorized, fmt.Errorf("%s", msg))
		return
	}
	if r.URL.Path != "/" {
		dest += "?next=" + url.QueryEscape(r.URL.RequestURI())
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	// A logged-in operator has no business on the login form, and an install
	// with no operator has no business on it either.
	empty, err := s.opt.Auth.Empty()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if empty != (r.URL.Path == "/setup") {
		dest := "/login"
		if empty {
			dest = "/setup"
		}
		http.Redirect(w, r, dest, http.StatusSeeOther)
		return
	}
	title := "Sign in"
	if empty {
		title = "Create the first operator"
	}
	s.render(w, s.pages["login.html"], "bare", s.chrome(title, ""))
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
	// Token is the one-time setup token, on /api/setup only.
	Token string `json:"token"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req credentials
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !s.verify(r.Context(), req.Username, req.Password) {
		// One message for both failure modes: a distinct "no such user" tells an
		// attacker which names are worth guessing passwords for.
		writeErr(w, http.StatusUnauthorized, fmt.Errorf("invalid username or password"))
		return
	}
	s.issueSession(w, r, strings.ToLower(strings.TrimSpace(req.Username)))
}

// verify runs one password check under the concurrency cap. A caller that
// gives up while queued (the browser closed the tab) is dropped rather than
// spending 68ms of bcrypt on a request nobody is waiting for.
func (s *Server) verify(ctx context.Context, username, password string) bool {
	select {
	case s.verifying <- struct{}{}:
		defer func() { <-s.verifying }()
	case <-ctx.Done():
		return false
	}
	return s.opt.Auth.Verify(username, password)
}

// handleSetup creates the first operator. It is reachable without a session
// only while the store is empty, which requireAuth and this check both enforce.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	empty, err := s.opt.Auth.Empty()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if !empty {
		writeErr(w, http.StatusConflict, fmt.Errorf("an operator account already exists; sign in at /login"))
		return
	}
	var req credentials
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.opt.SetupToken.Check(req.Token); err != nil {
		writeErr(w, http.StatusForbidden, err)
		return
	}
	if err := s.opt.Auth.Create(req.Username, req.Password); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// The token authorizes exactly one account, once.
	if err := s.opt.SetupToken.Spend(); err != nil {
		s.opt.Logger.Error("could not remove the spent setup token; delete it by hand",
			"path", s.opt.SetupToken.Path, "err", err)
	}
	s.opt.Logger.Info("created the first operator account", "user", req.Username)
	s.issueSession(w, r, strings.ToLower(strings.TrimSpace(req.Username)))
}

func (s *Server) issueSession(w http.ResponseWriter, r *http.Request, user string) {
	token, err := s.opt.Sessions.Create(user)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isHTTPS(r),
	})
	writeJSON(w, http.StatusOK, map[string]string{"username": user})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, _ := r.Cookie(auth.CookieName); cookie != nil {
		s.opt.Sessions.Delete(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isHTTPS(r),
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "signed out"})
}

func (s *Server) handleAccountGet(w http.ResponseWriter, r *http.Request) {
	names, err := s.opt.Auth.Usernames()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"username":     s.sessionUser(r),
		"operators":    names,
		"min_password": auth.MinPasswordLen,
	})
}

type passwordChange struct {
	Current string `json:"current"`
	New     string `json:"new"`
}

func (s *Server) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == "" {
		writeErr(w, http.StatusUnauthorized, fmt.Errorf("authentication required"))
		return
	}
	var req passwordChange
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.opt.Auth.ChangePassword(user, req.Current, req.New); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// Every other session for this operator was opened with the old password.
	s.opt.Sessions.DeleteUser(user)
	s.opt.Logger.Info("operator password changed", "user", user)
	s.issueSession(w, r, user)
}

func (s *Server) sessionUser(r *http.Request) string {
	cookie, _ := r.Cookie(auth.CookieName)
	if cookie == nil {
		return ""
	}
	user, _ := s.opt.Sessions.User(cookie.Value)
	return user
}

// isHTTPS reports whether the request reached us over TLS, directly or through
// Traefik. It decides the Secure cookie flag: setting it unconditionally would
// break the plain-HTTP bootstrap window before the CA issues the control
// plane's own leaf.
func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
