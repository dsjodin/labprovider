package netbox

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/dsjodin/labprovider/services/dns-sync/internal/model"
)

const pageSize = 200

// Client reads NetBox IPAM and produces the desired DNS record set. NetBox is
// the source of truth per technitium-dns_design.md sec 4.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// New builds a Client. caBundle may be "" to use the system trust store; a
// non-empty path is loaded as an additional PEM bundle (use this to trust the
// step-ca root that issued NetBox's cert).
func New(baseURL, token, caBundle string) (*Client, error) {
	if baseURL == "" {
		return nil, errors.New("netbox base url is required")
	}
	if token == "" {
		return nil, errors.New("netbox token is required")
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}
	if caBundle != "" {
		pem, err := os.ReadFile(caBundle)
		if err != nil {
			return nil, fmt.Errorf("read netbox CA bundle %s: %w", caBundle, err)
		}
		pool, _ := x509.SystemCertPool()
		if pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates parsed from %s", caBundle)
		}
		tr.TLSClientConfig.RootCAs = pool
	}
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Transport: tr, Timeout: 30 * time.Second},
	}, nil
}

// authHeader builds the Authorization header for the stored token. NetBox 4.6
// v2 tokens are stored as the full composite "nbt_<key>.<token>" and sent as
// a Bearer credential; anything else is a legacy v1 token.
func (c *Client) authHeader() string {
	if strings.HasPrefix(c.Token, "nbt_") {
		return "Bearer " + c.Token
	}
	return "Token " + c.Token
}

// errorBody reads up to ~200 chars of a non-2xx response body (NetBox puts
// the reason in a "detail" field) so errors are diagnosable from logs.
func errorBody(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 200))
	return strings.TrimSpace(string(b))
}

type ipAddress struct {
	Address string `json:"address"`
	DNSName string `json:"dns_name"`
}

type ipListResp struct {
	Next    *string     `json:"next"`
	Results []ipAddress `json:"results"`
}

// Desired returns the desired forward + PTR record set computed from NetBox.
// It implements reconcile.Source.
func (c *Client) Desired(ctx context.Context) ([]model.Record, error) {
	ips, err := c.listIPs(ctx)
	if err != nil {
		return nil, err
	}
	return buildRecords(ips), nil
}

func (c *Client) listIPs(ctx context.Context) ([]ipAddress, error) {
	out := []ipAddress{}
	next := fmt.Sprintf("%s/api/ipam/ip-addresses/?limit=%d", c.BaseURL, pageSize)
	for next != "" {
		page, err := c.getPage(ctx, next)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Results...)
		if page.Next != nil {
			next = *page.Next
		} else {
			next = ""
		}
	}
	return out, nil
}

func (c *Client) getPage(ctx context.Context, url string) (*ipListResp, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", c.authHeader())
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("netbox GET: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("netbox GET %s: status %d body=%s", url, resp.StatusCode, errorBody(resp.Body))
	}
	var out ipListResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode netbox response: %w", err)
	}
	return &out, nil
}

// buildRecords converts NetBox IP rows into the desired record set. One A
// record per (dns_name, IP); one PTR per IP using a deterministically-chosen
// canonical name when more than one dns_name targets the same IP.
func buildRecords(ips []ipAddress) []model.Record {
	type ipName struct {
		addr netip.Addr
		name string
	}
	var pairs []ipName
	for _, ip := range ips {
		name := model.NormalizeFQDN(ip.DNSName)
		if name == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(ip.Address)
		if err != nil {
			continue
		}
		addr := prefix.Addr()
		if !addr.Is4() {
			// Scaffold scope: A + PTR only (no AAAA). See design sec 7.
			continue
		}
		pairs = append(pairs, ipName{addr: addr, name: name})
	}

	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].addr != pairs[j].addr {
			return pairs[i].addr.Less(pairs[j].addr)
		}
		return pairs[i].name < pairs[j].name
	})

	records := make([]model.Record, 0, len(pairs)*2)
	canonical := map[netip.Addr]string{}
	for _, p := range pairs {
		records = append(records, model.Record{
			Zone: model.ForwardZoneFor(p.name),
			Name: p.name,
			Type: "A",
			Data: p.addr.String(),
		})
		if _, ok := canonical[p.addr]; !ok {
			canonical[p.addr] = p.name
		}
	}
	for addr, name := range canonical {
		records = append(records, model.Record{
			Zone: model.ReverseZoneFor(addr),
			Name: model.PTRNameFor(addr),
			Type: "PTR",
			Data: name,
		})
	}

	sort.Slice(records, func(i, j int) bool { return records[i].Key() < records[j].Key() })
	return records
}
