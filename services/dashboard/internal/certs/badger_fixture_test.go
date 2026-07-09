package certs

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"math/big"
	"net"
	"testing"
	"time"

	badger "github.com/dgraph-io/badger/v3"
)

// TestReaderList_BadgerV3Fixture builds a BadgerDB with the badger/v3 engine
// (the format step-ca 0.30.2's smallstep/nosql writes - manifest v7) in the
// exact smallstep/nosql key encoding, then reads it back through Reader. This
// pins the reader to the v3 on-disk format: a v4 engine would refuse or migrate
// this DB, so a green run here is what the version match buys us.
func TestReaderList_BadgerV3Fixture(t *testing.T) {
	dbDir := t.TempDir()

	activeDER := makeCert(t, 4096, "active.sddc.lab", []string{"active.sddc.lab", "alias.sddc.lab"}, net.ParseIP("10.0.0.10"))
	revokedDER := makeCert(t, 4097, "revoked.sddc.lab", []string{"revoked.sddc.lab"}, nil)

	writeFixtureDB(t, dbDir, activeDER, revokedDER)

	r := &Reader{Path: dbDir, SnapshotRoot: t.TempDir()}
	got, err := r.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d certs, want 2: %+v", len(got), got)
	}

	byCN := map[string]Cert{}
	for _, c := range got {
		byCN[c.CommonName] = c
	}

	active, ok := byCN["active.sddc.lab"]
	if !ok {
		t.Fatal("active cert missing")
	}
	if active.Revoked {
		t.Error("active cert should not be revoked")
	}
	if active.Provisioner != "admin" {
		t.Errorf("provisioner = %q, want admin", active.Provisioner)
	}
	if active.Serial != serialHex(big.NewInt(4096)) {
		t.Errorf("serial = %q, want %q", active.Serial, serialHex(big.NewInt(4096)))
	}
	wantSANs := map[string]bool{"active.sddc.lab": false, "alias.sddc.lab": false, "10.0.0.10": false}
	for _, s := range active.SANs {
		if _, ok := wantSANs[s]; ok {
			wantSANs[s] = true
		}
	}
	for s, seen := range wantSANs {
		if !seen {
			t.Errorf("SAN %q missing from %v", s, active.SANs)
		}
	}

	revoked, ok := byCN["revoked.sddc.lab"]
	if !ok {
		t.Fatal("revoked cert missing")
	}
	if !revoked.Revoked {
		t.Error("revoked cert should be flagged revoked")
	}
}

func makeCert(t *testing.T, serial int64, cn string, dns []string, ip net.IP) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		DNSNames:     dns,
	}
	if ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

// writeFixtureDB writes the issued/data/revoked buckets exactly as step-ca does:
// keyed by the decimal serial via the smallstep/nosql key encoding
// (toBadgerKey), issued value = raw DER, data value = JSON with the provisioner,
// revoked value = JSON with the Serial field.
func writeFixtureDB(t *testing.T, dir string, activeDER, revokedDER []byte) {
	t.Helper()
	db, err := badger.Open(badger.DefaultOptions(dir).WithLogger(nil))
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Fatalf("close fixture db: %v", err)
		}
	}()

	dataJSON, _ := json.Marshal(map[string]any{
		"provisioner": map[string]string{"name": "admin"},
	})
	revJSON, _ := json.Marshal(map[string]string{"Serial": "4097"})

	err = db.Update(func(txn *badger.Txn) error {
		if err := txn.Set(toBadgerKey(bucketIssued, "4096"), activeDER); err != nil {
			return err
		}
		if err := txn.Set(toBadgerKey(bucketIssuedData, "4096"), dataJSON); err != nil {
			return err
		}
		if err := txn.Set(toBadgerKey(bucketIssued, "4097"), revokedDER); err != nil {
			return err
		}
		return txn.Set(toBadgerKey(bucketRevoked, "4097"), revJSON)
	})
	if err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}
