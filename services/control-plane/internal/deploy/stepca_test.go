package deploy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeLeaf writes a self-signed leaf with the given SANs plus its key to
// temp files and returns their paths.
func writeLeaf(t *testing.T, dns []string) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: dns[0]},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		DNSNames:     dns,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile = filepath.Join(dir, "leaf.crt")
	keyFile = filepath.Join(dir, "leaf.key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

func TestCertMatchesDNSIdentityMultiSAN(t *testing.T) {
	certFile, keyFile := writeLeaf(t, []string{"dashboard.sddc.lab", "certsrv.sddc.lab"})

	if !CertMatchesDNSIdentity(certFile, keyFile, "dashboard.sddc.lab") {
		t.Error("single primary SAN should match")
	}
	if !CertMatchesDNSIdentity(certFile, keyFile, "dashboard.sddc.lab", "certsrv.sddc.lab") {
		t.Error("both required SANs are present; should match")
	}
	// A leaf missing a newly required SAN must be treated as stale so it is
	// reissued rather than reused.
	if CertMatchesDNSIdentity(certFile, keyFile, "dashboard.sddc.lab", "missing.sddc.lab") {
		t.Error("a missing required SAN must not match")
	}
}
