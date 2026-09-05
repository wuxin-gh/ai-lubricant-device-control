# Device Control Protocol — v0 (final)

Status: **final for v0**. Resolves all open questions from the M0 draft (see
§15 for the decision log). Framing decisions inherited from [PLAN.md §3](../PLAN.md).

`protocol_version` for this document is **0**.

## 1. Scope and conventions

This document defines the wire protocol between a **device** (the phone running
the control app) and a **server** (self-hosted, operated by the device owner).
It does not define the server's northbound API to control clients — that is
implementation-defined.

- "MUST/SHOULD/MAY" are used in the RFC 2119 sense.
- All byte sizes are decimal (`KiB`/`MiB` are binary).
- All timestamps are RFC 3339 with a timezone offset, e.g. `2026-08-23T12:00:00Z`.
- All coordinates are **integer physical pixels**, origin top-left, in the
  screen's **current** orientation. There is no scaling and no dp anywhere in
  this protocol.
- Field names are `snake_case`. Message `type` values are `kebab-case`.

## 2. Transport and framing

- WebSocket (RFC 6455) over TLS. Plain `ws://` is permitted only for
  loopback/LAN development; a server SHOULD refuse non-TLS otherwise.
- One long-lived connection per device. The **device is always the dialer**.
- Payloads are WebSocket **text** frames, each carrying exactly one JSON object.
  Newlines inside the JSON are allowed but not meaningful; no framing delimiter
  is used beyond the WebSocket frame itself.
- Maximum frame size: **4 MiB**. Either side MAY close with `4002` on overflow.
  Compression (`permessage-deflate`) SHOULD be negotiated when available; it
  materially helps the UI-tree payloads in §8.
- The server endpoint path is implementation-defined and user-configured. The
  device app MUST NOT ship a default server address (PLAN.md §1).

## 3. Envelope

Every message is a JSON object with a `type` field. **Unknown `type` values MUST
be ignored** (not an error) — this is the forward-compatibility hinge. Unknown
*fields* within a known type MUST likewise be ignored.

### 3.1 Device → Server

```jsonc
// register — MUST be the first frame after WS open, exactly once per connection
{ "type": "register",
  "protocol_version": 0,
  "device_id": "dev_7f3a…",
  "auth": { "scheme": "token", "token": "…" },   // §11
  "capabilities": ["get_screen_state", "tap", "type_text", "swipe"],
  "device_info": {                               // informational, all optional
    "platform": "android",
    "os_version": "14",
    "model": "Pixel 7",
    "app_version": "0.1.0",
    "screen": { "w": 1080, "h": 2400, "density": 420 }
  }
}
```

```jsonc
// heartbeat — keepalive; see §10
{ "type": "heartbeat", "device_id": "dev_7f3a…", "seq": 42 }

// call-response — reply to exactly one server `call`, correlated by request_id
{ "type": "call-response",
  "request_id": "req_01J…",
  "ok": true,
  "data": { }                  // REQUIRED when ok=true (may be {})
}
{ "type": "call-response",
  "request_id": "req_01J…",
  "ok": false,
  "error": {                   // REQUIRED when ok=false — structured, see §12
    "code": "unsupported",
    "message": "cmd not in declared capabilities",
    "retryable": false,
    "details": { }             // optional, code-specific
  }
}

// event — unsolicited device→server push. v0 defines exactly two kinds (§9).
{ "type": "event", "kind": "control-revoked", "data": { "reason": "…" } }
```

### 3.2 Server → Device

```jsonc
// registered — ack of register; negotiated parameters live here
{ "type": "registered",
  "protocol_version": 0,
  "device_id": "dev_7f3a…",
  "server_time": "2026-08-23T12:00:00Z",
  "session_id": "ses_01J…",              // per-connection, for log correlation
  "heartbeat_interval_s": 15,            // device SHOULD adopt this
  "heartbeat_timeout_s": 60,
  "accepted_capabilities": ["get_screen_state", "tap", "type_text", "swipe"]
}

// call — server asks the device to execute one command
{ "type": "call",
  "request_id": "req_01J…",
  "cmd": "tap",
  "args": { "x": 540, "y": 1200 },
  "timeout_ms": 15000                    // optional; device default 15000
}

// call-cancel — best-effort cancellation of an in-flight call
{ "type": "call-cancel", "request_id": "req_01J…" }
```

### 3.3 Identifiers

