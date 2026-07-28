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
		vars[m[1]] = unquote(m[2])
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

// unquote strips bash-style surrounding quotes and any trailing comment, so
// NETBOX_PORT="8444"  # loopback only yields 8444 rather than a value that
// fails checkPort with no hint about why. v is the raw text after the '=',
// untrimmed: whether a '#' has whitespace in front of it is the whole
// difference between a comment and a character in the value.
func unquote(v string) string {
	q := strings.TrimLeft(v, " \t")
	if len(q) >= 2 && (q[0] == '"' || q[0] == '\'') {
		if end := strings.IndexByte(q[1:], q[0]); end >= 0 {
			return q[1 : end+1]
		}
		return strings.TrimSpace(q)
	}
	if i := commentStart(v); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

// commentStart returns the index of the '#' that begins a trailing comment, or
// -1. Bash only starts a comment at a '#' that whitespace precedes, and so does
// this: without that rule HARBOR_ADMIN_PASSWORD=Str0ng#Pass validates clean,
// deploys as Str0ng, and the operator cannot log in with the password they set.
func commentStart(v string) int {
	for i := 1; i < len(v); i++ {
		if v[i] == '#' && (v[i-1] == ' ' || v[i-1] == '\t') {
			return i
		}
	}
	return -1
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

// MissingBlock renders the entries a config is missing as text ready to append
// to it: each missing assignment with its example value, preceded by the
// comment lines that document it.
//
// This is what turns a labprovider upgrade from a manual diff into one click.
// The example gains variables between versions, PUT /api/config refuses to save
// a file missing any of them, and the operator's only other options are hand
// merging or downloading the new example and re-entering every value they set.
//
// Returns "" when nothing is missing.
func MissingBlock(content, example []byte) string {
	missing := map[string]bool{}
	for _, name := range MissingFromExample(content, example) {
		missing[name] = true
	}
	if len(missing) == 0 {
		return ""
	}

	var out []string
	var comments []string
	for _, line := range strings.Split(string(example), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			comments = append(comments, line)
			continue
		}
		m := lineRe.FindStringSubmatch(line)
		if m == nil {
			// A blank line ends a comment block: comments attached to a
			// variable sit directly above it.
			comments = nil
			continue
		}
		if missing[m[1]] {
			out = append(out, comments...)
			out = append(out, line)
		}
		comments = nil
	}
	if len(out) == 0 {
		return ""
	}
	return "\n# Added from the example - review the values before deploying.\n" +
		strings.Join(out, "\n") + "\n"
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
