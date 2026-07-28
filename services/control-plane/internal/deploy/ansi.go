package deploy

import "strings"

// stripANSI removes terminal control sequences from one log line. Deploy output
// is rendered as plain text in a <pre>, so the colour codes the step CLI and
// docker emit are not interpreted - they show up literally as "[32m[1m" in the
// middle of every line. Carriage-return redraws collapse to the last frame,
// which is what a terminal would have left on screen.
func stripANSI(s string) string {
	s = strings.TrimRight(s, "\r")
	if i := strings.LastIndexByte(s, '\r'); i >= 0 {
		s = s[i+1:]
	}
	if !hasControl(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c == 0x1b:
			i += escLen(s[i:])
		case c == '\t':
			b.WriteByte(c)
			i++
		case c < 0x20 || c == 0x7f:
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// hasControl reports whether s holds anything stripANSI would rewrite. UTF-8
// continuation bytes are all >= 0x80, so multi-byte runes are never touched.
func hasControl(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < 0x20 && c != '\t') || c == 0x7f {
			return true
		}
	}
	return false
}

// escLen returns the length of the escape sequence starting at s[0] (ESC). An
// unterminated sequence consumes the rest of the line rather than leaking its
// parameter bytes as text.
func escLen(s string) int {
	if len(s) < 2 {
		return len(s)
	}
	switch s[1] {
	case '[': // CSI: parameter and intermediate bytes, then a final byte 0x40-0x7e
		for i := 2; i < len(s); i++ {
			if s[i] >= 0x40 && s[i] <= 0x7e {
				return i + 1
			}
		}
		return len(s)
	case ']': // OSC: terminated by BEL or ST (ESC \)
		for i := 2; i < len(s); i++ {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
		}
		return len(s)
	}
	return 2
}
