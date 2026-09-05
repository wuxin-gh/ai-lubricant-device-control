// Package wsclient is the persistent WebSocket connection to the server, with
// the full §7 lifecycle: full-jitter backoff, register-on-open, heartbeat, call
// dispatch, and close-code classification. Mirrors android/app/.../net/WsClient.kt.
//
// Reconnect rules (§7 + §13):
//   - 4003 auth_failed → token is dead: wipe the credential, surface FatalAuth
//     (needsRePair=true), STOP. Reconnecting would loop forever against a
//     deleted/revoked record.
//   - 4004 protocol_version_unsupported → app is too old/new: FatalVersion
//     (needsRePair=false, the credential is fine), STOP.
//   - 4002/4007/4008/4009/4010 and 1000-range closes → backoff and reconnect.
//   - Attempt counter resets to 0 only after a connection stayed up longer than
//     the cap (30s); resetting on register-success would let a register-then-drop
//     loop hammer the server.
//   - On every reconnect, all in-flight state is discarded (§7): running calls
//     are abandoned WITHOUT responses, the dispatcher's dedup set is cleared,
//     and the heartbeat seq resets.
//
// Non-idempotent commands are never retried across a reconnect (§6).
package wsclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"device-control/ios/internal/caps"
	"device-control/ios/internal/creds"
	"device-control/ios/internal/dispatcher"
	"device-control/ios/internal/wire"
)

// State is the connection state surfaced to the host (the equivalent of the
// Android ConnectionState sealed class; the host surfaces it in logs/metrics).
type State int

const (
	StateIdle State = iota
	StateConnecting
	StateConnected
	StateDisconnected // will retry
	StateFatalAuth    // 4003: needs re-pair
	StateFatalVersion // 4004: needs app upgrade
)

// stateNames maps each State to a stable log vocabulary. Order MUST match the
// iota declaration above.
var stateNames = [...]string{
	"idle",
	"connecting",
	"connected",
	"disconnected",
	"fatal_auth",
	"fatal_version",
}

// String renders a State for logs so integrators do not hand-roll a switch that
// drifts as new states are added.
func (s State) String() string {
	i := int(s)
	if i >= 0 && i < len(stateNames) {
		return stateNames[i]
	}
	return fmt.Sprintf("state(%d)", i)
}

// OnWipe is called after the credential is wiped on 4003.
type OnWipe func()

// Client runs the connect loop.
type Client struct {
	Dispatcher *dispatcher.Dispatcher
	OnWipe     OnWipe

	// DeviceInfo, if non-nil, is sent in register.device_info (spec §3.1). The
	// host fills it from go-ios device properties.
	DeviceInfo json.RawMessage

	stateMu  sync.RWMutex
	state    State
	stopped  bool
	userStop bool
}

// State returns the current connection state.
func (c *Client) State() State {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.state
}

func (c *Client) setState(s State) {
	c.stateMu.Lock()
	c.state = s
	c.stateMu.Unlock()
}

// Stop breaks the reconnect loop. userInitiated distinguishes a user tap from a
// network drop (unused here but kept for parity with the Android surface).
func (c *Client) Stop(userInitiated bool) {
	c.stateMu.Lock()
	c.stopped = true
	c.userStop = userInitiated
	c.stateMu.Unlock()
}

// ConnectLoop runs until Stop is called or a fatal close is received. It
// suspends the caller for the lifetime of the connection — run it on a
// host-managed goroutine, not on a request path.
func (c *Client) ConnectLoop(ctx context.Context, cred *creds.Credential) error {
	attempt := 0
	for {
		c.stateMu.RLock()
		stop := c.stopped
		c.stateMu.RUnlock()
		if stop {
			c.setState(StateIdle)
			return nil
		}

		upSince := time.Now()
		outcome := c.runOnce(ctx, cred)

		switch outcome {
		case outcomeFatalAuth:
			if c.OnWipe != nil {
				c.OnWipe()
			}
			c.setState(StateFatalAuth)
			return errors.New("auth failed: token is no longer valid; re-pair")
		case outcomeFatalVersion:
			c.setState(StateFatalVersion)
			return errors.New("protocol version mismatch; upgrade the driver")
		case outcomeUserStopped:
			c.setState(StateIdle)
			return nil
		case outcomeRetryable:
			if time.Since(upSince) > backoffCap {
				attempt = 0
			} else {
				attempt++
			}
			delay := ComputeBackoffDelay(attempt)
			c.setState(StateDisconnected)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
	}
}

type outcome int

const (
	outcomeRetryable outcome = iota
	outcomeFatalAuth
	outcomeFatalVersion
	outcomeUserStopped
)

// runOnce is one dial → register → serve → close cycle.
func (c *Client) runOnce(ctx context.Context, cred *creds.Credential) outcome {
	c.setState(StateConnecting)
	c.Dispatcher.ResetForNewConnection()

	wsURL, err := wsURL(cred.ServerURL)
	if err != nil {
		return outcomeRetryable // malformed URL: treat as retryable, will keep failing
	}

	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{},
	})
	if err != nil {
		return outcomeRetryable
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	conn.SetReadLimit(wire.MaxFrameBytes)

	// Send register immediately (§4.1: within 10s of open). The full §13 close
	// classification runs on whatever close code the server eventually returns.
	reg := wire.Register{
		Type:            wire.TypeRegister,
		ProtocolVersion: wire.Version,
		DeviceID:        cred.DeviceID,
		Auth:            wire.Auth{Scheme: wire.SchemeToken, Token: cred.Token},
		Capabilities:    caps.ForDevice(),
		DeviceInfo:      c.DeviceInfo,
	}
	if err := writeJSON(ctx, conn, reg); err != nil {
		return outcomeRetryable
	}

	// Wait for registered (or a close) with a 10s budget — matches the server's
	// 4008 register-timeout so we don't hang if the server is silent.
	regCtx, regCancel := context.WithTimeout(ctx, wire.RegisterTimeoutSec*time.Second)
	registered, err := readRegistered(regCtx, conn)
	regCancel()
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return outcomeRetryable
		}
		// A server close surfaces here as a read error; classify by close code.
		return classifyClose(closeCodeFromErr(err))
	}
	_ = registered // accepted_capabilities/heartbeat params noted but we keep our own cadence

	c.setState(StateConnected)

	// Heartbeat loop: every heartbeat_interval_s, no ack (§10).
	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	go c.heartbeat(hbCtx, conn, cred.DeviceID, registered.HeartbeatIntervalS)

	// Call dispatch loop: pull inbound frames, run calls through the dispatcher.
	for {
		_, data, rerr := conn.Read(ctx)
		if rerr != nil {
			hbCancel()
			return classifyClose(closeCodeFromErr(rerr))
		}
		var msg struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue // malformed JSON: ignore, do not tear down the connection
		}
		switch msg.Type {
		case wire.TypeCall:
			go c.handleCall(ctx, conn, data)
		case wire.TypeCallCancel:
			// §6: cancel is advisory. The dispatcher has no cooperative cancel;
			// a handler that already finished sends its real result (explicitly
			// allowed), otherwise the server sees our late response.
		case wire.TypeRegistered:
			// A second registered on one connection is a server bug; ignore.
		default:
			// §3: unknown frame types MUST be ignored, not an error.
		}
	}
}

