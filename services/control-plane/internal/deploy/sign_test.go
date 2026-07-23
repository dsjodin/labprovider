package deploy

import (
	"context"
	"strings"
	"testing"
)

// A real CSR that omits the mandatory PKCS#10 attributes [0] field. OpenSSL
// accepts it; Go's x509 parser (and step-ca) reject it with "sequence
// truncated". SignCSR must surface an actionable message instead.
const csrMissingAttrs = `-----BEGIN CERTIFICATE REQUEST-----
MIICzzCCAbkCAQAwgY0xgYowGQYDVQQDDBJ0ZXN0LnN0b3JhdmFsbGEuc2UwFAYD
VQQLDA1JVCBEZXBhcnRtZW50MBQGA1UECgwNU3RvcmF2YWxsYSBBQjAOBgNVBAcM
B09lcmVicm8wDQYDVQQIDAZPZXJicm8wCQYDVQQGEwJTRTAXBgkqhkiG9w0BCQEW
CmRzQHRlc3Quc2UwggEiMA0GCSqGSIb3DQEBAQUAA4IBDwAwggEKAoIBAQDO13MT
NWvlEWJH3Tuoal48S3BrfFHEAEo8Z++TqjwjwHbkwMk3D2b0B3nqQzuJdMq2Ao2o
kmQxybJoCQBPdQ6+s7XIBsCsxsgRv25Kmj+9sioijh8MJ21Bb6smBu2O0mwr44Zl
4JdS6fWmSuSGKfqRtUofXZfGE34GdSxxkuXRs5v1igs3orlo1FEbjU+HvnyiOTMg
JcjKYXB0ypvmErjemLir66gKqw7Bc28iQu6+M351COPxago5KajO4hvCT8za6jCP
ZLAxLGMK385zBTmYW20QfUn2rua0pGTSmVkyPBY6WNIwLg5+UcygpK8W8jPLJDYJ
jiuE2MYLCWqgLSsRAgMBAAEwCwYJKoZIhvcNAQELA4IBAQDOqb1aWpzYx7K+MQTr
z0wXRgTa173atnHm2erkyQLNBTj5SjrUpLZfhXkd78AttnNHD2rWzRyIu5OiPsHy
yilJdaFjALI5LsqQeNR/YHRaEJ4OKf/gfgdm24jIORGOSUdk3+pCutZgCR++NErI
8t1X0ag70WB3RQ56b2PlKVZqkujwL0lDPf9PFVsJycY2/0DAd9h3uZoZbHif9jvZ
TVj9mJmYYGhgD6QiuP4Dy152s+3jVsG468lqw1M4W1xpPhSMqiog+qKC9cnK3Zt/
lnj2zEhDrhU91uGg6IBiwO186r9arH5TvnNN9OcOr9MyUTT3FPjAiqDxuKNPkpKy
MP5R
-----END CERTIFICATE REQUEST-----
`

func TestSignCSRMissingAttributes(t *testing.T) {
	_, err := SignCSR(context.Background(), map[string]string{}, []byte(csrMissingAttrs))
	if err == nil {
		t.Fatal("expected error for CSR missing attributes field")
	}
	if !strings.Contains(err.Error(), "mandatory PKCS#10 attributes field") {
		t.Fatalf("expected attributes-field diagnostic, got: %v", err)
	}
	if strings.Contains(err.Error(), "sequence truncated") {
		t.Fatalf("opaque asn1 error leaked to user: %v", err)
	}
}

func TestSignCSRNotPEM(t *testing.T) {
	_, err := SignCSR(context.Background(), map[string]string{}, []byte("not a csr"))
	if err == nil || !strings.Contains(err.Error(), "not a PEM-encoded") {
		t.Fatalf("expected PEM error, got: %v", err)
	}
}
