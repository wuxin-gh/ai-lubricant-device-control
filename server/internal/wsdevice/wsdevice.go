// Package wsdevice implements the device-facing WebSocket endpoint: the
// register handshake (spec §4), frame routing (§3.1), and liveness reaping (§7).
package wsdevice

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"device-control/server/internal/hub"
	"device-control/server/internal/protocol"
	"device-control/server/internal/store"
)

// Handler serves the device WebSocket endpoint.
type Handler struct {
	Store *store.Store
	Hub   *hub.Hub
	Log   *slog.Logger
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// No CompressionMode override: the library negotiates
		// permessage-deflate by default, which spec §2 asks for.
	})
	if err != nil {
		h.Log.Warn("ws accept failed", "err", err, "remote", r.RemoteAddr)
		return
	}
	ws.SetReadLimit(protocol.MaxFrameBytes) // spec §2

	// Everything below runs on this one goroutine until register succeeds.
	h.serve(r.Context(), ws, r.RemoteAddr)
}

func (h *Handler) serve(ctx context.Context, ws *websocket.Conn, remote string) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	reg, err := h.awaitRegister(ctx, ws, remote)
	if err != nil {
		return // awaitRegister has already closed with the right code
	}

	sessionID := protocol.NewID("ses_")
	c := hub.NewConn(h.Hub, ws, reg.DeviceID, sessionID, reg.Capabilities)

	if err := wsjson.Write(ctx, ws, protocol.Registered{
		Type:                 protocol.TypeRegistered,
		ProtocolVersion:      protocol.Version,
		DeviceID:             reg.DeviceID,
		ServerTime:           time.Now().UTC().Format(time.RFC3339),
		SessionID:            sessionID,
		HeartbeatIntervalS:   protocol.HeartbeatIntervalSec,
		HeartbeatTimeoutS:    protocol.HeartbeatTimeoutSec,
		AcceptedCapabilities: reg.Capabilities,
	}); err != nil {
		h.Log.Warn("write registered failed", "device_id", reg.DeviceID, "err", err)
		return
	}

	// Registered: only now may the server send calls (spec §4.2).
	h.Hub.Add(c)
	h.Store.UpdateCapabilities(reg.DeviceID, reg.Capabilities)
	h.Log.Info("device registered",
		"device_id", reg.DeviceID, "session_id", sessionID,
		"capabilities", len(reg.Capabilities), "remote", remote)

	defer func() {
		h.Hub.Remove(c)
		h.Log.Info("device disconnected", "device_id", reg.DeviceID, "session_id", sessionID)
	}()

	go h.reap(ctx, c)
	h.readLoop(ctx, c, ws)
}

// awaitRegister reads and validates the first frame, which must be `register`
// within RegisterTimeoutSec (spec §4.1).
func (h *Handler) awaitRegister(ctx context.Context, ws *websocket.Conn, remote string) (*protocol.Inbound, error) {
	regCtx, cancel := context.WithTimeout(ctx, protocol.RegisterTimeoutSec*time.Second)
	defer cancel()

	var in protocol.Inbound
	if err := wsjson.Read(regCtx, ws, &in); err != nil {
		// Distinguish "took too long" from "sent garbage": the former is 4008,
		// the latter is a normal protocol error close.
		if errors.Is(regCtx.Err(), context.DeadlineExceeded) {
			_ = ws.Close(protocol.CloseRegisterTimeout, "no register within deadline")
		} else {
			_ = ws.Close(websocket.StatusProtocolError, "unreadable first frame")
		}
		h.Log.Warn("register handshake failed", "err", err, "remote", remote)
		return nil, err
	}

	if in.Type != protocol.TypeRegister {
		_ = ws.Close(websocket.StatusProtocolError, "first frame must be register")
		return nil, errors.New("first frame was " + in.Type)
	}

	// Version is server-authoritative (spec §14). A device omitting the field is
	// rejected rather than assumed compatible.
	if in.ProtocolVersion == nil || *in.ProtocolVersion != protocol.Version {
		_ = ws.Close(protocol.CloseVersionUnsupported, "unsupported protocol_version")
		return nil, errors.New("protocol version mismatch")
	}

	if in.Auth == nil || in.Auth.Scheme != protocol.SchemeToken {
		// Unknown scheme is an auth failure, not a version failure (spec §11).
		_ = ws.Close(protocol.CloseAuthFailed, "unsupported auth scheme")
		return nil, errors.New("bad auth scheme")
	}
	if in.DeviceID == "" || !h.Store.Authenticate(in.DeviceID, in.Auth.Token) {
		_ = ws.Close(protocol.CloseAuthFailed, "invalid credentials")
		h.Log.Warn("device auth failed", "device_id", in.DeviceID, "remote", remote)
		return nil, errors.New("auth failed")
	}
	return &in, nil
}

