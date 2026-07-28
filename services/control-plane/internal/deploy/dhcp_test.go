package deploy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func dhcpEnv() map[string]string {
	return map[string]string{
		"DHCP_ENABLE":      "true",
		"DHCP_SCOPE_NAME":  "lab",
		"DHCP_RANGE_START": "192.168.12.150",
		"DHCP_RANGE_END":   "192.168.12.199",
		"DHCP_SUBNET_MASK": "255.255.255.0",
		"DHCP_ROUTER":      "192.168.12.1",
		"DHCP_LEASE_DAYS":  "1",
		"SEARCH_DOMAIN":    "sddc.lab",
	}
}

func TestValidateDHCPScope(t *testing.T) {
	if err := validateDHCPScope(dhcpEnv()); err != nil {
		t.Fatalf("valid scope rejected: %v", err)
	}

	// Nothing is checked when DHCP is off, so a lab that never turns it on is
	// not held to the example's placeholder addresses.
	off := dhcpEnv()
	off["DHCP_ENABLE"] = "false"
	off["DHCP_RANGE_START"] = "not-an-address"
	if err := validateDHCPScope(off); err != nil {
		t.Errorf("DHCP off should skip validation, got %v", err)
	}

	for name, mutate := range map[string]func(map[string]string){
		"reversed range":     func(e map[string]string) { e["DHCP_RANGE_END"] = "192.168.12.100" },
		"start off subnet":   func(e map[string]string) { e["DHCP_RANGE_START"] = "10.0.0.5" },
		"end off subnet":     func(e map[string]string) { e["DHCP_RANGE_END"] = "192.168.13.10" },
		"bad router":         func(e map[string]string) { e["DHCP_ROUTER"] = "192.168.12" },
		"noncontiguous mask": func(e map[string]string) { e["DHCP_SUBNET_MASK"] = "255.255.0.255" },
	} {
		t.Run(name, func(t *testing.T) {
			env := dhcpEnv()
			mutate(env)
			if err := validateDHCPScope(env); err == nil {
				t.Errorf("%s was accepted, want an error", name)
			}
		})
	}
}

// A /16 range that a /24 would reject must pass when the mask says /16: the
// check follows the configured mask, not an assumed one.
func TestValidateDHCPScopeHonorsTheMask(t *testing.T) {
	env := dhcpEnv()
	env["DHCP_SUBNET_MASK"] = "255.255.0.0"
	env["DHCP_RANGE_END"] = "192.168.200.199"
	if err := validateDHCPScope(env); err != nil {
		t.Errorf("range inside the /16 rejected: %v", err)
	}
}

func TestProvisionDHCPSetsAndEnablesTheScope(t *testing.T) {
	var (
		mu       sync.Mutex
		setQuery url.Values
		enabled  string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		switch r.URL.Path {
		case "/api/dhcp/scopes/set":
			setQuery = r.URL.Query()
		case "/api/dhcp/scopes/enable":
			enabled = r.URL.Query().Get("name")
		}
		mu.Unlock()
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}))
	defer srv.Close()

	env := dhcpEnv()
	env["DNS_FQDN"] = "dns.sddc.lab"
	rc := &RunCtx{Env: env, Log: func(string, ...any) {}}
	api := technitiumAPI{base: srv.URL}
	if err := provisionTechnitiumDHCP(context.Background(), rc, api, "tok"); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if enabled != "lab" {
		t.Errorf("enabled scope = %q, want lab (setting a scope does not enable it)", enabled)
	}
	for key, want := range map[string]string{
		"name":             "lab",
		"startingAddress":  "192.168.12.150",
		"endingAddress":    "192.168.12.199",
		"subnetMask":       "255.255.255.0",
		"routerAddress":    "192.168.12.1",
		"domainName":       "sddc.lab",
		"leaseTimeDays":    "1",
		"useThisDnsServer": "true",
		"dnsUpdates":       "true",
	} {
		if got := setQuery.Get(key); got != want {
			t.Errorf("scopes/set %s = %q, want %q", key, got, want)
		}
	}
}

// Turning DHCP off has to disable a scope an earlier deploy left running,
// otherwise the lab keeps answering DHCP after the operator said stop.
func TestProvisionDHCPDisablesTheScopeWhenTurnedOff(t *testing.T) {
	var (
		mu       sync.Mutex
		disabled string
		sets     int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		switch r.URL.Path {
		case "/api/dhcp/scopes/list":
			mu.Unlock()
			_, _ = io.WriteString(w, `{"status":"ok","response":{"scopes":[{"name":"lab"},{"name":"other"}]}}`)
			return
		case "/api/dhcp/scopes/disable":
			disabled = r.URL.Query().Get("name")
		case "/api/dhcp/scopes/set":
			sets++
		}
		mu.Unlock()
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}))
	defer srv.Close()

	env := dhcpEnv()
	env["DHCP_ENABLE"] = "false"
	rc := &RunCtx{Env: env, Log: func(string, ...any) {}}
	if err := provisionTechnitiumDHCP(context.Background(), rc, technitiumAPI{base: srv.URL}, "tok"); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if disabled != "lab" {
		t.Errorf("disabled = %q, want lab (and only lab; other scopes are not ours)", disabled)
	}
	if sets != 0 {
		t.Errorf("scopes/set called %d times with DHCP off, want 0", sets)
	}
}

// A DHCP API that will not answer must not fail a DNS deploy that asked for no
// DHCP at all.
func TestProvisionDHCPToleratesAListFailureWhenDisabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()

	var logged strings.Builder
	env := dhcpEnv()
	env["DHCP_ENABLE"] = "false"
	rc := &RunCtx{Env: env, Log: func(f string, a ...any) { logged.WriteString(f) }}
	if err := provisionTechnitiumDHCP(context.Background(), rc, technitiumAPI{base: srv.URL}, "tok"); err != nil {
		t.Fatalf("unreachable DHCP API failed a DHCP-off deploy: %v", err)
	}
	if !strings.Contains(logged.String(), "NOTICE") {
		t.Error("the tolerated failure was not reported in the deploy log")
	}
}
