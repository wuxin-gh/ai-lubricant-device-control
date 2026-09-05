// Package hub tracks connected devices and routes calls to them.
//
// Concurrency model: each device connection owns one goroutine that reads
// frames, plus one that writes. Hub state is guarded by a mutex; per-connection
// state lives in conn and is only touched under conn.mu. Nothing here blocks on
// a device: a slow or wedged device must not stall the hub or other devices.
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"device-control/server/internal/protocol"
)

var (
	ErrDeviceOffline = errors.New("device not connected")
	ErrOverloaded    = errors.New("device has too many in-flight calls")
	ErrUnsupported   = errors.New("device did not declare this capability")
)

// pending is one outstanding call awaiting its call-response.
type pending struct {
	ch chan *result
}

type result struct {
	Data json.RawMessage
	Err  *protocol.Error
}

// Conn is one registered device connection.
type Conn struct {
	DeviceID     string
	SessionID    string
	Capabilities []string
	ConnectedAt  time.Time

	ws  *websocket.Conn
	hub *Hub

	// writeMu serialises writes: a websocket connection supports only one
	// concurrent writer, and calls can be issued from many HTTP handlers.
	writeMu sync.Mutex

	mu       sync.Mutex
	pending  map[string]*pending
	lastSeen time.Time
	closed   bool
}

// Hub is safe for concurrent use.
type Hub struct {
	mu    sync.RWMutex
	conns map[string]*Conn // by device_id; at most one connection per device
}

func New() *Hub {
	return &Hub{conns: map[string]*Conn{}}
}

// Add registers a connection, evicting any prior connection for the same
// device with close 4009 (spec §4.4, last-writer-wins). Eviction happens in a
// goroutine so a wedged old connection cannot block the new one's handshake.
func (h *Hub) Add(c *Conn) {
	h.mu.Lock()
	old := h.conns[c.DeviceID]
	h.conns[c.DeviceID] = c
	h.mu.Unlock()

	if old != nil && old != c {
		go old.closeWith(protocol.CloseReplaced, "replaced by newer connection")
	}
}

// Remove drops a connection, but only if it is still the registered one — a
// late cleanup from an evicted connection must not unregister its replacement.
func (h *Hub) Remove(c *Conn) {
	h.mu.Lock()
	if h.conns[c.DeviceID] == c {
		delete(h.conns, c.DeviceID)
	}
	h.mu.Unlock()
}

// Get returns the live connection for a device, if any.
func (h *Hub) Get(deviceID string) (*Conn, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	c, ok := h.conns[deviceID]
	return c, ok
}

// Online lists currently connected device ids.
func (h *Hub) Online() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]string, 0, len(h.conns))
	for id := range h.conns {
		out = append(out, id)
	}
	return out
}

// NewConn wraps an authenticated, registered websocket.
func NewConn(hub *Hub, ws *websocket.Conn, deviceID, sessionID string, caps []string) *Conn {
	return &Conn{
		DeviceID:     deviceID,
		SessionID:    sessionID,
		Capabilities: caps,
		ConnectedAt:  time.Now(),
		ws:           ws,
		hub:          hub,
		pending:      map[string]*pending{},
		lastSeen:     time.Now(),
	}
}

// Supports reports whether cmd was declared in the device's capabilities.
// The server must never send an undeclared cmd (spec §8).
func (c *Conn) Supports(cmd string) bool {
	c.mu.Lock()
	caps := c.Capabilities
	c.mu.Unlock()
	for _, s := range caps {
		if s == cmd {
			return true
		}
	}
	return false
}

// SetCapabilities handles the capabilities-changed event (spec §9).
func (c *Conn) SetCapabilities(caps []string) {
	c.mu.Lock()
	c.Capabilities = caps
	c.mu.Unlock()
}

// Touch records that a frame arrived; any frame resets the liveness timer
// (spec §7), not only heartbeats.
func (c *Conn) Touch() {
	c.mu.Lock()
	c.lastSeen = time.Now()
	c.mu.Unlock()
}

// LastSeen reports the time of the most recent inbound frame.
func (c *Conn) LastSeen() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastSeen
}

func (c *Conn) write(ctx context.Context, v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return wsjson.Write(ctx, c.ws, v)
}

