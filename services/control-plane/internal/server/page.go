package server

import (
	"fmt"
	"html/template"
	"strings"

	"github.com/dsjodin/labprovider/services/control-plane/internal/access"
	"github.com/dsjodin/labprovider/services/control-plane/internal/certs"
	"github.com/dsjodin/labprovider/services/control-plane/internal/disk"
	"github.com/dsjodin/labprovider/services/control-plane/internal/dns"
	"github.com/dsjodin/labprovider/services/control-plane/internal/docker"
	"github.com/dsjodin/labprovider/services/control-plane/internal/ipam"
	"github.com/dsjodin/labprovider/services/control-plane/internal/logs"
)

// Status is a panel's health for both the template and the JSON API.
type Status struct {
	State string `json:"state"` // "ok" | "unavailable" | "disabled"
	Error string `json:"error,omitempty"`
}

func (s Status) OK() bool          { return s.State == "ok" }
func (s Status) Unavailable() bool { return s.State == "unavailable" }
func (s Status) Disabled() bool    { return s.State == "disabled" }

func ok() Status                 { return Status{State: "ok"} }
func unavailable(e error) Status { return Status{State: "unavailable", Error: e.Error()} }
func disabled(msg string) Status { return Status{State: "disabled", Error: msg} }

type Page struct {
	// Chrome drives templates/layout.html. Its fields are all json:"-", so
	// /api/state stays exactly the JSON mirror of the data panels it has
	// always been.
	Chrome

	FQDN        string        `json:"fqdn"`
	GeneratedAt string        `json:"generated_at"`
	Access      AccessPanel   `json:"access"`
	Certs       CertsPanel    `json:"certificates"`
	DNS         DNSPanel      `json:"dns"`
	IPAM        IPAMPanel     `json:"ipam"`
	Disk        DiskPanel     `json:"disk"`
	Services    ServicesPanel `json:"services"`
	Errors      ErrorsPanel   `json:"recent_errors"`
}

// AccessPanel lists deployed web UIs with their lab credentials. CAReady is
// true when the step-ca root certificate is on disk and downloadable.
type AccessPanel struct {
	Status  Status         `json:"status"`
	Entries []access.Entry `json:"entries"`
	CAReady bool           `json:"ca_ready"`
}

type CertsPanel struct {
	Status  Status        `json:"status"`
	Summary certs.Summary `json:"summary"`
}

type DNSPanel struct {
	Status   Status       `json:"status"`
	Overview dns.Overview `json:"overview"`
}

type IPAMPanel struct {
	Status   Status        `json:"status"`
	Overview ipam.Overview `json:"overview"`
}

type DiskPanel struct {
	Status   Status        `json:"status"`
	Overview disk.Overview `json:"overview"`
}

// MaxDirBytes is the largest measured directory, which the panel's bars are
// drawn relative to. Relative to the filesystem would be truthful and useless:
// a 2 GiB directory on a 500 GB disk is one invisible pixel, and the question
// the panel answers is which directory is eating the lab.
func (p DiskPanel) MaxDirBytes() uint64 {
	var max uint64
	for _, d := range p.Overview.Dirs {
		if d.Bytes > max {
			max = d.Bytes
		}
	}
	return max
}

// ServicesPanel is one row per labprovider service, with the containers backing
// it as detail. Unmanaged holds displayed containers that belong to no registry
// service. Containers is the flat Docker view the panel used to be, kept
// because /api/state is a scripted surface.
type ServicesPanel struct {
	Status     Status             `json:"status"`
	Services   []ServiceRow       `json:"services"`
	Unmanaged  []docker.Container `json:"unmanaged"`
	Containers []docker.Container `json:"containers"`
}

func (p ServicesPanel) count(state string) int {
	n := 0
	for _, r := range p.Services {
		if r.State == state {
			n++
		}
	}
	return n
}

func (p ServicesPanel) CountRunning() int     { return p.count(stateRunning) }
func (p ServicesPanel) CountDegraded() int    { return p.count(stateDegraded) }
func (p ServicesPanel) CountStopped() int     { return p.count(stateStopped) }
func (p ServicesPanel) CountNotDeployed() int { return p.count(stateNotDeployed) }

// Attention is the services the dashboard shows in full: deployed and not
// running. "not deployed" is left out on purpose - on a fresh lab that is every
// service, and a summary listing everything is not a summary.
func (p ServicesPanel) Attention() []ServiceRow {
	var out []ServiceRow
	for _, r := range p.Services {
		if r.Degraded() || r.Stopped() {
			out = append(out, r)
		}
	}
	return out
}

// ServicesPage is the full-width services view behind the sidebar's Services
// entry. The dashboard panel is a summary of the same data.
type ServicesPage struct {
	Chrome
	Services ServicesPanel
}

// ServiceRow is a registry service joined with its deploy history, its address
// and data directory from the managed config, and its live containers.
type ServiceRow struct {
	Name       string             `json:"name"`
	State      string             `json:"state"` // running | degraded | stopped | not deployed
	Core       bool               `json:"core"`
	FQDN       string             `json:"fqdn,omitempty"`
	URL        string             `json:"url,omitempty"`
	DataDir    string             `json:"data_dir,omitempty"`
	LastAction string             `json:"last_action,omitempty"`
	LastResult string             `json:"last_result,omitempty"`
	LastAt     string             `json:"last_at,omitempty"`
	Containers []docker.Container `json:"containers"`
}

func (r ServiceRow) Running() bool  { return r.State == stateRunning }
func (r ServiceRow) Degraded() bool { return r.State == stateDegraded }
func (r ServiceRow) Stopped() bool  { return r.State == stateStopped }

// RunningCount is how many of the service's containers are up, for the "(2 up)"
// qualifier on a card that reports three.
func (r ServiceRow) RunningCount() int {
	n := 0
	for _, c := range r.Containers {
		if c.State == "running" {
			n++
		}
	}
	return n
}

type ErrorsPanel struct {
	Status  Status       `json:"status"`
	Entries []logs.Entry `json:"entries"`
}

var tmplFuncs = template.FuncMap{
	"join":  func(sep string, items []string) string { return strings.Join(items, sep) },
	"bytes": humanBytes,
	"pctOf": pctOf,
}

// pctOf is a bar width in percent, floored at 1 so a measured-but-tiny
// directory still draws something rather than reading as zero.
func pctOf(n, of uint64) int {
	if of == 0 {
		return 0
	}
	p := int(n * 100 / of)
	if p < 1 && n > 0 {
		return 1
	}
	return p
}

// humanBytes renders a byte count the way df -h does. Powers of 1024, one
// decimal place above the kilobyte, because "482.3 GiB free" is the whole
// message and the exact byte count never is.
func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
