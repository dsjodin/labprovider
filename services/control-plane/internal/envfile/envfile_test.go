package envfile

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	content := []byte(`# comment
HOST_IP="192.168.12.121/24"
SEARCH_DOMAIN=sddc.lab

# CA_PASSWORD=""
S3_PORT='8333'
`)
	vars := Parse(content)
	want := map[string]string{
		"HOST_IP":       "192.168.12.121/24",
		"SEARCH_DOMAIN": "sddc.lab",
		"S3_PORT":       "8333",
	}
	for k, v := range want {
		if vars[k] != v {
			t.Errorf("Parse[%s] = %q, want %q", k, vars[k], v)
		}
	}
	if _, ok := vars["CA_PASSWORD"]; ok {
		t.Errorf("commented-out variable was parsed")
	}
}

func TestParseTrailingComments(t *testing.T) {
	content := []byte(`NETBOX_PORT="8444"   # loopback only
DEPOT_HTTP_PORT=8088 # readiness probe
SAMBA_PASSWORD="pa#ss"
NETBOX_ALLOWED_HOSTS='a.lab b.lab' # two hosts
HARBOR_ADMIN_PASSWORD=Str0ng#Pass
LLDAP_ADMIN_PASSWORD=one two#three # trailing
KMIP_PASSWORD=#leading
S3_SECRET_KEY= # not set yet
`)
	want := map[string]string{
		"NETBOX_PORT":          "8444",
		"DEPOT_HTTP_PORT":      "8088",
		"SAMBA_PASSWORD":       "pa#ss",
		"NETBOX_ALLOWED_HOSTS": "a.lab b.lab",
		// A '#' with no whitespace in front of it is part of the value, the way
		// bash reads it. Truncating here silently changed operator passwords.
		"HARBOR_ADMIN_PASSWORD": "Str0ng#Pass",
		"LLDAP_ADMIN_PASSWORD":  "one two#three",
		"KMIP_PASSWORD":         "#leading",
		"S3_SECRET_KEY":         "",
	}
	vars := Parse(content)
	for k, v := range want {
		if vars[k] != v {
			t.Errorf("Parse[%s] = %q, want %q", k, vars[k], v)
		}
	}
}

func TestComposeSafeAndLengthChecks(t *testing.T) {
	for _, v := range []string{`pa$$word`, `say"hi`, "line\nbreak", `back\slash`, "it's"} {
		if checkComposeSafe(v) == nil {
			t.Errorf("checkComposeSafe(%q) = nil, want error", v)
		}
	}
	if err := checkComposeSafe("Sup3r-s4fe_value.ok"); err != nil {
		t.Errorf("checkComposeSafe rejected a safe value: %v", err)
	}
	if checkMinLen(50)("short") == nil {
		t.Error("checkMinLen(50) accepted a short value")
	}
	if err := checkMinLen(3)("abc"); err != nil {
		t.Errorf("checkMinLen(3) rejected an exact-length value: %v", err)
	}
	if checkExactLen(32)("31characterslong_padding_here_") == nil {
		t.Error("checkExactLen(32) accepted a wrong-length value")
	}
}

// The shipped example is the completeness contract and the operator's starting
// template, so it must satisfy every rule the schema enforces except the
// deliberate CHANGE_ME placeholders.
func TestExampleSatisfiesNonPlaceholderChecks(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "config", "labprovider.env.example"))
	if err != nil {
		t.Skipf("example not readable: %v", err)
	}
	vars := Parse(content)
	for _, issue := range ValidateAll(vars) {
		if strings.HasPrefix(vars[issue.Var], "CHANGE_ME") {
			continue
		}
		t.Errorf("%s: %s (value %q)", issue.Var, issue.Msg, vars[issue.Var])
	}
}

func TestMissingFromExample(t *testing.T) {
	example := []byte("A=1\nB=2\nC=3\n")
	content := []byte("A=1\nC=3\nEXTRA=9\n")
	got := MissingFromExample(content, example)
	if !slices.Equal(got, []string{"B"}) {
		t.Errorf("MissingFromExample = %v, want [B]", got)
	}
}

