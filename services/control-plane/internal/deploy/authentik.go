package deploy

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Authentik deploys the Authentik identity provider (OIDC + outbound SCIM),
// the port of bootstrap/authentik.sh: server + worker + postgres, a bootstrap
// blueprint (group, lab user, OIDC provider, VCF application), certificate
// discovery via /certs, and the brand web certificate PATCH. Until the brand
// certificate is set Authentik serves a self-signed cert, so bootstrap-phase
// API calls skip verification; the final check verifies the step-ca chain.
type Authentik struct{}

func (Authentik) Name() string   { return "authentik" }
func (Authentik) Deps() []string { return []string{"ca"} }

func (a Authentik) Deploy(ctx context.Context, rc *RunCtx) error {
	env := rc.Env
	runtime := rc.Workdir("authentik")

	if len(env["AUTHENTIK_SECRET_KEY"]) < 50 {
		return fmt.Errorf("AUTHENTIK_SECRET_KEY must be at least 50 characters long")
	}
	for _, name := range []string{"AUTHENTIK_ADMIN_PASSWORD", "AUTHENTIK_API_TOKEN", "AUTHENTIK_PG_PASSWORD",
		"AUTHENTIK_BOOTSTRAP_CLIENT_SECRET", "AUTHENTIK_BOOTSTRAP_USER_PASSWORD"} {
		v := env[name]
		if strings.ContainsAny(v, `"\`) {
			return fmt.Errorf("%s must not contain double quotes or backslashes", name)
		}
	}
	if err := requireCAReady(ctx, env); err != nil {
		return err
	}

	for _, dir := range []string{runtime, env["AUTHENTIK_DIR"]} {
		if err := EnsureDir(dir, 0o755, -1, -1); err != nil {
			return err
		}
	}
	if err := EnsureDir(filepath.Join(env["AUTHENTIK_DIR"], "data"), 0o755, 1000, 1000); err != nil {
		return err
	}
	if err := EnsureDir(filepath.Join(env["AUTHENTIK_DIR"], "postgres"), 0o700, -1, -1); err != nil {
		return err
	}
	if err := chownR(filepath.Join(env["AUTHENTIK_DIR"], "postgres"), 70, 70); err != nil {
		return err
	}

	blueprint, err := buildAuthentikBlueprintBlock(env)
	if err != nil {
		return err
	}
	if err := EnsureDir(runtime+"/blueprints", 0o755, -1, -1); err != nil {
		return err
	}
	renderEnv := withDerived(env, map[string]string{
		"AUTHENTIK_BOOTSTRAP_CLIENT_REDIRECT_URIS_BLOCK": blueprint,
	})
	if err := Render("authentik-blueprint.yaml.tpl", renderEnv, runtime+"/blueprints/labprovider-vcf.yaml", 0o644); err != nil {
		return err
	}

	if err := Render("docker-compose.authentik.yml.tpl", env, runtime+"/docker-compose.yml", 0o644); err != nil {
		return err
	}
	cmp := rc.Compose("authentik")
	if err := cmp.Down(ctx); err != nil {
		return err
	}
	if err := cmp.Up(ctx); err != nil {
		return err
	}

	api := authentikAPI{env: env}
	readyURL := api.base() + "/-/health/ready/"
	rc.Log("Waiting for Authentik at %s.", readyURL)
	if err := api.waitStatus(ctx, readyURL, "", []int{200, 204}, 90, 2*time.Second); err != nil {
		return err
	}
	// /-/health/ready/ signals HTTP readiness but not token-auth readiness:
	// for a few seconds after start Authentik rejects even valid tokens.
	rc.Log("Waiting for Authentik API token authentication.")
	if err := api.waitStatus(ctx, api.base()+"/api/v3/core/brands/", env["AUTHENTIK_API_TOKEN"], []int{200}, 30, 2*time.Second); err != nil {
		return fmt.Errorf("Authentik API token authentication did not become ready (on a re-run, persistent data may hold a different admin token than AUTHENTIK_API_TOKEN): %w", err)
	}
	rc.Log("Authentik is ready at https://%s (OIDC discovery under /application/o/vcf/).",
		env["AUTHENTIK_FQDN"])
	return nil
}

func (a Authentik) Remove(ctx context.Context, rc *RunCtx) error {
	cmp := rc.Compose("authentik")
	if err := cmp.Down(ctx); err != nil {
		return err
	}
	if err := os.RemoveAll(rc.Workdir("authentik")); err != nil {
		return err
	}
	rc.Log("Removed Authentik containers and runtime files. Persistent data in %s was preserved.", rc.Env["AUTHENTIK_DIR"])
	return nil
}

// buildAuthentikBlueprintBlock builds the YAML redirect-URIs list entries.
func buildAuthentikBlueprintBlock(env map[string]string) (string, error) {
	var lines []string
	for _, uri := range strings.Split(env["AUTHENTIK_BOOTSTRAP_CLIENT_REDIRECT_URIS"], ",") {
		if uri == "" {
			return "", fmt.Errorf("AUTHENTIK_BOOTSTRAP_CLIENT_REDIRECT_URIS contains an empty entry")
		}
		if !strings.HasPrefix(uri, "https://") {
			return "", fmt.Errorf("AUTHENTIK_BOOTSTRAP_CLIENT_REDIRECT_URIS entries must start with https://: %s", uri)
		}
		if strings.Contains(uri, `"`) {
			return "", fmt.Errorf("AUTHENTIK_BOOTSTRAP_CLIENT_REDIRECT_URIS entries must not contain double quotes")
		}
		lines = append(lines, "        - matching_mode: strict\n          url: \""+uri+"\"")
	}
	if len(lines) == 0 {
		return "", fmt.Errorf("AUTHENTIK_BOOTSTRAP_CLIENT_REDIRECT_URIS must not be empty")
	}
	return strings.Join(lines, "\n"), nil
}