- `device_id` — server-assigned at pairing, opaque, stable for the device's
  lifetime. Recommended shape `dev_` + 22 chars base62. Max 64 bytes.
- `request_id` — server-generated, unique **per connection** (uniqueness across
  reconnects is not required, since §7 discards in-flight state on reconnect).
  Max 64 bytes.
- `session_id` — server-generated per accepted connection.

## 4. Connection lifecycle

```
device dials  ──▶  WS open
              ──▶  register
              ◀──  registered            (or close with 4xxx, §13)
              ◀──▶ call / call-response / heartbeat / event …
```

1. The device MUST send `register` as the first frame, within **10 s** of WS
   open. A server that does not receive it in time closes with `4008`.
2. The server MUST reply `registered` before sending any `call`. A device that
   receives a `call` before `registered` MUST ignore it.
3. The server MUST NOT accept a second `register` on the same connection; it
   closes with `4007`.
4. If the same `device_id` registers on a new connection, the server MUST close
   the older connection with `4009` (`replaced`) — last writer wins. This is the
   normal outcome of a device reconnecting after a half-open network drop.
5. Either side may close cleanly at any time (WS `1000`/`1001`).

## 5. Request/response semantics

- Every `call` gets **exactly one** `call-response` with the same `request_id`,
  unless the connection drops first.
- The device MAY execute calls concurrently, but **MUST serialize commands that
  mutate UI state** (everything except `get_screen_state` and `list_apps`) in
  the order received. Interleaving two taps is a correctness hazard, not an
  optimization.
- In-flight limit: the server MUST NOT have more than **8** outstanding calls
  per device. A device receiving a 9th replies `error.code = "overloaded"`.
- `timeout_ms` is the device-side budget for producing a response. On expiry the
  device MUST reply with `error.code = "timeout"` rather than staying silent.
  Cap: the device MAY clamp `timeout_ms` to **60000**.
- The server SHOULD apply its own timeout of `timeout_ms` + 5 s, after which it
  abandons the `request_id` and MUST ignore a late `call-response` for it.
- `call-cancel` is advisory: the device SHOULD abandon the work and MUST still
  send a `call-response` — either the real result (if it already finished) or
  `error.code = "cancelled"`.

## 6. Idempotency

Input commands are **not** idempotent — a replayed `tap` taps twice. Therefore:

- The device MUST NOT re-execute a `call` whose `request_id` it has already
  seen on the current connection; it re-sends the cached response if it has one,
  otherwise replies `error.code = "duplicate_request"`.
- The server MUST NOT retry a `call` across a reconnect. After a reconnect the
  correct recovery is `get_screen_state`, then decide. This is the reason §7
  drops in-flight state instead of trying to resume it.

## 7. Liveness, reconnect, and reaping

- The device sends `heartbeat` every `heartbeat_interval_s` (default **15 s**),
  with a monotonically increasing `seq` per connection. Heartbeats are not
  acked; they exist so the server can reap.
- The server reaps a connection with no frame of any kind received within
  `heartbeat_timeout_s` (default **60 s**), closing with `4010` (`stale`).
  Any received frame (not only `heartbeat`) resets the timer.
- On any disconnect the device reconnects with **exponential backoff with full
  jitter**: `delay = random(0, min(cap, base * 2^attempt))`, `base = 1 s`,
  `cap = 30 s`. The attempt counter resets to 0 after a connection that stayed
  up longer than `cap`.
- On reconnect **all in-flight state is discarded** on both sides: the device
  abandons any running calls without sending responses; the server abandons all
  outstanding `request_id`s. Recovery is a fresh `get_screen_state` (§6).

## 8. Command vocabulary (v0 minimal closed loop)

The server MUST NOT send a `cmd` absent from the device's declared
`capabilities`; if it does, the device replies `error.code = "unsupported"`.
All coordinates are integer physical pixels in current orientation (§1).

