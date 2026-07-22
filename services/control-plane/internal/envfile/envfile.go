// Package envfile parses, validates, and persists the shared labprovider.env
// configuration file that the config wizard edits and the deploy engine reads.
// Parsing mirrors how bash `source` reads the file's KEY="value" shape; the
// raw text is stored as uploaded so comments and ordering survive round-trips.
package envfile

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var lineRe = regexp.MustCompile(`^[[:space:]]*([A-Za-z_][A-Za-z0-9_]*)=(.*)$`)

// Parse extracts KEY=value assignments. Values keep bash-style surrounding
// quotes stripped; no interpolation is performed (the example file uses only
// literal values).
func Parse(content []byte) map[string]string {
	vars := map[string]string{}
	for _, line := range strings.Split(string(content), "\n") {
		m := lineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		vars[m[1]] = unquote(strings.TrimSpace(m[2]))
	}
	return vars
}

// Names returns the variable names defined in content, in file order.
func Names(content []byte) []string {
	var names []string
	for _, line := range strings.Split(string(content), "\n") {
		if m := lineRe.FindStringSubmatch(line); m != nil {
			names = append(names, m[1])
		}
	}
	return names
}

func unquote(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}

// MissingFromExample lists variables the example defines that content does
// not: the Go port of check_provider_env_is_current.
func MissingFromExample(content, example []byte) []string {
	have := Parse(content)
	var missing []string
	for _, name := range Names(example) {
		if _, ok := have[name]; !ok {
			missing = append(missing, name)
		}
	}
	return missing
}

// Store persists the managed config file with atomic replace.
type Store struct {
	Path        string // managed labprovider.env
	ExamplePath string // shipped example, the wizard's starting template
}

// Load returns the managed config, falling back to the example when no
// config has been uploaded yet. ok reports whether a managed config exists.
func (s Store) Load() (content []byte, ok bool, err error) {
	b, err := os.ReadFile(s.Path)
	if err == nil {
		return b, true, nil
	}
	if !os.IsNotExist(err) {
		return nil, false, err
	}
	b, err = os.ReadFile(s.ExamplePath)
	if err != nil {
		return nil, false, fmt.Errorf("no config at %s and no example at %s: %w", s.Path, s.ExamplePath, err)
	}
	return b, false, nil
}

// Example returns the shipped example file.
func (s Store) Example() ([]byte, error) {
	return os.ReadFile(s.ExamplePath)
}

// Save atomically replaces the managed config (tmp + rename, 0600).
func (s Store) Save(content []byte) error {
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".labprovider.env.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.Path)
}
