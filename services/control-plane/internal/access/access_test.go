package access

import (
	"io"
	"log/slog"
	"testing"

	"github.com/dsjodin/labprovider/services/control-plane/internal/deploy"
	"github.com/dsjodin/labprovider/services/control-plane/internal/envfile"
)

func TestBuildSkipsUnconfigured(t *testing.T) {
	env := map[string]string{
		"DNS_FQDN":                  "dns.sddc.lab",
		"TECHNITIUM_ADMIN_PASSWORD": "s3cret",
		"AUTHENTIK_FQDN":            "idp.sddc.lab",
		"AUTHENTIK_ADMIN_PASSWORD":  "akpass",
		// NetBox has no FQDN: must be skipped.
		"NETBOX_SUPERUSER_NAME": "admin",
	}
	got := Build(env)
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d: %+v", len(got), got)
	}
	if got[0].Name != "Technitium DNS" || got[0].URL != "https://dns.sddc.lab" ||
		got[0].Username != "admin" || got[0].Password != "s3cret" {
		t.Errorf("technitium entry wrong: %+v", got[0])
	}
	if got[1].Name != "Authentik" || got[1].Username != "akadmin" || got[1].Password != "akpass" {
		t.Errorf("authentik entry wrong: %+v", got[1])
	}
}

func TestBuildLLDAPAndMailpit(t *testing.T) {
	env := map[string]string{
		"LLDAP_FQDN":           "ldap.sddc.lab",
		"LLDAP_ADMIN_USER":     "admin",
		"LLDAP_ADMIN_PASSWORD": "lldappass",
		"MAILPIT_FQDN":         "mail.sddc.lab",
	}
	got := Build(env)
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d: %+v", len(got), got)
	}
	if got[0].Name != "LLDAP" || got[0].URL != "https://ldap.sddc.lab" ||
		got[0].Username != "admin" || got[0].Password != "lldappass" {
		t.Errorf("lldap entry wrong: %+v", got[0])
	}
	if got[1].Name != "Mailpit" || got[1].URL != "https://mail.sddc.lab" ||
		got[1].Username != "" || got[1].Password != "" || got[1].Note != "No authentication" {
		t.Errorf("mailpit entry wrong: %+v", got[1])
	}
}

func TestLLDAPCarriesVCFDirectoryValues(t *testing.T) {
	env := map[string]string{
		"LLDAP_FQDN":                  "ldap.sddc.lab",
		"LLDAP_ADMIN_USER":            "admin",
		"LLDAP_ADMIN_PASSWORD":        "lldappass",
		"LLDAP_BASE_DN":               "dc=sddc,dc=lab",
		"LLDAP_LDAP_PORT":             "3890",
		"LLDAP_LDAPS_PORT":            "6360",
		"LLDAP_VCENTER_BIND_USER":     "svc-vcenter",
		"LLDAP_VCENTER_BIND_PASSWORD": "bindpass",
	}
	got := Build(env)
	if len(got) != 1 {
		t.Fatalf("want 1 entry, got %d", len(got))
	}
	want := map[string]string{
		"Server URL (LDAPS)":   "ldaps://ldap.sddc.lab:6360",
		"Server URL (LDAP)":    "ldap://ldap.sddc.lab:3890",
		"Base DN for users":    "ou=people,dc=sddc,dc=lab",
		"Base DN for groups":   "ou=groups,dc=sddc,dc=lab",
		"Bind DN":              "uid=svc-vcenter,ou=people,dc=sddc,dc=lab",
		"Bind password":        "bindpass",
		"Login attribute":      "uid",
		"Membership attribute": "member",
	}
	have := map[string]string{}
	for _, d := range got[0].Details {
		have[d.Label] = d.Value
	}
	for label, value := range want {
		if have[label] != value {
			t.Errorf("%s = %q, want %q", label, have[label], value)
		}
	}
}

// Without a base DN there is nothing to derive, and half a set of DNs is worse
// than none.
func TestLLDAPWithoutBaseDNHasNoDetails(t *testing.T) {
	got := Build(map[string]string{"LLDAP_FQDN": "ldap.sddc.lab"})
	if len(got) != 1 || got[0].Details != nil {
		t.Fatalf("want one entry with no details, got %+v", got)
	}
}

func TestBuildIncludesTraefik(t *testing.T) {
	env := map[string]string{
		"TRAEFIK_FQDN":               "traefik.sddc.lab",
		"TRAEFIK_DASHBOARD_USER":     "admin",
		"TRAEFIK_DASHBOARD_PASSWORD": "tpass",
	}
	got := Build(env)
	if len(got) != 1 || got[0].Name != "Traefik" || got[0].URL != "https://traefik.sddc.lab" {
		t.Fatalf("want Traefik entry, got %+v", got)
	}
}

// Every Access panel entry names a registry service, and a panel entry for a
// service nobody deploys would render credentials for a thing that cannot
// exist. The reverse is not checked: ca, chrony, rsyslog, dns-sync, samba, and
// kmip deliberately have no entry, because they have no web login to hand the
// operator.
func TestAccessEntriesNameRegisteredServices(t *testing.T) {
	engine := deploy.NewEngine(envfile.Store{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	deploy.RegisterAll(engine)
	registered := map[string]bool{}
	for _, svc := range engine.Services() {
		registered[svc.Name()] = true
	}
	for _, s := range services {
		if !registered[s.service] {
			t.Errorf("Access panel entry %q names service %q, which no deployer registers", s.name, s.service)
		}
	}
}
