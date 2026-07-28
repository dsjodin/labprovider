package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	return &Store{Path: filepath.Join(t.TempDir(), "users.json")}
}

func TestCreateAndVerify(t *testing.T) {
	s := testStore(t)
	empty, err := s.Empty()
	if err != nil || !empty {
		t.Fatalf("Empty on a missing file = %v, %v; want true, nil", empty, err)
	}
	if err := s.Create("Operator", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	if empty, _ := s.Empty(); empty {
		t.Error("store still reports empty after Create")
	}
	// Usernames are normalised to lower case, so the login form is not
	// case-sensitive in a way the operator has to remember.
	if !s.Verify("operator", "correct-horse-battery") {
		t.Error("Verify rejected the correct password")
	}
	if !s.Verify("OPERATOR", "correct-horse-battery") {
		t.Error("Verify is case-sensitive on the username")
	}
	if s.Verify("operator", "wrong-password-here") {
		t.Error("Verify accepted a wrong password")
	}
	if s.Verify("nobody", "correct-horse-battery") {
		t.Error("Verify accepted an unknown user")
	}
	if err := s.Create("operator", "another-long-password"); err == nil {
		t.Error("Create accepted a duplicate username")
	}
}

func TestStoreIsNotWorldReadable(t *testing.T) {
	s := testStore(t)
	if err := s.Create("operator", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("users.json mode = %o, want 600", mode)
	}
	// The file holds bcrypt hashes, never the cleartext.
	b, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "correct-horse-battery") {
		t.Error("the cleartext password was written to disk")
	}
}

func TestPasswordAndUsernameRules(t *testing.T) {
	s := testStore(t)
	if err := s.Create("operator", "short"); err == nil {
		t.Error("Create accepted a password below the minimum length")
	}
	if err := s.Create("operator", strings.Repeat("a", 73)); err == nil {
		t.Error("Create accepted a password past bcrypt's 72-byte truncation point")
	}
	for _, name := range []string{"", "a", "has space", "UPPER!", strings.Repeat("x", 33)} {
		if err := s.Create(name, "correct-horse-battery"); err == nil {
			t.Errorf("Create accepted invalid username %q", name)
		}
	}
}

func TestChangePassword(t *testing.T) {
	s := testStore(t)
	if err := s.Create("operator", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangePassword("operator", "wrong-old-password", "new-password-here"); err == nil {
		t.Error("ChangePassword accepted a wrong current password")
	}
	if err := s.ChangePassword("operator", "correct-horse-battery", "short"); err == nil {
		t.Error("ChangePassword accepted a too-short new password")
	}
	if err := s.ChangePassword("operator", "correct-horse-battery", "new-password-here"); err != nil {
		t.Fatal(err)
	}
	if s.Verify("operator", "correct-horse-battery") {
		t.Error("the old password still works")
	}
	if !s.Verify("operator", "new-password-here") {
		t.Error("the new password does not work")
	}
}

func TestDeleteKeepsTheLastOperator(t *testing.T) {
	s := testStore(t)
	if err := s.Create("first", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("first"); err == nil {
		t.Error("Delete removed the only operator, which would reopen /setup")
	}
	if err := s.Create("second", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("first"); err != nil {
		t.Fatal(err)
	}
	names, err := s.Usernames()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "second" {
		t.Errorf("Usernames = %v, want [second]", names)
	}
}

func TestCorruptStoreIsAnError(t *testing.T) {
	s := testStore(t)
	if err := os.WriteFile(s.Path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Regenerating the file the way state.json does would silently drop every
	// operator back to the unauthenticated setup flow.
	if _, err := s.Empty(); err == nil {
		t.Error("a corrupt store reported no error")
	}
}

func TestSessions(t *testing.T) {
	s := NewSessions(time.Hour)
	token, err := s.Create("operator")
	if err != nil {
		t.Fatal(err)
	}
	if user, ok := s.User(token); !ok || user != "operator" {
		t.Errorf("User = %q, %v; want operator, true", user, ok)
	}
	if _, ok := s.User("not-a-token"); ok {
		t.Error("an unknown token was accepted")
	}
	if _, ok := s.User(""); ok {
		t.Error("an empty token was accepted")
	}

	other, err := s.Create("operator")
	if err != nil {
		t.Fatal(err)
	}
	if other == token {
		t.Error("two sessions got the same token")
	}
	s.Delete(token)
	if _, ok := s.User(token); ok {
		t.Error("a deleted session still resolves")
	}

	// A password change must end the operator's other sessions.
	s.DeleteUser("operator")
	if _, ok := s.User(other); ok {
		t.Error("DeleteUser left a session behind")
	}
}

func TestSessionsExpire(t *testing.T) {
	s := NewSessions(time.Millisecond)
	token, err := s.Create("operator")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, ok := s.User(token); ok {
		t.Error("an expired session still resolves")
	}
}
