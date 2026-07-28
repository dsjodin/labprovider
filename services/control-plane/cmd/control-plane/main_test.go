package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dsjodin/labprovider/services/control-plane/internal/deploy"
	"github.com/dsjodin/labprovider/services/control-plane/internal/envfile"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// schemaOnlyServices are names the validation table declares variables for
// with no deployer behind them. Listed explicitly so the gap is a decision
// rather than an accident: msca is emulated in-process by mscaManager, not
// deployed as a compose stack.
var schemaOnlyServices = map[string]bool{"msca": true}

// TestRegistryMatchesSchema keeps the deploy registry and the validation table
// in agreement. Without it, a new service whose variables are never added to
// the schema deploys with unvalidated config, and a schema entry whose deployer
// is renamed silently stops being validated - both silent failures.
func TestRegistryMatchesSchema(t *testing.T) {
	engine := deploy.NewEngine(envfile.Store{}, nil, discardLogger())
	deploy.RegisterAll(engine)

	inSchema := map[string]bool{}
	for _, name := range envfile.SchemaServices() {
		inSchema[name] = true
	}

	registered := map[string]bool{}
	for _, svc := range engine.Services() {
		registered[svc.Name()] = true
		if !inSchema[svc.Name()] {
			t.Errorf("service %q is registered but has no variables in internal/envfile/schema.go", svc.Name())
		}
	}

	for name := range inSchema {
		if !registered[name] && !schemaOnlyServices[name] {
			t.Errorf("schema declares variables for %q but no deployer is registered; add one or add it to schemaOnlyServices", name)
		}
	}

	for name := range schemaOnlyServices {
		if registered[name] {
			t.Errorf("%q is in schemaOnlyServices but is now registered; drop the exception", name)
		}
	}
}

// TestRegistryDepsAreRegistered catches a dependency naming a service that
// Register was never called for. Resolve would fail at deploy time instead.
func TestRegistryDepsAreRegistered(t *testing.T) {
	engine := deploy.NewEngine(envfile.Store{}, nil, discardLogger())
	deploy.RegisterAll(engine)

	registered := map[string]bool{}
	for _, svc := range engine.Services() {
		registered[svc.Name()] = true
	}
	for _, svc := range engine.Services() {
		for _, dep := range svc.Deps() {
			if !registered[dep] {
				t.Errorf("service %q depends on unregistered service %q", svc.Name(), dep)
			}
		}
	}
}

func TestResolveTLS(t *testing.T) {
	log := discardLogger()

	if resolveTLS("", "", log) {
		t.Error("no cert/key configured should not use TLS")
	}

	// A configured path that does not exist must fall back, not crash.
	if resolveTLS("/nonexistent/dashboard.crt", "/nonexistent/dashboard.key", log) {
		t.Error("missing cert file should fall back to HTTP")
	}

	// A malformed cert file must fall back too.
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.pem")
	if err := os.WriteFile(bad, []byte("not a pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	if resolveTLS(bad, bad, log) {
		t.Error("malformed cert should fall back to HTTP")
	}

	// A valid keypair must select TLS.
	certPath, keyPath := writeKeypair(t, dir)
	if !resolveTLS(certPath, keyPath, log) {
		t.Error("valid cert/key should use TLS")
	}
}

func writeKeypair(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "dashboard.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"dashboard.test"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPath = filepath.Join(dir, "dashboard.crt")
	keyPath = filepath.Join(dir, "dashboard.key")

	certOut, _ := os.Create(certPath)
	defer certOut.Close()
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyOut, _ := os.Create(keyPath)
	defer keyOut.Close()
	if err := pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}
