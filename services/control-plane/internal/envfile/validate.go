package envfile

import (
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

// Issue is one validation finding, attributable to a variable so the wizard
// can render it inline.
type Issue struct {
	Var string `json:"var"`
	Msg string `json:"msg"`
}

var fqdnRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)+$`)

func checkFQDN(v string) error {
	if !fqdnRe.MatchString(v) {
		return fmt.Errorf("invalid FQDN")
	}
	return nil
}

func checkIPv4(v string) error {
	a, err := netip.ParseAddr(v)
	if err != nil || !a.Is4() {
		return fmt.Errorf("invalid IPv4 address")
	}
	return nil
}

func checkCIDR(v string) error {
	p, err := netip.ParsePrefix(v)
	if err != nil || !p.Addr().Is4() {
		return fmt.Errorf("invalid IPv4 CIDR (expected e.g. 192.168.12.121/24)")
	}
	return nil
}

func checkPort(v string) error {
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("invalid TCP/UDP port")
	}
	return nil
}

// checkLoopbackReadinessPort is checkPort plus a guard against the ports
// Traefik owns. The depot is fronted by Traefik on 80/443; its published port
// is only a loopback readiness probe, so 80/443 would collide with Traefik.
func checkLoopbackReadinessPort(v string) error {
	if err := checkPort(v); err != nil {
		return err
	}
	if v == "80" || v == "443" {
		return fmt.Errorf("must not be 80 or 443 (Traefik owns those); use a loopback readiness port")
	}
	return nil
}

// checkComposeSafe rejects the characters that break the rendered compose file.
// Values land in docker-compose YAML verbatim (internal/deploy/render.go), so a
// "$" is eaten by Compose's own interpolation - a password of pa$$word reaches
// the container as "pa" with only a warning - and a quote, backslash, or newline
// terminates or rewrites the YAML scalar. Rejecting beats escaping: escaping
// only fixes the "$" case and adds a rule the operator has to remember.
func checkComposeSafe(v string) error {
	if strings.ContainsAny(v, "$\"'\n\r\\") {
		return fmt.Errorf(`must not contain $ " ' backslash or newline (compose interpolation and YAML quoting)`)
	}
	return nil
}

// checkMinLen and checkExactLen build length validators for secrets whose
// backing service enforces a size (NetBox and Authentik want >= 50, Zitadel
// wants exactly 32). Keeping them in the schema means the wizard reports the
// problem instead of the deploy failing minutes in.
func checkMinLen(n int) func(string) error {
	return func(v string) error {
		if len(v) < n {
			return fmt.Errorf("must be at least %d characters", n)
		}
		return nil
	}
}

func checkExactLen(n int) func(string) error {
	return func(v string) error {
		if len(v) != n {
			return fmt.Errorf("must be exactly %d characters", n)
		}
		return nil
	}
}

func checkAbsPath(v string) error {
	if !strings.HasPrefix(v, "/") {
		return fmt.Errorf("path must be absolute")
	}
	return nil
}

func checkNotPlaceholder(v string) error {
	if strings.HasPrefix(v, "CHANGE_ME") {
		return fmt.Errorf("replace placeholder value before deploying")
	}
	return nil
}

// checkImage enforces the repo's pinned-image rule: explicit tag, never latest.
func checkImage(v string) error {
	if !strings.Contains(v, ":") {
		return fmt.Errorf("image must include an explicit tag")
	}
	if strings.HasSuffix(v, ":latest") {
		return fmt.Errorf("image must not use the latest tag")
	}
	return nil
}

// checkPgIdentifier keeps role/db names safe for direct SQL interpolation
// (the CA module's read-only role provisioning), same rule as the bash
// validate_pg_identifier.
func checkPgIdentifier(v string) error {
	if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(v) {
		return fmt.Errorf("must be a valid PostgreSQL identifier ([A-Za-z_][A-Za-z0-9_]*)")
	}
	return nil
}

// checkHourDuration enforces the SERVICE_CERT_DURATION shape (e.g. 8760h).
func checkHourDuration(v string) error {
	if !regexp.MustCompile(`^[0-9]+h$`).MatchString(v) {
		return fmt.Errorf("must be an hour duration such as 8760h")
	}
	return nil
}

var emailRe = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

func checkEmail(v string) error {
	if !emailRe.MatchString(v) {
		return fmt.Errorf("invalid email address")
	}
	return nil
}

func checkPositiveInt(v string) error {
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return fmt.Errorf("must be a positive whole number")
	}
	return nil
}

func checkBool(v string) error {
	if v != "true" && v != "false" {
		return fmt.Errorf("must be either true or false")
	}
	return nil
}

// DeriveHostIP returns the raw IPv4 and the surrounding network CIDR from a
// HOST_IP value like 192.168.12.121/24 (the Go port of derive_host_ip_fields).
func DeriveHostIP(hostIP string) (ipv4, networkCIDR string, err error) {
	p, err := netip.ParsePrefix(hostIP)
	if err != nil || !p.Addr().Is4() {
		return "", "", fmt.Errorf("HOST_IP must be IPv4 CIDR notation: %q", hostIP)
	}
	return p.Addr().String(), p.Masked().String(), nil
}
