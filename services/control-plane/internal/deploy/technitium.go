package deploy

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

// Technitium deploys the containerized DNS server, the port of
// bootstrap/technitium.sh. Deploying it is the explicit opt-in to running DNS
// on this host: after the listener, forwarder, HTTPS endpoint, and API tokens
// are all verified, the host resolver is pointed at Technitium. The
// systemd-resolved stub listener is already disabled by install.sh.
type Technitium struct{}

func (Technitium) Name() string   { return "technitium" }
func (Technitium) Deps() []string { return []string{"ca"} }

const technitiumResolvMarker = "Managed by labprovider (technitium deploy)"

func (t Technitium) Deploy(ctx context.Context, rc *RunCtx) error {
	env := rc.Env
	certDir := env["TECHNITIUM_CERT_DIR"]
	runtime := rc.Workdir("technitium")

	for _, v := range []struct{ name, dir string }{
		{"TECHNITIUM_DATA_DIR", env["TECHNITIUM_DATA_DIR"]},
		{"TECHNITIUM_CERT_DIR", certDir},
	} {
		if err := requireOutsideRuntime(v.dir, runtime, v.name, "Technitium content"); err != nil {
			return err
		}
	}
	if err := validateDHCPScope(env); err != nil {
		return err
	}
	if err := requireCAReady(ctx, env); err != nil {
		return err
	}
	if err := EnsureDir(runtime, 0o755, -1, -1); err != nil {
		return err
	}
	if err := EnsureDir(env["TECHNITIUM_DATA_DIR"], 0o755, 1000, 1000); err != nil {
		return err
	}

	if err := IssueCert(ctx, rc, env["DNS_FQDN"], certDir, "technitium"); err != nil {
		return err
	}
	if err := buildTechnitiumChainBundles(ctx, rc, certDir); err != nil {
		return err
	}
	if err := buildTechnitiumPfx(rc, certDir); err != nil {
		return err
	}
	if err := Render("docker-compose.technitium.yml.tpl", env, runtime+"/docker-compose.yml", 0o644); err != nil {
		return err
	}

	cmp := rc.Compose("technitium")
	// Pre-pull BEFORE stopping the running container: when Technitium is the
	// host resolver, stopping it first would take DNS down and an un-cached
	// image could not be pulled. A failed pull aborts with the old server
	// still running.
	if err := cmp.Pull(ctx); err != nil {
		return fmt.Errorf("pull %s failed; the running DNS server was left untouched: %w", env["TECHNITIUM_IMAGE"], err)
	}
	if err := cmp.Down(ctx); err != nil {
		return err
	}
	if err := preflightPort53(rc); err != nil {
		return err
	}
	if err := cmp.Up(ctx); err != nil {
		return err
	}

	rc.Log("Waiting for the Technitium DNS listener on 127.0.0.1:53.")
	if err := waitDNSListener(ctx, env["DNS_FQDN"], 60, 2*time.Second); err != nil {
		return err
	}

	api := newTechnitiumAPI(env)
	adminToken, err := api.AdminToken(ctx, rc)
	if err != nil {
		return err
	}

	// Upstream forwarder; this deployer is the only owner of the setting.
	out, err := api.callOK(ctx, "/api/settings/set", url.Values{
		"token": {adminToken}, "forwarders": {env["DNS_FORWARDER"]}, "forwarderProtocol": {"Udp"},
	})
	if err != nil {
		return fmt.Errorf("set the Technitium upstream forwarder %s: %w", env["DNS_FORWARDER"], err)
	}
	if resp, _ := out["response"].(map[string]any); resp != nil {
		if recursion, _ := resp["recursion"].(string); recursion == "Deny" {
			return fmt.Errorf("Technitium recursion is disabled (recursion=Deny); external names cannot be resolved")
		}
	}
	rc.Log("Technitium upstream forwarder set to %s (UDP).", env["DNS_FORWARDER"])

	rc.Log("Verifying the upstream forwarder %s answers.", env["DNS_FORWARDER"])
	if err := waitForwarderAnswers(ctx, env["DNS_FORWARDER"], env["SEARCH_DOMAIN"], 30, 2*time.Second); err != nil {
		return err
	}

	// Web service TLS with the step-ca PKCS#12 bundle (container-internal port
	// 53443, published as TECHNITIUM_HTTPS_PORT).
	pfxPassword, err := os.ReadFile(filepath.Join(certDir, "technitium-pfx-password"))
	if err != nil {
		return err
	}
	if _, err := api.callOK(ctx, "/api/settings/set", url.Values{
		"token":                            {adminToken},
		"webServiceEnableTls":              {"true"},
		"webServiceTlsPort":                {"53443"},
		"webServiceTlsCertificatePath":     {"/etc/labprovider/technitium-certs/technitium.pfx"},
		"webServiceTlsCertificatePassword": {string(pfxPassword)},
	}); err != nil {
		return fmt.Errorf("enable Technitium web service TLS: %w", err)
	}
	rc.Log("Technitium web service TLS enabled with the step-ca certificate.")
	httpsURL := fmt.Sprintf("https://%s:%s/", env["DNS_FQDN"], env["TECHNITIUM_HTTPS_PORT"])
	if err := WaitHTTPSPinned(ctx, httpsURL, filepath.Join(env["CA_DATA_DIR"], "certs", "root_ca.crt"), 30, 2*time.Second); err != nil {
		return err
	}

	if err := provisionTechnitiumDHCP(ctx, rc, api, adminToken); err != nil {
		return err
	}
	if err := provisionTechnitiumDNSSyncToken(ctx, rc, api, adminToken); err != nil {
		return err
	}
	if err := provisionTechnitiumDashboardToken(ctx, rc, api, adminToken); err != nil {
		return err
	}
	if err := pointHostResolverAtTechnitium(rc); err != nil {
		return err
	}

	rc.Log("Technitium is ready. Web console: http://%s:%s and https://%s:%s",
		env["DNS_FQDN"], env["TECHNITIUM_HTTP_PORT"], env["DNS_FQDN"], env["TECHNITIUM_HTTPS_PORT"])
	return nil
}

