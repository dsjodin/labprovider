package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Keycloak deploys the Keycloak identity provider, the port of
// bootstrap/keycloak.sh. The opinionated bootstrap realm import is built with
// encoding/json (replacing the json_escape/heredoc templating); Keycloak only
// applies it on first start, so existing realms are never reconciled.
type Keycloak struct{}

func (Keycloak) Name() string   { return "keycloak" }
func (Keycloak) Deps() []string { return []string{"ca"} }

func (k Keycloak) Deploy(ctx context.Context, rc *RunCtx) error {
	env := rc.Env
	runtime := rc.Workdir("keycloak")

	if err := requireCAReady(ctx, env); err != nil {
		return err
	}

	realm, err := buildKeycloakRealm(env)
	if err != nil {
		return err
	}
	if err := EnsureDir(runtime+"/import", 0o755, -1, -1); err != nil {
		return err
	}
	if err := os.WriteFile(runtime+"/import/labprovider-realm.json", realm, 0o644); err != nil {
		return err
	}

	if err := EnsureDir(filepath.Join(env["KEYCLOAK_DIR"], "data"), 0o755, 1000, 1000); err != nil {
		return err
	}

	if err := Render("docker-compose.keycloak.yml.tpl", env, runtime+"/docker-compose.yml", 0o644); err != nil {
		return err
	}
	cmp := rc.Compose("keycloak")
	if err := cmp.Down(ctx); err != nil {
		return err
	}
	if err := cmp.Up(ctx); err != nil {
		return err
	}

	// Keycloak serves plain HTTP behind Traefik; probe the loopback-published
	// port so readiness does not depend on DNS. Public access is at
	// https://<KEYCLOAK_FQDN> through Traefik.
	url := fmt.Sprintf("http://%s:%s/", env["KEYCLOAK_FQDN"], env["KEYCLOAK_PORT"])
	rc.Log("Waiting for Keycloak at %s.", url)
	if err := waitHTTPPinned(ctx, url, 45, 2*time.Second); err != nil {
		return err
	}
	rc.Log("Keycloak is ready at https://%s (realm %s; VCF trusts the step-ca root - upload %s/certs/root_ca.crt).",
		env["KEYCLOAK_FQDN"], env["KEYCLOAK_BOOTSTRAP_REALM_NAME"], env["CA_DATA_DIR"])
	return nil
}

func (k Keycloak) Remove(ctx context.Context, rc *RunCtx) error {
	cmp := rc.Compose("keycloak")
	if err := cmp.Down(ctx); err != nil {
		return err
	}
	if err := os.RemoveAll(rc.Workdir("keycloak")); err != nil {
		return err
	}
	rc.Log("Removed Keycloak containers and runtime files. Persistent data in %s was preserved.", rc.Env["KEYCLOAK_DIR"])
	return nil
}

// buildKeycloakRealm assembles the opinionated bootstrap realm: one realm,
// one group, one OIDC client for VCF-style integration, and (when configured)
// one lab user.
func buildKeycloakRealm(env map[string]string) ([]byte, error) {
	var redirectURIs []string
	for _, uri := range strings.Split(env["KEYCLOAK_BOOTSTRAP_CLIENT_REDIRECT_URIS"], ",") {
		if uri == "" {
			return nil, fmt.Errorf("KEYCLOAK_BOOTSTRAP_CLIENT_REDIRECT_URIS contains an empty entry")
		}
		if !strings.HasPrefix(uri, "https://") {
			return nil, fmt.Errorf("KEYCLOAK_BOOTSTRAP_CLIENT_REDIRECT_URIS entries must start with https://: %s", uri)
		}
		redirectURIs = append(redirectURIs, uri)
	}
	if len(redirectURIs) == 0 {
		return nil, fmt.Errorf("KEYCLOAK_BOOTSTRAP_CLIENT_REDIRECT_URIS must not be empty")
	}

	realm := map[string]any{
		"realm":                       env["KEYCLOAK_BOOTSTRAP_REALM_NAME"],
		"enabled":                     true,
		"displayName":                 env["KEYCLOAK_BOOTSTRAP_REALM_NAME"],
		"sslRequired":                 "external",
		"registrationAllowed":         false,
		"registrationEmailAsUsername": false,
		"rememberMe":                  false,
		"verifyEmail":                 false,
		"loginWithEmailAllowed":       true,
		"duplicateEmailsAllowed":      false,
		"resetPasswordAllowed":        false,
		"editUsernameAllowed":         false,
		"bruteForceProtected":         false,
		"groups": []map[string]any{
			{"name": env["KEYCLOAK_BOOTSTRAP_GROUP_NAME"]},
		},
		"clients": []map[string]any{{
			"clientId":                  env["KEYCLOAK_BOOTSTRAP_CLIENT_ID"],
			"name":                      env["KEYCLOAK_BOOTSTRAP_CLIENT_ID"],
			"description":               "VCF SSO",
			"enabled":                   true,
			"protocol":                  "openid-connect",
			"publicClient":              false,
			"secret":                    env["KEYCLOAK_BOOTSTRAP_CLIENT_SECRET"],
			"standardFlowEnabled":       true,
			"implicitFlowEnabled":       false,
			"directAccessGrantsEnabled": true,
			"serviceAccountsEnabled":    false,
			"frontchannelLogout":        true,
			"alwaysDisplayInConsole":    true,
			"redirectUris":              redirectURIs,
			"webOrigins":                []string{"+"},
			"protocolMappers": []map[string]any{{
				"name":            "groups",
				"protocol":        "openid-connect",
				"protocolMapper":  "oidc-group-membership-mapper",
				"consentRequired": false,
				"config": map[string]string{
					"full.path":            "false",
					"id.token.claim":       "true",
					"access.token.claim":   "true",
					"userinfo.token.claim": "true",
					"claim.name":           "groups",
				},
			}},
			"defaultClientScopes":  []string{"web-origins", "profile", "email", "roles"},
			"optionalClientScopes": []string{"address", "phone", "offline_access", "microprofile-jwt"},
			"attributes": map[string]string{
				"backchannel.logout.session.required":      "true",
				"backchannel.logout.revoke.offline.tokens": "false",
			},
		}},
	}

	username, password := env["KEYCLOAK_BOOTSTRAP_USERNAME"], env["KEYCLOAK_BOOTSTRAP_USER_PASSWORD"]
	if username != "" || password != "" {
		if username == "" || password == "" || env["KEYCLOAK_BOOTSTRAP_USER_EMAIL_DOMAIN"] == "" {
			return nil, fmt.Errorf("KEYCLOAK_BOOTSTRAP_USERNAME, KEYCLOAK_BOOTSTRAP_USER_PASSWORD, and KEYCLOAK_BOOTSTRAP_USER_EMAIL_DOMAIN must all be set to bootstrap the lab user")
		}
		realm["users"] = []map[string]any{{
			"username":      username,
			"email":         username + "@" + env["KEYCLOAK_BOOTSTRAP_USER_EMAIL_DOMAIN"],
			"enabled":       true,
			"emailVerified": true,
			"groups":        []string{"/" + env["KEYCLOAK_BOOTSTRAP_GROUP_NAME"]},
			"credentials": []map[string]any{{
				"type": "password", "value": password, "temporary": false,
			}},
		}}
	}
	return json.MarshalIndent(realm, "", "  ")
}
