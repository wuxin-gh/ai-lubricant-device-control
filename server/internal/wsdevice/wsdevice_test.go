package wsdevice_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"device-control/server/internal/httpapi"
	"device-control/server/internal/hub"
	"device-control/server/internal/protocol"
	"device-control/server/internal/store"
	"device-control/server/internal/wsdevice"
)

const adminToken = "test-admin-token"

// harness is a fully wired server on a loopback listener.
type harness struct {
	t      *testing.T
	url    string
	hub    *hub.Hub
	store  *store.Store
	server *httptest.Server
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	h := hub.New()
	// Discard logs: these tests assert on behaviour, not output.
	lg := slog.New(slog.NewTextHandler(io.Discard, nil))

	api := &httpapi.Server{Store: st, Hub: h, Log: lg, AdminToken: adminToken}
	ws := &wsdevice.Handler{Store: st, Hub: h, Log: lg}

	mux := http.NewServeMux()
	mux.Handle("/", api.Routes())
	mux.Handle("/ws", ws)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &harness{t: t, url: srv.URL, hub: h, store: st, server: srv}
}

func (h *harness) post(path, body string, admin bool) (*http.Response, map[string]any) {
	h.t.Helper()
	req, err := http.NewRequest("POST", h.url+path, bytes.NewReader([]byte(body)))
	if err != nil {
		h.t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if admin {
		req.Header.Set("Authorization", "Bearer "+adminToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

// pair mints a code and redeems it, returning fresh device credentials.
func (h *harness) pair() (deviceID, token string) {
	h.t.Helper()
	_, mint := h.post("/admin/pairing-codes", `{"label":"test"}`, true)
	code, ok := mint["code"].(string)
	if !ok || code == "" {
		h.t.Fatalf("no pairing code in response: %v", mint)
	}
	body, _ := json.Marshal(map[string]string{"code": code})
	resp, out := h.post("/pair", string(body), false)
	if resp.StatusCode != 200 {
		h.t.Fatalf("pair failed: %d %v", resp.StatusCode, out)
	}
	return out["device_id"].(string), out["token"].(string)
}

// wsURL is the device endpoint for this harness.
func (h *harness) wsURL() string {
	return "ws" + h.url[len("http"):] + "/ws"
}

// dialAndRegister connects a fake device and completes the §4 handshake.
func (h *harness) dialAndRegister(
	ctx context.Context, deviceID, token string, caps []string,
) (*websocket.Conn, protocol.Registered) {
	h.t.Helper()
	c, _, err := websocket.Dial(ctx, h.wsURL(), nil)
	if err != nil {
		h.t.Fatalf("dial: %v", err)
	}
	reg := map[string]any{
		"type":             protocol.TypeRegister,
		"protocol_version": protocol.Version,
		"device_id":        deviceID,
		"auth":             map[string]string{"scheme": "token", "token": token},
		"capabilities":     caps,
	}
	if err := wsjson.Write(ctx, c, reg); err != nil {
		h.t.Fatalf("write register: %v", err)
	}
	var ack protocol.Registered
	if err := wsjson.Read(ctx, c, &ack); err != nil {
		h.t.Fatalf("read registered: %v", err)
	}
	if ack.Type != protocol.TypeRegistered {
		h.t.Fatalf("expected registered, got %q", ack.Type)
	}
	return c, ack
}

// serveOneCall reads a single `call` frame and replies per replyFn.
func serveOneCall(
	t *testing.T, ctx context.Context, c *websocket.Conn,
	replyFn func(call protocol.Call) any,
) {
	t.Helper()
	var call protocol.Call
	if err := wsjson.Read(ctx, c, &call); err != nil {
		t.Errorf("device read call: %v", err)
		return
	}
	if call.Type != protocol.TypeCall {
		t.Errorf("expected call, got %q", call.Type)
		return
	}
	if err := wsjson.Write(ctx, c, replyFn(call)); err != nil {
		t.Errorf("device write response: %v", err)
	}
}

// TestEndToEndCall is the M3 acceptance test: pair, register, drive a command,
// and get the device's data back through the admin API.
func TestEndToEndCall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h := newHarness(t)
	deviceID, token := h.pair()

	dev, ack := h.dialAndRegister(ctx, deviceID, token, []string{"get_screen_state", "tap"})
	defer dev.Close(websocket.StatusNormalClosure, "done")

	if ack.DeviceID != deviceID {
		t.Errorf("registered.device_id = %q, want %q", ack.DeviceID, deviceID)
	}
	if ack.ProtocolVersion != protocol.Version {
		t.Errorf("registered.protocol_version = %d, want %d", ack.ProtocolVersion, protocol.Version)
	}
	if ack.HeartbeatIntervalS != protocol.HeartbeatIntervalSec {
		t.Errorf("heartbeat_interval_s = %d, want %d",
			ack.HeartbeatIntervalS, protocol.HeartbeatIntervalSec)
	}
	if ack.SessionID == "" {
		t.Error("registered.session_id is empty")
	}

	// The device answers the tap with ok:true.
	go serveOneCall(t, ctx, dev, func(call protocol.Call) any {
		return map[string]any{
			"type":       protocol.TypeCallResponse,
			"request_id": call.RequestID,
			"ok":         true,
			"data":       map[string]any{"tapped": true},
		}
	})

	resp, out := h.post("/admin/devices/"+deviceID+"/call",
		`{"cmd":"tap","args":{"x":540,"y":1200}}`, true)
	if resp.StatusCode != 200 {
		t.Fatalf("call status = %d, body %v", resp.StatusCode, out)
	}
	if ok, _ := out["ok"].(bool); !ok {
		t.Fatalf("expected ok:true, got %v", out)
	}
	data, _ := out["data"].(map[string]any)
	if tapped, _ := data["tapped"].(bool); !tapped {
		t.Errorf("expected data.tapped=true, got %v", out["data"])
	}
}

// TestDeviceErrorSurfacesVerbatim checks a structured device refusal (spec §12)
// reaches the operator as ok:false with its code intact, not as a 5xx.
func TestDeviceErrorSurfacesVerbatim(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h := newHarness(t)
	deviceID, token := h.pair()
	dev, _ := h.dialAndRegister(ctx, deviceID, token, []string{"tap"})
	defer dev.Close(websocket.StatusNormalClosure, "done")

	go serveOneCall(t, ctx, dev, func(call protocol.Call) any {
		return map[string]any{
			"type":       protocol.TypeCallResponse,
			"request_id": call.RequestID,
			"ok":         false,
			"error": map[string]any{
				"code":      protocol.ErrStaleNode,
				"message":   "node_id not in latest tree",
				"retryable": false,
			},
		}
	})

	resp, out := h.post("/admin/devices/"+deviceID+"/call", `{"cmd":"tap","args":{"node_id":"node_x"}}`, true)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200 (device refusal is not a server error); body %v",
			resp.StatusCode, out)
	}
	if ok, _ := out["ok"].(bool); ok {
		t.Fatalf("expected ok:false, got %v", out)
	}
	errObj, _ := out["error"].(map[string]any)
	if got := errObj["code"]; got != protocol.ErrStaleNode {
		t.Errorf("error.code = %v, want %q", got, protocol.ErrStaleNode)
	}
}

// TestCapabilityGate covers spec §8: the server must not send an undeclared cmd.
func TestCapabilityGate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h := newHarness(t)
	deviceID, token := h.pair()
	dev, _ := h.dialAndRegister(ctx, deviceID, token, []string{"tap"}) // no open_app
	defer dev.Close(websocket.StatusNormalClosure, "done")

	resp, out := h.post("/admin/devices/"+deviceID+"/call", `{"cmd":"open_app","args":{"package":"com.x"}}`, true)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for undeclared cmd; body %v", resp.StatusCode, out)
	}
	errObj, _ := out["error"].(map[string]any)
	if got := errObj["code"]; got != protocol.ErrUnsupported {
		t.Errorf("error.code = %v, want %q", got, protocol.ErrUnsupported)
	}
}