func (t Technitium) Remove(ctx context.Context, rc *RunCtx) error {
	cmp := rc.Compose("technitium")
	if err := cmp.Down(ctx); err != nil {
		return err
	}
	if err := os.RemoveAll(rc.Workdir("technitium")); err != nil {
		return err
	}
	restoreHostResolver(rc)
	rc.Log("Removed Technitium containers and runtime files. Persistent data in %s and certificates in %s were preserved.",
		rc.Env["TECHNITIUM_DATA_DIR"], rc.Env["TECHNITIUM_CERT_DIR"])
	return nil
}

// buildTechnitiumChainBundles writes the CA chain bundle (intermediate+root)
// and the roots bundle alongside the leaf.
func buildTechnitiumChainBundles(ctx context.Context, rc *RunCtx, certDir string) error {
	env := rc.Env
	intermediate, err := os.ReadFile(filepath.Join(env["CA_DATA_DIR"], "certs", "intermediate_ca.crt"))
	if err != nil {
		return err
	}
	root, err := os.ReadFile(filepath.Join(env["CA_DATA_DIR"], "certs", "root_ca.crt"))
	if err != nil {
		return err
	}
	chain := filepath.Join(certDir, "technitium-ca-chain.pem")
	if err := os.WriteFile(chain, append(intermediate, root...), 0o644); err != nil {
		return err
	}
	roots := filepath.Join(certDir, "technitium-ca-roots.pem")
	if err := os.WriteFile(roots, root, 0o644); err != nil {
		return err
	}
	for _, f := range []string{chain, roots} {
		_ = os.Chown(f, 1000, 1000)
	}
	return nil
}