// Call sends a cmd and waits for its call-response.
//
// It enforces the capability gate (§8), the in-flight cap (§5), and the server
// grace timeout (§5: timeout_ms + 5s), and sends a best-effort call-cancel when
// the caller's context is cancelled.
func (c *Conn) Call(ctx context.Context, cmd string, args json.RawMessage, timeoutMS int) (json.RawMessage, error) {
	if !c.Supports(cmd) {
		return nil, ErrUnsupported
	}
	timeoutMS = protocol.ClampCallTimeout(timeoutMS)
	requestID := protocol.NewID("req_")
	p := &pending{ch: make(chan *result, 1)}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, ErrDeviceOffline
	}
	if len(c.pending) >= protocol.MaxInFlightPerDevice {
		c.mu.Unlock()
		return nil, ErrOverloaded
	}
	c.pending[requestID] = p
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, requestID)
		c.mu.Unlock()
	}()

	call := protocol.Call{
		Type:      protocol.TypeCall,
		RequestID: requestID,
		Cmd:       cmd,
		Args:      args,
		TimeoutMS: timeoutMS,
	}
	if err := c.write(ctx, call); err != nil {
		return nil, err
	}

	// Server-side budget is the device's own budget plus grace (spec §5).
	deadline := time.NewTimer(time.Duration(timeoutMS+protocol.ServerGraceMS) * time.Millisecond)
	defer deadline.Stop()

	select {
	case res := <-p.ch:
		if res.Err != nil {
			return nil, res.Err
		}
		return res.Data, nil
	case <-deadline.C:
		// Abandon the request_id; a late response is ignored (spec §5).
		return nil, &protocol.Error{
			Code:      protocol.ErrTimeout,
			Message:   "no call-response within server budget",
			Retryable: true,
		}
	case <-ctx.Done():
		c.sendCancel(requestID)
		return nil, ctx.Err()
	}
}

// sendCancel emits an advisory call-cancel (spec §5). Best-effort by design:
// the caller has already given up, so a failure here changes nothing.
func (c *Conn) sendCancel(requestID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = c.write(ctx, protocol.CallCancel{
		Type:      protocol.TypeCallCancel,
		RequestID: requestID,
	})
}

// Deliver routes a call-response to its waiter. An unknown request_id is
// ignored: it is the normal shape of a response that lost the timeout race.
func (c *Conn) Deliver(requestID string, res *result) {
	c.mu.Lock()
	p, ok := c.pending[requestID]
	c.mu.Unlock()
	if !ok {
		return
	}
	select {
	case p.ch <- res:
	default: // buffered size 1; a second response for one id is dropped
	}
}

// DeliverOK and DeliverErr keep result unexported while letting the ws layer
// hand results back.
func (c *Conn) DeliverOK(requestID string, data json.RawMessage) {
	c.Deliver(requestID, &result{Data: data})
}

func (c *Conn) DeliverErr(requestID string, e *protocol.Error) {
	c.Deliver(requestID, &result{Err: e})
}

// closeWith closes the websocket with an application close code, and fails all
// in-flight calls so their callers unblock immediately rather than waiting out
// their timeouts.
func (c *Conn) closeWith(code websocket.StatusCode, reason string) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	waiters := make([]*pending, 0, len(c.pending))
	for _, p := range c.pending {
		waiters = append(waiters, p)
	}
	c.pending = map[string]*pending{}
	c.mu.Unlock()

	for _, p := range waiters {
		select {
		case p.ch <- &result{Err: &protocol.Error{
			Code:      protocol.ErrDeviceError,
			Message:   "connection closed: " + reason,
			Retryable: true,
		}}:
		default:
		}
	}
	_ = c.ws.Close(code, reason)
}

// Close terminates the connection with an application close code.
func (c *Conn) Close(code websocket.StatusCode, reason string) { c.closeWith(code, reason) }

// IsOnline reports whether a device currently has a live connection.
func (h *Hub) IsOnline(deviceID string) bool {
	_, ok := h.Get(deviceID)
	return ok
}

// Call routes one command to a device by id.
//
// The three return values separate the failure modes a caller must distinguish:
// a transport/routing failure (err), a structured refusal from the device
// itself (callErr), and success (data). A device that answers "no" is not a
// server error, so callErr is returned alongside a nil err.
func (h *Hub) Call(
	ctx context.Context,
	deviceID, cmd string,
	args json.RawMessage,
	timeoutMS int,
) (data json.RawMessage, callErr *protocol.Error, err error) {
	c, ok := h.Get(deviceID)
	if !ok {
		return nil, nil, ErrDeviceOffline
	}
	data, err = c.Call(ctx, cmd, args, timeoutMS)
	if err != nil {
		// A structured device error is a protocol-level answer, not a failure
		// of the call machinery — hand it back separately.
		var pe *protocol.Error
		if errors.As(err, &pe) {
			return nil, pe, nil
		}
		return nil, nil, err
	}
	return data, nil, nil
}

// Disconnect closes a device's live connection, if any. Used by credential
// revocation so it takes effect without waiting for a heartbeat timeout.
func (h *Hub) Disconnect(deviceID string, code int, reason string) {
	if c, ok := h.Get(deviceID); ok {
		c.Close(websocket.StatusCode(code), reason)
	}
}
