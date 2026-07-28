package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dsjodin/labprovider/services/control-plane/internal/auth"
	"github.com/dsjodin/labprovider/services/control-plane/internal/certs"
	"github.com/dsjodin/labprovider/services/control-plane/internal/config"
	"github.com/dsjodin/labprovider/services/control-plane/internal/deploy"
	"github.com/dsjodin/labprovider/services/control-plane/internal/dns"
	"github.com/dsjodin/labprovider/services/control-plane/internal/docker"
	"github.com/dsjodin/labprovider/services/control-plane/internal/envfile"
	"github.com/dsjodin/labprovider/services/control-plane/internal/ipam"
	"github.com/dsjodin/labprovider/services/control-plane/internal/msca"
	"github.com/dsjodin/labprovider/services/control-plane/internal/server"
)

// refreshDNSSyncBuiltins rewrites the live built-in DNS record file dns-sync
// re-reads each pass, so services added or removed in a wizard save publish (or
// retire) their names on the next reconcile without a dns-sync redeploy. No-op
// until dns-sync is deployed; failures are logged, never fatal.
func refreshDNSSyncBuiltins(store envfile.Store, logger *slog.Logger) {
	content, ok, err := store.Load()
	if err != nil || !ok {
		return
	}
	env := envfile.Parse(content)
	if ipv4, network, err := envfile.DeriveHostIP(env["HOST_IP"]); err == nil {
		env["HOST_IPV4"] = ipv4
		env["HOST_NETWORK_CIDR"] = network
	}
	if err := deploy.RefreshBuiltinRecordsFile(env); err != nil {
		logger.Warn("refresh dns-sync builtin records", "err", err)
	}
}

// version identifies the running build. The Dockerfile stamps it with
// git describe at -ldflags time; a local `go build` leaves it "dev".
//
// Without it there is no way - from the UI, the API, or docker inspect - to
// tell which commit is running, which makes "did my fix deploy?" unanswerable
// and bug reports unattributable.
var version = "dev"