// buildTechnitiumPfx converts the PEM material into technitium.pfx with a
// generated persisted password; rebuilt whenever the PEM is newer. Technitium
// (.NET) requires the web TLS certificate as PKCS#12; the Legacy-RC2 encoder
// matches what `openssl pkcs12 -export` produced for the bash module.
func buildTechnitiumPfx(rc *RunCtx, certDir string) error {
	pfxFile := filepath.Join(certDir, "technitium.pfx")
	passwordFile := filepath.Join(certDir, "technitium-pfx-password")

	if _, err := os.Stat(passwordFile); err != nil {
		raw := make([]byte, 24)
		if _, err := rand.Read(raw); err != nil {
			return err
		}
		if err := os.WriteFile(passwordFile, []byte(base64.StdEncoding.EncodeToString(raw)), 0o600); err != nil {
			return err
		}
		rc.Log("Generated Technitium PKCS#12 password at: %s", passwordFile)
	}
	_ = os.Chmod(passwordFile, 0o600)
	_ = os.Chown(passwordFile, 1000, 1000)
	password, err := os.ReadFile(passwordFile)
	if err != nil {
		return err
	}

	certPath := filepath.Join(certDir, "technitium.crt")
	keyPath := filepath.Join(certDir, "technitium.key")
	// The password file is a source too. Lose it and a new password is written
	// above, but the existing bundle is still newer than the cert and key - so
	// it would not be rebuilt, and Deploy would then send the new password for
	// the old bundle. Technitium accepts the settings write and fails to load
	// its own TLS certificate, surfacing as a WaitHTTPSPinned timeout with
	// nothing pointing at the password.
	if fresh, err := fileNewer(pfxFile, certPath, keyPath, passwordFile); err == nil && fresh {
		_ = os.Chmod(pfxFile, 0o600)
		_ = os.Chown(pfxFile, 1000, 1000)
		return nil
	}

	rc.Log("Building the Technitium PKCS#12 bundle at %s.", pfxFile)
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return err
	}
	leaf, chain, err := parseCertChain(certPEM)
	if err != nil {
		return err
	}
	key, err := parsePrivateKey(keyPEM)
	if err != nil {
		return err
	}
	pfx, err := pkcs12.LegacyRC2.Encode(key, leaf, chain, string(password))
	if err != nil {
		return fmt.Errorf("encode PKCS#12: %w", err)
	}
	if err := os.WriteFile(pfxFile, pfx, 0o600); err != nil {
		return err
	}
	return os.Chown(pfxFile, 1000, 1000)
}

// fileNewer reports whether target exists and is newer than every source.
func fileNewer(target string, sources ...string) (bool, error) {
	ti, err := os.Stat(target)
	if err != nil {
		return false, err
	}
	for _, s := range sources {
		si, err := os.Stat(s)
		if err != nil {
			return false, err
		}
		if si.ModTime().After(ti.ModTime()) {
			return false, nil
		}
	}
	return true, nil
}

func parseCertChain(pemBytes []byte) (leaf *x509.Certificate, chain []*x509.Certificate, err error) {
	var certs []*x509.Certificate
	for {
		var block *pem.Block
		block, pemBytes = pem.Decode(pemBytes)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, nil, err
		}
		certs = append(certs, c)
	}
	if len(certs) == 0 {
		return nil, nil, fmt.Errorf("no certificates in PEM")
	}
	return certs[0], certs[1:], nil
}

func parsePrivateKey(pemBytes []byte) (any, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("no PEM block in key file")
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	if k, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

// preflightPort53 test-binds TCP and UDP :53, plus :67 when DHCP is enabled.
// install.sh disables the systemd-resolved stub listener, so a holder here is a
// real conflict. The DHCP check matters more than the DNS one: a second DHCP
// server on the segment does not fail loudly, it just races to answer.
func preflightPort53(rc *RunCtx) error {
	l, err := net.Listen("tcp", ":53")
	if err != nil {
		return fmt.Errorf("port 53/tcp is already in use and labprovider will not stop the holder automatically (a leftover unbound or dnsmasq? if systemd-resolved holds it, re-run install.sh): %w", err)
	}
	l.Close()
	u, err := net.ListenPacket("udp", ":53")
	if err != nil {
		return fmt.Errorf("port 53/udp is already in use and labprovider will not stop the holder automatically: %w", err)
	}
	u.Close()
	if rc.Env["DHCP_ENABLE"] != "true" {
		return nil
	}
	d, err := net.ListenPacket("udp", ":67")
	if err != nil {
		return fmt.Errorf("DHCP_ENABLE is true but port 67/udp is already in use (dnsmasq or isc-dhcp-server?); stop the holder or set DHCP_ENABLE=false: %w", err)
	}
	d.Close()
	return nil
}

// resolverVia127 queries the DNS server on 127.0.0.1:53 directly, regardless
// of the host or container resolv.conf.
func resolverVia127() *net.Resolver {
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 2 * time.Second}
			return d.DialContext(ctx, network, "127.0.0.1:53")
		},
	}
}