// authentikAPI wraps the bootstrap-phase API calls: FQDN pinned to
// 127.0.0.1, TLS verification skipped (self-signed until the brand
// certificate is set).
type authentikAPI struct {
	env map[string]string
}

func (a authentikAPI) base() string {
	// Plain HTTP on the loopback-published port; Traefik fronts the public HTTPS.
	return fmt.Sprintf("http://%s:%s", a.env["AUTHENTIK_FQDN"], a.env["AUTHENTIK_PORT"])
}

func (a authentikAPI) client() *http.Client {
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // pre-brand-cert bootstrap window
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				_, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				return dialer.DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
			},
		},
	}
}

func (a authentikAPI) do(ctx context.Context, method, url string, payload []byte) (int, []byte, error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.env["AUTHENTIK_API_TOKEN"])
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, b, nil
}

// waitStatus polls url (optionally token-authenticated) until the status is
// in want.
func (a authentikAPI) waitStatus(ctx context.Context, url, token string, want []int, attempts int, interval time.Duration) error {
	var last int
	for i := 0; i < attempts; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := a.client().Do(req)
		if err == nil {
			resp.Body.Close()
			last = resp.StatusCode
			for _, w := range want {
				if last == w {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
	return fmt.Errorf("%s did not become ready (last HTTP status %d)", url, last)
}

// firstResult returns the given string field from a {"results":[{...}]}
// response, scanning every result. When the structured shape does not match,
// it falls back to a flat "field":"value" search over the raw body - the
// same tolerance the bash module's sed extraction had.
func firstResult(body []byte, field string) string {
	var out struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(body, &out); err == nil {
		for _, r := range out.Results {
			if v, ok := r[field].(string); ok && v != "" {
				return v
			}
		}
	}
	m := regexp.MustCompile(`"` + regexp.QuoteMeta(field) + `":"([^"]*)"`).FindSubmatch(body)
	if m != nil {
		return string(m[1])
	}
	return ""
}
