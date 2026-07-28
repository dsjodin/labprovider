package deploy

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeDNS answers every query with the given RCODE, echoing the question so
// Go's resolver accepts the reply. It exists to prove waitForwarderAnswers
// treats "the server said no" as reachable and "the server said nothing" as
// not, which is the whole difference between a lab with internet and a lab
// whose forwarder only serves its own zones.
func fakeDNS(t *testing.T, rcode byte) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if n < 12 {
				continue
			}
			reply := make([]byte, n)
			copy(reply, buf[:n])
			reply[2] |= 0x80        // QR: this is a response
			reply[3] = 0x80 | rcode // RA, plus the rcode under test
			_, _ = pc.WriteTo(reply, addr)
		}
	}()
	return pc.LocalAddr().String()
}

func TestWaitForwarderAnswers(t *testing.T) {
	const (
		rcodeNXDomain = 3
		rcodeRefused  = 5
	)
	// An internal forwarder with no route to the internet answers NXDOMAIN or
	// REFUSED for anything outside its zones. Both mean it is up.
	for name, rcode := range map[string]byte{"NXDOMAIN": rcodeNXDomain, "REFUSED": rcodeRefused} {
		t.Run(name, func(t *testing.T) {
			addr := fakeDNS(t, rcode)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := waitForwarderAnswers(ctx, addr, "lab.local", 2, 100*time.Millisecond); err != nil {
				t.Errorf("waitForwarderAnswers = %v, want nil", err)
			}
		})
	}

	t.Run("no server", func(t *testing.T) {
		// A port nothing is listening on: closed immediately so the address is
		// routable but dead.
		pc, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := pc.LocalAddr().String()
		pc.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		err = waitForwarderAnswers(ctx, addr, "lab.local", 2, 100*time.Millisecond)
		if err == nil {
			t.Fatal("waitForwarderAnswers = nil, want an error for a dead forwarder")
		}
		if !strings.Contains(err.Error(), "DNS_FORWARDER") {
			t.Errorf("error should point at DNS_FORWARDER: %v", err)
		}
	})

	t.Run("bare address gets port 53", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		err := waitForwarderAnswers(ctx, "192.0.2.1", "lab.local", 1, time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), "192.0.2.1:53") {
			t.Errorf("error = %v, want the forwarder reported as 192.0.2.1:53", err)
		}
	})
}
