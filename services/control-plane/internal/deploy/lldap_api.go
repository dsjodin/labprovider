package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// lldapAPI is a minimal client for the LLDAP GraphQL API over the loopback-
// published admin-UI port (the FQDN is dialed at 127.0.0.1, so provisioning
// does not depend on DNS). LLDAP serves plain HTTP on 17170 behind Traefik.
type lldapAPI struct {
	base   string // http://<LLDAP_FQDN>:<LLDAP_UI_PORT>
	token  string // admin JWT from /auth/simple/login
	client *http.Client
}

func newLLDAPAPI(env map[string]string) *lldapAPI {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	client := &http.Client{
		Timeout: 30 * time.Second,
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
	return &lldapAPI{
		base:   fmt.Sprintf("http://%s:%s", env["LLDAP_FQDN"], env["LLDAP_UI_PORT"]),
		client: client,
	}
}

// login authenticates as the admin user and stores the session JWT.
func (a *lldapAPI) login(ctx context.Context, username, password string) error {
	payload := map[string]string{"username": username, "password": password}
	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+"/auth/simple/login", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("LLDAP login as %s returned HTTP %d: %.200s", username, resp.StatusCode, body)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("LLDAP login: bad JSON: %w", err)
	}
	if out.Token == "" {
		return fmt.Errorf("LLDAP login returned no token")
	}
	a.token = out.Token
	return nil
}

// graphql executes a query with variables and decodes data into out (nil to
// discard). GraphQL "errors" in the body fail the call.
func (a *lldapAPI) graphql(ctx context.Context, query string, variables map[string]any, out any) error {
	reqBody := map[string]any{"query": query, "variables": variables}
	b, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+"/api/graphql", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.token)
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("LLDAP GraphQL returned HTTP %d: %.300s", resp.StatusCode, body)
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("LLDAP GraphQL: bad JSON: %w", err)
	}
	if len(envelope.Errors) > 0 {
		return fmt.Errorf("LLDAP GraphQL error: %s", envelope.Errors[0].Message)
	}
	if out != nil && len(envelope.Data) > 0 {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("LLDAP GraphQL: bad data JSON: %w", err)
		}
	}
	return nil
}

type lldapGroup struct {
	ID          int    `json:"id"`
	DisplayName string `json:"displayName"`
	Users       []struct {
		ID string `json:"id"`
	} `json:"users"`
}

// groups returns every group with its members.
func (a *lldapAPI) groups(ctx context.Context) ([]lldapGroup, error) {
	var out struct {
		Groups []lldapGroup `json:"groups"`
	}
	if err := a.graphql(ctx, `{ groups { id displayName users { id } } }`, nil, &out); err != nil {
		return nil, err
	}
	return out.Groups, nil
}

// ensureGroup returns the id of the named group, creating it when absent.
func (a *lldapAPI) ensureGroup(ctx context.Context, groups []lldapGroup, name string) (int, error) {
	for _, g := range groups {
		if strings.EqualFold(g.DisplayName, name) {
			return g.ID, nil
		}
	}
	var out struct {
		CreateGroup lldapGroup `json:"createGroup"`
	}
	q := `mutation($name: String!) { createGroup(name: $name) { id displayName } }`
	if err := a.graphql(ctx, q, map[string]any{"name": name}, &out); err != nil {
		return 0, err
	}
	return out.CreateGroup.ID, nil
}

// userExists reports whether a user id is already present.
func (a *lldapAPI) userExists(ctx context.Context, id string) (bool, error) {
	var out struct {
		Users []struct {
			ID string `json:"id"`
		} `json:"users"`
	}
	if err := a.graphql(ctx, `{ users { id } }`, nil, &out); err != nil {
		return false, err
	}
	for _, u := range out.Users {
		if strings.EqualFold(u.ID, id) {
			return true, nil
		}
	}
	return false, nil
}

// ensureUser creates the user when absent. LLDAP requires id and email.
func (a *lldapAPI) ensureUser(ctx context.Context, id, email, displayName string) error {
	exists, err := a.userExists(ctx, id)
	if err != nil || exists {
		return err
	}
	q := `mutation($user: CreateUserInput!) { createUser(user: $user) { id } }`
	vars := map[string]any{"user": map[string]any{"id": id, "email": email, "displayName": displayName}}
	return a.graphql(ctx, q, vars, nil)
}

// addToGroup adds a user to a group when not already a member (idempotent).
func (a *lldapAPI) addToGroup(ctx context.Context, groups []lldapGroup, userID string, groupID int) error {
	for _, g := range groups {
		if g.ID != groupID {
			continue
		}
		for _, u := range g.Users {
			if strings.EqualFold(u.ID, userID) {
				return nil
			}
		}
	}
	q := `mutation($u: String!, $g: Int!) { addUserToGroup(userId: $u, groupId: $g) { ok } }`
	return a.graphql(ctx, q, map[string]any{"u": userID, "g": groupID}, nil)
}
