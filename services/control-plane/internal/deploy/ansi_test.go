package deploy

import "testing"

func TestStripANSI(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain", "Issuing certificate for *.lab.io.", "Issuing certificate for *.lab.io."},
		{"step cli colours", "\x1b[32m\x1b[32m\u2714\x1b[0m \x1b[1mCA\x1b[0m\x1b[1m:\x1b[0m https://ca.lab.io:9000", "\u2714 CA: https://ca.lab.io:9000"},
		{"osc title", "\x1b]0;docker\x07up", "up"},
		{"osc st terminated", "\x1b]0;docker\x1b\\up", "up"},
		{"unterminated csi", "done \x1b[32", "done "},
		{"trailing cr", "line\r", "line"},
		{"progress redraw", "pulling 10%\rpulling 90%\rdone", "done"},
		{"crlf keeps content", "line\r\r\r", "line"},
		{"tab kept", "a\tb", "a\tb"},
		{"multibyte kept", "\u2714 \u00e5\u00e4\u00f6", "\u2714 \u00e5\u00e4\u00f6"},
		{"backspace dropped", "a\bb", "ab"},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripANSI(c.in); got != c.want {
				t.Errorf("stripANSI(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestEmitStripsANSI(t *testing.T) {
	r := newRun(1, []string{"traefik"}, false)
	r.emit(Event{Type: "log", Service: "traefik", Line: "\x1b[32m\u2714\x1b[0m \x1b[1mCertificate\x1b[0m: /certs/wildcard.crt"})
	replay, _ := r.Subscribe()
	if len(replay) != 1 {
		t.Fatalf("got %d events, want 1", len(replay))
	}
	want := "\u2714 Certificate: /certs/wildcard.crt"
	if replay[0].Line != want {
		t.Errorf("Line = %q, want %q", replay[0].Line, want)
	}
}
