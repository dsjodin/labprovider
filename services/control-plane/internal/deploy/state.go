package deploy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// StateStore persists advisory deploy history at
// /opt/labprovider/control-plane/state.json. Docker remains the source of
// truth for what is running; nothing gates on this file except UI display.
type StateStore struct {
	Path string

	mu sync.Mutex
}

type ServiceState struct {
	LastAction string    `json:"last_action"` // deploy | remove
	Result     string    `json:"result"`      // ok | failed: ...
	At         time.Time `json:"at"`
}

type State struct {
	Services map[string]ServiceState `json:"services"`
}

func (s *StateStore) load() (State, error) {
	st := State{Services: map[string]ServiceState{}}
	b, err := os.ReadFile(s.Path)
	if os.IsNotExist(err) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		// A corrupt advisory file must not block deploys; start fresh.
		return State{Services: map[string]ServiceState{}}, nil
	}
	if st.Services == nil {
		st.Services = map[string]ServiceState{}
	}
	return st, nil
}

// Record updates one service's last action, written atomically.
func (s *StateStore) Record(service, action, result string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.load()
	if err != nil {
		return err
	}
	st.Services[service] = ServiceState{LastAction: action, Result: result, At: time.Now().UTC()}

	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}

// Snapshot returns the current state for the services API.
func (s *StateStore) Snapshot() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, _ := s.load()
	return st
}
