package envfile

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// ServiceHash fingerprints the configuration one service is deployed from:
// every schema variable required by that service or by "common", in schema
// order. The deploy engine records it per service so a dependency whose config
// changed since its last successful deploy is redeployed rather than skipped.
//
// Only schema variables are hashed. A service's compose template can read a
// variable the schema does not list for it (the NetBox seeding reads other
// services' FQDNs, for instance), so this is a strong signal rather than a
// complete one - it never reports a change that did not happen, which is what
// makes it safe to gate a skip on.
func ServiceHash(vars map[string]string, service string) string {
	h := sha256.New()
	for _, name := range VariablesFor(service) {
		fmt.Fprintf(h, "%s=%s\n", name, vars[name])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// VariablesFor lists the schema variables one service is deployed from - its
// own plus "common" - in schema order. ServiceHash fingerprints exactly this
// list, and the per-service page shows exactly this list, so the configuration
// an operator reads is the configuration a redeploy is gated on.
func VariablesFor(service string) []string {
	var out []string
	for _, req := range schema {
		for _, svc := range req.RequiredBy {
			if svc == service || svc == "common" {
				out = append(out, req.Name)
				break
			}
		}
	}
	return out
}