// waitDNSListener waits until the server ANSWERS at all: NXDOMAIN counts as
// up (the lab zone does not exist yet at first deploy), only timeouts and
// connection errors count as down.
func waitDNSListener(ctx context.Context, probeName string, attempts int, interval time.Duration) error {
	r := resolverVia127()
	return retry(ctx, attempts, interval, "the Technitium DNS listener on 127.0.0.1:53",
		func(ctx context.Context) error {
			qctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			_, err := r.LookupHost(qctx, probeName)
			var dnsErr *net.DNSError
			if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
				return nil
			}
			return err
		})
}

// waitForwarderAnswers verifies the resolver DNS_FORWARDER names is reachable
// and speaking DNS.
//
// It deliberately does not require an answer for a public name. This check used
// to resolve one.one.one.one, which gates the deploy on the lab having a route
// to the internet - and AGENTS.md names isolated and air-gapped labs as a
// primary use case, with DNS_FORWARDER explicitly an operator-chosen resolver.
// On one of those the deploy failed after ~60s with "Technitium cannot resolve
// external names" while DNS was working exactly as configured, and because
// Technitium is a foundation service the whole deploy UI stayed locked.
//
// probeName is immaterial: NXDOMAIN, SERVFAIL, and REFUSED all prove the
// forwarder answered, which is what this is checking. Only silence means it did
// not. The query goes out on the wire directly rather than through
// net.Resolver, because net.Resolver reports "the server replied REFUSED" and
// "nothing is listening" as the same kind of error, and that distinction is the
// entire check.
func waitForwarderAnswers(ctx context.Context, forwarder, probeName string, attempts int, interval time.Duration) error {
	if forwarder == "" {
		return fmt.Errorf("DNS_FORWARDER is empty")
	}
	if _, _, err := net.SplitHostPort(forwarder); err != nil {
		forwarder = net.JoinHostPort(forwarder, "53")
	}
	what := fmt.Sprintf("the upstream forwarder %s (check DNS_FORWARDER reachability)", forwarder)
	return retry(ctx, attempts, interval, what, func(ctx context.Context) error {
		return dnsAnswered(ctx, forwarder, probeName)
	})
}

// dnsAnswered sends one A query to addr over UDP and reports whether a reply
// carrying the query's ID came back. The RCODE is not inspected on purpose.
func dnsAnswered(ctx context.Context, addr, name string) error {
	query, id, err := dnsQuery(name)
	if err != nil {
		return err
	}
	d := net.Dialer{Timeout: 2 * time.Second}
	conn, err := d.DialContext(ctx, "udp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline := time.Now().Add(4 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	if _, err := conn.Write(query); err != nil {
		return err
	}
	reply := make([]byte, 512)
	n, err := conn.Read(reply)
	if err != nil {
		return err
	}
	if n < 12 {
		return fmt.Errorf("reply was %d bytes, too short to be DNS", n)
	}
	if reply[0] != query[0] || reply[1] != query[1] {
		return fmt.Errorf("reply id %d does not match query id %d", uint16(reply[0])<<8|uint16(reply[1]), id)
	}
	if reply[2]&0x80 == 0 {
		return fmt.Errorf("reply has the query bit set, not the response bit")
	}
	return nil
}

