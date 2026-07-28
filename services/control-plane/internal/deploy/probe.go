package deploy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// retry runs fn up to attempts times, interval apart, until it returns nil.
//
// This is one helper in the package that already owns probing, not an
// abstraction layer. It replaces six copies of the same eighteen lines, each of
// which carried its own ctx.Done() arm - and the cancellation arm is the part
// that is easy to get wrong, because getting it wrong means a cancelled deploy
// keeps polling for the rest of its budget. "Three similar lines is better than
// a premature abstraction" still holds; six copies of eighteen lines with a
// cancellation path in each is the other side of that trade.
//
// what names the thing being waited on, and appears in the failure.
func retry(ctx context.Context, attempts int, interval time.Duration, what string, fn func(context.Context) error) error {
	var lastErr error
	for i := 0; i < attempts; i++ {
		if lastErr = fn(ctx); lastErr == nil {
			return nil
		}
		// Nothing to wait for after the final attempt. Waiting anyway made the
		// outcome a coin flip whenever fn had already consumed the whole
		// context: both select arms were ready, so an exhausted retry returned
		// either the bare ctx.Err() or the message naming what failed.
		if i == attempts-1 {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
	return fmt.Errorf("%s did not become ready: %w", what, lastErr)
}

// WaitTCP polls until addr accepts a TCP connection, the Go equivalent of the
// bash readiness loops (attempts x interval, fail with a pointed message).
func WaitTCP(ctx context.Context, addr string, attempts int, interval time.Duration) error {
	return retry(ctx, attempts, interval, addr, func(ctx context.Context) error {
		d := net.Dialer{Timeout: 2 * time.Second}
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return err
		}
		return conn.Close()
	})
}

// pinnedDialer keeps the host from the URL (so TLS SNI and virtual hosting
// still work) but connects to 127.0.0.1 on the same port. Every service in a
// labprovider deployment runs on this one host, so the loopback is reachable
// before DNS is, which is what makes deploy-time probing possible at all.
func pinnedDialer(timeout time.Duration) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: timeout}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
	}
}

// waitHTTPPinned polls a plain-HTTP url whose host is pinned to 127.0.0.1
// until it answers with a status < 500. Redirects are not followed: a service
// fronted by Traefik commonly 3xx-redirects HTTP to its public HTTPS URL, and
// following that would hit Traefik's step-ca-signed wildcard, which this client
// does not trust - a 3xx already means the backend is up.
func waitHTTPPinned(ctx context.Context, url string, attempts int, interval time.Duration) error {
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{DialContext: pinnedDialer(3 * time.Second)},
	}
	// Every probe built a fresh client and transport and never closed the pool,
	// leaving one connection pool per call for the GC. A deploy runs dozens.
	defer client.CloseIdleConnections()
	return retryHTTP(ctx, client, url, attempts, interval)
}

// retryHTTP polls url until it answers with a status under 500. A 5xx is the
// service answering while still starting, so it keeps waiting; anything below
// that means the backend is up. Shared by the plain-HTTP and pinned-HTTPS
// probes, which differ only in how their client is built.
func retryHTTP(ctx context.Context, client *http.Client, url string, attempts int, interval time.Duration) error {
	return retry(ctx, attempts, interval, url, func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode >= 500 {
			return fmt.Errorf("status %d", resp.StatusCode)
		}
		return nil
	})
}
