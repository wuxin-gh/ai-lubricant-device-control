# device-control

A self-hosted, open-source tool for remotely controlling your **own** Android
device from a server you run yourself. The device app dials out to your server
over a long-lived WebSocket; the server sends commands (read screen, tap, type,
swipe, screenshot) and the device executes them and reports results.

> **Self-hosted only.** This repository ships **no default server address**.
> You run the server yourself and point the device app at it. The project does
> not collect, relay, or host any user data — whoever deploys the server is the
> data controller for their own deployment.

## Status

Pre-alpha, but end-to-end runnable. Protocol v0 is finalized — see
[spec/protocol-v0.md](spec/protocol-v0.md) — plus two non-privileged extension
commands (`get_foreground_app`, `open_url`; §8.8). Both device implementations
are in place: the Android app (`android/`, accessibility service + foreground
WebSocket) and the iOS driver (`ios/`, host node over WebDAgent/go-ios). The Go
reference server (`server/`) builds and runs.

Neither device path is verified against a live phone in this repository's CI —
Android has unit coverage only (mocked), and the iOS driver is marked
`REAL-DEVICE GATE` at its WDA/go-ios boundaries. Treat "works on a real device"
as unproven until you run it on one.

See [PLAN.md](PLAN.md) for the roadmap (M0–M4) and the hard constraints, and
[spec/protocol-v0.md Appendix A](spec/protocol-v0.md#appendix-a--capability-alignment-and-honest-degradation)
for the capability-alignment and honest-degradation table (what a non-privileged
implementation can and cannot do relative to adb/host-driven tooling).

## Scope / what this is

- A tool, analogous to scrcpy or android-remote-control-mcp.
- For operating **devices you own and control** (your phone, a dedicated test
  device, an internal farm).
- Free protocol (not MCP) for low latency and NAT traversal convenience.

## What this is NOT

- Not a hosted service. No vendor backend is bundled or implied.
- Not for operating devices you do not own. Misuse is out of scope and not
  supported.
- Not affiliated with any company or commercial product.

## Quick start

Run the [Go reference server](server/), pair a device (Android app or the iOS
host node), point it at your server, and drive it. The protocol is frozen in
[spec/protocol-v0.md](spec/protocol-v0.md); the Android `core/` library and
[Go reference server](server/) build and test green; the Android app and iOS
driver are complete but await real-device acceptance. See
[PLAN.md §6](PLAN.md#6-实施里程碑) for milestone status.

## License

MIT. See [LICENSE](LICENSE) and [NOTICE](NOTICE) for third-party attributions.
