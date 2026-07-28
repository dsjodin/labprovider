package deploy

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

// LLDAP deploys LLDAP, a lightweight LDAP directory with a web admin UI. It
// serves plaintext LDAP and LDAPS (terminated with a step-ca leaf) on published
// host ports for appliance binds; the admin UI is fronted by Traefik. The three
// OIDC IdPs can federate this directory as their LDAP source.
type LLDAP struct{}

func (LLDAP) Name() string   { return "lldap" }
func (LLDAP) Deps() []string { return []string{"ca"} }

func (l LLDAP) Deploy(ctx context.Context, rc *RunCtx) error {
	env := rc.Env
	if err := EnsureDir(rc.Workdir("lldap"), 0o755, -1, -1); err != nil {
		return err
	}
	if err := EnsureDir(env["LLDAP_DATA_DIR"], 0o755, 1000, 1000); err != nil {
		return err
	}
	if err := IssueCert(ctx, rc, env["LLDAP_FQDN"], env["LLDAP_CERT_DIR"], "lldap"); err != nil {
		return err
	}
	if err := Render("docker-compose.lldap.yml.tpl", env, rc.Workdir("lldap")+"/docker-compose.yml", 0o644); err != nil {
		return err
	}
	cmp := rc.Compose("lldap")
	if err := cmp.Down(ctx); err != nil {
		return err
	}
	if err := cmp.Up(ctx); err != nil {
		return err
	}

	// The admin UI serves plain HTTP on the loopback-published port; Traefik
	// fronts it publicly at https://<LLDAP_FQDN>.
	url := fmt.Sprintf("http://%s:%s/", env["LLDAP_FQDN"], env["LLDAP_UI_PORT"])
	rc.Log("Waiting for the LLDAP admin UI at %s.", url)
	if err := waitHTTPPinned(ctx, url, 45, 2*time.Second); err != nil {
		return err
	}
	rc.Log("Waiting for the LDAP listener on port %s.", env["LLDAP_LDAP_PORT"])
	if err := WaitTCP(ctx, net.JoinHostPort("127.0.0.1", env["LLDAP_LDAP_PORT"]), 15, 2*time.Second); err != nil {
		return err
	}

	if err := seedVCenterDirectory(ctx, rc); err != nil {
		return err
	}

	rc.Log("LLDAP is ready: ldap://%s:%s, ldaps://%s:%s (base %s); admin UI at https://%s.",
		env["HOST_IPV4"], env["LLDAP_LDAP_PORT"], env["HOST_IPV4"], env["LLDAP_LDAPS_PORT"],
		env["LLDAP_BASE_DN"], env["LLDAP_FQDN"])
	return nil
}

const (
	lldapGroupVCenterAdmins   = "vsphere-admins"
	lldapGroupVCenterReadOnly = "vsphere-readonly"
)

