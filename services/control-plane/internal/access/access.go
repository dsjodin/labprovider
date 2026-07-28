// Package access builds the list of deployed web UIs and their lab credentials
// from the managed config, for the dashboard's Access panel. This is a
// lab-only convenience: passwords come straight from labprovider.env in
// cleartext so an operator can find and log into each service quickly.
package access

// Entry is one service's address and login, as shown on the dashboard.
// Details carries the extra values an appliance's setup wizard asks for, so an
// operator copies them instead of deriving them.
type Entry struct {
	Name     string   `json:"name"`
	Service  string   `json:"service"` // registry service name, for /service/{name}
	URL      string   `json:"url"`
	Username string   `json:"username"`
	Password string   `json:"password"`
	Note     string   `json:"note,omitempty"`
	Details  []Detail `json:"details,omitempty"`
}

// Detail is one labelled value in an entry's expanded view.
type Detail struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// svc maps a service to the env keys its Access entry is built from. userLit is
// used when the service has a fixed admin login not stored in the config;
// otherwise userKey names the username variable. details, when set, derives the
// entry's extra rows from the config.
type svc struct {
	name    string
	service string // registry service name; the display name is not it
	fqdnKey string
	userKey string
	userLit string
	passKey string
	note    string
	details func(env map[string]string) []Detail
}

// Traefik fronts every service at https://<FQDN>; the entries are ordered the
// way an operator is likely to reach for them.
var services = []svc{
	{name: "Technitium DNS", service: "technitium", fqdnKey: "DNS_FQDN", userLit: "admin", passKey: "TECHNITIUM_ADMIN_PASSWORD"},
	{name: "NetBox", service: "netbox", fqdnKey: "NETBOX_FQDN", userKey: "NETBOX_SUPERUSER_NAME", passKey: "NETBOX_SUPERUSER_PASSWORD"},
	{name: "Keycloak", service: "keycloak", fqdnKey: "KEYCLOAK_FQDN", userKey: "KEYCLOAK_ADMIN_USER", passKey: "KEYCLOAK_ADMIN_PASSWORD", note: "Admin console at /admin/"},
	{name: "Authentik", service: "authentik", fqdnKey: "AUTHENTIK_FQDN", userLit: "akadmin", passKey: "AUTHENTIK_ADMIN_PASSWORD"},
	{name: "Zitadel", service: "zitadel", fqdnKey: "ZITADEL_FQDN", userKey: "ZITADEL_ADMIN_USERNAME", passKey: "ZITADEL_ADMIN_PASSWORD"},
	{name: "LLDAP", service: "lldap", fqdnKey: "LLDAP_FQDN", userKey: "LLDAP_ADMIN_USER", passKey: "LLDAP_ADMIN_PASSWORD", note: "Directory admin UI", details: vcfDirectoryValues},
	{name: "SFTPGo", service: "sftp", fqdnKey: "SFTP_FQDN", userKey: "SFTP_ADMIN_USER", passKey: "SFTP_ADMIN_PASSWORD"},
	{name: "SeaweedFS S3", service: "s3", fqdnKey: "S3_FQDN", userKey: "S3_ACCESS_KEY", passKey: "S3_SECRET_KEY", note: "S3 API (access key / secret key)"},
	{name: "VCF Depot", service: "depot", fqdnKey: "DEPOT_FQDN", userKey: "DEPOT_BASIC_AUTH_USER", passKey: "DEPOT_BASIC_AUTH_PASSWORD"},
	{name: "Harbor", service: "harbor", fqdnKey: "HARBOR_FQDN", userLit: "admin", passKey: "HARBOR_ADMIN_PASSWORD", note: "Registry UI; docker login needs the step-ca root trusted"},
	{name: "Mailpit", service: "mailpit", fqdnKey: "MAILPIT_FQDN", note: "No authentication"},
	{name: "Traefik", service: "traefik", fqdnKey: "TRAEFIK_FQDN", userKey: "TRAEFIK_DASHBOARD_USER", passKey: "TRAEFIK_DASHBOARD_PASSWORD", note: "Dashboard"},
}

// vcfDirectoryValues are the exact fields the vCenter/VCF SSO wizard asks for
// when adding an OpenLDAP identity source. The lldap deployer pre-provisions
// ou=people, ou=groups, and the bind account, so these are derivable - but
// deriving them by hand is the step in VCF integration most likely to be typed
// wrong, and a wrong DN fails as "cannot connect" with no detail.
func vcfDirectoryValues(env map[string]string) []Detail {
	base := env["LLDAP_BASE_DN"]
	if base == "" {
		return nil
	}
	var details []Detail
	if port := env["LLDAP_LDAPS_PORT"]; port != "" {
		details = append(details, Detail{"Server URL (LDAPS)", "ldaps://" + env["LLDAP_FQDN"] + ":" + port})
	}
	if port := env["LLDAP_LDAP_PORT"]; port != "" {
		details = append(details, Detail{"Server URL (LDAP)", "ldap://" + env["LLDAP_FQDN"] + ":" + port})
	}
	details = append(details,
		Detail{"Base DN for users", "ou=people," + base},
		Detail{"Base DN for groups", "ou=groups," + base},
	)
	if bind := env["LLDAP_VCENTER_BIND_USER"]; bind != "" {
		details = append(details,
			Detail{"Bind DN", "uid=" + bind + ",ou=people," + base},
			Detail{"Bind password", env["LLDAP_VCENTER_BIND_PASSWORD"]},
		)
	}
	details = append(details,
		Detail{"Login attribute", "uid"},
		Detail{"Membership attribute", "member"},
		Detail{"Seeded groups", "vsphere-admins, vsphere-readonly"},
	)
	return details
}

// Build returns an Access entry for every service whose FQDN is configured. A
// service with no FQDN (not part of this deployment) is skipped.
func Build(env map[string]string) []Entry {
	var out []Entry
	for _, s := range services {
		fqdn := env[s.fqdnKey]
		if fqdn == "" {
			continue
		}
		user := s.userLit
		if s.userKey != "" {
			user = env[s.userKey]
		}
		entry := Entry{
			Name:     s.name,
			Service:  s.service,
			URL:      "https://" + fqdn,
			Username: user,
			Password: env[s.passKey],
			Note:     s.note,
		}
		if s.details != nil {
			entry.Details = s.details(env)
		}
		out = append(out, entry)
	}
	return out
}
