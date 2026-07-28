package deploy

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// PyKMIP verifies client certificates against ca_path. step-ca signs leaves
// with the intermediate, so a bundle holding only the root cannot verify a
// client that presents its leaf without the chain.
func TestWriteClientCABundleHoldsRootAndIntermediate(t *testing.T) {
	caDataDir := t.TempDir()
	certDir := t.TempDir()
	certs := filepath.Join(caDataDir, "certs")
	if err := os.MkdirAll(certs, 0o755); err != nil {
		t.Fatal(err)
	}
	// No trailing newline on the root: concatenating naively would glue the two
	// PEM blocks together and produce a file OpenSSL cannot parse.
	if err := os.WriteFile(filepath.Join(certs, "root_ca.crt"),
		[]byte("-----BEGIN CERTIFICATE-----\nROOT\n-----END CERTIFICATE-----"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(certs, "intermediate_ca.crt"),
		[]byte("-----BEGIN CERTIFICATE-----\nINTERMEDIATE\n-----END CERTIFICATE-----\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := writeClientCABundle(caDataDir, certDir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(certDir, "client_ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(got, []byte("BEGIN CERTIFICATE")); n != 2 {
		t.Errorf("bundle has %d certificates, want 2 (root and intermediate)", n)
	}
	if !bytes.Contains(got, []byte("ROOT")) || !bytes.Contains(got, []byte("INTERMEDIATE")) {
		t.Errorf("bundle is missing one of the two certificates: %s", got)
	}
	if bytes.Contains(got, []byte("-----END CERTIFICATE----------BEGIN CERTIFICATE-----")) {
		t.Error("the two PEM blocks were concatenated without a separating newline")
	}
}

func TestWriteClientCABundleFailsWithoutTheIntermediate(t *testing.T) {
	caDataDir := t.TempDir()
	certs := filepath.Join(caDataDir, "certs")
	if err := os.MkdirAll(certs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(certs, "root_ca.crt"), []byte("root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Silently shipping a root-only bundle is the bug this change fixes, so a
	// missing intermediate must fail the deploy rather than degrade.
	if err := writeClientCABundle(caDataDir, t.TempDir()); err == nil {
		t.Error("writeClientCABundle succeeded without the intermediate")
	}
}