// dnsQuery builds a single-question A/IN query in wire format. An empty name
// queries the root, which every resolver answers for.
func dnsQuery(name string) (msg []byte, id uint16, err error) {
	var idBytes [2]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return nil, 0, err
	}
	id = uint16(idBytes[0])<<8 | uint16(idBytes[1])
	msg = []byte{
		idBytes[0], idBytes[1],
		0x01, 0x00, // recursion desired
		0x00, 0x01, // one question
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	}
	for _, label := range strings.Split(strings.Trim(name, "."), ".") {
		if label == "" {
			continue
		}
		if len(label) > 63 {
			return nil, 0, fmt.Errorf("dns label %q is longer than 63 bytes", label)
		}
		msg = append(msg, byte(len(label)))
		msg = append(msg, label...)
	}
	msg = append(msg, 0x00) // root label terminates the name
	msg = append(msg, 0x00, 0x01, 0x00, 0x01)
	return msg, id, nil
}

// DHCPScope is one Technitium DHCP scope, as configured from DHCP_*.
type DHCPScope struct {
	Name       string
	RangeStart string
	RangeEnd   string
	SubnetMask string
	Router     string
	DomainName string
	LeaseDays  string
}

func dhcpScopeFrom(env map[string]string) DHCPScope {
	return DHCPScope{
		Name:       env["DHCP_SCOPE_NAME"],
		RangeStart: env["DHCP_RANGE_START"],
		RangeEnd:   env["DHCP_RANGE_END"],
		SubnetMask: env["DHCP_SUBNET_MASK"],
		Router:     env["DHCP_ROUTER"],
		DomainName: env["SEARCH_DOMAIN"],
		LeaseDays:  env["DHCP_LEASE_DAYS"],
	}
}

// validateDHCPScope is the cross-field half of the DHCP configuration, which
// the schema table cannot express: the range has to run forwards and it has to
// sit in the subnet the router is on. Both mistakes produce a scope Technitium
// accepts and no client can use, so they are worth catching before the deploy.
func validateDHCPScope(env map[string]string) error {
	if env["DHCP_ENABLE"] != "true" {
		return nil
	}
	start, err := netip.ParseAddr(env["DHCP_RANGE_START"])
	if err != nil || !start.Is4() {
		return fmt.Errorf("DHCP_RANGE_START must be an IPv4 address: %q", env["DHCP_RANGE_START"])
	}
	end, err := netip.ParseAddr(env["DHCP_RANGE_END"])
	if err != nil || !end.Is4() {
		return fmt.Errorf("DHCP_RANGE_END must be an IPv4 address: %q", env["DHCP_RANGE_END"])
	}
	if end.Less(start) {
		return fmt.Errorf("DHCP_RANGE_END (%s) must not be below DHCP_RANGE_START (%s)", end, start)
	}
	router, err := netip.ParseAddr(env["DHCP_ROUTER"])
	if err != nil || !router.Is4() {
		return fmt.Errorf("DHCP_ROUTER must be an IPv4 address: %q", env["DHCP_ROUTER"])
	}
	mask, err := netip.ParseAddr(env["DHCP_SUBNET_MASK"])
	if err != nil || !mask.Is4() {
		return fmt.Errorf("DHCP_SUBNET_MASK must be an IPv4 dotted mask such as 255.255.255.0: %q", env["DHCP_SUBNET_MASK"])
	}
	bits, ok := maskBits(mask)
	if !ok {
		return fmt.Errorf("DHCP_SUBNET_MASK is not a contiguous netmask: %q", env["DHCP_SUBNET_MASK"])
	}
	subnet := netip.PrefixFrom(router, bits).Masked()
	for name, addr := range map[string]netip.Addr{"DHCP_RANGE_START": start, "DHCP_RANGE_END": end} {
		if !subnet.Contains(addr) {
			return fmt.Errorf("%s (%s) is outside the DHCP_ROUTER subnet %s", name, addr, subnet)
		}
	}
	return nil
}

// maskBits converts a dotted netmask to a prefix length, reporting false when
// the mask has holes in it (255.255.0.255 and friends).
func maskBits(mask netip.Addr) (int, bool) {
	b := mask.As4()
	bits := 0
	seenZero := false
	for _, octet := range b {
		for i := 7; i >= 0; i-- {
			if octet&(1<<i) != 0 {
				if seenZero {
					return 0, false
				}
				bits++
			} else {
				seenZero = true
			}
		}
	}
	return bits, true
}