func TestValidate(t *testing.T) {
	env := map[string]string{
		"HOST_IP":       "192.168.12.121/24",
		"SEARCH_DOMAIN": "sddc.lab",
		"WORKDIR":       "/opt/labprovider/runtime",
		"S3_FQDN":       "s3.sddc.lab",
		"S3_PORT":       "8333",
		"S3_ACCESS_KEY": "CHANGE_ME",
		"S3_SECRET_KEY": "secret",
		"S3_DATA_DIR":   "/opt/labprovider/seaweedfs",
		"S3_IMAGE":      "docker.io/chrislusf/seaweedfs:latest",
	}
	issues := Validate(env, []string{"s3"})
	byVar := map[string]string{}
	for _, i := range issues {
		byVar[i.Var] = i.Msg
	}
	if len(issues) != 2 {
		t.Fatalf("Validate returned %d issues, want 2: %v", len(issues), issues)
	}
	if _, ok := byVar["S3_ACCESS_KEY"]; !ok {
		t.Errorf("placeholder S3_ACCESS_KEY not flagged")
	}
	if _, ok := byVar["S3_IMAGE"]; !ok {
		t.Errorf("latest-tag S3_IMAGE not flagged")
	}

	// chrony vars are not required when only s3 is selected
	for _, i := range issues {
		if i.Var == "CHRONY_SERVER_1" {
			t.Errorf("unrelated service variable required: %v", i)
		}
	}
}

func TestDeriveHostIP(t *testing.T) {
	ip, network, err := DeriveHostIP("192.168.12.121/24")
	if err != nil {
		t.Fatal(err)
	}
	if ip != "192.168.12.121" || network != "192.168.12.0/24" {
		t.Errorf("DeriveHostIP = %s, %s", ip, network)
	}
	if _, _, err := DeriveHostIP("192.168.12.121"); err == nil {
		t.Errorf("plain IP accepted; CIDR is required")
	}
}

// Upgrading labprovider adds variables to the example, and PUT /api/config then
// refuses to save until every one is present. The block is what turns that from
// a hand merge into one click, so it has to carry the example's values and the
// comments that explain them - not just the names.
func TestMissingBlockCarriesValuesAndComments(t *testing.T) {
	example := []byte(`# Host settings
HOST_IP="192.168.12.121/24"

# HARBOR_DB_PASSWORD is generated when empty.
# Do not change it once the database exists.
HARBOR_DB_PASSWORD=""
HARBOR_FQDN="harbor.sddc.lab"
`)
	content := []byte("HOST_IP=\"10.0.0.5/24\"\n")

	block := MissingBlock(content, example)
	for _, want := range []string{
		"HARBOR_DB_PASSWORD=\"\"",
		"HARBOR_FQDN=\"harbor.sddc.lab\"",
		"# HARBOR_DB_PASSWORD is generated when empty.",
		"# Do not change it once the database exists.",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("block is missing %q:\n%s", want, block)
		}
	}
	// Already present, so neither it nor its comment may be re-added.
	if strings.Contains(block, "HOST_IP=") || strings.Contains(block, "# Host settings") {
		t.Errorf("block re-added a variable the config already has:\n%s", block)
	}

	// Appending it makes the config complete, which is the whole point.
	merged := append(append([]byte{}, content...), block...)
	if got := MissingFromExample(merged, example); len(got) != 0 {
		t.Errorf("after appending the block, still missing %v", got)
	}
	if got := MissingBlock(merged, example); got != "" {
		t.Errorf("a complete config produced a block: %q", got)
	}
}

// The real upgrade path, against the shipped example: strip a handful of
// variables the way an older config would lack them, then check the block puts
// exactly those back and nothing else.
func TestMissingBlockRoundTripsTheShippedExample(t *testing.T) {
	example, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "config", "labprovider.env.example"))
	if err != nil {
		t.Skipf("example not readable: %v", err)
	}
	dropped := map[string]bool{"HARBOR_FQDN": true, "KMIP_PORT": true, "DNS_FORWARDER": true}

	var older []string
	for _, line := range strings.Split(string(example), "\n") {
		if m := lineRe.FindStringSubmatch(line); m != nil && dropped[m[1]] {
			continue
		}
		older = append(older, line)
	}
	content := []byte(strings.Join(older, "\n"))

	missing := MissingFromExample(content, example)
	if len(missing) != len(dropped) {
		t.Fatalf("missing = %v, want the %d dropped variables", missing, len(dropped))
	}
	merged := append(append([]byte{}, content...), MissingBlock(content, example)...)
	if got := MissingFromExample(merged, example); len(got) != 0 {
		t.Errorf("after one click the config is still missing %v", got)
	}
	// The restored values must be the example's, not empty.
	vars := Parse(merged)
	for name := range dropped {
		if vars[name] != Parse(example)[name] {
			t.Errorf("%s = %q, want the example's %q", name, vars[name], Parse(example)[name])
		}
	}
}
