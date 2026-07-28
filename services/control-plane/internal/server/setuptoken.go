package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SetupToken makes the first-run window an authenticated bootstrap instead of a
// race.
//
// install.sh starts the control plane host-networked on :8445 over plain HTTP
// and prints "open /setup to create the operator account". Until that account
// exists /setup is reachable by anyone on the segment, and whoever reaches it
// first owns a root-equivalent surface: the control plane runs as root with the
// Docker socket mounted. The installer saying "use a trusted lab network" is
// advice; the exposure is real.
//
// The token is generated on first start when no operator exists, written 0600
// next to the other control-plane state, and printed to the container log where
// install.sh can surface it. It is required as a third field on /setup and
// deleted the moment an account is created - it authorizes exactly one action,
// once.
//
// Deliberately not a password: the operator copies it once from a terminal they
// are already looking at, so it is 32 characters of base64 rather than
// something memorable.
type SetupToken struct {
	Path string

	value string
}

// NewSetupToken loads the token at path, generating one if absent. It returns
// the value so the caller can log it. An empty Path disables the check, which
// is what the tests and the read-only dashboard deployment use.
func NewSetupToken(path string) (*SetupToken, error) {
	t := &SetupToken{Path: path}
	if path == "" {
		return t, nil
	}
	b, err := os.ReadFile(path)
	if err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			t.value = v
			return t, nil
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	t.value = base64.RawURLEncoding.EncodeToString(raw)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(t.value+"\n"), 0o600); err != nil {
		return nil, err
	}
	return t, nil
}

// Value is the current token, or "" when the check is disabled or the token has
// already been spent.
func (t *SetupToken) Value() string {
	if t == nil {
		return ""
	}
	return t.value
}

// Check compares a submitted token in constant time. A disabled or spent token
// accepts anything, which is safe because handleSetup independently refuses to
// run once an operator exists.
func (t *SetupToken) Check(got string) error {
	if t == nil || t.value == "" {
		return nil
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(got)), []byte(t.value)) != 1 {
		return fmt.Errorf("setup token is wrong; it was printed by install.sh and is in %s on the host", t.Path)
	}
	return nil
}

// Spend deletes the token after the first operator is created. A failure to
// remove the file is not fatal - the account now exists, so handleSetup rejects
// every later call regardless - but it is worth reporting, because a token file
// left on disk reads like a live credential.
func (t *SetupToken) Spend() error {
	if t == nil || t.Path == "" {
		return nil
	}
	t.value = ""
	if err := os.Remove(t.Path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
