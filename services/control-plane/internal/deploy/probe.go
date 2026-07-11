package deploy

import (
	"context"
	"fmt"
	"net"
	"time"
)

// WaitTCP polls until addr accepts a TCP connection, the Go equivalent of the
// bash readiness loops (attempts x interval, fail with a pointed message).
func WaitTCP(ctx context.Context, addr string, attempts int, interval time.Duration) error {
	var lastErr error
	for i := 0; i < attempts; i++ {
		d := net.Dialer{Timeout: 2 * time.Second}
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err == nil {
			conn.Close()
			return nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
	return fmt.Errorf("%s did not become reachable: %w", addr, lastErr)
}