func main() {
	// Bootstrap logger: config.Load itself warns about unreadable token files
	// and malformed values, so it needs somewhere to warn to before the
	// configured level is known.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	cfg := config.Load()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)
	logger.Info("control plane starting", "version", version, "log_level", cfg.LogLevel)

	opt := server.Options{
		FQDN:             cfg.FQDN,
		Version:          version,
		WarnDays:         cfg.CertWarnDays,
		LogTail:          cfg.LogTail,
		ContainerFilters: cfg.ContainerFilters,
		Timeout:          cfg.UpstreamTimeout,
		Logger:           logger,
	}

	// Each provider is optional; a nil provider renders its panel as
	// "not configured" rather than failing the page.
	if cfg.StepCADSN != "" {
		r, err := certs.NewReader(cfg.StepCADSN, cfg.StepCAPassword)
		if err != nil {
			logger.Error("init stepca certs reader", "err", err)
		} else {
			opt.Certs = r
		}
	}
	if cfg.TechnitiumURL != "" && cfg.TechnitiumToken != "" {
		c, err := dns.New(cfg.TechnitiumURL, cfg.TechnitiumToken, cfg.TechnitiumCABundle, cfg.UpstreamTimeout)
		if err != nil {
			logger.Error("init technitium client", "err", err)
		} else {
			opt.DNS = c
		}
	}
	if cfg.NetboxURL != "" && cfg.NetboxToken != "" {
		c, err := ipam.New(cfg.NetboxURL, cfg.NetboxToken, cfg.NetboxCABundle, cfg.UpstreamTimeout)
		if err != nil {
			logger.Error("init netbox client", "err", err)
		} else {
			opt.IPAM = c
		}
	}
	if cfg.DockerHost != "" {
		c, err := docker.New(cfg.DockerHost, cfg.UpstreamTimeout)
		if err != nil {
			logger.Error("init docker client", "err", err)
		} else {
			opt.Docker = c
		}
	}

	// The control plane serves plain HTTP (Traefik terminates TLS); the same
	// decision drives the certsrv listener below.
	useTLS := resolveTLS(cfg.TLSCert, cfg.TLSKey, logger)

	// Optional Microsoft-CA web-enrollment emulator (certsrv) for VCF. Managed
	// by mscaManager so enabling it in the wizard binds the listener on the next
	// config save, with no control-plane restart.
	var msca *mscaManager

	// The deploy engine needs the shipped example config (baked into the image
	// by install.sh's build). Without it - the legacy --dashboard deployment -
	// the server stays a read-only dashboard.
	if _, err := os.Stat(cfg.ExamplePath); err == nil {
		store := envfile.Store{Path: cfg.ConfigPath, ExamplePath: cfg.ExamplePath}

		// Engine-enabled deployments resolve the panel upstreams from the
		// managed config at page-load time; explicit CONTROL_PLANE_* env vars
		// (the legacy compose wiring) win when set above.
		src := lazySource{store: store, timeout: cfg.UpstreamTimeout}
		if opt.Certs == nil {
			opt.Certs = lazyCerts{src}
		}
		if opt.DNS == nil {
			opt.DNS = lazyDNS{src}
		}
		if opt.IPAM == nil {
			opt.IPAM = lazyIPAM{src}
		}

		engine := deploy.NewEngine(store, &deploy.StateStore{Path: cfg.StatePath}, logger)
		engine.ListenAddr = cfg.Addr
		deploy.RegisterAll(engine)
		opt.Engine = engine

		msca = &mscaManager{store: store, logger: logger, tlsCert: cfg.TLSCert, tlsKey: cfg.TLSKey, useTLS: useTLS}
		opt.OnConfigSaved = func() {
			msca.Reconcile()
			refreshDNSSyncBuiltins(store, logger)
		}
	} else {
		logger.Warn("deploy engine disabled: example config not found", "path", cfg.ExamplePath)
	}

	// Operator login. The control plane runs as root with the Docker socket
	// mounted, so an unauthenticated write surface is remote root, not
	// information disclosure. With no account yet, the server serves only the
	// one-time /setup form.
	if cfg.UsersPath != "" {
		opt.Auth = &auth.Store{Path: cfg.UsersPath}
		opt.Sessions = auth.NewSessions(cfg.SessionTTL)
		if empty, err := opt.Auth.Empty(); err != nil {
			logger.Error("read user store", "path", cfg.UsersPath, "err", err)
			os.Exit(1)
		} else if empty {
			// The token turns the first-run window from "whoever reaches /setup
			// first owns this host" into an authenticated bootstrap. Generated
			// only while no account exists, and spent by the account creation.
			token, err := server.NewSetupToken(filepath.Join(filepath.Dir(cfg.UsersPath), "setup-token"))
			if err != nil {
				logger.Error("prepare the setup token", "err", err)
				os.Exit(1)
			}
			opt.SetupToken = token
			logger.Warn("no operator account yet: open /setup to create the first one",
				"path", cfg.UsersPath, "setup_token", token.Value())
		}
	} else {
		logger.Warn("authentication disabled: CONTROL_PLANE_USERS_PATH is empty")
	}

	srv, err := server.New(opt)
	if err != nil {
		logger.Error("build server", "err", err)
		os.Exit(1)
	}

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelShutdown()
		_ = httpSrv.Shutdown(shutdownCtx)
		if msca != nil {
			msca.Stop()
		}
	}()

	// The certsrv emulator is a second listener on its own port, serving plain
	// HTTP behind Traefik. Bind it now if VMSCA is already enabled; a later
	// wizard save re-runs this via OnConfigSaved.
	if msca != nil {
		msca.Reconcile()
	}

	logger.Info("starting control-plane",
		"addr", cfg.Addr, "fqdn", cfg.FQDN, "tls", useTLS, "auth", opt.Auth != nil,
		"certs", opt.Certs != nil, "dns", opt.DNS != nil,
		"ipam", opt.IPAM != nil, "docker", opt.Docker != nil,
	)

	if useTLS {
		err = httpSrv.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
	} else {
		err = httpSrv.ListenAndServe()
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("http server exited", "err", err)
		os.Exit(1)
	}
}

