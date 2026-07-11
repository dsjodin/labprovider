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
// env map, so templates reference variables as {{.S3_PORT}}; a reference to
// an unset variable fails the render (missingkey=error) instead of silently
// producing an empty string like envsubst did.
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