// readLoop dispatches inbound frames until the connection ends.
func (h *Handler) readLoop(ctx context.Context, c *hub.Conn, ws *websocket.Conn) {
	for {
		var in protocol.Inbound
		if err := wsjson.Read(ctx, ws, &in); err != nil {
			return // includes normal closure
		}
		c.Touch() // any frame is liveness evidence (spec §7)

		switch in.Type {
		case protocol.TypeHeartbeat:
			// Nothing to do: Touch already recorded it and heartbeats are
			// unacked (spec §10).

		case protocol.TypeCallResponse:
			h.handleCallResponse(c, &in)

		case protocol.TypeEvent:
			h.handleEvent(c, &in)

		case protocol.TypeRegister:
			// A second register on one connection is fatal (spec §4.3).
			c.Close(protocol.CloseDuplicateRegister, "duplicate register")
			return

		default:
			// Unknown type: ignore. This is the forward-compat hinge (spec §3)
			// and must not be an error.
			h.Log.Debug("ignoring unknown frame type", "type", in.Type, "device_id", c.DeviceID)
		}
	}
}

func (h *Handler) handleCallResponse(c *hub.Conn, in *protocol.Inbound) {
	if in.RequestID == "" || in.OK == nil {
		h.Log.Warn("malformed call-response", "device_id", c.DeviceID)
		return
	}
	if *in.OK {
		data := in.Data
		if data == nil {
			data = json.RawMessage(`{}`) // data is required when ok=true (§3.1)
		}
		c.DeliverOK(in.RequestID, data)
		return
	}
	e := in.Error
	if e == nil {
		// ok=false without error violates §3.1; synthesise one so the waiter
		// gets a structured answer instead of hanging.
		e = &protocol.Error{Code: protocol.ErrDeviceError, Message: "device reported failure without error object"}
	}
	c.DeliverErr(in.RequestID, e)
}

func (h *Handler) handleEvent(c *hub.Conn, in *protocol.Inbound) {
	switch in.Kind {
	case protocol.EventCapabilitiesChanged:
		var d struct {
			Capabilities []string `json:"capabilities"`
		}
		if err := json.Unmarshal(in.Data, &d); err != nil {
			h.Log.Warn("bad capabilities-changed payload", "device_id", c.DeviceID, "err", err)
			return
		}
		c.SetCapabilities(d.Capabilities)
		h.Store.UpdateCapabilities(c.DeviceID, d.Capabilities)
		h.Log.Info("capabilities changed", "device_id", c.DeviceID, "count", len(d.Capabilities))

	case protocol.EventControlRevoked:
		// The owner turned control off on the device. Treat the device as
		// uncontrollable until it registers again (spec §9).
		h.Log.Warn("control revoked by device owner", "device_id", c.DeviceID)
		c.Close(websocket.StatusNormalClosure, "control revoked on device")

	default:
		// Unknown event kinds are ignored (spec §9).
		h.Log.Debug("ignoring unknown event kind", "kind", in.Kind, "device_id", c.DeviceID)
	}
}

// reap closes connections that have gone silent past the heartbeat timeout
// (spec §7). It checks at a fraction of the timeout so detection lag is bounded.
func (h *Handler) reap(ctx context.Context, c *hub.Conn) {
	timeout := protocol.HeartbeatTimeoutSec * time.Second
	ticker := time.NewTicker(timeout / 4)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if time.Since(c.LastSeen()) > timeout {
				h.Log.Warn("reaping stale device", "device_id", c.DeviceID,
					"silent_for", time.Since(c.LastSeen()).Round(time.Second))
				c.Close(protocol.CloseStale, "heartbeat timeout")
				return
			}
		}
	}
}