func (c *Client) handleCall(ctx context.Context, conn *websocket.Conn, raw []byte) {
	var call wire.Call
	if err := json.Unmarshal(raw, &call); err != nil {
		return
	}
	resp := c.Dispatcher.Dispatch(ctx, call)
	_ = writeJSON(ctx, conn, resp)
}

func (c *Client) heartbeat(ctx context.Context, conn *websocket.Conn, deviceID string, intervalS int) {
	if intervalS <= 0 {
		intervalS = wire.HeartbeatIntervalSec
	}
	ticker := time.NewTicker(time.Duration(intervalS) * time.Second)
	defer ticker.Stop()
	var seq int64
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = writeJSON(ctx, conn, wire.Heartbeat{
				Type:     wire.TypeHeartbeat,
				DeviceID: deviceID,
				Seq:      seq,
			})
			seq++
		}
	}
}

// classifyClose maps a WS close code to an outcome per §13.
func classifyClose(code int) outcome {
	switch code {
	case wire.CloseAuthFailed:
		return outcomeFatalAuth
	case wire.CloseVersionUnsupported:
		return outcomeFatalVersion
	default:
		// 4002/4007/4008/4009/4010, 1000-range, -1 (no code) → backoff.
		return outcomeRetryable
	}
}

// closeCodeFromErr extracts the WebSocket close code from a read error. In
// coder/websocket, close codes come from the error returned by Read, via
// websocket.CloseStatus(err) — not from a method on Conn. Returns -1 when the
// read did not fail due to a close with a code.
func closeCodeFromErr(err error) int {
	if err == nil {
		return -1
	}
	return int(websocket.CloseStatus(err))
}

// wsURL converts an http(s):// server base to ws(s):// + the fixed /ws/device
// path (server httpapi.go: device WebSocket endpoint).
func wsURL(base string) (string, error) {
	trimmed := strings.TrimRight(base, "/")
	switch {
	case strings.HasPrefix(trimmed, "https://"):
		return "wss://" + strings.TrimPrefix(trimmed, "https://") + "/ws/device", nil
	case strings.HasPrefix(trimmed, "http://"):
		return "ws://" + strings.TrimPrefix(trimmed, "http://") + "/ws/device", nil
	case strings.HasPrefix(trimmed, "ws://"), strings.HasPrefix(trimmed, "wss://"):
		return trimmed + "/ws/device", nil
	default:
		// Default to ws://; the host surfaces a warning for non-loopback http.
		return "ws://" + trimmed + "/ws/device", nil
	}
}

func writeJSON(ctx context.Context, conn *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, b)
}

func readRegistered(ctx context.Context, conn *websocket.Conn) (wire.Registered, error) {
	_, data, err := conn.Read(ctx)
	if err != nil {
		return wire.Registered{}, err
	}
	var msg struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		return wire.Registered{}, err
	}
	if msg.Type != wire.TypeRegistered {
		// The server may have closed before sending registered; the read that
		// errored on close is already handled by the caller. A non-registered
		// frame here is a protocol violation; treat as retryable.
		return wire.Registered{}, fmt.Errorf("expected registered, got %q", msg.Type)
	}
	var reg wire.Registered
	if err := json.Unmarshal(data, &reg); err != nil {
		return wire.Registered{}, err
	}
	return reg, nil
}

const (
	backoffBase     = 1 * time.Second
	backoffCap      = 30 * time.Second
	backoffShiftCap = 15
)

// ComputeBackoffDelay is the full-jitter backoff (§7):
//
//	delay = random(0, min(cap, base * 2^attempt))
//
// The whole range is uniform-random, not "half + random half". The shift is
// capped at 15: 2^15 = 32768 > the 30s cap in milliseconds, so larger attempts
// saturate at the cap rather than overflowing int64. Public so the formula is
// unit-testable without a live socket.
func ComputeBackoffDelay(attempt int) time.Duration {
	shift := attempt
	if shift > backoffShiftCap {
		shift = backoffShiftCap
	}
	upper := int64(backoffBase) << shift
	if upper > int64(backoffCap) {
		upper = int64(backoffCap)
	}
	if upper <= 0 {
		upper = int64(backoffCap)
	}
	return time.Duration(rand.Int63n(upper + 1))
}