// provisionTechnitiumDHCP configures and enables the lab DHCP scope, or
// disables a previously configured one when DHCP_ENABLE goes back to false.
// Technitium already runs here and registers its own leases in DNS, so this is
// scope provisioning through the existing API client - not a second service.
func provisionTechnitiumDHCP(ctx context.Context, rc *RunCtx, api technitiumAPI, adminToken string) error {
	env := rc.Env
	scope := dhcpScopeFrom(env)

	if env["DHCP_ENABLE"] != "true" {
		names, err := api.DHCPScopeNames(ctx, adminToken)
		if err != nil {
			// Nothing was asked for and nothing is being changed; a DHCP API
			// that will not answer must not fail a DNS deploy.
			rc.Log("NOTICE: could not list DHCP scopes to confirm none is active: %v", err)
			return nil
		}
		for _, name := range names {
			if name != scope.Name {
				continue
			}
			if err := api.DisableDHCPScope(ctx, adminToken, name); err != nil {
				return fmt.Errorf("disable the DHCP scope %s after DHCP_ENABLE was set to false: %w", name, err)
			}
			rc.Log("DHCP_ENABLE is false: disabled the %s scope (its configuration was kept).", name)
		}
		return nil
	}

	if err := api.SetDHCPScope(ctx, adminToken, scope); err != nil {
		return fmt.Errorf("configure the DHCP scope %s: %w", scope.Name, err)
	}
	if err := api.EnableDHCPScope(ctx, adminToken, scope.Name); err != nil {
		return fmt.Errorf("enable the DHCP scope %s: %w", scope.Name, err)
	}
	rc.Log("DHCP scope %q is serving %s-%s (mask %s, router %s, DNS %s, domain %s, %s day leases).",
		scope.Name, scope.RangeStart, scope.RangeEnd, scope.SubnetMask, scope.Router,
		env["DNS_FQDN"], scope.DomainName, scope.LeaseDays)
	return nil
}

// provisionTechnitiumDNSSyncToken mints (or reuses) the dns-sync API token.
// The token now belongs to the admin user but is created via the admin
// session instead of raw first-boot credentials.
func provisionTechnitiumDNSSyncToken(ctx context.Context, rc *RunCtx, api technitiumAPI, adminToken string) error {
	env := rc.Env
	if err := EnsureDir(env["DNS_SYNC_SECRETS_DIR"], 0o700, -1, -1); err != nil {
		return err
	}
	tokenFile := filepath.Join(env["DNS_SYNC_SECRETS_DIR"], "technitium.token")
	if stored, err := os.ReadFile(tokenFile); err == nil && len(stored) > 0 {
		if api.TokenValid(ctx, string(stored), "/api/zones/list") {
			rc.Log("Reusing existing Technitium API token: %s", tokenFile)
			_ = os.Chmod(tokenFile, 0o600)
			_ = os.Chown(tokenFile, 1000, 1000)
			return nil
		}
		rc.Log("Stored Technitium API token is no longer valid; creating a replacement.")
	}
	token, err := api.CreateUserToken(ctx, "admin", rc.Env["TECHNITIUM_ADMIN_PASSWORD"], "labprovider-dns-sync")
	if err != nil {
		return fmt.Errorf("create the dns-sync Technitium token: %w", err)
	}
	if err := os.WriteFile(tokenFile, []byte(token), 0o600); err != nil {
		return err
	}
	_ = os.Chown(tokenFile, 1000, 1000)
	rc.Log("Provisioned a Technitium API token for dns-sync at: %s", tokenFile)
	return nil
}

