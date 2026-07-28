package deploy

import "testing"

func TestDCToDomain(t *testing.T) {
	cases := map[string]string{
		"dc=sddc,dc=lab":    "sddc.lab",
		"dc=example,dc=com": "example.com",
		"dc=a,dc=b,dc=c":    "a.b.c",
		"DC=Sddc,DC=Lab":    "sddc.lab",
		"ou=people,dc=sddc": "sddc",
		"o=corp":            "lab.local",
		"":                  "lab.local",
		"dc=sddc , dc=lab":  "sddc.lab",
	}
	for in, want := range cases {
		if got := dcToDomain(in); got != want {
			t.Errorf("dcToDomain(%q) = %q, want %q", in, got, want)
		}
	}
}
