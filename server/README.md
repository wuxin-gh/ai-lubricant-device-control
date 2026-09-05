# device-control reference server

A minimal, self-hosted server implementing [`spec/protocol-v0.md`](../spec/protocol-v0.md).
Single static binary, no database, no external services.

This is a **reference implementation**: it exists so you can exercise the
protocol with `curl` and so device-side work (M1/M2) has something to dial.
It is deliberately small — you are meant to be able to read all of it.

## Build and run

```sh
cd server
go build ./cmd/device-control-server

# The admin token guards every /admin route. There is no default; the server
# refuses to start without one, because the admin API can drive a phone.
# Passed by env, not a flag, to keep it out of the process list.
export DEVICE_CONTROL_ADMIN_TOKEN=$(openssl rand -base64 32)
./device-control-server -addr :8080
```

Flags: `-addr` (listen address, default `127.0.0.1:8787`), `-state` (path to the
JSON state file, default `./device-control-state.json`, written `0600`), `-v`
(debug logging).

> **TLS:** the server speaks plain HTTP. Run it behind a TLS-terminating reverse
> proxy for anything but loopback/LAN testing — the protocol carries a
> long-lived device token, and §2 of the spec requires TLS off-LAN.

## Endpoints

| method | path | auth | purpose |
|---|---|---|---|
| `POST` | `/pair` | pairing code | device redeems a code for `{device_id, token}` |
| `GET`/`WS` | `/ws/device` | device token (in `register`) | the device protocol endpoint (spec §3) |
| `POST` | `/admin/pairing-codes` | admin bearer | mint a pairing code |
| `GET` | `/admin/devices` | admin bearer | list devices + online state |
| `POST` | `/admin/devices/{id}/call` | admin bearer | send one `call`, block for the response |
| `DELETE` | `/admin/devices/{id}` | admin bearer | revoke credentials, drop the connection |

The `/admin` surface is **not** part of the wire protocol — spec §1 leaves the
northbound API implementation-defined. Treat it as a debugging console.

## Walkthrough

```sh
ADMIN="Authorization: Bearer $DEVICE_CONTROL_ADMIN_TOKEN"

# 1. Mint a pairing code (valid 10 minutes, single use).
curl -sX POST localhost:8080/admin/pairing-codes -H "$ADMIN" \
     -H 'Content-Type: application/json' -d '{"label":"my pixel"}'
# → {"code":"K7QM-3XPD","expires_at":"..."}

# 2. The device redeems it. Returns the long-lived token exactly once.
curl -sX POST localhost:8080/pair \
     -H 'Content-Type: application/json' -d '{"code":"K7QM-3XPD"}'
# → {"device_id":"dev_...","token":"...","protocol_version":0}

# 3. The device connects to ws://localhost:8080/ws/device and sends `register`.
#    (M2's Android app does this; until then see the tests for a fake device.)

# 4. Drive it.
curl -sX POST localhost:8080/admin/devices/dev_XXX/call -H "$ADMIN" \
     -H 'Content-Type: application/json' \
     -d '{"cmd":"tap","args":{"x":540,"y":1200}}'
# → {"ok":true,"data":{}}
```

A device that refuses a command answers `200` with `{"ok":false,"error":{...}}` —
a device saying "no" is a valid protocol answer, not a server error. Reserve
5xx for the server's own failures.

## What it implements

Spec sections enforced here: the §4 handshake sequence (register-first, 10 s
timeout, one register per connection, last-writer-wins eviction with close
`4009`), §5 exactly-one-response plus the 8-call in-flight cap and the
`timeout_ms + 5s` server budget, §7 heartbeat reaping at 60 s and discarding
in-flight state on disconnect, §8 the capability gate (an undeclared `cmd` is
rejected before it reaches the device), §9 both event kinds, §11 pairing, §12
verbatim pass-through of device error objects, and the §13 close codes.

Credentials are stored as SHA-256 hashes with constant-time comparison, so the
state file cannot yield usable tokens.

## What it does not do

No TLS termination, no multi-operator accounts or RBAC, no audit log, no
persistent call history, no rate limiting, no clustering (state is in-memory
plus one JSON file; the hub holds at most one connection per device in a single
process). Screen-state responses are passed through untouched — no decoding of
the §8.2 TSV. Deliberate: M4 covers hardening, and self-hosters are expected to
replace or extend this rather than deploy it as-is at scale.

## Tests

```sh
go test ./...
```

The suite in `internal/wsdevice` drives a fake device over a real loopback
WebSocket and covers the acceptance path (pair → register → call → response)
plus auth rejection, the capability gate, version rejection, connection
replacement, unknown-type forward-compat, and revocation.

> `go test -race` needs a C toolchain new enough for the Go version's race
> runtime. With an old MinGW gcc (e.g. 8.1.0) it aborts with `0xc0000139`
> (entry point not found) before running any test — reproducible with a
> two-line empty test, so it is a toolchain gap, not a test failure. The hub is
> concurrent, so running `-race` on a machine with a current toolchain is worth
> doing.