| cmd | args | data (on ok) | notes |
|---|---|---|---|
| `get_screen_state` | `{ include_screenshot?: bool, max_nodes?: int, cursor?: string }` | `{ screen, tree, screenshot?, next_cursor? }` | see §8.1–§8.3, §8.7 |
| `tap` | `{ x, y }` \| `{ node_id }` | `{}` | node_id: a11y action preferred (§8.4) |
| `long_press` | `{ x, y, duration_ms? }` \| `{ node_id }` | `{}` | default 500 ms |
| `double_tap` | `{ x, y }` \| `{ node_id }` | `{}` | |
| `swipe` | `{ x1, y1, x2, y2, duration_ms? }` | `{}` | default 300 ms |
| `scroll` | `{ direction, node_id? }` | `{}` | direction ∈ up/down/left/right; scrolls focused/target scrollable |
| `scroll_to_node` | `{ node_id }` | `{}` | bring `off` node on-screen; §8.5 |
| `type_text` | `{ text }` | `{}` | into currently focused editable |
| `set_text` | `{ node_id, text }` | `{}` | a11y `ACTION_SET_TEXT` on the node (replaces content) |
| `press_key` | `{ key }` | `{}` | key ∈ §8.6 enumeration |
| `dismiss_keyboard` | `{}` | `{}` | no-op if hidden → still `ok` |
| `press_back` | `{}` | `{}` | |
| `press_home` | `{}` | `{}` | |
| `press_recents` | `{}` | `{}` | |
| `open_app` | `{ package }` | `{}` | |
| `list_apps` | `{ include_system?: bool }` | `{ apps: [{ package, name }] }` | default excludes system apps |

`press_back`/`press_home`/`press_recents` are distinct commands (not `press_key`
values) so they can be capability-gated independently on read-only platforms.

### 8.1 `screen` object

```jsonc
"screen": { "w": 1080, "h": 2400, "density": 420, "orientation": "portrait" }
```
`orientation` ∈ `portrait` | `landscape`. `w`/`h` reflect current orientation.
`density` is the Android **densityDpi** integer (e.g. 420 for ~2.625x), matching
ARC-MCP `ScreenInfo.densityDpi` — not a float scale factor.

### 8.2 UI tree — `tree` (compact TSV)

`tree` is a single string carrying the accessibility tree in a compact,
token-frugal form. The format **aligns with ARC-MCP's `CompactTreeFormatter`**
(PLAN.md §2 reuse source), so the v0 device implementation can emit it with
little adaptation. Structure:

```
note:structural-only nodes are omitted from the tree
note:certain elements are custom and will not be properly reported, ...
note:flags: on=onscreen off=offscreen clk=clickable lclk=longClickable foc=focusable scr=scrollable edt=editable ena=enabled
note:offscreen items require scroll_to_node before interaction
screen:<w>x<h> density:<dpi> orientation:<orientation>
node_id	class	text	desc	res_id	bounds	flags
<one TSV row per kept node, pre-order>
hierarchy:
<node_id per line, indented 2 spaces per kept-depth>
```

**Node filtering (kept vs omitted):** a node is **kept** (emitted as a row) iff
any of: non-empty `text`, non-empty `desc`, non-empty `res_id`, `clickable`,
`longClickable`, `scrollable`, `editable`. Structural-only containers are
omitted from the TSV rows but their children are still walked; the separate
`hierarchy:` block encodes nesting by indentation. This is exactly ARC-MCP's
filter — it keeps the wire small and the meaningful nodes addressable.

**TSV header (fixed, ordered):**

```
node_id	class	text	desc	res_id	bounds	flags
```

| column | meaning |
|---|---|
| `node_id` | **string**, opaque node id. v0 recommends ARC-MCP's scheme: `node_` + hex of a hash over `resourceId\|className\|bounds\|depth\|index\|parentId`, which is **stable across re-parses while the UI is unchanged** (lets a caller correlate nodes across snapshots). Implementations MAY use any opaque string ≤ 64 chars; clients MUST treat it as opaque. |
| `class` | simple class name (last `.`-segment of the widget class), e.g. `Button`, `EditText`. `-` if unknown. |
| `text` | visible text, sanitized (TAB/newline → space, trimmed), `-` if empty. Truncated to **100 chars** + `...truncated`. Exception: a merged WebView node (whose text is collapsed from many DOM nodes and exists only in this output) is never truncated, since the content is otherwise unrecoverable. |
| `desc` | content-description / a11y label, same sanitization as `text`, `-` if empty. |
| `res_id` | resource-id entry name (segment after `/`), `-` if none. |
| `bounds` | `left,top,right,bottom` in physical pixels. |
| `flags` | comma-joined, **first token always `on`/`off`** (onscreen/offscreen), then any of `clk,lclk,foc,scr,edt,ena` when true. `-` is never used for flags (at minimum `on` or `off` appears). |

**Null/empty convention:** empty string fields are rendered as `-` (a single
dash), **not** as an empty field. This matches ARC-MCP and avoids ambiguous
adjacent tabs. TAB/newline/backslash inside a value are replaced with space
(ARC-MCP sanitization) — values are never quoted and never contain a literal
TAB, so columns split cleanly on `\t`.