// provisionTechnitiumDashboardToken creates the non-admin 'dashboard' user,
// grants it Settings:View plus per-zone View on every existing zone (zone
// visibility needs the explicit per-zone grant), and mints its scoped token
// for the control plane's DNS panel. Grants are re-applied on every run so
// zones created since the last run are picked up; a still-valid stored token
// (operator override included) is reused.
func provisionTechnitiumDashboardToken(ctx context.Context, rc *RunCtx, api technitiumAPI, adminToken string) error {
	env := rc.Env
	secretsDir := env["CONTROL_PLANE_SECRETS_DIR"]
	if secretsDir == "" {
		rc.Log("NOTICE: CONTROL_PLANE_SECRETS_DIR is not set; skipping dashboard Technitium token provisioning.")
		return nil
	}
	if err := EnsureDir(secretsDir, 0o700, 1000, 1000); err != nil {
		return err
	}
	tokenFile := filepath.Join(secretsDir, "technitium.token")

	if !api.UserExists(ctx, adminToken, "dashboard") {
		raw := make([]byte, 24)
		if _, err := rand.Read(raw); err != nil {
			return err
		}
		pass := base64.StdEncoding.EncodeToString(raw) + "Aa1!"
		if err := api.CreateUser(ctx, adminToken, "dashboard", "labprovider Dashboard", pass); err != nil {
			return fmt.Errorf("create the Technitium dashboard user: %w", err)
		}
	}

	// Settings:View (the DNS panel reads settings/get); the admin groups are
	// re-sent so the section grant does not drop their access.
	if _, err := api.callOK(ctx, "/api/admin/permissions/set", url.Values{
		"token":            {adminToken},
		"section":          {"Settings"},
		"groupPermissions": {"Administrators|true|true|true|DNS Administrators|true|true|true"},
		"userPermissions":  {"dashboard|true|false|false"},
	}); err != nil {
		return fmt.Errorf("grant the Technitium dashboard user Settings:View: %w", err)
	}

	zones, err := api.ZoneNames(ctx, adminToken)
	if err != nil {
		return err
	}
	for _, zone := range zones {
		// Only userPermissions is sent: the API syncs user and group tables
		// independently, so the zone's admin-group access stays untouched.
		if _, err := api.callOK(ctx, "/api/zones/permissions/set", url.Values{
			"token":           {adminToken},
			"zone":            {zone},
			"userPermissions": {"admin|true|true|true|dashboard|true|false|false"},
		}); err != nil {
			rc.Log("NOTICE: could not grant the dashboard user View on Technitium zone %s: %v", zone, err)
		}
	}

	if stored, err := os.ReadFile(tokenFile); err == nil && len(stored) > 0 {
		if api.TokenValid(ctx, string(stored), "/api/settings/get") {
			rc.Log("Reusing existing dashboard Technitium token: %s", tokenFile)
			_ = os.Chmod(tokenFile, 0o600)
			_ = os.Chown(tokenFile, 1000, 1000)
			return nil
		}
		rc.Log("Stored dashboard Technitium token is no longer valid; creating a replacement.")
	}
	token, err := api.CreateToken(ctx, adminToken, "dashboard", "labprovider-dashboard")
	if err != nil {
		return fmt.Errorf("create the dashboard Technitium token: %w", err)
	}
	if !api.TokenValid(ctx, token, "/api/settings/get") {
		return fmt.Errorf("freshly minted Technitium dashboard token cannot read settings; check the Settings:View grant")
	}
	if err := os.WriteFile(tokenFile, []byte(token), 0o600); err != nil {
		return err
	}
	_ = os.Chown(tokenFile, 1000, 1000)
	rc.Log("Provisioned a read-only dashboard Technitium token at: %s", tokenFile)
	return nil
}

// hostEtc returns the host /etc path: /host/etc when running in the
// control-plane container (install.sh mounts it), /etc otherwise. It is a
// variable so tests can point the resolv.conf rewrite at a temporary
// directory instead of the machine they run on.
var hostEtc = hostEtcDefault

// systemdResolvConf is the resolver file systemd-resolved publishes. A variable
// for the same reason hostEtc is one: the restore path's behavior when it is
// absent is the interesting case, and the test machine's own /run is not it.
var systemdResolvConf = "/run/systemd/resolve/resolv.conf"

func hostEtcDefault() string {
	if fi, err := os.Stat("/host/etc"); err == nil && fi.IsDir() {
		return "/host/etc"
	}
	return "/etc"
}