// resolveTLS reports whether the server should serve HTTPS. It returns true only
// when both paths are set and load as a valid keypair; otherwise it logs a
// warning and returns false so the caller serves plaintext HTTP instead of
// crash-looping on a missing or malformed cert.
func resolveTLS(certPath, keyPath string, logger *slog.Logger) bool {
	if certPath == "" || keyPath == "" {
		logger.Warn("no TLS cert configured (CONTROL_PLANE_TLS_CERT/CONTROL_PLANE_TLS_KEY); serving plaintext HTTP - do not use outside a trusted lab")
		return false
	}
	if _, err := tls.LoadX509KeyPair(certPath, keyPath); err != nil {
		logger.Warn("TLS cert/key unreadable; falling back to plaintext HTTP - do not use outside a trusted lab",
			"cert", certPath, "key", keyPath, "err", err)
		return false
	}
	return true
}

// mscaManager owns the certsrv (MSCA) emulator listener and rebinds it from the
// managed config. Because the listener is bound from config that on a fresh
// install is saved after the process starts, Reconcile is called both at
// startup and on every wizard save, so enabling/disabling VMSCA (or changing its
// credentials) takes effect without a control-plane restart.
type mscaManager struct {
	store   envfile.Store
	logger  *slog.Logger
	tlsCert string
	tlsKey  string
	useTLS  bool

	mu  sync.Mutex
	srv *http.Server
}

// Reconcile stops any running certsrv listener and starts a fresh one when
// VMSCA is enabled. Safe to call repeatedly.
func (m *mscaManager) Reconcile() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.srv != nil {
		_ = m.srv.Close()
		m.srv = nil
	}

	h, addr, err := buildMSCA(m.store, m.logger)
	if err != nil {
		m.logger.Warn("msca certsrv emulator disabled", "err", err)
		return
	}
	if h == nil {
		return // VMSCA not enabled
	}

	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	m.srv = srv
	go func() {
		m.logger.Info("starting msca certsrv emulator", "addr", addr, "tls", m.useTLS)
		var err error
		if m.useTLS {
			err = srv.ListenAndServeTLS(m.tlsCert, m.tlsKey)
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			m.logger.Error("msca listener exited", "err", err)
		}
	}()
}

// Stop shuts the current certsrv listener down (process shutdown).
func (m *mscaManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.srv != nil {
		_ = m.srv.Close()
		m.srv = nil
	}
}

// buildMSCA constructs the certsrv emulator handler from the managed config
// when VMSCA_ENABLE is true, returning (nil, "", nil) when it is off. The signer
// and CA-chain closures reload the managed config on each request, so a CA
// deployed or reconfigured after startup is picked up without a restart -
// exactly like the dashboard's /api/csr/sign path.
func buildMSCA(store envfile.Store, logger *slog.Logger) (http.Handler, string, error) {
	content, saved, err := store.Load()
	if err != nil {
		return nil, "", err
	}
	if !saved {
		return nil, "", nil
	}
	env := envfile.Parse(content)
	if !strings.EqualFold(env["VMSCA_ENABLE"], "true") {
		return nil, "", nil
	}
	user, pass := env["VMSCA_USERNAME"], env["VMSCA_PASSWORD"]
	if user == "" || pass == "" {
		return nil, "", fmt.Errorf("VMSCA_USERNAME and VMSCA_PASSWORD must be set")
	}
	port := env["VMSCA_PORT"]
	if port == "" {
		port = "8446"
	}

	sign := func(ctx context.Context, csr []byte) ([]byte, error) {
		content, saved, err := store.Load()
		if err != nil {
			return nil, err
		}
		if !saved {
			return nil, fmt.Errorf("no configuration saved")
		}
		return deploy.SignCSR(ctx, envfile.Parse(content), csr)
	}
	caChain := func() ([]byte, error) {
		content, _, err := store.Load()
		if err != nil {
			return nil, err
		}
		dir := envfile.Parse(content)["CA_DATA_DIR"]
		inter, err := os.ReadFile(filepath.Join(dir, "certs", "intermediate_ca.crt"))
		if err != nil {
			return nil, err
		}
		root, err := os.ReadFile(filepath.Join(dir, "certs", "root_ca.crt"))
		if err != nil {
			return nil, err
		}
		return append(inter, root...), nil
	}

	h := msca.New(msca.Config{Username: user, Password: pass, Template: env["VMSCA_TEMPLATE"]}, sign, caChain, logger)
	return h, ":" + port, nil
}
