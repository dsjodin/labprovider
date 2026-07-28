package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dsjodin/labprovider/services/control-plane/internal/access"
	"github.com/dsjodin/labprovider/services/control-plane/internal/certs"
	"github.com/dsjodin/labprovider/services/control-plane/internal/dns"
	"github.com/dsjodin/labprovider/services/control-plane/internal/docker"
	"github.com/dsjodin/labprovider/services/control-plane/internal/ipam"
)

// TestWritePreview dumps every page plus the stylesheet to LABPROVIDER_PREVIEW_DIR
// so the rendered UI can be opened in a browser. It is a development aid, not an
// assertion: with the variable unset it skips.
func TestWritePreview(t *testing.T) {
	dir := os.Getenv("LABPROVIDER_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set LABPROVIDER_PREVIEW_DIR to dump rendered pages")
	}
	if err := os.MkdirAll(filepath.Join(dir, "static"), 0o755); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	srv := testServer(t, Options{
		FQDN: "labprovider.vcf.lab",
		Certs: stubCerts{out: []certs.Cert{
			{CommonName: "dashboard.vcf.lab", SANs: []string{"dashboard.vcf.lab"}, Provisioner: "acme", NotAfter: now.Add(48 * time.Hour)},
			{CommonName: "netbox.vcf.lab", SANs: []string{"netbox.vcf.lab", "ipam.vcf.lab"}, Provisioner: "acme", NotAfter: now.Add(200 * 24 * time.Hour)},
			{CommonName: "old.vcf.lab", Provisioner: "acme", NotAfter: now.Add(-24 * time.Hour)},
		}},
		DNS: stubDNS{out: dns.Overview{
			Forwarders: []string{"1.1.1.1"}, TLSPort: 853, TLSReachable: true,
			Zones: []dns.ZoneInfo{{Name: "vcf.lab", Type: "Primary", RecordCount: 24}},
		}},
		IPAM: stubIPAM{out: ipam.Overview{PrefixCount: 3, IPCount: 21, DNSNames: []string{"netbox.vcf.lab", "dns.vcf.lab"}}},
		Docker: stubDocker{list: []docker.Container{
			{Name: "labprovider-technitium", State: "running", Health: "healthy", Uptime: "3 days", Image: "technitium/dns-server:13.6.0"},
			{Name: "labprovider-netbox", State: "exited", Uptime: "-", Image: "netboxcommunity/netbox:v4.2.2"},
		}},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	page := srv.collect(context.Background())
	page.Chrome = srv.chrome("Dashboard", "dashboard")

	// The services pages need a registry, which this server has no engine for.
	// Build their payloads directly; the point of the preview is the markup.
	rows := []ServiceRow{
		{Name: "ca", State: stateRunning, Core: true, FQDN: "ca.vcf.lab", URL: "https://ca.vcf.lab",
			DataDir: "/opt/labprovider/step-ca", LastAction: "deploy", LastResult: "ok", LastAt: "2026-07-26 09:12",
			Containers: []docker.Container{{Name: "labprovider-step-ca", State: "running", Health: "healthy", Uptime: "3 days", Image: "smallstep/step-ca:0.28.1"}}},
		{Name: "netbox", State: stateDegraded, FQDN: "netbox.vcf.lab", URL: "https://netbox.vcf.lab",
			DataDir: "/opt/labprovider/netbox", LastAction: "deploy", LastResult: "ok", LastAt: "2026-07-26 09:20",
			Containers: []docker.Container{
				{Name: "labprovider-netbox", State: "running", Uptime: "2 hours", Image: "netboxcommunity/netbox:v4.2.2"},
				{Name: "labprovider-netbox-postgres", State: "exited", Uptime: "-", Image: "postgres:16-alpine"},
			}},
		{Name: "mailpit", State: stateNotDeployed},
	}
	svcChrome := srv.chrome("Services", "services")
	svcChrome.HasEngine = true
	detailChrome := srv.chrome("netbox", "services")
	detailChrome.HasEngine = true
	logsChrome := srv.chrome("Logs", "logs")
	logsChrome.HasEngine = true

	for _, tc := range []struct {
		file, tmpl, layout string
		data               any
	}{
		{"dashboard.html", "dashboard.html", "layout", page},
		{"services.html", "services.html", "layout", ServicesPage{Chrome: svcChrome, Services: ServicesPanel{Status: ok(), Services: rows}}},
		{"service.html", "service.html", "layout", ServicePage{
			Chrome: detailChrome,
			Row:    rows[1],
			Deps:   []string{"ca", "technitium"},
			Status: ok(),
			Access: &access.Entry{Name: "NetBox", Service: "netbox", URL: "https://netbox.vcf.lab", Username: "admin", Password: "lab-password"},
			Vars: []ConfigVar{
				{Name: "NETBOX_FQDN", Value: "netbox.vcf.lab"},
				{Name: "NETBOX_DIR", Value: "/opt/labprovider/netbox"},
				{Name: "NETBOX_SUPERUSER_PASSWORD", Value: "lab-password", Secret: true},
				{Name: "NETBOX_POSTGRES_PASSWORD", Secret: true, Generated: true},
			},
			Logs: []ContainerLog{{Container: "labprovider-netbox", Lines: []string{
				"netbox: starting", "netbox: listening on 0.0.0.0:8080",
			}}},
		}},
		{"logs.html", "logs.html", "layout", LogsPage{
			Chrome: logsChrome, Status: ok(), Selected: "labprovider-technitium",
			Tail: defaultLogTail, TailSizes: []int{100, 200, 500, 2000},
			Containers: []docker.Container{
				{Name: "labprovider-netbox", State: "exited"},
				{Name: "labprovider-technitium", State: "running"},
			},
		}},
		{"config.html", "wizard.html", "layout", srv.chrome("Configuration", "config")},
		{"deploy.html", "deploy.html", "layout", srv.chrome("Deploy", "deploy")},
		{"csr.html", "csr.html", "layout", srv.chrome("Sign CSR", "csr")},
		{"account.html", "account.html", "layout", srv.chrome("Account", "account")},
		{"login.html", "login.html", "bare", srv.chrome("Sign in", "")},
	} {
		rec := httptest.NewRecorder()
		srv.render(rec, srv.pages[tc.tmpl], tc.layout, tc.data)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", tc.file, rec.Code)
		}
		if err := os.WriteFile(filepath.Join(dir, tc.file), rec.Body.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	for name, served := range srv.assets.urls {
		body := srv.assets.files[served]
		if err := os.WriteFile(filepath.Join(dir, served[1:]), body, 0o644); err != nil {
			t.Fatal(err)
		}
		_ = name
	}
}