**`hierarchy:` block:** after the TSV rows, a line `hierarchy:` then one line
per kept node containing only its `node_id`, indented by 2 spaces per
kept-depth. This is the structural companion to the flat TSV; clients that
only need to act on nodes can ignore it.

**Multi-window:** the device MAY emit one `--- window:N type:T pkg:P title:T
[activity:A] layer:L focused:B ---` header line before each window's TSV
section (ARC-MCP `formatMultiWindow`). v0 does not require multi-window; a
single-window device emits one section with no window header. If multi-window
is unavailable the device MAY emit a leading
`note:DEGRADED — multi-window unavailable, only active window reported`.

**`max_nodes` cap (default 1500):** caps the number of kept-node TSV rows
across all windows. If truncated, the device appends a final line

```
note:truncated at <n> of <total> nodes — narrow the query or use a cursor
```

after the last TSV row (before `hierarchy:`), so the caller knows the tree is
incomplete. A `note:` line is used rather than a fake TSV row because `node_id`
is an opaque string (§8.2) with no reserved sentinel value, and because `note:`
is already the format's out-of-band channel — a parser splitting rows on `\t`
sees a single-field line and skips it. The `hierarchy:` block is truncated to
the same set of nodes.

Example (single window):
```
note:structural-only nodes are omitted from the tree
note:certain elements are custom and will not be properly reported, ...
note:flags: on=onscreen off=offscreen clk=clickable lclk=longClickable foc=focusable scr=scrollable edt=editable ena=enabled
note:offscreen items require scroll_to_node before interaction
screen:1080x2400 density:420 orientation:portrait
node_id	class	text	desc	res_id	bounds	flags
node_b2e4	TextView	Settings	-	title	48,120,400,180	on,ena
node_9c01	EditText	-	Search	search_box	48,220,832,320	on,foc,edt,ena
node_3a7f	Button	Save	-	save_btn	864,220,1032,320	on,clk,ena
hierarchy:
node_b2e4
node_9c01
node_3a7f
```

Here the root container was structural-only and therefore omitted from the rows;
its three kept children all sit at kept-depth 0, so the `hierarchy:` block is
flat. Note that the TSV rows and the `hierarchy:` lines are in the **same
pre-order** — a conforming emitter walks the tree once and appends to both.

> **M1 reconciliation note:** the column set, flags, `-` null convention,
> 100-char truncation, `hierarchy:` block, and `note:` lines above are taken
> verbatim from ARC-MCP `CompactTreeFormatter` so the reused code emits this
> format directly. The only v0 deltas are the `max_nodes` cap + `note:truncated`
> line (ARC-MCP uses cursor pagination instead — see §8.7) and the
> recommendation that `node_id` be the ARC-MCP hash scheme.

### 8.3 Screenshot — `screenshot`

Present only when `include_screenshot=true`. Object:
```jsonc
"screenshot": { "format": "jpeg", "w": 1080, "h": 2400, "b64": "…" }
```
- Format **JPEG**, quality default **80** (matching ARC-MCP `ScreenCaptureProvider.DEFAULT_QUALITY`).
- **Longest side ≤ 700 px** (ARC-MCP `SCREENSHOT_MAX_SIZE = 700`). The device downscales before encoding; `w`/`h` reported are the emitted dimensions which may differ from `screen`.
- Base64 (standard, padded). Hard cap **1.5 MiB** encoded; if exceeded the device lowers quality/scale to fit rather than failing.
- **Annotation**: On-device MAY draw red dashed bounding boxes (stroke 2dp scaled) and pill-shaped semi-transparent red labels with white bold text showing the node's `node_id` with prefix stripped, but **this is not a protocol field** — annotations are a client-side rendering concern, invisible to the wire. (Resolves the "annotation format" question by ARC-MCP's own practice: annotation is drawn post-hoc on the client, not embedded.)
- On-device privacy redaction (PLAN.md §2 `privacy/*`) MAY blur regions before encoding; redaction happens before the bytes leave the device.

### 8.4 `node_id` targeting

Several commands accept `node_id` instead of coordinates. `node_id` is the
**string** id from the most recent `get_screen_state` tree on this connection.
IDs are **stable across re-parses while the UI is unchanged** (ARC-MCP hashes
`resourceId|className|bounds|depth|index|parentId`), so a caller may reuse ids
from a prior snapshot if nothing mutated; after any mutation, re-fetch.

