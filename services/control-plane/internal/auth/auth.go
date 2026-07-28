// Package auth is the control plane's operator login: a file-backed user store
// plus in-memory sessions.
//
// The store is a 0600 JSON file next to state.json, written atomically, holding
// one bcrypt hash per operator. A file rather than a database is deliberate: the
// control plane builds with CGO_ENABLED=0, and one table of users does not
// justify a SQL dependency in a binary whose only other one is a Postgres
// driver. Sessions live in memory and do not survive a restart.
package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// MinPasswordLen is the floor for an operator password. The control plane runs
// as root with the Docker socket mounted, so this is not a low-value login.
const MinPasswordLen = 12

var usernameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,31}$`)

// User is one operator. The hash is bcrypt; the cleartext is never stored.
type User struct {
	Username  string    `json:"username"`
	Hash      string    `json:"hash"`
	CreatedAt time.Time `json:"created_at"`
}

type userFile struct {
	Users []User `json:"users"`
}

// Store is the user database at Path. All methods are safe for concurrent use.
type Store struct {
	Path string

	mu sync.Mutex
}

func (s *Store) load() ([]User, error) {
	b, err := os.ReadFile(s.Path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f userFile
	if err := json.Unmarshal(b, &f); err != nil {
		// Unlike state.json this is not advisory: silently starting fresh would
		// drop every operator back to the unauthenticated setup flow.
		return nil, fmt.Errorf("%s is corrupt: %w", s.Path, err)
	}
	return f.Users, nil
}

// save writes users atomically with 0600, so a crash mid-write cannot leave a
// truncated file that locks every operator out.
func (s *Store) save(users []User) error {
	b, err := json.MarshalIndent(userFile{Users: users}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}

// Empty reports whether any operator exists yet. A true result puts the server
// into the first-run setup flow.
func (s *Store) Empty() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	users, err := s.load()
	if err != nil {
		return false, err
	}
	return len(users) == 0, nil
}

// Usernames returns the configured operators, for the account page.
func (s *Store) Usernames() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	users, err := s.load()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(users))
	for _, u := range users {
		names = append(names, u.Username)
	}
	return names, nil
}

// Create adds an operator. It fails if the name is taken.
func (s *Store) Create(username, password string) error {
	username = strings.TrimSpace(strings.ToLower(username))
	if err := checkUsername(username); err != nil {
		return err
	}
	if err := checkPassword(password); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	users, err := s.load()
	if err != nil {
		return err
	}
	for _, u := range users {
		if u.Username == username {
			return fmt.Errorf("user %q already exists", username)
		}
	}
	users = append(users, User{Username: username, Hash: string(hash), CreatedAt: time.Now().UTC()})
	return s.save(users)
}

// Verify reports whether the credentials match. It always runs a bcrypt
// comparison, so an unknown username costs the same as a wrong password.
func (s *Store) Verify(username, password string) bool {
	username = strings.TrimSpace(strings.ToLower(username))

	s.mu.Lock()
	users, err := s.load()
	s.mu.Unlock()
	if err != nil {
		return false
	}
	// A hash of "" that nothing can match, so the timing of an unknown user
	// looks like the timing of a wrong password.
	hash := "$2a$10$1111111111111111111111111111111111111111111111111111u"
	for _, u := range users {
		if u.Username == username {
			hash = u.Hash
			break
		}
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// ChangePassword rotates one operator's password after checking the old one.
func (s *Store) ChangePassword(username, old, newPass string) error {
	if !s.Verify(username, old) {
		return fmt.Errorf("current password is incorrect")
	}
	if err := checkPassword(newPass); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPass), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	username = strings.TrimSpace(strings.ToLower(username))
	s.mu.Lock()
	defer s.mu.Unlock()
	users, err := s.load()
	if err != nil {
		return err
	}
	for i := range users {
		if users[i].Username == username {
			users[i].Hash = string(hash)
			return s.save(users)
		}
	}
	return fmt.Errorf("user %q not found", username)
}

// Delete removes an operator. The last one cannot be removed: an empty store
// reopens the unauthenticated setup flow.
func (s *Store) Delete(username string) error {
	username = strings.TrimSpace(strings.ToLower(username))

	s.mu.Lock()
	defer s.mu.Unlock()
	users, err := s.load()
	if err != nil {
		return err
	}
	if len(users) <= 1 {
		return fmt.Errorf("cannot remove the last operator")
	}
	kept := make([]User, 0, len(users))
	for _, u := range users {
		if u.Username != username {
			kept = append(kept, u)
		}
	}
	if len(kept) == len(users) {
		return fmt.Errorf("user %q not found", username)
	}
	return s.save(kept)
}

func checkUsername(v string) error {
	if !usernameRe.MatchString(v) {
		return fmt.Errorf("username must be 2-32 characters of a-z, 0-9, dot, dash, or underscore")
	}
	return nil
}

func checkPassword(v string) error {
	if len(v) < MinPasswordLen {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLen)
	}
	// bcrypt silently truncates past 72 bytes, which would make a long password
	// weaker than it looks.
	if len(v) > 72 {
		return fmt.Errorf("password must be at most 72 characters")
	}
	return nil
}
