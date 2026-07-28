package envfile

// requirement binds one variable to its validator and the services that need
// it. The pseudo-service "common" is required by every deploy. This table is
// the Go port of the bash require_*_vars / validate_* functions; it grows one
// block per service as deployers are ported.
type requirement struct {
	Name       string
	RequiredBy []string
	Checks     []func(string) error
}

var schema = []requirement{
	// Common host identity (require_common_vars).
	{"HOST_IP", []string{"common"}, []func(string) error{checkCIDR}},
	{"SEARCH_DOMAIN", []string{"common"}, []func(string) error{checkFQDN}},
	{"WORKDIR", []string{"common"}, []func(string) error{checkAbsPath}},

	// Allowed client networks (require_allow_net_vars).
	{"ALLOW_NET_1", []string{"chrony"}, []func(string) error{checkCIDR}},
	{"ALLOW_NET_2", []string{"chrony"}, []func(string) error{checkCIDR}},
	{"ALLOW_NET_3", []string{"chrony"}, []func(string) error{checkCIDR}},

	// Chrony (require_ntp_vars plus the containerized image/dir).
	{"CHRONY_SERVER_1", []string{"chrony"}, []func(string) error{checkFQDN}},
	{"CHRONY_SERVER_2", []string{"chrony"}, []func(string) error{checkFQDN}},
	{"CHRONY_SERVER_3", []string{"chrony"}, []func(string) error{checkFQDN}},
	{"CHRONY_IMAGE", []string{"chrony"}, []func(string) error{checkImage}},
	{"CHRONY_DIR", []string{"chrony"}, []func(string) error{checkAbsPath}},
	{"NTP_FQDN", []string{"chrony"}, []func(string) error{checkFQDN}},

	// rsyslog (require_rsyslog_vars plus the containerized image).
	{"SYSLOG_PORT", []string{"rsyslog"}, []func(string) error{checkPort}},
	{"SYSLOG_LOG_DIR", []string{"rsyslog"}, []func(string) error{checkAbsPath}},
	{"RSYSLOG_IMAGE", []string{"rsyslog"}, []func(string) error{checkImage}},

	// step-ca (require_ca_vars). Cross-field rules (paths nested correctly,
	// RO role differs from owner) live in the CA deployer.
	{"CA_FQDN", []string{"ca"}, []func(string) error{checkFQDN}},
	{"CA_PORT", []string{"ca"}, []func(string) error{checkPort}},
	{"CA_DATA_DIR", []string{"ca"}, []func(string) error{checkAbsPath}},
	{"CA_NAME", []string{"ca"}, nil},
	{"CA_PROVISIONER_NAME", []string{"ca"}, nil},
	{"SERVICE_CERT_DURATION", []string{"ca"}, []func(string) error{checkNotPlaceholder, checkHourDuration}},
	{"CA_ENABLE_ACME", []string{"ca"}, []func(string) error{checkBool}},
	{"CA_IMAGE", []string{"ca"}, []func(string) error{checkImage}},
	{"CA_POSTGRES_IMAGE", []string{"ca"}, []func(string) error{checkImage}},
	{"CA_POSTGRES_DB", []string{"ca"}, []func(string) error{checkPgIdentifier}},
	{"CA_POSTGRES_USER", []string{"ca"}, []func(string) error{checkPgIdentifier}},
	{"CA_POSTGRES_PASSWORD", []string{"ca"}, []func(string) error{checkNotPlaceholder, checkComposeSafe}},
	{"CA_POSTGRES_PORT", []string{"ca"}, []func(string) error{checkPort}},
	{"CA_POSTGRES_DATA_DIR", []string{"ca"}, []func(string) error{checkAbsPath}},
	{"CA_POSTGRES_RO_USER", []string{"ca"}, []func(string) error{checkPgIdentifier}},
	{"CA_POSTGRES_RO_PASSWORD", []string{"ca"}, []func(string) error{checkNotPlaceholder, checkComposeSafe}},

	// Microsoft-CA web-enrollment emulator (certsrv) for VCF. Fronts step-ca so
	// SDDC Manager can enroll via its Microsoft CA integration.
	{"VMSCA_ENABLE", []string{"msca"}, []func(string) error{checkBool}},
	{"VMSCA_FQDN", []string{"msca"}, []func(string) error{checkFQDN}},
	{"VMSCA_PORT", []string{"msca"}, []func(string) error{checkPort}},
	{"VMSCA_USERNAME", []string{"msca"}, nil},
	{"VMSCA_PASSWORD", []string{"msca"}, []func(string) error{checkNotPlaceholder, checkComposeSafe}},
	{"VMSCA_TEMPLATE", []string{"msca"}, nil},

	// Traefik reverse proxy (single :80/:443 ingress, wildcard termination).
	// Mandatory: every service template carries Traefik labels and only Traefik
	// terminates TLS, so there is no enable flag.
	{"TRAEFIK_IMAGE", []string{"traefik"}, []func(string) error{checkImage}},
	{"TRAEFIK_FQDN", []string{"traefik"}, []func(string) error{checkFQDN}},
	{"TRAEFIK_DIR", []string{"traefik"}, []func(string) error{checkAbsPath}},
	{"TRAEFIK_DASHBOARD_USER", []string{"traefik"}, nil},
	{"TRAEFIK_DASHBOARD_PASSWORD", []string{"traefik"}, []func(string) error{checkNotPlaceholder, checkComposeSafe}},

	// Technitium DNS (require_technitium_vars plus the admin password used to
	// rotate the first-boot credentials).
	{"DNS_FQDN", []string{"technitium"}, []func(string) error{checkFQDN}},
	{"DNS_FORWARDER", []string{"technitium"}, []func(string) error{checkIPv4}},
	// Also required by traefik: the file-provider router that fronts the console
	// when DHCP puts Technitium on the host network reads this port.
	{"TECHNITIUM_HTTP_PORT", []string{"technitium", "traefik"}, []func(string) error{checkLoopbackReadinessPort}},
	{"TECHNITIUM_HTTPS_PORT", []string{"technitium"}, []func(string) error{checkPort}},
	{"TECHNITIUM_DATA_DIR", []string{"technitium"}, []func(string) error{checkAbsPath}},
	{"TECHNITIUM_CERT_DIR", []string{"technitium"}, []func(string) error{checkAbsPath}},
	{"TECHNITIUM_IMAGE", []string{"technitium"}, []func(string) error{checkImage}},
	{"TECHNITIUM_ADMIN_PASSWORD", []string{"technitium"}, []func(string) error{checkNotPlaceholder, checkComposeSafe}},
	{"DNS_SYNC_SECRETS_DIR", []string{"technitium", "dns-sync"}, []func(string) error{checkAbsPath}},

	// DHCP, served by the Technitium container rather than a second service.
	// DHCP_ENABLE is required by traefik too: enabling it moves Technitium onto
	// the host network, and Traefik then reaches the admin console through its
	// file provider instead of container labels, so a toggle has to redeploy
	// both. Range sanity (start <= end, inside the router's subnet) is a
	// cross-field rule and lives in the technitium deployer.
	{"DHCP_ENABLE", []string{"technitium", "traefik"}, []func(string) error{checkBool}},
	{"DHCP_SCOPE_NAME", []string{"technitium"}, []func(string) error{checkNotPlaceholder}},
	{"DHCP_RANGE_START", []string{"technitium"}, []func(string) error{checkIPv4}},
	{"DHCP_RANGE_END", []string{"technitium"}, []func(string) error{checkIPv4}},
	{"DHCP_SUBNET_MASK", []string{"technitium"}, []func(string) error{checkIPv4}},
	{"DHCP_ROUTER", []string{"technitium"}, []func(string) error{checkIPv4}},
	{"DHCP_LEASE_DAYS", []string{"technitium"}, []func(string) error{checkPositiveInt}},

	// NetBox (require_netbox_vars). Only netbox-owned variables: the built-in
	// service seeding reads other services' FQDN/port vars but tolerates
	// whatever the example defines, so they are not hard requirements here.
	{"NETBOX_FQDN", []string{"netbox"}, []func(string) error{checkFQDN}},
	{"NETBOX_PORT", []string{"netbox"}, []func(string) error{checkLoopbackReadinessPort}},
	{"NETBOX_DIR", []string{"netbox"}, []func(string) error{checkAbsPath}},
	{"NETBOX_MEDIA_DIR", []string{"netbox"}, []func(string) error{checkAbsPath}},
	{"NETBOX_POSTGRES_DATA_DIR", []string{"netbox"}, []func(string) error{checkAbsPath}},
	{"NETBOX_REDIS_DATA_DIR", []string{"netbox"}, []func(string) error{checkAbsPath}},
	{"NETBOX_IMAGE", []string{"netbox"}, []func(string) error{checkImage}},
	{"NETBOX_POSTGRES_IMAGE", []string{"netbox"}, []func(string) error{checkImage}},
	{"NETBOX_REDIS_IMAGE", []string{"netbox"}, []func(string) error{checkImage}},
	{"NETBOX_NGINX_IMAGE", []string{"netbox"}, []func(string) error{checkImage}},
	{"NETBOX_POSTGRES_DB", []string{"netbox"}, []func(string) error{checkPgIdentifier}},
	{"NETBOX_POSTGRES_USER", []string{"netbox"}, []func(string) error{checkPgIdentifier}},
	{"NETBOX_POSTGRES_PASSWORD", []string{"netbox"}, []func(string) error{checkNotPlaceholder, checkComposeSafe}},
	{"NETBOX_REDIS_PASSWORD", []string{"netbox"}, []func(string) error{checkNotPlaceholder, checkComposeSafe}},
	{"NETBOX_SECRET_KEY", []string{"netbox"}, []func(string) error{checkNotPlaceholder, checkComposeSafe, checkMinLen(50)}},
	{"NETBOX_ALLOWED_HOSTS", []string{"netbox"}, nil},
	{"NETBOX_CSRF_TRUSTED_ORIGINS", []string{"netbox"}, nil},
	{"NETBOX_SUPERUSER_NAME", []string{"netbox"}, nil},
	{"NETBOX_SUPERUSER_EMAIL", []string{"netbox"}, []func(string) error{checkEmail}},
	{"NETBOX_SUPERUSER_PASSWORD", []string{"netbox"}, []func(string) error{checkNotPlaceholder, checkComposeSafe}},

	// dns-sync (require_dns_sync_vars). Interval and URL shapes are checked
	// in the deployer.
	{"LABPROVIDER_FQDN", []string{"dns-sync"}, []func(string) error{checkFQDN}},
	{"DNS_SYNC_IMAGE", []string{"dns-sync"}, []func(string) error{checkImage}},
	{"DNS_SYNC_DIR", []string{"dns-sync"}, []func(string) error{checkAbsPath}},
	{"DNS_SYNC_NETBOX_URL", []string{"dns-sync"}, nil},
	{"DNS_SYNC_TECHNITIUM_URL", []string{"dns-sync"}, nil},
	{"DNS_SYNC_INTERVAL", []string{"dns-sync"}, nil},

	// VCF offline depot (require_depot_vars).
	{"DEPOT_FQDN", []string{"depot"}, []func(string) error{checkFQDN}},
	{"DEPOT_HTTP_PORT", []string{"depot"}, []func(string) error{checkLoopbackReadinessPort}},
	{"DEPOT_DIR", []string{"depot"}, []func(string) error{checkAbsPath}},
	{"DEPOT_DATA_DIR", []string{"depot"}, []func(string) error{checkAbsPath}},
	{"DEPOT_AUTH_DIR", []string{"depot"}, []func(string) error{checkAbsPath}},
	{"DEPOT_BASIC_AUTH_USER", []string{"depot"}, nil},
	{"DEPOT_BASIC_AUTH_PASSWORD", []string{"depot"}, []func(string) error{checkNotPlaceholder, checkComposeSafe}},
	{"DEPOT_IMAGE", []string{"depot"}, []func(string) error{checkImage}},

	// Keycloak (require_keycloak_vars).
	{"KEYCLOAK_DIR", []string{"keycloak"}, []func(string) error{checkAbsPath}},
	{"KEYCLOAK_FQDN", []string{"keycloak"}, []func(string) error{checkFQDN}},
	{"KEYCLOAK_PORT", []string{"keycloak"}, []func(string) error{checkLoopbackReadinessPort}},
	{"KEYCLOAK_IMAGE", []string{"keycloak"}, []func(string) error{checkImage}},
	{"KEYCLOAK_ADMIN_USER", []string{"keycloak"}, nil},
	{"KEYCLOAK_ADMIN_PASSWORD", []string{"keycloak"}, []func(string) error{checkNotPlaceholder, checkComposeSafe}},
	{"KEYCLOAK_BOOTSTRAP_REALM_NAME", []string{"keycloak"}, []func(string) error{checkNotPlaceholder}},
	{"KEYCLOAK_BOOTSTRAP_GROUP_NAME", []string{"keycloak"}, []func(string) error{checkNotPlaceholder}},
	{"KEYCLOAK_BOOTSTRAP_CLIENT_ID", []string{"keycloak"}, []func(string) error{checkNotPlaceholder}},
	{"KEYCLOAK_BOOTSTRAP_CLIENT_SECRET", []string{"keycloak"}, []func(string) error{checkNotPlaceholder, checkComposeSafe}},
	{"KEYCLOAK_BOOTSTRAP_CLIENT_REDIRECT_URIS", []string{"keycloak"}, nil},

	// Authentik (require_authentik_vars).
	{"AUTHENTIK_DIR", []string{"authentik"}, []func(string) error{checkAbsPath}},
	{"AUTHENTIK_FQDN", []string{"authentik"}, []func(string) error{checkFQDN}},
	{"AUTHENTIK_PORT", []string{"authentik"}, []func(string) error{checkLoopbackReadinessPort}},
	{"AUTHENTIK_IMAGE", []string{"authentik"}, []func(string) error{checkImage}},
	{"AUTHENTIK_POSTGRES_IMAGE", []string{"authentik"}, []func(string) error{checkImage}},
	{"AUTHENTIK_ADMIN_PASSWORD", []string{"authentik"}, []func(string) error{checkNotPlaceholder, checkComposeSafe}},
	{"AUTHENTIK_API_TOKEN", []string{"authentik"}, []func(string) error{checkNotPlaceholder, checkComposeSafe}},
	{"AUTHENTIK_SECRET_KEY", []string{"authentik"}, []func(string) error{checkNotPlaceholder, checkComposeSafe, checkMinLen(50)}},
	{"AUTHENTIK_PG_DB", []string{"authentik"}, []func(string) error{checkPgIdentifier}},
	{"AUTHENTIK_PG_USER", []string{"authentik"}, []func(string) error{checkPgIdentifier}},
	{"AUTHENTIK_PG_PASSWORD", []string{"authentik"}, []func(string) error{checkNotPlaceholder, checkComposeSafe}},
	{"AUTHENTIK_BOOTSTRAP_CLIENT_ID", []string{"authentik"}, []func(string) error{checkNotPlaceholder}},
	{"AUTHENTIK_BOOTSTRAP_CLIENT_SECRET", []string{"authentik"}, []func(string) error{checkNotPlaceholder, checkComposeSafe}},
	{"AUTHENTIK_BOOTSTRAP_GROUP_NAME", []string{"authentik"}, []func(string) error{checkNotPlaceholder}},
	{"AUTHENTIK_BOOTSTRAP_USERNAME", []string{"authentik"}, []func(string) error{checkNotPlaceholder}},
	{"AUTHENTIK_BOOTSTRAP_USER_PASSWORD", []string{"authentik"}, []func(string) error{checkNotPlaceholder, checkComposeSafe}},
	{"AUTHENTIK_BOOTSTRAP_USER_EMAIL_DOMAIN", []string{"authentik"}, []func(string) error{checkFQDN}},
	{"AUTHENTIK_BOOTSTRAP_CLIENT_REDIRECT_URIS", []string{"authentik"}, nil},

	// Zitadel (require_zitadel_vars).
	{"ZITADEL_DIR", []string{"zitadel"}, []func(string) error{checkAbsPath}},
	{"ZITADEL_FQDN", []string{"zitadel"}, []func(string) error{checkFQDN}},
	{"ZITADEL_PORT", []string{"zitadel"}, []func(string) error{checkPort}},
	{"ZITADEL_IMAGE", []string{"zitadel"}, []func(string) error{checkImage}},
	{"ZITADEL_LOGIN_IMAGE", []string{"zitadel"}, []func(string) error{checkImage}},
	{"ZITADEL_NGINX_IMAGE", []string{"zitadel"}, []func(string) error{checkImage}},
	{"ZITADEL_POSTGRES_IMAGE", []string{"zitadel"}, []func(string) error{checkImage}},
	{"ZITADEL_MASTERKEY", []string{"zitadel"}, []func(string) error{checkNotPlaceholder, checkComposeSafe, checkExactLen(32)}},
	{"ZITADEL_ADMIN_USERNAME", []string{"zitadel"}, []func(string) error{checkNotPlaceholder}},
	{"ZITADEL_ADMIN_PASSWORD", []string{"zitadel"}, []func(string) error{checkNotPlaceholder, checkComposeSafe}},
	{"ZITADEL_PG_DB", []string{"zitadel"}, []func(string) error{checkPgIdentifier}},
	{"ZITADEL_PG_USER", []string{"zitadel"}, []func(string) error{checkPgIdentifier}},
	{"ZITADEL_PG_PASSWORD", []string{"zitadel"}, []func(string) error{checkNotPlaceholder, checkComposeSafe}},
	{"ZITADEL_BOOTSTRAP_CLIENT_ID", []string{"zitadel"}, []func(string) error{checkNotPlaceholder}},
	{"ZITADEL_BOOTSTRAP_CLIENT_SECRET", []string{"zitadel"}, []func(string) error{checkNotPlaceholder, checkComposeSafe}},
	{"ZITADEL_BOOTSTRAP_GROUP_NAME", []string{"zitadel"}, []func(string) error{checkNotPlaceholder}},
	{"ZITADEL_BOOTSTRAP_USERNAME", []string{"zitadel"}, []func(string) error{checkNotPlaceholder}},
	{"ZITADEL_BOOTSTRAP_USER_PASSWORD", []string{"zitadel"}, []func(string) error{checkNotPlaceholder, checkComposeSafe}},
	{"ZITADEL_BOOTSTRAP_USER_EMAIL_DOMAIN", []string{"zitadel"}, []func(string) error{checkFQDN}},
	{"ZITADEL_BOOTSTRAP_CLIENT_REDIRECT_URIS", []string{"zitadel"}, nil},

	// SFTPGo (require_sftp_vars). The optional SFTP_BACKUP_* trio is
	// all-or-nothing, checked in the deployer.
	{"SFTP_FQDN", []string{"sftp"}, []func(string) error{checkFQDN}},
	{"SFTP_PORT", []string{"sftp"}, []func(string) error{checkPort}},
	{"SFTP_ADMIN_PORT", []string{"sftp"}, []func(string) error{checkLoopbackReadinessPort}},
	{"SFTP_ADMIN_USER", []string{"sftp"}, nil},
	{"SFTP_ADMIN_PASSWORD", []string{"sftp"}, []func(string) error{checkNotPlaceholder, checkComposeSafe}},
	{"SFTP_DATA_DIR", []string{"sftp"}, []func(string) error{checkAbsPath}},
	{"SFTP_HOME_DIR", []string{"sftp"}, []func(string) error{checkAbsPath}},
	{"SFTPGO_IMAGE", []string{"sftp"}, []func(string) error{checkImage}},

	// SeaweedFS S3 (require_s3_vars).
	{"S3_FQDN", []string{"s3"}, []func(string) error{checkFQDN}},
	{"S3_PORT", []string{"s3"}, []func(string) error{checkPort}},
	{"S3_ACCESS_KEY", []string{"s3"}, []func(string) error{checkNotPlaceholder, checkComposeSafe}},
	{"S3_SECRET_KEY", []string{"s3"}, []func(string) error{checkNotPlaceholder, checkComposeSafe}},
	{"S3_DATA_DIR", []string{"s3"}, []func(string) error{checkAbsPath}},
	{"S3_IMAGE", []string{"s3"}, []func(string) error{checkImage}},

	// Mailpit SMTP sink.
	{"MAILPIT_IMAGE", []string{"mailpit"}, []func(string) error{checkImage}},
	{"MAILPIT_FQDN", []string{"mailpit"}, []func(string) error{checkFQDN}},
	{"MAILPIT_SMTP_PORT", []string{"mailpit"}, []func(string) error{checkPort}},
	{"MAILPIT_UI_PORT", []string{"mailpit"}, []func(string) error{checkLoopbackReadinessPort}},
	{"MAILPIT_DATA_DIR", []string{"mailpit"}, []func(string) error{checkAbsPath}},

	// LLDAP directory. LDAPS is terminated with a step-ca leaf, so lldap needs
	// the CA; the base DN shape is free-form (dc=...).
	{"LLDAP_IMAGE", []string{"lldap"}, []func(string) error{checkImage}},
	{"LLDAP_FQDN", []string{"lldap"}, []func(string) error{checkFQDN}},
	{"LLDAP_LDAP_PORT", []string{"lldap"}, []func(string) error{checkPort}},
	{"LLDAP_LDAPS_PORT", []string{"lldap"}, []func(string) error{checkPort}},
	{"LLDAP_UI_PORT", []string{"lldap"}, []func(string) error{checkLoopbackReadinessPort}},
	{"LLDAP_DATA_DIR", []string{"lldap"}, []func(string) error{checkAbsPath}},
	{"LLDAP_CERT_DIR", []string{"lldap"}, []func(string) error{checkAbsPath}},
	{"LLDAP_BASE_DN", []string{"lldap"}, nil},
	{"LLDAP_ADMIN_USER", []string{"lldap"}, nil},
	{"LLDAP_ADMIN_PASSWORD", []string{"lldap"}, []func(string) error{checkNotPlaceholder, checkComposeSafe}},
	{"LLDAP_JWT_SECRET", []string{"lldap"}, []func(string) error{checkNotPlaceholder, checkComposeSafe}},
	{"LLDAP_KEY_SEED", []string{"lldap"}, []func(string) error{checkNotPlaceholder, checkComposeSafe}},
	{"LLDAP_VCENTER_BIND_USER", []string{"lldap"}, nil},
	{"LLDAP_VCENTER_BIND_PASSWORD", []string{"lldap"}, []func(string) error{checkNotPlaceholder, checkComposeSafe}},
	{"LLDAP_VCENTER_ADMIN_USER", []string{"lldap"}, nil},
	{"LLDAP_VCENTER_ADMIN_PASSWORD", []string{"lldap"}, []func(string) error{checkNotPlaceholder, checkComposeSafe}},

	// PyKMIP key server for the VCF encryption workflows (image built locally,
	// like chrony/rsyslog). KMIP_PORT stays on all interfaces: vCenter connects
	// to it directly, and the protocol is TLS with client-certificate auth.
	{"KMIP_IMAGE", []string{"kmip"}, []func(string) error{checkImage}},
	{"KMIP_PYKMIP_VERSION", []string{"kmip"}, []func(string) error{checkNotPlaceholder}},
	{"KMIP_FQDN", []string{"kmip"}, []func(string) error{checkFQDN}},
	{"KMIP_PORT", []string{"kmip"}, []func(string) error{checkPort}},
	{"KMIP_DIR", []string{"kmip"}, []func(string) error{checkAbsPath}},
	{"KMIP_DATA_DIR", []string{"kmip"}, []func(string) error{checkAbsPath}},

	// Harbor container registry. HARBOR_HTTP_PORT is the cleartext port the
	// generated compose file publishes; the deployer rewrites it to the
	// loopback, so it is a readiness port like the other web services'.
	// HARBOR_VERSION must match HARBOR_PREPARE_IMAGE's tag: prepare validates
	// harbor.yml's _version against itself.
	{"HARBOR_FQDN", []string{"harbor"}, []func(string) error{checkFQDN}},
	{"HARBOR_VERSION", []string{"harbor"}, []func(string) error{checkNotPlaceholder}},
	{"HARBOR_PREPARE_IMAGE", []string{"harbor"}, []func(string) error{checkImage}},
	{"HARBOR_HTTP_PORT", []string{"harbor"}, []func(string) error{checkLoopbackReadinessPort}},
	{"HARBOR_DATA_DIR", []string{"harbor"}, []func(string) error{checkAbsPath}},
	{"HARBOR_ADMIN_PASSWORD", []string{"harbor"}, []func(string) error{checkNotPlaceholder, checkComposeSafe}},
	{"HARBOR_DB_PASSWORD", []string{"harbor"}, []func(string) error{checkNotPlaceholder, checkComposeSafe}},
	{"HARBOR_TRIVY_ENABLE", []string{"harbor"}, []func(string) error{checkBool}},

	// Samba SMB/CIFS share (image built locally, like chrony/rsyslog).
	{"SAMBA_IMAGE", []string{"samba"}, []func(string) error{checkImage}},
	{"SAMBA_FQDN", []string{"samba"}, []func(string) error{checkFQDN}},
	{"SAMBA_PORT", []string{"samba"}, []func(string) error{checkPort}},
	{"SAMBA_SHARE_NAME", []string{"samba"}, nil},
	{"SAMBA_SHARE_DIR", []string{"samba"}, []func(string) error{checkAbsPath}},
	{"SAMBA_USER", []string{"samba"}, nil},
	{"SAMBA_PASSWORD", []string{"samba"}, []func(string) error{checkNotPlaceholder, checkComposeSafe}},
}

