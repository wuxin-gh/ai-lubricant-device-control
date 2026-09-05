// Package httpapi exposes the northbound HTTP surface: device pairing plus a
// small admin API for driving devices.
//
// The admin API is NOT part of the device-control wire protocol (spec §1 leaves
// it implementation-defined). It exists so a self-hoster can exercise the
// protocol with curl.
package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"device-control/server/internal/hub"
	"device-control/server/internal/protocol"
	"device-control/server/internal/store"
)

// PairingCodeTTL is how long a freshly minted pairing code stays redeemable.
const PairingCodeTTL = 10 * time.Minute

// Server wires the store and hub into HTTP handlers.
type Server struct {
	Store *store.Store
	Hub   *hub.Hub
	Log   *slog.Logger

	// AdminToken guards every /admin route. Required: the admin API can drive
	// a phone, so it is never left open.
	AdminToken string
}

// Routes returns the mux for the whole HTTP surface.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// Device-facing. Auth is the pairing code itself / the device token.
	mux.HandleFunc("POST /pair", s.handlePair)

	// Operator-facing, all behind AdminToken.
	mux.Handle("POST /admin/pairing-codes", s.requireAdmin(s.handleNewPairingCode))
	mux.Handle("GET /admin/devices", s.requireAdmin(s.handleListDevices))
	mux.Handle("POST /admin/devices/{device_id}/call", s.requireAdmin(s.handleCall))
	mux.Handle("DELETE /admin/devices/{device_id}", s.requireAdmin(s.handleRevoke))

	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

// constantTimeEqual compares two secrets without leaking length via timing:
// hashing first makes both operands fixed-width.
func constantTimeEqual(a, b string) bool {
	ha := sha256.Sum256([]byte(a))
	hb := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

// requireAdmin enforces a constant-time bearer check on admin routes.
func (s *Server) requireAdmin(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if s.AdminToken == "" || !constantTimeEqual(got, s.AdminToken) {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "missing or invalid admin token")
			return
		}
		next(w, r)
	})
}

// handlePair redeems a pairing code for long-lived device credentials (spec §11).
func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Code) == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "code is required")
		return
	}

	deviceID, token, err := s.Store.RedeemPairingCode(req.Code)
	if errors.Is(err, store.ErrUnknownCode) {
		// Deliberately does not distinguish unknown from expired.
		writeErr(w, http.StatusForbidden, "invalid_code", "unknown or expired pairing code")
		return
	}
	if err != nil {
		s.Log.Error("redeem pairing code", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal", "could not complete pairing")
		return
	}

	s.Log.Info("device paired", "device_id", deviceID)
	// The token is returned exactly once; only its hash is persisted.
	writeJSON(w, http.StatusOK, map[string]any{
		"device_id":        deviceID,
		"token":            token,
		"protocol_version": protocol.Version,
	})
}

func (s *Server) handleNewPairingCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Label string `json:"label"`
	}
	// Body is optional.
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req)

	code, expires, err := s.Store.NewPairingCode(req.Label, PairingCodeTTL)
	if err != nil {
		s.Log.Error("mint pairing code", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal", "could not mint code")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"code":       code,
		"expires_at": expires.UTC().Format(time.RFC3339),
	})
}

func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	type deviceView struct {
		DeviceID     string   `json:"device_id"`
		Label        string   `json:"label,omitempty"`
		Online       bool     `json:"online"`
		Capabilities []string `json:"capabilities,omitempty"`
		CreatedAt    string   `json:"created_at"`
		LastSeen     string   `json:"last_seen,omitempty"`
	}
	out := []deviceView{}
	for _, d := range s.Store.List() {
		v := deviceView{
			DeviceID:     d.DeviceID,
			Label:        d.Label,
			Online:       s.Hub.IsOnline(d.DeviceID),
			Capabilities: d.Capabilities,
			CreatedAt:    d.CreatedAt.Format(time.RFC3339),
		}
		if !d.LastSeen.IsZero() {
			v.LastSeen = d.LastSeen.Format(time.RFC3339)
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": out})
}

// handleCall sends one protocol `call` to a device and blocks for its response.
func (s *Server) handleCall(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("device_id")

	var req struct {
		Cmd       string          `json:"cmd"`
		Args      json.RawMessage `json:"args"`
		TimeoutMS int             `json:"timeout_ms"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if req.Cmd == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "cmd is required")
		return
	}

	data, callErr, err := s.Hub.Call(r.Context(), deviceID, req.Cmd, req.Args, req.TimeoutMS)
	switch {
	case errors.Is(err, hub.ErrDeviceOffline):
		writeErr(w, http.StatusServiceUnavailable, "device_offline", "device is not connected")
		return
	case errors.Is(err, hub.ErrUnsupported):
		// Spec §8: the server must not send a cmd the device did not declare.
		writeErr(w, http.StatusBadRequest, protocol.ErrUnsupported,
			"device did not declare capability "+req.Cmd)
		return
	case errors.Is(err, hub.ErrOverloaded):
		writeErr(w, http.StatusTooManyRequests, protocol.ErrOverloaded, "too many in-flight calls")
		return
	case err != nil:
		writeErr(w, http.StatusGatewayTimeout, "no_response", err.Error())
		return
	}

	if callErr != nil {
		// The device answered with a structured error — surface it verbatim.
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": callErr})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": json.RawMessage(data)})
}

func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("device_id")
	if err := s.Store.Revoke(deviceID); errors.Is(err, store.ErrNoSuchDevice) {
		writeErr(w, http.StatusNotFound, "not_found", "no such device")
		return
	} else if err != nil {
		s.Log.Error("revoke device", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal", "could not revoke")
		return
	}
	// Drop any live connection so revocation takes effect immediately.
	s.Hub.Disconnect(deviceID, protocol.CloseAuthFailed, "credential revoked")
	writeJSON(w, http.StatusOK, map[string]any{"revoked": deviceID})
}
