package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The generated compose file publishes Harbor's cleartext port on every
// interface, because Harbor's own configuration takes a port and no address.
// This is the rewrite that keeps Tier 1.1's rule intact.
func TestBindLoopbackPublish(t *testing.T) {
	const generated = `services:
  proxy:
    image: goharbor/nginx-photon:v2.13.0
    ports:
      - 8086:8080
    networks:
      - harbor
  core:
    image: goharbor/harbor-core:v2.13.0
`
	path := filepath.Join(t.TempDir(), "docker-compose.yml")
	if err := os.WriteFile(path, []byte(generated), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := bindLoopbackPublish(path, "8086"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `- "127.0.0.1:8086:8080"`) {
		t.Errorf("port publish not rewritten:\n%s", got)
	}
	// Everything else is untouched: this edits one line of a generated file.
	if !strings.Contains(string(got), "goharbor/harbor-core:v2.13.0") {
		t.Error("the rewrite should leave the rest of the file alone")
	}
}

func TestBindLoopbackPublishAcceptsQuotedPorts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "docker-compose.yml")
	if err := os.WriteFile(path, []byte("    ports:\n      - \"8086:8080\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := bindLoopbackPublish(path, "8086"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), `- "127.0.0.1:8086:8080"`) {
		t.Errorf("quoted port not rewritten: %s", got)
	}
}

// A generated file is a moving target. If the shape changes, failing the deploy
// beats publishing the cleartext port on every interface and saying nothing.
func TestBindLoopbackPublishFailsWhenTheShapeChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "docker-compose.yml")
	if err := os.WriteFile(path, []byte("services:\n  proxy:\n    ports:\n      - 9999:9000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := bindLoopbackPublish(path, "8086")
	if err == nil {
		t.Fatal("expected an error when the expected publish line is absent")
	}
	if !strings.Contains(err.Error(), "every interface") {
		t.Errorf("error should say what the risk is: %v", err)
	}
}

// prepare writes to four container paths. /config and /data were missing, so
// the config tree and the secrets were written inside the throwaway container
// and compose failed on the first missing env_file.
func TestPrepareArgsMountEveryPathPrepareWritesTo(t *testing.T) {
	env := map[string]string{
		"HARBOR_PREPARE_IMAGE": "docker.io/goharbor/prepare:v2.13.0",
		"HARBOR_DATA_DIR":      "/opt/labprovider/harbor",
		"HARBOR_TRIVY_ENABLE":  "false",
	}
	got := strings.Join(prepareArgs(env, "/opt/labprovider/runtime/harbor"), " ")
	for _, mount := range []string{
		"/opt/labprovider/runtime/harbor:/input",
		"/opt/labprovider/runtime/harbor:/compose_location",
		"/opt/labprovider/runtime/harbor/common/config:/config",
		"/opt/labprovider/harbor:/data",
		"/:/hostfs",
	} {
		if !strings.Contains(got, mount) {
			t.Errorf("missing mount %s in: %s", mount, got)
		}
	}
	if strings.Contains(got, "--with-trivy") {
		t.Errorf("scanner enabled without HARBOR_TRIVY_ENABLE: %s", got)
	}
}

func TestPrepareArgsEnableTrivy(t *testing.T) {
	env := map[string]string{"HARBOR_TRIVY_ENABLE": "true"}
	got := prepareArgs(env, "/runtime/harbor")
	if got[len(got)-1] != "--with-trivy" {
		t.Errorf("--with-trivy should follow the prepare subcommand: %v", got)
	}
}

// Harbor's compose file is generated, so the deployer must not run against a
// stale one if prepare failed to write it.
func TestHarborDepsIncludeTraefik(t *testing.T) {
	deps := Harbor{}.Deps()
	want := map[string]bool{"ca": false, "traefik": false}
	for _, d := range deps {
		if _, ok := want[d]; ok {
			want[d] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("harbor should depend on %s: Traefik owns the route, the CA owns the wildcard cert", name)
		}
	}
}
