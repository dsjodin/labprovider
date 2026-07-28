package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// technitiumAPI is a minimal client for the Technitium HTTP API on the local
// console port. Bootstrap-phase calls go over HTTP on 127.0.0.1 only, like
// the bash module. Every endpoint returns {"status":"ok",...} on success.
type technitiumAPI struct {
	base string // http://127.0.0.1:<TECHNITIUM_HTTP_PORT>
}

// technitiumClient bounds every bootstrap call. Technitium is a foundation
// service and the engine is single-flight, so a container that accepts the
// connection and never answers would wedge every later deploy until the control
// plane restarts. Every other API client here already has a timeout.
var technitiumClient = &http.Client{Timeout: 30 * time.Second}

func newTechnitiumAPI(env map[string]string) technitiumAPI {
	return technitiumAPI{base: "http://127.0.0.1:" + env["TECHNITIUM_HTTP_PORT"]}
}

// call GETs path with params and decodes the JSON response into a generic
// map after checking status == want (empty want skips the check).
func (t technitiumAPI) call(ctx context.Context, path string, params url.Values) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.base+path+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := technitiumClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("%s: non-JSON response: %.200s", path, body)
	}
	return out, nil
}

func (t technitiumAPI) callOK(ctx context.Context, path string, params url.Values) (map[string]any, error) {
	out, err := t.call(ctx, path, params)
	if err != nil {
		return nil, err
	}
	if s, _ := out["status"].(string); s != "ok" {
		return out, fmt.Errorf("%s: status %v: %v", path, out["status"], out["errorMessage"])
	}
	return out, nil
}

// Login returns a session token for user/pass.
func (t technitiumAPI) Login(ctx context.Context, user, pass string) (string, error) {
	out, err := t.callOK(ctx, "/api/user/login", url.Values{"user": {user}, "pass": {pass}})
	if err != nil {
		return "", err
	}
	token, _ := out["token"].(string)
	if token == "" {
		return "", fmt.Errorf("login returned no token")
	}
	return token, nil
}

// AdminToken logs in with the configured admin password, falling back to the
// first-boot admin/admin and rotating it to the configured value (fixes the
// re-run failure and the default-credentials window; IMPROVEMENTS #1).
func (t technitiumAPI) AdminToken(ctx context.Context, rc *RunCtx) (string, error) {
	pass := rc.Env["TECHNITIUM_ADMIN_PASSWORD"]
	if token, err := t.Login(ctx, "admin", pass); err == nil {
		return token, nil
	}
	token, err := t.Login(ctx, "admin", "admin")
	if err != nil {
		return "", fmt.Errorf("cannot authenticate to Technitium with TECHNITIUM_ADMIN_PASSWORD or the first-boot credentials: %w", err)
	}
	// changePassword takes the CURRENT password (the first-boot "admin" we just
	// authenticated with) as pass and the new one as newPass. Omitting newPass
	// fails with "Parameter 'newPass' missing".
	if _, err := t.callOK(ctx, "/api/user/changePassword", url.Values{"token": {token}, "pass": {"admin"}, "newPass": {pass}}); err != nil {
		return "", fmt.Errorf("rotate the Technitium admin password: %w", err)
	}
	rc.Log("Rotated the Technitium admin password from the first-boot default to TECHNITIUM_ADMIN_PASSWORD.")
	return t.Login(ctx, "admin", pass)
}

// TokenValid probes an API token with the given call (e.g. /api/zones/list).
func (t technitiumAPI) TokenValid(ctx context.Context, token, probePath string) bool {
	out, err := t.call(ctx, probePath, url.Values{"token": {token}})
	if err != nil {
		return false
	}
	s, _ := out["status"].(string)
	return s == "ok"
}

// tokenField extracts the token from a response, checking both the top level
// (user/createToken) and the nested response object (admin/sessions/createToken).
func tokenField(out map[string]any) string {
	if token, _ := out["token"].(string); token != "" {
		return token
	}
	if resp, _ := out["response"].(map[string]any); resp != nil {
		if token, _ := resp["token"].(string); token != "" {
			return token
		}
	}
	return ""
}

// CreateUserToken mints a permanent API token by authenticating as the user
// (the verified /api/user/createToken endpoint; see TECHNITIUM_API.md).
func (t technitiumAPI) CreateUserToken(ctx context.Context, user, pass, tokenName string) (string, error) {
	out, err := t.callOK(ctx, "/api/user/createToken", url.Values{
		"user": {user}, "pass": {pass}, "tokenName": {tokenName},
	})
	if err != nil {
		return "", err
	}
	token := tokenField(out)
	if token == "" {
		return "", fmt.Errorf("user/createToken returned no token (response keys: %v)", keysOf(out))
	}
	return token, nil
}

