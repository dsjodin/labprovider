package deploy

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"text/template"
)

//go:embed templates
var templatesFS embed.FS

// Render writes an embedded template to dest with the given mode. Data is the
// env map, so templates reference variables as {{.S3_PORT}}.
//
// missingkey=error is worth less than it looks, and the difference matters when
// reading a rendered file that came out wrong. It fires on an *absent* map key,
// not on a key present with an empty value - and in practice every lookup finds
// a key, because PUT /api/config refuses to save a file missing any variable
// the example defines and envfile.Parse creates an entry for every assignment.
// So an empty variable still renders as an empty string, exactly the way
// envsubst did. What the option does catch is a template referencing a name
// that is in no config at all: a typo, or a variable dropped from the example.
//
// The real protection against empty values is envfile.Validate, which rejects
// them for the variables the schema lists for each service. The gap is anything
// a template reads that the schema does not list for that service - a category
// hash.go documents as real.
//
// Deliberately not made strict about empty values: thirteen variables in the
// example are legitimately empty (the generated secrets), and
// MaterializeSecrets fills them in only for the services being deployed, so a
// strict render would fail deploys that work today.
func Render(name string, data map[string]string, dest string, mode os.FileMode) error {
	b, err := templatesFS.ReadFile("templates/" + name)
	if err != nil {
		return fmt.Errorf("embedded template %s: %w", name, err)
	}
	tmpl, err := template.New(name).Option("missingkey=error").Parse(string(b))
	if err != nil {
		return fmt.Errorf("parse template %s: %w", name, err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		return fmt.Errorf("render template %s: %w", name, err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(dest, out.Bytes(), mode); err != nil {
		return err
	}
	return os.Chmod(dest, mode)
}
