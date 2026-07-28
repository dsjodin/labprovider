package deploy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// KMIP deploys PyKMIP as a KMIP 1.2 endpoint for the VCF encryption workflows
// (vSAN encryption, VM encryption, and the key-provider configuration in
// vCenter). It is deliberately minimal: TLS with client-certificate auth on
// 5696, a step-ca-issued leaf, and a SQLite object store.
//
// PyKMIP describes itself as a demonstration and testing server, which is
// exactly the labprovider use case. It is not a production KMS.
type KMIP struct{}

func (KMIP) Name() string   { return "kmip" }
func (KMIP) Deps() []string { return []string{"ca"} }

func (k KMIP) Deploy(ctx context.Context, rc *RunCtx) error {
	env := rc.Env
	runtime := rc.Workdir("kmip")
	certDir := filepath.Join(env["KMIP_DIR"], "certs")

	// KMIP_DATA_DIR holds the managed-object database: the keys vCenter stores
	// here are the ones that decrypt its VMs, so it must survive a remove.
	if err := requireOutsideRuntime(env["KMIP_DATA_DIR"], runtime, "KMIP_DATA_DIR", "the key database"); err != nil {
		return err
	}

	if err := requireCAReady(ctx, env); err != nil {
		return err
	}
	for _, dir := range []string{runtime, filepath.Join(runtime, "policies"), env["KMIP_DIR"]} {
		if err := EnsureDir(dir, 0o755, -1, -1); err != nil {
			return err
		}
	}
	// PyKMIP runs as root in the image and opens the database itself.
	if err := EnsureDir(env["KMIP_DATA_DIR"], 0o700, -1, -1); err != nil {
		return err
	}

	if err := IssueCert(ctx, rc, env["KMIP_FQDN"], certDir, "kmip", env["HOST_IPV4"]); err != nil {
		return err
	}
	// Clients authenticate with certificates this CA issued, so the server needs
	// the CA material to verify them. Both the root AND the intermediate go in:
	// step-ca signs leaves with the intermediate, and a client that presents its
	// leaf alone - without the chain - cannot be verified against the root by
	// itself. Trusting the intermediate directly makes verification work either
	// way, and it is the only issuer in this CA anyway.
	if err := writeClientCABundle(env["CA_DATA_DIR"], certDir); err != nil {
		return err
	}

	if err := Render("kmip.conf.tpl", env, filepath.Join(runtime, "server.conf"), 0o644); err != nil {
		return err
	}
	if err := Render("kmip.Dockerfile", env, filepath.Join(runtime, "build", "Dockerfile"), 0o644); err != nil {
		return err
	}
	if err := Render("docker-compose.kmip.yml.tpl", env, filepath.Join(runtime, "docker-compose.yml"), 0o644); err != nil {
		return err
	}

	cmp := rc.Compose("kmip")
	rc.Log("Building the KMIP image %s.", env["KMIP_IMAGE"])
	if err := cmp.Build(ctx, env["KMIP_IMAGE"], filepath.Join(runtime, "build")); err != nil {
		return err
	}
	tagBuiltVersion(ctx, rc, cmp, env["KMIP_IMAGE"], "kmip")
	if err := cmp.Down(ctx); err != nil {
		return err
	}
	if err := cmp.Up(ctx); err != nil {
		return err
	}

	// KMIP speaks TTLV over TLS, so there is no HTTP probe: a completed TCP
	// handshake on the published port is the readiness signal.
	addr := "127.0.0.1:" + env["KMIP_PORT"]
	rc.Log("Waiting for the KMIP endpoint at %s.", addr)
	if err := WaitTCP(ctx, addr, 30, 2*time.Second); err != nil {
		return err
	}
	rc.Log("KMIP is ready at %s:%s. In vCenter add a Standard Key Provider pointing there, "+
		"then upload the step-ca root (%s) and a client certificate signed by it (sign vCenter's CSR at /csr).",
		env["KMIP_FQDN"], env["KMIP_PORT"], filepath.Join(env["CA_DATA_DIR"], "certs", "root_ca.crt"))
	return nil
}

// writeClientCABundle writes root_ca.crt + intermediate_ca.crt to
// <certDir>/client_ca.crt, the anchor set PyKMIP verifies client certificates
// against. Each PEM in the file is a trust anchor, so a client leaf signed by
// the intermediate verifies whether or not the client sends the chain.
func writeClientCABundle(caDataDir, certDir string) error {
	var bundle []byte
	for _, name := range []string{"root_ca.crt", "intermediate_ca.crt"} {
		path := filepath.Join(caDataDir, "certs", name)
		pem, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read the CA %s at %s: %w", name, path, err)
		}
		if len(pem) > 0 && pem[len(pem)-1] != '\n' {
			pem = append(pem, '\n')
		}
		bundle = append(bundle, pem...)
	}
	return os.WriteFile(filepath.Join(certDir, "client_ca.crt"), bundle, 0o644)
}

func (k KMIP) Remove(ctx context.Context, rc *RunCtx) error {
	cmp := rc.Compose("kmip")
	if err := cmp.Down(ctx); err != nil {
		return err
	}
	if err := os.RemoveAll(rc.Workdir("kmip")); err != nil {
		return err
	}
	rc.Log("Removed KMIP containers and runtime files. The key database in %s and the certificates in %s were preserved.",
		rc.Env["KMIP_DATA_DIR"], filepath.Join(rc.Env["KMIP_DIR"], "certs"))
	return nil
}
