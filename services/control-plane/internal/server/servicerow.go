package server

import (
	"github.com/dsjodin/labprovider/services/control-plane/internal/deploy"
	"github.com/dsjodin/labprovider/services/control-plane/internal/docker"
	"github.com/dsjodin/labprovider/services/control-plane/internal/envfile"
)

// serviceMeta maps a registry service to the config keys that describe it: the
// name it answers on and the directory its data lives in. web marks the ones
// Traefik fronts over HTTPS, which are the only ones with a URL worth clicking;
// the rest (NTP, syslog, SMB, KMIP, LDAP wire protocols) get their FQDN shown
// as plain text rather than a link that would not work.
var serviceMeta = map[string]struct {
	fqdnKey string
	dirKey  string
	web     bool
}{
	"ca":         {"CA_FQDN", "CA_DATA_DIR", true},
	"technitium": {"DNS_FQDN", "TECHNITIUM_DATA_DIR", true},
	"traefik":    {"TRAEFIK_FQDN", "TRAEFIK_DIR", true},
	"netbox":     {"NETBOX_FQDN", "NETBOX_DIR", true},
	"dns-sync":   {"", "DNS_SYNC_DIR", false},
	"chrony":     {"NTP_FQDN", "CHRONY_DIR", false},
	"rsyslog":    {"SYSLOG_FQDN", "SYSLOG_LOG_DIR", false},
	"depot":      {"DEPOT_FQDN", "DEPOT_DATA_DIR", true},
	"keycloak":   {"KEYCLOAK_FQDN", "KEYCLOAK_DIR", true},
	"authentik":  {"AUTHENTIK_FQDN", "AUTHENTIK_DIR", true},
	"zitadel":    {"ZITADEL_FQDN", "ZITADEL_DIR", true},
	"s3":         {"S3_FQDN", "S3_DATA_DIR", true},
	"sftp":       {"SFTP_FQDN", "SFTP_DATA_DIR", true},
	"mailpit":    {"MAILPIT_FQDN", "MAILPIT_DATA_DIR", true},
	"lldap":      {"LLDAP_FQDN", "LLDAP_DATA_DIR", true},
	"samba":      {"SAMBA_FQDN", "SAMBA_SHARE_DIR", false},
	"kmip":       {"KMIP_FQDN", "KMIP_DATA_DIR", false},
	"harbor":     {"HARBOR_FQDN", "HARBOR_DATA_DIR", true},
}

// Service row states, in the order the operator cares about them.
const (
	stateRunning     = "running"
	stateDegraded    = "degraded"
	stateStopped     = "stopped"
	stateNotDeployed = "not deployed"
)

// serviceRows joins what the page already collects - the deploy registry, the
// recorded deploy history, the managed config, and the live container list -
// into one row per labprovider service. The operator thinks in services;
// Docker's own view (one row per container, with a name like
// labprovider-netbox-postgres-1) is the detail underneath, not the top level.
//
// containers must be the unfiltered listing: CONTROL_PLANE_CONTAINER_FILTERS is
// operator-editable, and a service missing from it would otherwise read as
// stopped when it is running.
func (s *Server) serviceRows(containers []docker.Container) []ServiceRow {
	if s.opt.Engine == nil {
		return nil
	}

	byProject := map[string][]docker.Container{}
	for _, c := range containers {
		if c.Project != "" {
			byProject[c.Project] = append(byProject[c.Project], c)
		}
	}

	var state deploy.State
	if s.opt.Engine.State != nil {
		state = s.opt.Engine.State.Snapshot()
	}
	var env map[string]string
	if content, saved, err := s.opt.Engine.Store.Load(); err == nil && saved {
		env = envfile.Parse(content)
	}

	rows := make([]ServiceRow, 0, len(s.opt.Engine.Services()))
	for _, svc := range s.opt.Engine.Services() {
		name := svc.Name()
		deployed := false
		row := ServiceRow{
			Name:       name,
			Core:       isFoundation(name),
			Containers: byProject[projectOf(name)],
		}
		if st, ok := state.Services[name]; ok {
			row.LastAction = st.LastAction
			row.LastResult = st.Result
			row.LastAt = st.At.Format("2006-01-02 15:04")
			deployed = st.LastAction == "deploy" && st.Result == "ok"
		}
		row.State = rowState(row.Containers, deployed)
		if meta, ok := serviceMeta[name]; ok {
			row.FQDN = env[meta.fqdnKey]
			row.DataDir = env[meta.dirKey]
			if meta.web && row.FQDN != "" {
				row.URL = "https://" + row.FQDN
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// rowState reduces a service's containers to one word. "stopped" and "not
// deployed" are deliberately distinct: the first says the operator has a lab to
// restart, the second that there is nothing there yet.
func rowState(containers []docker.Container, deployed bool) string {
	if len(containers) == 0 {
		if deployed {
			return stateStopped
		}
		return stateNotDeployed
	}
	running, unhealthy := 0, false
	for _, c := range containers {
		if c.State == "running" {
			running++
		}
		if c.Health == "unhealthy" {
			unhealthy = true
		}
	}
	switch {
	case running == 0:
		return stateStopped
	case running < len(containers) || unhealthy:
		return stateDegraded
	default:
		return stateRunning
	}
}

// unmanagedContainers returns the displayed containers that belong to no
// registry service, so a stray container is visible rather than silently
// dropped by the service-centric view.
func (s *Server) unmanagedContainers(containers []docker.Container, rows []ServiceRow) []docker.Container {
	claimed := map[string]bool{}
	for _, row := range rows {
		for _, c := range row.Containers {
			claimed[c.ID] = true
		}
	}
	var out []docker.Container
	for _, c := range containers {
		if !claimed[c.ID] {
			out = append(out, c)
		}
	}
	return out
}