The device resolves `node_id` via its internal node cache (ARC-MCP
`AccessibilityNodeCache`) and **MAY prefer an accessibility action over a
coordinate gesture** — i.e. on `tap {node_id}` it is permitted (and
recommended, matching ARC-MCP `ActionExecutorImpl.clickNode`) to call
`AccessibilityNodeInfo.performAction(ACTION_CLICK)` on the resolved node rather
than synthesizing a gesture at the bounds center. The two are observably
different in edge cases (WebView, custom views); v0 treats both as conforming
and does not wire a separate `click_node` vs `tap_node` command. If a caller
needs a *physical* tap specifically, it should use coordinate args.

If `node_id` cannot be resolved (stale, scrolled off and reaped, different
window), the device replies `error.code = "stale_node"`; the caller re-reads
and retries. Exactly one of a coordinate pair or `node_id` MUST be present for
targeting commands.

### 8.5 `scroll_to_node` and offscreen nodes

Nodes with the `off` flag (offscreen) cannot be acted on directly. v0 adopts
ARC-MCP's recovery: a `scroll_to_node` command scrolls the nearest scrollable
ancestor of the target into view.

| cmd | args | data | notes |
|---|---|---|---|
| `scroll_to_node` | `{ node_id }` | `{}` | scrolls target's nearest scrollable ancestor; ARC-MCP defaults: max **5** attempts, **300 ms** settle between attempts |

After `scroll_to_node`, the target should appear `on` in the next
`get_screen_state`; if still `off`, it is not scrollable into view and the
caller must give up or navigate. `scroll_to_node` on a node already on-screen
is a no-op returning `ok`.

### 8.6 `press_key` enumeration (v0)

`enter`, `tab`, `delete`, `backspace`, `escape`, `space`,
`dpad_up`, `dpad_down`, `dpad_left`, `dpad_right`, `dpad_center`.
Volume/power keys are intentionally excluded from v0.

### 8.7 Pagination (optional, ARC-MCP-aligned)

For very large trees, the device MAY support cursor pagination instead of the
`max_nodes` truncation in §8.2. If the device supports it, `get_screen_state`
accepts an optional `cursor` string and returns `next_cursor` in `data` when
more pages remain. The cursor format is `<snapshot_id>.<page>` where
`snapshot_id` is a base-36 timestamp and pages are **200** kept nodes each
(ARC-MCP `PAGE_SIZE = 200`, `CURSOR_RADIX = 36`). v0 does not require either
`max_nodes` truncation or cursor pagination to be implemented — but a device
MUST implement one of them so callers never receive an unbounded tree.

## 9. Events (device → server)

v0 keeps `event` **minimal** but not empty: unsolicited push is limited to two
operational kinds, both cheap and useful for a control UI. No screen-change
streaming in v0 (poll `get_screen_state` instead).

| kind | data | meaning |
|---|---|---|
| `control-revoked` | `{ reason }` | The device owner disabled control locally (accessibility service off, kill switch, app backgrounded past policy). Server SHOULD mark the device uncontrollable until re-`register`. |
| `capabilities-changed` | `{ capabilities: [...] }` | The device's capability set changed at runtime (e.g. a permission was granted). Server SHOULD replace its stored set; it does not re-`register`. |

Servers MUST ignore unknown `event.kind` values (§3). Devices are never required
to emit events; a server MUST function if none ever arrive.

## 10. Heartbeat

Covered operationally in §7. The only field beyond `type`/`device_id` is `seq`
(monotonic per connection, informational — lets the server spot gaps in logs).
Heartbeats are not acknowledged and carry no other payload in v0.

## 11. Pairing & auth

- **Pairing (one-time):** the server generates a short **pairing code** (out of
  band: shown in the server's admin UI). The device owner enters it in the app.
  The app POSTs the code to the server's pairing endpoint (implementation-defined
  HTTP route) and receives `{ device_id, token }` — a **long-lived bearer
  token**. Shown/returned once; the device persists it with `0600` perms.
- **Connect-time auth:** `register.auth` is:
  ```jsonc
  "auth": { "scheme": "token", "token": "<opaque bearer>" }
  ```
  `scheme` is an enum to allow future methods without a breaking change. **v0
  defines only `"token"`.** A server receiving an unknown scheme closes with
  `4003`. (Resolves the draft's token-vs-TOTP question: v0 is bearer token;
  `scheme` is the extension point, TOTP/mTLS can be added later as new schemes
  with no envelope change.)
