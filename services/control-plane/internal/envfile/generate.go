package envfile

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// generatedSecrets are the machine-to-machine secrets labprovider generates
// when the operator leaves them empty, mapped to the length it generates. No
// human ever types these: they are read by one container and written by
// another, so making an operator invent 11 of them is friction with no payoff.
// Where the backing service enforces a size (NetBox and Authentik want >= 50,
// Zitadel exactly 32) the length here satisfies it.
//
// Everything an operator does have a reason to choose - admin passwords, the
// bootstrap client secrets they paste into VCF, the S3 access key - stays out
// of this map.
var generatedSecrets = map[string]int{
	"CA_POSTGRES_PASSWORD":     32,
	"CA_POSTGRES_RO_PASSWORD":  32,
	"NETBOX_POSTGRES_PASSWORD": 32,
	"NETBOX_REDIS_PASSWORD":    32,
	"NETBOX_SECRET_KEY":        64,
	"AUTHENTIK_SECRET_KEY":     64,
	"AUTHENTIK_PG_PASSWORD":    32,
	"ZITADEL_MASTERKEY":        32,
	"ZITADEL_PG_PASSWORD":      32,
	"LLDAP_JWT_SECRET":         32,
	"LLDAP_KEY_SEED":           32,
	"HARBOR_DB_PASSWORD":       32,
}

// IsGenerated reports whether a variable is generated when left empty, so the
// wizard can label it instead of flagging it.
func IsGenerated(name string) bool {
	_, ok := generatedSecrets[name]
	return ok
}

// IsSecret reports whether a variable holds a credential, so a page that lists
// configuration can mask it. This is the naming convention checkComposeSafe is
// attached by, read back: the schema table has no secret flag, and adding one
// would be a second place to keep in sync with the checks.
//
// The _DIR/_FILE/_PATH exclusion is not cosmetic - DNS_SYNC_SECRETS_DIR and
// CA_PASSWORD_FILE are paths worth reading, and masking them would hide the
// answer to "where is it".
func IsSecret(name string) bool {
	switch {
	case strings.HasSuffix(name, "_DIR"), strings.HasSuffix(name, "_FILE"), strings.HasSuffix(name, "_PATH"):
		return false
	}
	for _, suffix := range []string{"_PASSWORD", "_SECRET", "_KEY", "MASTERKEY", "_TOKEN", "_PEPPER", "_SEED"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return strings.Contains(name, "_SECRET_") || strings.Contains(name, "_KEY_")
}

// GeneratedNames returns the generated variables, sorted.
func GeneratedNames() []string {
	out := make([]string, 0, len(generatedSecrets))
	for name := range generatedSecrets {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// LookupSecret returns a variable's effective value: the configured one when
// set, otherwise the generated one persisted under dir. Readers outside the
// deploy engine need this, because a generated variable is empty in the managed
// config by design - the dashboard's certificate panel builds its PostgreSQL
// DSN from CA_POSTGRES_RO_PASSWORD without ever going through a deploy run.
// Empty means neither source has a value yet.
func LookupSecret(vars map[string]string, dir, name string) string {
	if v := vars[name]; v != "" {
		return v
	}
	if !IsGenerated(name) {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// MaterializeSecrets fills in every empty generated secret required by the
// given services, persisting each value under dir so it is stable across
// deploys, and returns the names it had to generate. An operator-chosen value
// always wins: a non-empty variable is never touched, and a value already on
// disk is reused rather than rotated. That matters because several of these are
// baked into a PostgreSQL data directory at initdb time - rotating one on
// redeploy would lock the platform out of its own database.
func MaterializeSecrets(vars map[string]string, services []string, dir string) ([]string, error) {
	want := map[string]bool{"common": true}
	for _, s := range services {
		want[s] = true
	}

	var generated []string
	for _, req := range schema {
		n, ok := generatedSecrets[req.Name]
		if !ok || vars[req.Name] != "" {
			continue
		}
		needed := false
		for _, svc := range req.RequiredBy {
			if want[svc] {
				needed = true
				break
			}
		}
		if !needed {
			continue
		}
		value, created, err := loadOrCreateSecret(filepath.Join(dir, req.Name), n)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", req.Name, err)
		}
		vars[req.Name] = value
		if created {
			generated = append(generated, req.Name)
		}
	}
	return generated, nil
}

// loadOrCreateSecret returns the secret at path, generating and persisting one
// of n characters when the file is absent or empty.
func loadOrCreateSecret(path string, n int) (value string, created bool, err error) {
	b, err := os.ReadFile(path)
	if err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			return v, false, nil
		}
	} else if !os.IsNotExist(err) {
		return "", false, err
	}
	value, err = randomSecret(n)
	if err != nil {
		return "", false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", false, err
	}
	if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
		return "", false, err
	}
	return value, true, nil
}

// secretAlphabet is deliberately alphanumeric. Every generated value is
// interpolated into compose YAML, a PostgreSQL DSN, and a .pgpass line, and
// alphanumerics need no escaping in any of the three - the same reasoning as
// checkComposeSafe, which these values also have to pass.
const secretAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// randomSecret returns n characters from secretAlphabet. Bytes at or above the
// largest multiple of the alphabet size are discarded so every character is
// equally likely.
func randomSecret(n int) (string, error) {
	const limit = 256 - (256 % len(secretAlphabet))
	out := make([]byte, 0, n)
	buf := make([]byte, n)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if int(b) >= limit {
				continue
			}
			out = append(out, secretAlphabet[int(b)%len(secretAlphabet)])
			if len(out) == n {
				break
			}
		}
	}
	return string(out), nil
}
