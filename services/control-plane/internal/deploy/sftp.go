package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// SFTP deploys SFTPGo, the port of bootstrap/sftp.sh: step-ca cert for the
// HTTPS admin UI, data/home dirs owned by uid 1000, and the optional backup
// user created via the SFTPGo API when all three SFTP_BACKUP_* vars are set.
type SFTP struct{}

func (SFTP) Name() string   { return "sftp" }
func (SFTP) Deps() []string { return []string{"ca"} }

var sftpUsernameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func (s SFTP) Deploy(ctx context.Context, rc *RunCtx) error {
	env := rc.Env

	if err := validateSFTPBackupVars(env); err != nil {
		return err
	}
	if err := requireCAReady(ctx, env); err != nil {
		return err
	}
	if err := IssueCert(ctx, rc, env["SFTP_FQDN"], env["SFTP_CERT_DIR"], "sftpgo"); err != nil {
		return err
	}
	if err := EnsureDir(rc.Workdir("sftpgo"), 0o755, -1, -1); err != nil {
		return err
	}
	for _, dir := range []string{env["SFTP_DATA_DIR"], env["SFTP_HOME_DIR"]} {
		if err := EnsureDir(dir, 0o755, -1, -1); err != nil {
			return err
		}
		if err := chownR(dir, 1000, 1000); err != nil {
			return err
		}
	}
	if err := Render("docker-compose.sftpgo.yml.tpl", env, rc.Workdir("sftpgo")+"/docker-compose.yml", 0o644); err != nil {
		return err
	}
	cmp := rc.Compose("sftpgo")
	if err := cmp.Down(ctx); err != nil {
		return err
	}
	if err := cmp.Up(ctx); err != nil {
		return err
	}

	adminURL := fmt.Sprintf("https://%s:%s/", env["SFTP_FQDN"], env["SFTP_ADMIN_PORT"])
	rc.Log("Waiting for the SFTPGo admin UI at %s.", adminURL)
	caRoot := filepath.Join(env["CA_DATA_DIR"], "certs", "root_ca.crt")
	if err := WaitHTTPSPinned(ctx, adminURL, caRoot, 60, 2*time.Second); err != nil {
		return err
	}
	if err := ensureSFTPBackupUser(ctx, rc); err != nil {
		return err
	}
	rc.Log("SFTPGo is ready: sftp on port %s, admin UI at https://%s:%s/web/admin/login.",
		env["SFTP_PORT"], env["SFTP_FQDN"], env["SFTP_ADMIN_PORT"])
	return nil
}

func (s SFTP) Remove(ctx context.Context, rc *RunCtx) error {
	cmp := rc.Compose("sftpgo")
	if err := cmp.Down(ctx); err != nil {
		return err
	}
	if err := os.RemoveAll(rc.Workdir("sftpgo")); err != nil {
		return err
	}
	rc.Log("Removed SFTPGo containers and runtime files. Persistent data in %s, %s, and %s was preserved.",
		rc.Env["SFTP_DATA_DIR"], rc.Env["SFTP_HOME_DIR"], rc.Env["SFTP_CERT_DIR"])
	return nil
}

// validateSFTPBackupVars enforces all-or-nothing SFTP_BACKUP_* configuration.
func validateSFTPBackupVars(env map[string]string) error {
	user, pass, home := env["SFTP_BACKUP_USERNAME"], env["SFTP_BACKUP_PASSWORD"], env["SFTP_BACKUP_HOME_DIR"]
	if user == "" && pass == "" && home == "" {
		return nil
	}
	if user == "" || pass == "" || home == "" {
		return fmt.Errorf("SFTP_BACKUP_USERNAME, SFTP_BACKUP_PASSWORD, and SFTP_BACKUP_HOME_DIR must all be set to configure the backup user")
	}
	if !sftpUsernameRe.MatchString(user) {
		return fmt.Errorf("SFTP_BACKUP_USERNAME may only contain letters, numbers, dots, underscores, and hyphens")
	}
	if strings.HasPrefix(pass, "CHANGE_ME") {
		return fmt.Errorf("replace placeholder SFTP_BACKUP_PASSWORD before continuing")
	}
	if !strings.HasPrefix(home, env["SFTP_DATA_DIR"]+"/") {
		return fmt.Errorf("SFTP_BACKUP_HOME_DIR must be located under SFTP_DATA_DIR so the SFTPGo container can use it")
	}
	return nil
}

// ensureSFTPBackupUser creates the optional backup user once; existing users
// are left unchanged on later runs.
func ensureSFTPBackupUser(ctx context.Context, rc *RunCtx) error {
	env := rc.Env
	if env["SFTP_BACKUP_USERNAME"] == "" {
		return nil
	}
	if err := EnsureDir(env["SFTP_BACKUP_HOME_DIR"], 0o755, -1, -1); err != nil {
		return err
	}
	if err := chownR(env["SFTP_BACKUP_HOME_DIR"], 1000, 1000); err != nil {
		return err
	}

	client, err := pinnedHTTPSClient(filepath.Join(env["CA_DATA_DIR"], "certs", "root_ca.crt"))
	if err != nil {
		return err
	}
	base := fmt.Sprintf("https://%s:%s", env["SFTP_FQDN"], env["SFTP_ADMIN_PORT"])

	// Basic-auth token request.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v2/token", nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(env["SFTP_ADMIN_USER"], env["SFTP_ADMIN_PASSWORD"])
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("obtain an SFTPGo API token: %w", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("obtain an SFTPGo API token: HTTP %d", resp.StatusCode)
	}
	var tokenResp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil || tokenResp.AccessToken == "" {
		return fmt.Errorf("no access_token in the SFTPGo token response")
	}

	userURL := base + "/api/v2/users/" + env["SFTP_BACKUP_USERNAME"]
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, userURL, nil)
	req.Header.Set("Authorization", "Bearer "+tokenResp.AccessToken)
	resp, err = client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil // existing backup users are left unchanged
	}
	if resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("check SFTPGo backup user %s: HTTP %d", env["SFTP_BACKUP_USERNAME"], resp.StatusCode)
	}

	homeInContainer := "/srv/sftpgo/" + strings.TrimPrefix(env["SFTP_BACKUP_HOME_DIR"], env["SFTP_DATA_DIR"]+"/")
	payload, err := json.Marshal(map[string]any{
		"username":    env["SFTP_BACKUP_USERNAME"],
		"password":    env["SFTP_BACKUP_PASSWORD"],
		"home_dir":    homeInContainer,
		"status":      1,
		"permissions": map[string][]string{"/": {"*"}},
		"filesystem":  map[string]int{"provider": 0},
	})
	if err != nil {
		return err
	}
	req, _ = http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/v2/users", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+tokenResp.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("create SFTPGo backup user %s: HTTP %d", env["SFTP_BACKUP_USERNAME"], resp.StatusCode)
	}
	rc.Log("Created SFTPGo backup user %s.", env["SFTP_BACKUP_USERNAME"])
	return nil
}
