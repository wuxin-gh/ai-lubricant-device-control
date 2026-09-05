// Package creds persists the long-lived device credential (server URL, device
// id, token) returned by POST /pair. Mirrors android/app/.../storage/CredentialStore.kt:
// app-private file, atomic write, 0600 perms.
//
// PLAN.md §1: no default server address is ever bundled; the credential store
// is the only place the server endpoint lives, and it is written by the user
// running `device-control-ios pair`.
package creds

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Credential is the persisted result of a successful pairing.
type Credential struct {
	ServerURL string `json:"server_url"`
	DeviceID  string `json:"device_id"`
	Token     string `json:"token"`
}

// Store is safe for concurrent use.
type Store struct {
	path string
	mu   sync.Mutex
}

// New returns a store backed at path. The file is created lazily on first Save.
func New(path string) *Store {
	return &Store{path: path}
}

// Load reads the credential, or returns (nil, nil) if none exists.
func (s *Store) Load() (*Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read credential %s: %w", s.path, err)
	}
	var c Credential
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse credential %s: %w", s.path, err)
	}
	return &c, nil
}

// Save writes the credential atomically with 0600 perms: write a sibling .tmp,
// then rename over the target (atomic on both POSIX and Windows).
func (s *Store) Save(c Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if dir := filepath.Dir(s.path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Clear removes the credential file.
func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
