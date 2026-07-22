package deploy

import (
	"context"
	"fmt"
	"net"
	"net/http"
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

// waitHTTPPinned polls a plain-HTTP url whose host is pinned to 127.0.0.1
// until it answers with a status < 500.
func waitHTTPPinned(ctx context.Context, url string, attempts int, interval time.Duration) error {
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				_, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				return dialer.DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
			},
		},
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				return nil
			}
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
	return fmt.Errorf("%s did not become ready: %w", url, lastErr)
}