// Validate checks every variable required by the selected services (plus
// "common") and returns all findings at once, unlike bash's fail-fast, so the
// wizard can annotate the whole file in one pass.
func Validate(vars map[string]string, services []string) []Issue {
	want := map[string]bool{"common": true}
	for _, s := range services {
		want[s] = true
	}

	var issues []Issue
	for _, req := range schema {
		needed := false
		for _, svc := range req.RequiredBy {
			if want[svc] {
				needed = true
				break
			}
		}
		if !needed {
			continue
		}
		v, ok := vars[req.Name]
		if !ok {
			issues = append(issues, Issue{req.Name, "missing required variable"})
			continue
		}
		if v == "" {
			// Present but empty is how an operator asks for a generated value;
			// the deploy engine materializes it (see MaterializeSecrets). For
			// anything else it is still a missing value.
			if !IsGenerated(req.Name) {
				issues = append(issues, Issue{req.Name, "missing required variable"})
			}
			continue
		}
		for _, check := range req.Checks {
			if err := check(v); err != nil {
				issues = append(issues, Issue{req.Name, err.Error()})
			}
		}
	}
	return issues
}

// SchemaServices lists every service the table declares variables for, in
// table order, excluding the "common" pseudo-service.
func SchemaServices() []string {
	seen := map[string]bool{}
	var all []string
	for _, req := range schema {
		for _, svc := range req.RequiredBy {
			if svc != "common" && !seen[svc] {
				seen[svc] = true
				all = append(all, svc)
			}
		}
	}
	return all
}

// ValidateAll validates against every service in the schema; the wizard uses
// this so an uploaded config is checked completely, not per-selection.
func ValidateAll(vars map[string]string) []Issue {
	return Validate(vars, SchemaServices())
}
