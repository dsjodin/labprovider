package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dsjodin/provider-box/services/control-plane/internal/certs"
	"github.com/dsjodin/provider-box/services/control-plane/internal/dns"
	"github.com/dsjodin/provider-box/services/control-plane/internal/envfile"
	"github.com/dsjodin/provider-box/services/control-plane/internal/ipam"
)

// Lazy panel providers for the engine-enabled deployment (install.sh). The
// legacy compose deployment configured the panels through CONTROL_PLANE_*
// process env vars, but under install.sh the values only exist after the
// wizard saves a config and the services deploy. These providers resolve the
// managed config and the auto-provisioned token files on every call, so
// panels light up as soon as their service is deployed - no restart needed.
// A missing config or token renders the panel "unavailable" with the reason.

type lazySource struct {
	store   envfile.Store
	timeout time.Duration
}

func (l lazySource) env() (map[string]string, error) {
	content, saved, err := l.store.Load()
	if err != nil {
		return nil, err
	}
	if !saved {
		return nil, fmt.Errorf("no configuration saved yet (use the config wizard)")
	}
	return envfile.Parse(content), nil
}

func (l lazySource) secretsDir(env map[string]string) string {
	if v := env["CONTROL_PLANE_SECRETS_DIR"]; v != "" {
		return v
	}
	return "/opt/provider-box/control-plane/secrets"
}

func (l lazySource) token(env map[string]string, name, producer string) (string, error) {
	b, err := os.ReadFile(filepath.Join(l.secretsDir(env), name))
	if err != nil {
		return "", fmt.Errorf("no %s yet (auto-provisioned when %s deploys)", name, producer)
	}
	return strings.TrimSpace(string(b)), nil
}

func (l lazySource) caBundle(env map[string]string) string {
	return filepath.Join(env["CA_DATA_DIR"], "certs", "root_ca.crt")
}

type lazyCerts struct{ lazySource }

func (l lazyCerts) List(ctx context.Context) ([]certs.Cert, error) {
	env, err := l.env()
	if err != nil {
		return nil, err
	}
	for _, key := range []string{"CA_POSTGRES_RO_USER", "CA_POSTGRES_RO_PASSWORD", "CA_POSTGRES_PORT", "CA_POSTGRES_DB"} {
		if env[key] == "" {
			return nil, fmt.Errorf("%s is not set in the configuration", key)
		}
	}
	dsn := (&url.URL{
		Scheme:   "postgresql",
		User:     url.User(env["CA_POSTGRES_RO_USER"]),
		Host:     "127.0.0.1:" + env["CA_POSTGRES_PORT"],
		Path:     "/" + env["CA_POSTGRES_DB"],
		RawQuery: "sslmode=disable",
	}).String()
	r, err := certs.NewReader(dsn, env["CA_POSTGRES_RO_PASSWORD"])
	if err != nil {
		return nil, err
	}
	return r.List(ctx)
}

type lazyDNS struct{ lazySource }

func (l lazyDNS) Fetch(ctx context.Context) (dns.Overview, error) {
	env, err := l.env()
	if err != nil {
		return dns.Overview{}, err
	}
	token, err := l.token(env, "technitium.token", "technitium")
	if err != nil {
		return dns.Overview{}, err
	}
	c, err := dns.New(fmt.Sprintf("https://%s:%s", env["DNS_FQDN"], env["TECHNITIUM_HTTPS_PORT"]),
		token, l.caBundle(env), l.timeout)
	if err != nil {
		return dns.Overview{}, err
	}
	return c.Fetch(ctx)
}

type lazyIPAM struct{ lazySource }

func (l lazyIPAM) Fetch(ctx context.Context) (ipam.Overview, error) {
	env, err := l.env()
	if err != nil {
		return ipam.Overview{}, err
	}
	token, err := l.token(env, "netbox-readonly.token", "netbox")
	if err != nil {
		return ipam.Overview{}, err
	}
	c, err := ipam.New(fmt.Sprintf("https://%s:%s", env["NETBOX_FQDN"], env["NETBOX_PORT"]),
		token, l.caBundle(env), l.timeout)
	if err != nil {
		return ipam.Overview{}, err
	}
	return c.Fetch(ctx)
}
