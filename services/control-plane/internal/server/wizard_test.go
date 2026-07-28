package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsjodin/labprovider/services/control-plane/internal/deploy"
	"github.com/dsjodin/labprovider/services/control-plane/internal/envfile"
)

// The upgrade path the wizard's button exists for: an older config, an example
// that has since gained variables, and one round trip that closes the gap.
func TestConfigValidateReturnsAnAppendableMissingBlock(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "labprovider.env")
	example := filepath.Join(dir, "example.env")
	if err := os.WriteFile(cfg, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(example, []byte("# What HARBOR_FQDN is for.\nHARBOR_FQDN=\"harbor.sddc.lab\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	engine := deploy.NewEngine(
		envfile.Store{Path: cfg, ExamplePath: example},
		&deploy.StateStore{Path: filepath.Join(dir, "state.json")},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	deploy.RegisterAll(engine)
	srv := testServer(t, Options{Engine: engine})

	post := func(body string) validateResponse {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/config/validate", strings.NewReader(body)))
		var resp validateResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode %s: %v", rec.Body.String(), err)
		}
		return resp
	}

	resp := post("")
	if len(resp.Missing) != 1 || resp.Missing[0] != "HARBOR_FQDN" {
		t.Fatalf("missing = %v, want [HARBOR_FQDN]", resp.Missing)
	}
	if !strings.Contains(resp.MissingBlock, `HARBOR_FQDN="harbor.sddc.lab"`) ||
		!strings.Contains(resp.MissingBlock, "# What HARBOR_FQDN is for.") {
		t.Fatalf("block does not carry the value and its comment: %q", resp.MissingBlock)
	}

	// What the button does: append, re-validate, nothing left.
	if after := post(resp.MissingBlock); len(after.Missing) != 0 {
		t.Errorf("after appending the block, still missing %v", after.Missing)
	}
}