// CreateToken mints a permanent API token for another user with the admin
// session (admin/sessions/createToken; the token nests under response).
func (t technitiumAPI) CreateToken(ctx context.Context, adminToken, user, tokenName string) (string, error) {
	out, err := t.callOK(ctx, "/api/admin/sessions/createToken", url.Values{
		"token": {adminToken}, "user": {user}, "tokenName": {tokenName},
	})
	if err != nil {
		return "", err
	}
	token := tokenField(out)
	if token == "" {
		return "", fmt.Errorf("admin/sessions/createToken returned no token (response keys: %v)", keysOf(out))
	}
	return token, nil
}

func keysOf(m map[string]any) []string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// UserExists reports whether the user is known.
func (t technitiumAPI) UserExists(ctx context.Context, adminToken, user string) bool {
	out, err := t.call(ctx, "/api/admin/users/get", url.Values{"token": {adminToken}, "user": {user}})
	if err != nil {
		return false
	}
	s, _ := out["status"].(string)
	return s == "ok"
}

func (t technitiumAPI) CreateUser(ctx context.Context, adminToken, user, displayName, pass string) error {
	_, err := t.callOK(ctx, "/api/admin/users/create", url.Values{
		"token": {adminToken}, "user": {user}, "displayName": {displayName}, "pass": {pass},
	})
	return err
}

// SetDHCPScope creates or updates a DHCP scope. Technitium's scopes/set is an
// upsert keyed by name, so this is idempotent: re-running a deploy with a wider
// range widens the existing scope instead of failing on a duplicate.
func (t technitiumAPI) SetDHCPScope(ctx context.Context, token string, s DHCPScope) error {
	params := url.Values{
		"token":            {token},
		"name":             {s.Name},
		"startingAddress":  {s.RangeStart},
		"endingAddress":    {s.RangeEnd},
		"subnetMask":       {s.SubnetMask},
		"routerAddress":    {s.Router},
		"domainName":       {s.DomainName},
		"leaseTimeDays":    {s.LeaseDays},
		"leaseTimeHours":   {"0"},
		"leaseTimeMinutes": {"0"},
		// The point of running DHCP here rather than as a separate service:
		// leases register in the lab zone automatically, and clients are handed
		// this same server as their resolver.
		"useThisDnsServer": {"true"},
		"dnsUpdates":       {"true"},
		// An address already answering pings is not handed out. Lab networks
		// collect static IPs nobody wrote down.
		"pingCheckEnabled": {"true"},
	}
	_, err := t.callOK(ctx, "/api/dhcp/scopes/set", params)
	return err
}

// EnableDHCPScope activates a scope so the server starts answering on it.
// Setting a scope does not enable it.
func (t technitiumAPI) EnableDHCPScope(ctx context.Context, token, name string) error {
	_, err := t.callOK(ctx, "/api/dhcp/scopes/enable", url.Values{"token": {token}, "name": {name}})
	return err
}

// DisableDHCPScope deactivates a scope, used when DHCP_ENABLE goes back to
// false with the scope still configured from an earlier deploy.
func (t technitiumAPI) DisableDHCPScope(ctx context.Context, token, name string) error {
	_, err := t.callOK(ctx, "/api/dhcp/scopes/disable", url.Values{"token": {token}, "name": {name}})
	return err
}

// DHCPScopeNames lists the configured scope names.
func (t technitiumAPI) DHCPScopeNames(ctx context.Context, token string) ([]string, error) {
	out, err := t.callOK(ctx, "/api/dhcp/scopes/list", url.Values{"token": {token}})
	if err != nil {
		return nil, err
	}
	resp, _ := out["response"].(map[string]any)
	rawScopes, _ := resp["scopes"].([]any)
	var names []string
	for _, s := range rawScopes {
		scope, _ := s.(map[string]any)
		if name, _ := scope["name"].(string); name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

// ZoneNames lists non-internal zone names.
func (t technitiumAPI) ZoneNames(ctx context.Context, token string) ([]string, error) {
	out, err := t.callOK(ctx, "/api/zones/list", url.Values{"token": {token}})
	if err != nil {
		return nil, err
	}
	resp, _ := out["response"].(map[string]any)
	rawZones, _ := resp["zones"].([]any)
	var names []string
	for _, z := range rawZones {
		zone, _ := z.(map[string]any)
		if internal, _ := zone["internal"].(bool); internal {
			continue
		}
		if name, _ := zone["name"].(string); name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}