// TestBadTokenRejected asserts a wrong token is closed with 4003 (spec §13) and
// never reaches registered.
func TestBadTokenRejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h := newHarness(t)
	deviceID, _ := h.pair()

	c, _, err := websocket.Dial(ctx, h.wsURL(), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "done")

	_ = wsjson.Write(ctx, c, map[string]any{
		"type":             protocol.TypeRegister,
		"protocol_version": protocol.Version,
		"device_id":        deviceID,
		"auth":             map[string]string{"scheme": "token", "token": "wrong-token"},
		"capabilities":     []string{"tap"},
	})

	var any map[string]any
	err = wsjson.Read(ctx, c, &any)
	if err == nil {
		t.Fatalf("expected close, got frame %v", any)
	}
	if got := websocket.CloseStatus(err); got != protocol.CloseAuthFailed {
		t.Errorf("close status = %d, want %d (auth_failed)", got, protocol.CloseAuthFailed)
	}
}

// TestUnknownProtocolVersionRejected covers spec §14 / close 4004.
func TestUnknownProtocolVersionRejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h := newHarness(t)
	deviceID, token := h.pair()

	c, _, err := websocket.Dial(ctx, h.wsURL(), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "done")

	_ = wsjson.Write(ctx, c, map[string]any{
		"type":             protocol.TypeRegister,
		"protocol_version": 99,
		"device_id":        deviceID,
		"auth":             map[string]string{"scheme": "token", "token": token},
		"capabilities":     []string{"tap"},
	})

	var frame map[string]any
	err = wsjson.Read(ctx, c, &frame)
	if err == nil {
		t.Fatalf("expected close, got frame %v", frame)
	}
	if got := websocket.CloseStatus(err); got != protocol.CloseVersionUnsupported {
		t.Errorf("close status = %d, want %d", got, protocol.CloseVersionUnsupported)
	}
}