// seedVCenterDirectory pre-provisions the accounts and groups a vCenter/VCF
// OpenLDAP identity source expects: a read-only bind account for the source
// connection, an admin test user, and the two group objects an operator maps
// to vCenter global permissions. Users, groups and membership go through the
// GraphQL API; passwords go through the in-image lldap_set_password utility
// (LLDAP stores passwords via OPAQUE, so there is no plain password field).
// Every step is idempotent, so a redeploy reconciles rather than duplicates.
func seedVCenterDirectory(ctx context.Context, rc *RunCtx) error {
	env := rc.Env
	bindUser := env["LLDAP_VCENTER_BIND_USER"]
	adminUser := env["LLDAP_VCENTER_ADMIN_USER"]
	domain := dcToDomain(env["LLDAP_BASE_DN"])

	api := newLLDAPAPI(env)
	if err := api.login(ctx, env["LLDAP_ADMIN_USER"], env["LLDAP_ADMIN_PASSWORD"]); err != nil {
		return err
	}

	groups, err := api.groups(ctx)
	if err != nil {
		return err
	}
	adminGID, err := api.ensureGroup(ctx, groups, lldapGroupVCenterAdmins)
	if err != nil {
		return fmt.Errorf("ensure group %s: %w", lldapGroupVCenterAdmins, err)
	}
	roGID, err := api.ensureGroup(ctx, groups, lldapGroupVCenterReadOnly)
	if err != nil {
		return fmt.Errorf("ensure group %s: %w", lldapGroupVCenterReadOnly, err)
	}

	if err := api.ensureUser(ctx, bindUser, bindUser+"@"+domain, "vCenter bind service account"); err != nil {
		return fmt.Errorf("ensure bind user %s: %w", bindUser, err)
	}
	if err := api.ensureUser(ctx, adminUser, adminUser+"@"+domain, "vCenter admin (test)"); err != nil {
		return fmt.Errorf("ensure admin user %s: %w", adminUser, err)
	}

	if err := setLLDAPPassword(ctx, rc, bindUser, env["LLDAP_VCENTER_BIND_PASSWORD"]); err != nil {
		return err
	}
	if err := setLLDAPPassword(ctx, rc, adminUser, env["LLDAP_VCENTER_ADMIN_PASSWORD"]); err != nil {
		return err
	}

	// Re-read memberships so the add is a no-op when already correct.
	groups, err = api.groups(ctx)
	if err != nil {
		return err
	}
	if err := api.addToGroup(ctx, groups, bindUser, roGID); err != nil {
		return fmt.Errorf("add %s to %s: %w", bindUser, lldapGroupVCenterReadOnly, err)
	}
	if err := api.addToGroup(ctx, groups, adminUser, adminGID); err != nil {
		return fmt.Errorf("add %s to %s: %w", adminUser, lldapGroupVCenterAdmins, err)
	}

	rc.Log("Seeded vCenter directory: bind account uid=%s,ou=people,%s (member of %s); test admin uid=%s,ou=people,%s (member of %s).",
		bindUser, env["LLDAP_BASE_DN"], lldapGroupVCenterReadOnly, adminUser, env["LLDAP_BASE_DN"], lldapGroupVCenterAdmins)
	rc.Log("vCenter OpenLDAP identity source: base DN ou=people,%s (users) / ou=groups,%s (groups); bind DN uid=%s,ou=people,%s; login attribute uid; membership attribute member.",
		env["LLDAP_BASE_DN"], env["LLDAP_BASE_DN"], bindUser, env["LLDAP_BASE_DN"])
	return nil
}

// setLLDAPPassword sets a user's password via the in-image lldap_set_password
// utility (OPAQUE), reachable at the container's own loopback HTTP port.
func setLLDAPPassword(ctx context.Context, rc *RunCtx, username, password string) error {
	cmp := rc.Compose("lldap")
	err := cmp.Exec(ctx, "lldap", "/app/lldap_set_password",
		"--base-url", "http://localhost:17170",
		"--admin-username", rc.Env["LLDAP_ADMIN_USER"],
		"--admin-password", rc.Env["LLDAP_ADMIN_PASSWORD"],
		"--username", username,
		"--password", password)
	if err != nil {
		return fmt.Errorf("set password for %s: %w", username, err)
	}
	return nil
}

// dcToDomain turns an LDAP base DN (dc=sddc,dc=lab) into a mail domain
// (sddc.lab). A DN with no dc components yields "lab.local".
func dcToDomain(baseDN string) string {
	var parts []string
	for _, rdn := range strings.Split(baseDN, ",") {
		rdn = strings.TrimSpace(rdn)
		if v, ok := strings.CutPrefix(strings.ToLower(rdn), "dc="); ok && v != "" {
			parts = append(parts, v)
		}
	}
	if len(parts) == 0 {
		return "lab.local"
	}
	return strings.Join(parts, ".")
}

func (l LLDAP) Remove(ctx context.Context, rc *RunCtx) error {
	cmp := rc.Compose("lldap")
	if err := cmp.Down(ctx); err != nil {
		return err
	}
	if err := os.RemoveAll(rc.Workdir("lldap")); err != nil {
		return err
	}
	rc.Log("Removed LLDAP containers and runtime files. Persistent data in %s was preserved.", rc.Env["LLDAP_DATA_DIR"])
	return nil
}
