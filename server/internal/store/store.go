// Package store persists device credentials and holds short-lived pairing codes.
//
// Tokens and pairing codes are stored only as SHA-256 hashes: a leaked state
// file does not yield usable credentials. Comparison is constant-time.
package store

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	// ErrUnknownCode covers both "never existed" and "expired" — the caller
	// must not be able to distinguish them.
	ErrUnknownCode  = errors.New("unknown or expired pairing code")
	ErrNoSuchDevice = errors.New("no such device")
)

// codeAlphabet omits I/O/0/1 so codes survive being read aloud or retyped.
const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

const codeLen = 8

// Device is a paired device's persisted record.
type Device struct {
	DeviceID     string    `json:"device_id"`
	TokenSHA256  string    `json:"token_sha256"`
	Label        string    `json:"label,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	Capabilities []string  `json:"capabilities,omitempty"`
	LastSeen     time.Time `json:"last_seen,omitempty"`
}

type pendingCode struct {
	label     string
	expiresAt time.Time
}

// Store is safe for concurrent use.
type Store struct {
	path string

	mu      sync.RWMutex
	devices map[string]*Device
	// codes maps a pairing code's hash to its pending record. In-memory only:
	// codes are short-lived, so losing them on restart is correct behaviour.
	codes map[string]pendingCode
}

type fileState struct {
	Devices []*Device `json:"devices"`
}

// Open loads the state file, creating an empty store if it does not exist.
func Open(path string) (*Store, error) {
	s := &Store{
		path:    path,
		devices: map[string]*Device{},
		codes:   map[string]pendingCode{},
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	var st fileState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("parse state %s: %w", path, err)
	}
	for _, d := range st.Devices {
		s.devices[d.DeviceID] = d
	}
	return s, nil
}

// saveLocked writes the state file atomically with 0600 perms. Caller holds mu.
func (s *Store) saveLocked() error {
	st := fileState{}
	for _, d := range s.devices {
		st.Devices = append(st.Devices, d)
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(s.path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	// os.Rename replaces an existing target on both POSIX and Windows.
	return os.Rename(tmp, s.path)
}

func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// NewPairingCode mints a pairing code valid for ttl. The plaintext is returned
// once and never stored.
func (s *Store) NewPairingCode(label string, ttl time.Duration) (string, time.Time, error) {
	raw := make([]byte, codeLen)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	var sb strings.Builder
	for i, b := range raw {
		if i == codeLen/2 {
			sb.WriteByte('-')
		}
		sb.WriteByte(codeAlphabet[int(b)%len(codeAlphabet)])
	}
	code := sb.String()
	expires := time.Now().Add(ttl)

	s.mu.Lock()
	defer s.mu.Unlock()
	// Opportunistically drop expired codes so the map cannot grow unbounded.
	now := time.Now()
	for h, p := range s.codes {
		if now.After(p.expiresAt) {
			delete(s.codes, h)
		}
	}
	s.codes[hashString(NormalizeCode(code))] = pendingCode{label: label, expiresAt: expires}
	return code, expires, nil
}

// NormalizeCode makes codes forgiving to type: case and separators are ignored.
func NormalizeCode(code string) string {
	var sb strings.Builder
	for _, r := range strings.ToUpper(code) {
		if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// RedeemPairingCode consumes a code and returns fresh credentials. The token
// plaintext is returned once; only its hash is persisted.
func (s *Store) RedeemPairingCode(code string) (deviceID, token string, err error) {
	h := hashString(NormalizeCode(code))

	s.mu.Lock()
	defer s.mu.Unlock()

	p, ok := s.codes[h]
	if !ok || time.Now().After(p.expiresAt) {
		delete(s.codes, h)
		return "", "", ErrUnknownCode
	}
	delete(s.codes, h) // single use

	tb := make([]byte, 32)
	if _, err := rand.Read(tb); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(tb)
	deviceID = protocolNewDeviceID()

	s.devices[deviceID] = &Device{
		DeviceID:    deviceID,
		TokenSHA256: hashString(token),
		Label:       p.label,
		CreatedAt:   time.Now().UTC(),
	}
	if err := s.saveLocked(); err != nil {
		delete(s.devices, deviceID)
		return "", "", err
	}
	return deviceID, token, nil
}

// protocolNewDeviceID is separated so store does not import protocol (which
// would be a cycle once protocol grows store-aware helpers).
func protocolNewDeviceID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return "dev_" + base64.RawURLEncoding.EncodeToString(b)
}

// Authenticate reports whether (deviceID, token) is a valid credential pair.
// It is constant-time in the token comparison and does not leak whether the
// device exists via timing shape beyond the map lookup.
func (s *Store) Authenticate(deviceID, token string) bool {
	s.mu.RLock()
	d, ok := s.devices[deviceID]
	s.mu.RUnlock()
	if !ok {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(d.TokenSHA256), []byte(hashString(token))) == 1
}

// UpdateCapabilities records the capability set a device declared.
func (s *Store) UpdateCapabilities(deviceID string, caps []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[deviceID]
	if !ok {
		return
	}
	d.Capabilities = caps
	d.LastSeen = time.Now().UTC()
	_ = s.saveLocked()
}

// List returns a copy of all device records.
func (s *Store) List() []Device {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Device, 0, len(s.devices))
	for _, d := range s.devices {
		out = append(out, *d)
	}
	return out
}

// Revoke deletes a device's credential.
func (s *Store) Revoke(deviceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.devices[deviceID]; !ok {
		return ErrNoSuchDevice
	}
	delete(s.devices, deviceID)
	return s.saveLocked()
}