// TestReplacedOnReconnect covers spec §4.4: a second connection for the same
// device evicts the first with 4009.
func TestReplacedOnReconnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h := newHarness(t)
	deviceID, token := h.pair()

	first, _ := h.dialAndRegister(ctx, deviceID, token, []string{"tap"})
	second, _ := h.dialAndRegister(ctx, deviceID, token, []string{"tap"})
	defer second.Close(websocket.StatusNormalClosure, "done")

	var frame map[string]any
	err := wsjson.Read(ctx, first, &frame)
	if err == nil {
		t.Fatalf("expected first connection to be closed, got frame %v", frame)
	}
	if got := websocket.CloseStatus(err); got != protocol.CloseReplaced {
		t.Errorf("close status = %d, want %d (replaced)", got, protocol.CloseReplaced)
	}
}

// TestUnknownTypeIgnored covers the forward-compat rule in spec §3: an unknown
// message type must not kill the connection.
func TestUnknownTypeIgnored(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h := newHarness(t)
	deviceID, token := h.pair()
	dev, _ := h.dialAndRegister(ctx, deviceID, token, []string{"tap"})
	defer dev.Close(websocket.StatusNormalClosure, "done")

	// A type from some future protocol version, plus an unknown field.
	if err := wsjson.Write(ctx, dev, map[string]any{
		"type":       "telemetry-from-v9",
		"device_id":  deviceID,
		"some_field": 123,
	}); err != nil {
		t.Fatalf("write unknown type: %v", err)
	}

	// The connection must still serve a call afterwards.
	go serveOneCall(t, ctx, dev, func(call protocol.Call) any {
		return map[string]any{
			"type":       protocol.TypeCallResponse,
			"request_id": call.RequestID,
			"ok":         true,
			"data":       map[string]any{},
		}
	})

	resp, out := h.post("/admin/devices/"+deviceID+"/call", `{"cmd":"tap","args":{"x":1,"y":2}}`, true)
	if resp.StatusCode != 200 {
		t.Fatalf("call after unknown frame: status %d, body %v", resp.StatusCode, out)
	}
	if ok, _ := out["ok"].(bool); !ok {
		t.Errorf("expected ok:true after unknown frame, got %v", out)
	}
}

