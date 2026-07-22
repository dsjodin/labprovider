package envfile

import (
	"slices"
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