- The token is opaque to the protocol (server decides format; random ≥ 256-bit
  recommended). It authenticates the device to the server only; it grants no
  ambient user identity (PLAN.md — device is not on the user's cookie/session).
- Token rotation/revocation is server-side and out of band; a revoked token
  causes `register` to be rejected with close `4003`.

## 12. Error taxonomy

`error` is **structured** (resolves the draft's string-vs-structured question):
```jsonc
{ "code": "…", "message": "…", "retryable": false, "details": { } }
```
- `code` — machine-readable, from the closed set below. Unknown codes MUST be
  treated by the server as a generic failure (forward-compat).
- `message` — human-readable, English, not for machine branching.
- `retryable` — whether an identical retry might succeed (transient vs terminal).
- `details` — optional, code-specific structured context.

| code | retryable | meaning |
|---|---|---|
| `unsupported` | no | `cmd` not in declared capabilities |
| `bad_args` | no | args missing/invalid for this `cmd` |
| `stale_node` | no | `node_id` not in latest tree (re-read) |
| `not_found` | no | target (app package, element) does not exist |
| `timeout` | yes | device exceeded `timeout_ms` |
| `cancelled` | no | superseded by `call-cancel` |
| `duplicate_request` | no | `request_id` already seen this connection |
| `overloaded` | yes | in-flight limit (§5) exceeded |
| `permission_denied` | no | OS/accessibility permission missing |
| `not_ready` | yes | service/permission still initializing |
| `device_error` | maybe | catch-all internal device failure (see `message`) |

## 13. WebSocket close codes (application range)

The device app MUST treat any close in `4000–4999` as terminal-for-this-attempt
and reconnect per §7, except `4003` (auth) which SHOULD surface to the user and
stop auto-retry until re-paired.

| code | name | who closes | meaning |
|---|---|---|---|
| `4002` | frame_too_large | either | frame exceeded §2 cap |
| `4003` | auth_failed | server | bad/expired token or unknown scheme |
| `4004` | protocol_version_unsupported | server | `register.protocol_version` not accepted |
| `4007` | duplicate_register | server | second `register` on one connection |
| `4008` | register_timeout | server | no `register` within 10 s |
| `4009` | replaced | server | same `device_id` reconnected elsewhere |
| `4010` | stale | server | heartbeat timeout (§7) |

## 14. Versioning

- `protocol_version` is an integer, **0** for this document, present on both
  `register` and `registered`.
- The server decides compatibility. If it cannot serve the device's version it
  closes with `4004` **before** any `call`. (Resolves the draft's versioning
  question: yes, an explicit integer on the handshake; server-authoritative.)
- Within a major version, additions are backward-compatible via the two ignore
  rules (unknown `type`, unknown fields). Removing/renaming a field or changing
  a `cmd`'s contract requires a version bump.

## 15. Decision log (M0 open questions → resolutions)

| draft §6 question | resolution | where |
|---|---|---|
| `register.auth` shape (token vs TOTP) | v0 = bearer `token`; `auth.scheme` enum is the extension point for TOTP/mTLS later | §11 |
| `get_screen_state` TSV columns | adopted ARC-MCP `CompactTreeFormatter` verbatim: 7 columns `node_id,class,text,desc,res_id,bounds,flags` + `note:` preamble + `hierarchy:` block; string (hash) `node_id`, `-` for empty, 100-char text truncation, `on/off,clk,lclk,foc,scr,edt,ena` flags | §8.2 |
| screenshot encoding/size + annotation | JPEG q≈70, ≤1600 px longest edge, ≤1.5 MiB b64; **annotation deferred** (client-side, tree bounds are truth) | §8.3 |
| does v0 need `event`? | yes, but minimal: only `control-revoked` + `capabilities-changed`; no screen streaming | §9 |
| error taxonomy (string vs structured) | structured `{code,message,retryable,details}` with a closed `code` set | §12 |
| `protocol_version` on handshake? | yes, integer `0` on `register`/`registered`, server-authoritative, close `4004` on mismatch | §14 |

Additional decisions made while finalizing: 4 MiB frame cap + `permessage-deflate`
(§2); exactly-one-response and mutating-command serialization (§5); non-idempotent
inputs → no cross-reconnect retry (§6); full-jitter backoff, discard in-flight on
reconnect (§7); `node_id` targets the latest tree with `stale_node` on miss
(§8.4); application close-code range (§13).

