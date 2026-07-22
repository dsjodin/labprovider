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
	if err := os.WriteFile(runtime+"/import/provider-box-realm.json", realm, 0o644); err != nil {
		return err
	}

	certDir := filepath.Join(env["KEYCLOAK_DIR"], "certs")
	if err := EnsureDir(filepath.Join(env["KEYCLOAK_DIR"], "data"), 0o755, 1000, 1000); err != nil {
		return err
	}
	if err := IssueCert(ctx, rc, env["KEYCLOAK_FQDN"], certDir, "keycloak"); err != nil {
		return err
	}
	if err := writeKeycloakFullChain(rc, certDir); err != nil {
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

	url := fmt.Sprintf("https://%s:%s/", env["KEYCLOAK_FQDN"], env["KEYCLOAK_PORT"])
	rc.Log("Waiting for Keycloak at %s.", url)
	caRoot := filepath.Join(env["CA_DATA_DIR"], "certs", "root_ca.crt")
	if err := WaitHTTPSPinned(ctx, url, caRoot, 45, 2*time.Second); err != nil {
		return err
	}
	rc.Log("Keycloak is ready at %s (realm %s; use keycloak-full-chain.pem for the VCF SSO chain upload).",
		url, env["KEYCLOAK_BOOTSTRAP_REALM_NAME"])
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

// writeKeycloakFullChain builds keycloak-full-chain.pem (leaf, intermediate,
// root - exactly 3 certificates) for the VCF SSO certificate-chain upload.
func writeKeycloakFullChain(rc *RunCtx, certDir string) error {
	env := rc.Env
	certPEM, err := os.ReadFile(filepath.Join(certDir, "keycloak.crt"))
	if err != nil {
		return err
	}
	// The leaf is the first certificate block in the served cert file.
	end := strings.Index(string(certPEM), "-----END CERTIFICATE-----")
	if end < 0 {
		return fmt.Errorf("no certificate in keycloak.crt")
	}
	leaf := certPEM[:end+len("-----END CERTIFICATE-----")+1]

	intermediate, err := os.ReadFile(filepath.Join(env["CA_DATA_DIR"], "certs", "intermediate_ca.crt"))
	if err != nil {
		return err
	}
	root, err := os.ReadFile(filepath.Join(env["CA_DATA_DIR"], "certs", "root_ca.crt"))
	if err != nil {
		return err
	}
	full := append(append(leaf, intermediate...), root...)
	if strings.Count(string(full), "BEGIN CERTIFICATE") != 3 {
		return fmt.Errorf("keycloak full certificate chain bundle must contain exactly 3 certificates")
	}
	fullChain := filepath.Join(certDir, "keycloak-full-chain.pem")
	if err := os.WriteFile(fullChain, full, 0o644); err != nil {
		return err
	}
	return os.Chown(fullChain, 1000, 1000)
}