// TestCapabilitiesChangedEvent covers spec §9: the event updates the stored set,
// so a previously-undeclared cmd becomes allowed without re-registering.
func TestCapabilitiesChangedEvent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h := newHarness(t)
	deviceID, token := h.pair()
	dev, _ := h.dialAndRegister(ctx, deviceID, token, []string{"tap"})
	defer dev.Close(websocket.StatusNormalClosure, "done")

	// open_app is initially gated.
	resp, _ := h.post("/admin/devices/"+deviceID+"/call", `{"cmd":"open_app","args":{"package":"com.x"}}`, true)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("precondition: open_app should be gated, got %d", resp.StatusCode)
	}

	if err := wsjson.Write(ctx, dev, map[string]any{
		"type": protocol.TypeEvent,
		"kind": protocol.EventCapabilitiesChanged,
		"data": map[string]any{"capabilities": []string{"tap", "open_app"}},
	}); err != nil {
		t.Fatalf("write event: %v", err)
	}

	// One persistent responder goroutine owns the socket for the rest of this
	// test. A websocket supports only a single concurrent reader, so the retry
	// loop below must NOT spawn a reader per attempt.
	answering, stopAnswering := context.WithCancel(ctx)
	defer stopAnswering()
	go func() {
		for answering.Err() == nil {
			var call protocol.Call
			if err := wsjson.Read(answering, dev, &call); err != nil {
				return // context cancelled or connection closed
			}
			if call.Type != protocol.TypeCall {
				continue
			}
			_ = wsjson.Write(answering, dev, map[string]any{
				"type":       protocol.TypeCallResponse,
				"request_id": call.RequestID,
				"ok":         true,
				"data":       map[string]any{},
			})
		}
	}()

	// The event is applied asynchronously, so poll until the gate opens.
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, out := h.post("/admin/devices/"+deviceID+"/call",
			`{"cmd":"open_app","args":{"package":"com.x"}}`, true)
		if resp.StatusCode == 200 {
			if ok, _ := out["ok"].(bool); ok {
				return // capability took effect
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("open_app still gated after capabilities-changed: %d %v", resp.StatusCode, out)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestAdminRequiresToken asserts the admin API is closed by default — it can
// drive a phone, so an unauthenticated caller must get nowhere.
func TestAdminRequiresToken(t *testing.T) {
	h := newHarness(t)

	for _, tc := range []struct{ name, path, body string }{
		{"mint code", "/admin/pairing-codes", `{}`},
		{"call device", "/admin/devices/dev_x/call", `{"cmd":"tap"}`},
	} {
		resp, _ := h.post(tc.path, tc.body, false)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s without token: status = %d, want 401", tc.name, resp.StatusCode)
		}
	}
}

// TestPairingCodeSingleUse asserts a redeemed code cannot be replayed.
func TestPairingCodeSingleUse(t *testing.T) {
	h := newHarness(t)

	_, mint := h.post("/admin/pairing-codes", `{"label":"once"}`, true)
	code := mint["code"].(string)
	body, _ := json.Marshal(map[string]string{"code": code})

	if resp, out := h.post("/pair", string(body), false); resp.StatusCode != 200 {
		t.Fatalf("first redeem failed: %d %v", resp.StatusCode, out)
	}
	resp, out := h.post("/pair", string(body), false)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("replayed code: status = %d, want 403; body %v", resp.StatusCode, out)
	}
}

// TestRevokeDisconnects asserts revocation drops the live connection instead of
// waiting for a heartbeat timeout.
func TestRevokeDisconnects(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h := newHarness(t)
	deviceID, token := h.pair()
	dev, _ := h.dialAndRegister(ctx, deviceID, token, []string{"tap"})
	defer dev.Close(websocket.StatusNormalClosure, "done")

	req, _ := http.NewRequest("DELETE", h.url+"/admin/devices/"+deviceID, nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("revoke status = %d, want 200", resp.StatusCode)
	}

	var frame map[string]any
	if err := wsjson.Read(ctx, dev, &frame); err == nil {
		t.Fatalf("expected connection closed after revoke, got %v", frame)
	} else if got := websocket.CloseStatus(err); got != protocol.CloseAuthFailed {
		t.Errorf("close status = %d, want %d", got, protocol.CloseAuthFailed)
	}
}
