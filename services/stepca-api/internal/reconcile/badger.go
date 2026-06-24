package reconcile

import (
	"context"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/dsjodin/provider-box/services/stepca-api/internal/store"
)

// BadgerSource reads step-ca's BadgerDB directly. Couples to step-ca's storage
// layout (smallstep/nosql Badger backend); isolated here so a step-ca version
// bump only touches this file.
//
// Verified against step-ca v0.30.2 + smallstep/nosql v0.8.0:
//   - bucket "x509_certs"         keyed by decimal serial, value = raw DER cert
//   - bucket "x509_certs_data"    keyed by decimal serial, value = JSON CertificateData
//   - bucket "revoked_x509_certs" keyed by decimal serial, value = JSON RevokedCertificateInfo
//
// smallstep/nosql encodes badger keys as
//   [2-byte LE bucket len][bucket][2-byte LE key len][key]
// so iterating one bucket means prefix-seeking on its first two segments.
type BadgerSource struct {
	Path string
}

const (
	bucketIssued     = "x509_certs"
	bucketIssuedData = "x509_certs_data"
	bucketRevoked    = "revoked_x509_certs"
)

func (b *BadgerSource) open() (*badger.DB, error) {
	opts := badger.DefaultOptions(b.Path).
		WithReadOnly(true).
		WithLogger(nil)
	return badger.Open(opts)
}

// bucketPrefix returns the [LE-len][bucket] segment used to prefix-scan one
// table in the smallstep/nosql Badger encoding.
func bucketPrefix(bucket string) []byte {
	out := make([]byte, 2+len(bucket))
	binary.LittleEndian.PutUint16(out[:2], uint16(len(bucket)))
	copy(out[2:], bucket)
	return out
}

// toBadgerKey mirrors smallstep/nosql/badger/v2.toBadgerKey so we can build
// the exact same key shape for point lookups (e.g. x509_certs_data by serial).
func toBadgerKey(bucket, key string) []byte {
	out := make([]byte, 0, 4+len(bucket)+len(key))
	var lb, lk [2]byte
	binary.LittleEndian.PutUint16(lb[:], uint16(len(bucket)))
	binary.LittleEndian.PutUint16(lk[:], uint16(len(key)))
	out = append(out, lb[:]...)
	out = append(out, bucket...)
	out = append(out, lk[:]...)
	out = append(out, key...)
	return out
}

// certData mirrors db.CertificateData in step-ca. Only the field stepca-api
// consumes is listed.
type certData struct {
	Provisioner *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"provisioner,omitempty"`
}

func (b *BadgerSource) Issued(ctx context.Context, yield func(store.Cert) error) error {
	db, err := b.open()
	if err != nil {
		return fmt.Errorf("open badger: %w", err)
	}
	defer db.Close()

	prefix := bucketPrefix(bucketIssued)
	return db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			item := it.Item()
			var raw []byte
			if err := item.Value(func(v []byte) error {
				raw = append(raw[:0], v...)
				return nil
			}); err != nil {
				return err
			}
			cert, err := x509.ParseCertificate(raw)
			if err != nil {
				// Bad row shouldn't abort the whole pass; reconcile picks it
				// up on the next tick if it heals.
				continue
			}
			if err := yield(store.Cert{
				Serial:      serialHex(cert.SerialNumber),
				CommonName:  cert.Subject.CommonName,
				SANs:        collectSANs(cert),
				NotBefore:   cert.NotBefore,
				NotAfter:    cert.NotAfter,
				Provisioner: lookupProvisioner(txn, cert.SerialNumber.String()),
				Status:      store.StatusActive,
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

func lookupProvisioner(txn *badger.Txn, decimalSerial string) string {
	item, err := txn.Get(toBadgerKey(bucketIssuedData, decimalSerial))
	if err != nil {
		return ""
	}
	var raw []byte
	if err := item.Value(func(v []byte) error {
		raw = append(raw[:0], v...)
		return nil
	}); err != nil {
		return ""
	}
	var cd certData
	if err := json.Unmarshal(raw, &cd); err != nil {
		return ""
	}
	if cd.Provisioner == nil {
		return ""
	}
	return cd.Provisioner.Name
}

// revokedRecord mirrors authority/db.RevokedCertificateInfo. The upstream
// struct has no JSON tags so fields serialize under their Go names; mirror
// only the fields stepca-api consumes.
type revokedRecord struct {
	Serial    string    `json:"Serial"`
	Reason    string    `json:"Reason"`
	RevokedAt time.Time `json:"RevokedAt"`
}

func (b *BadgerSource) Revoked(ctx context.Context, yield func(serial, reason string, at time.Time) error) error {
	db, err := b.open()
	if err != nil {
		return fmt.Errorf("open badger: %w", err)
	}
	defer db.Close()

	prefix := bucketPrefix(bucketRevoked)
	return db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			item := it.Item()
			var raw []byte
			if err := item.Value(func(v []byte) error {
				raw = append(raw[:0], v...)
				return nil
			}); err != nil {
				return err
			}
			var rec revokedRecord
			if err := json.Unmarshal(raw, &rec); err != nil {
				continue
			}
			n, ok := new(big.Int).SetString(rec.Serial, 10)
			if !ok {
				continue
			}
			at := rec.RevokedAt
			if at.IsZero() {
				at = time.Now().UTC()
			}
			if err := yield(serialHex(n), rec.Reason, at); err != nil {
				return err
			}
		}
		return nil
	})
}

func serialHex(n *big.Int) string {
	return strings.ToLower(fmt.Sprintf("%x", n))
}

func collectSANs(c *x509.Certificate) []string {
	out := []string{}
	out = append(out, c.DNSNames...)
	for _, ip := range c.IPAddresses {
		out = append(out, ip.String())
	}
	out = append(out, c.EmailAddresses...)
	for _, u := range c.URIs {
		out = append(out, u.String())
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
