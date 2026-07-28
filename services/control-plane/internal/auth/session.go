package auth

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

// CookieName is the session cookie the server sets.
const CookieName = "labprovider_session"

type session struct {
	user    string
	expires time.Time
}

// Sessions is an in-memory session table. Restarting the control plane logs
// everyone out, which is the trade for not needing a database.
type Sessions struct {
	ttl time.Duration

	mu sync.Mutex
	m  map[string]session
}

func NewSessions(ttl time.Duration) *Sessions {
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	return &Sessions{ttl: ttl, m: map[string]session{}}
}

// Create issues a token for user.
func (s *Sessions) Create(user string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep()
	s.m[token] = session{user: user, expires: time.Now().Add(s.ttl)}
	return token, nil
}

// User returns the session's operator and extends its lifetime, so an active
// operator is not logged out mid-deploy.
func (s *Sessions) User(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[token]
	if !ok || time.Now().After(sess.expires) {
		delete(s.m, token)
		return "", false
	}
	sess.expires = time.Now().Add(s.ttl)
	s.m[token] = sess
	return sess.user, true
}

func (s *Sessions) Delete(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, token)
}

// DeleteUser drops every session for one operator, so a password change ends
// any session opened with the old one.
func (s *Sessions) DeleteUser(user string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for token, sess := range s.m {
		if sess.user == user {
			delete(s.m, token)
		}
	}
}

// sweep drops expired entries. Callers hold s.mu.
func (s *Sessions) sweep() {
	now := time.Now()
	for token, sess := range s.m {
		if now.After(sess.expires) {
			delete(s.m, token)
		}
	}
}