// resolvBackup is the copy of the operator's own resolv.conf, taken before the
// first rewrite so removing Technitium can put back what was there rather than
// what the undo path guesses was there.
const resolvBackup = "resolv.conf.labprovider.bak"

// writeTechnitiumResolvConf replaces <dir>/resolv.conf with a file pointing at
// 127.0.0.1. The remove is what keeps a symlinked resolv.conf from being
// followed into systemd-resolved's own file.
//
// The original is copied to resolvBackup first, and only when no backup exists
// yet: a redeploy must not overwrite the operator's file with labprovider's own
// rewrite from the previous deploy.
func writeTechnitiumResolvConf(dir, searchDomain string) error {
	resolv := filepath.Join(dir, "resolv.conf")
	backup := filepath.Join(dir, resolvBackup)
	if _, err := os.Stat(backup); os.IsNotExist(err) {
		if original, err := os.ReadFile(resolv); err == nil && !strings.Contains(string(original), technitiumResolvMarker) {
			if err := os.WriteFile(backup, original, 0o644); err != nil {
				return err
			}
		}
	}
	content := fmt.Sprintf("# %s. Removed when technitium is removed.\nnameserver 127.0.0.1\nsearch %s\n",
		technitiumResolvMarker, searchDomain)
	os.Remove(resolv) // may be a symlink to systemd-resolved's file
	return os.WriteFile(resolv, []byte(content), 0o644)
}

// pointHostResolverAtTechnitium rewrites the host resolv.conf to 127.0.0.1
// and verifies resolution still works through the new path.
func pointHostResolverAtTechnitium(rc *RunCtx) error {
	rc.Log("Pointing the host resolver at Technitium (127.0.0.1).")
	if err := writeTechnitiumResolvConf(hostEtc(), rc.Env["SEARCH_DOMAIN"]); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// What matters here is that the resolver resolv.conf now points at answers
	// at all, not that the internet is reachable through it.
	if err := waitDNSListener(ctx, rc.Env["DNS_FQDN"], 3, 2*time.Second); err != nil {
		return fmt.Errorf("host DNS resolution is broken after pointing resolv.conf at Technitium: %w", err)
	}
	return nil
}

// restoreHostResolver puts back the resolv.conf that was there before the
// Technitium deploy rewrote it, when the marker says labprovider is what wrote
// the current one. (The stub listener stays disabled; install.sh owns that
// drop-in.)
//
// It restores the backup rather than assuming systemd-resolved. install.sh only
// touches resolv.conf behind `systemctl is-enabled systemd-resolved`, and this
// path used to drop that guard: on a host that does not run systemd-resolved -
// a minimal Debian image, ifupdown, NetworkManager's own resolv.conf, a static
// file - removing Technitium replaced a working resolver with a symlink to a
// path that does not exist, and said it had restored the resolver.
func restoreHostResolver(rc *RunCtx) {
	etc := hostEtc()
	resolv := filepath.Join(etc, "resolv.conf")
	b, err := os.ReadFile(resolv)
	if err != nil || !strings.Contains(string(b), technitiumResolvMarker) {
		return
	}
	backup := filepath.Join(etc, resolvBackup)
	if original, err := os.ReadFile(backup); err == nil {
		rc.Log("Restoring the host resolver from %s.", backup)
		if err := os.WriteFile(resolv, original, 0o644); err != nil {
			rc.Log("NOTICE: could not restore %s: %v (fix it manually)", resolv, err)
			return
		}
		os.Remove(backup)
		return
	}
	// No backup: fall back to the systemd-resolved symlink, but only when that
	// file actually exists. A dangling symlink is worse than the file we have.
	if _, err := os.Stat(systemdResolvConf); err != nil {
		rc.Log("NOTICE: %s still points at Technitium and no backup was found. "+
			"This host does not run systemd-resolved, so it was left alone - point it at your resolver manually.", resolv)
		return
	}
	rc.Log("Restoring the host resolver to systemd-resolved.")
	os.Remove(resolv)
	if err := os.Symlink(systemdResolvConf, resolv); err != nil {
		rc.Log("NOTICE: could not restore %s: %v (fix it manually)", resolv, err)
	}
}
