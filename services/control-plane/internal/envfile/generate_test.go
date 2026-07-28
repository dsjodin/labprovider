package envfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMaterializeSecretsGeneratesPersistsAndReuses(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"NETBOX_SECRET_KEY": "", "NETBOX_POSTGRES_PASSWORD": ""}

	generated, err := MaterializeSecrets(env, []string{"netbox"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(generated) != 3 {
		t.Fatalf("generated = %v, want the three empty netbox secrets", generated)
	}
	key := env["NETBOX_SECRET_KEY"]
	if len(key) != 64 {
		t.Fatalf("NETBOX_SECRET_KEY length = %d, want 64", len(key))
	}

	b, err := os.ReadFile(filepath.Join(dir, "NETBOX_SECRET_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "NETBOX_SECRET_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("secret file mode = %v, want 0600", info.Mode().Perm())
	}

	// A second deploy must reuse the persisted value, not rotate it: these end
	// up baked into a postgres data directory.
	again := map[string]string{"NETBOX_SECRET_KEY": ""}
	regenerated, err := MaterializeSecrets(again, []string{"netbox"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if again["NETBOX_SECRET_KEY"] != key {
		t.Errorf("second run rotated the secret: %q != %q", again["NETBOX_SECRET_KEY"], key)
	}
	for _, name := range regenerated {
		if name == "NETBOX_SECRET_KEY" {
			t.Error("NETBOX_SECRET_KEY reported as generated on the second run")
		}
	}
	if got := string(b); got == "" {
		t.Error("secret file is empty")
	}
}

func TestMaterializeSecretsKeepsOperatorValues(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"NETBOX_SECRET_KEY": "chosen-by-the-operator"}
	if _, err := MaterializeSecrets(env, []string{"netbox"}, dir); err != nil {
		t.Fatal(err)
	}
	if env["NETBOX_SECRET_KEY"] != "chosen-by-the-operator" {
		t.Errorf("overwrote an operator value: %q", env["NETBOX_SECRET_KEY"])
	}
	if _, err := os.Stat(filepath.Join(dir, "NETBOX_SECRET_KEY")); !os.IsNotExist(err) {
		t.Error("wrote a secret file for a variable the operator set")
	}
}

func TestMaterializeSecretsOnlyTouchesSelectedServices(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"ZITADEL_MASTERKEY": "", "NETBOX_SECRET_KEY": ""}
	if _, err := MaterializeSecrets(env, []string{"netbox"}, dir); err != nil {
		t.Fatal(err)
	}
	if env["ZITADEL_MASTERKEY"] != "" {
		t.Error("generated a zitadel secret for a netbox-only deploy")
	}
}

// Generated values have to survive the same schema checks an operator-chosen
// value does; a generated NETBOX_SECRET_KEY that fails checkMinLen(50) would
// only surface on a real deploy.
func TestGeneratedValuesPassTheirOwnSchemaChecks(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{}
	var services []string
	for _, req := range schema {
		if !IsGenerated(req.Name) {
			continue
		}
		env[req.Name] = ""
		services = append(services, req.RequiredBy...)
	}
	if _, err := MaterializeSecrets(env, services, dir); err != nil {
		t.Fatal(err)
	}
	for _, req := range schema {
		if !IsGenerated(req.Name) {
			continue
		}
		if env[req.Name] == "" {
			t.Errorf("%s was not generated", req.Name)
			continue
		}
		for _, check := range req.Checks {
			if err := check(env[req.Name]); err != nil {
				t.Errorf("%s: generated value fails its own check: %v", req.Name, err)
			}
		}
	}
}

func TestLookupSecret(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "CA_POSTGRES_RO_PASSWORD"), []byte("from-disk\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{"CA_POSTGRES_RO_PASSWORD": "", "CA_POSTGRES_PASSWORD": "configured"}

	if got := LookupSecret(vars, dir, "CA_POSTGRES_RO_PASSWORD"); got != "from-disk" {
		t.Errorf("empty config value should fall back to the file, got %q", got)
	}
	if got := LookupSecret(vars, dir, "CA_POSTGRES_PASSWORD"); got != "configured" {
		t.Errorf("configured value should win, got %q", got)
	}
	if got := LookupSecret(vars, dir, "NETBOX_SECRET_KEY"); got != "" {
		t.Errorf("nothing on either side should be empty, got %q", got)
	}
	if got := LookupSecret(map[string]string{}, dir, "SOME_OTHER_VAR"); got != "" {
		t.Errorf("a non-generated variable must not read from the secrets dir, got %q", got)
	}
}

func TestValidateAcceptsEmptyGeneratedVars(t *testing.T) {
	vars := map[string]string{}
	for _, req := range schema {
		vars[req.Name] = "" // present but empty
	}
	for _, issue := range Validate(vars, []string{"netbox"}) {
		if IsGenerated(issue.Var) {
			t.Errorf("%s flagged as %q; empty means generate it", issue.Var, issue.Msg)
		}
	}
	// A variable that is absent entirely is still missing, generated or not.
	found := false
	for _, issue := range Validate(map[string]string{}, []string{"netbox"}) {
		if issue.Var == "NETBOX_SECRET_KEY" {
			found = true
		}
	}
	if !found {
		t.Error("an absent NETBOX_SECRET_KEY should still be reported as missing")
	}
}

func TestIsSecret(t *testing.T) {
	secret := []string{
		"CA_POSTGRES_PASSWORD", "LLDAP_JWT_SECRET", "NETBOX_SECRET_KEY",
		"S3_ACCESS_KEY", "ZITADEL_MASTERKEY", "AUTHENTIK_API_TOKEN",
		"NETBOX_API_TOKEN_PEPPER", "LLDAP_KEY_SEED",
	}
	plain := []string{
		// KEYCLOAK_* is the reason this is not a substring match on "KEY".
		"KEYCLOAK_ADMIN_USER", "KEYCLOAK_BOOTSTRAP_CLIENT_ID", "KEYCLOAK_PORT",
		// Paths naming a secret are not the secret, and hiding them hides the
		// answer to "where is it".
		"DNS_SYNC_SECRETS_DIR", "CA_PASSWORD_FILE", "HOST_IP",
	}
	for _, name := range secret {
		if !IsSecret(name) {
			t.Errorf("IsSecret(%q) = false, want true", name)
		}
	}
	for _, name := range plain {
		if IsSecret(name) {
			t.Errorf("IsSecret(%q) = true, want false", name)
		}
	}
}

// Every generated secret must be masked: they are credentials by definition.
func TestGeneratedSecretsAreSecret(t *testing.T) {
	for _, name := range GeneratedNames() {
		if !IsSecret(name) {
			t.Errorf("generated secret %q is not classified as one", name)
		}
	}
}
