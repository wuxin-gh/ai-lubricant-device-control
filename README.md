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

Pre-alpha. Protocol v0 is finalized — see [spec/protocol-v0.md](spec/protocol-v0.md).
M1 (Android `core/` library) is done: it compiles and its 261 unit tests pass.
M3 (Go reference server) is done. Not yet runnable end-to-end — the Android app
shell (M2) is the next milestone. See [PLAN.md](PLAN.md) for the full plan,
roadmap (M0–M4), and the hard constraints this project operates under.

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

Nothing runnable end-to-end yet — the device app (M2) is still ahead. The
protocol is frozen in [spec/protocol-v0.md](spec/protocol-v0.md), the
[Android `core/` library](android/core/) compiles and tests green, and the
[Go reference server](server/) builds and runs. Next up is M2. See
[PLAN.md §6](PLAN.md#6-实施里程碑).

## License

MIT. See [LICENSE](LICENSE) and [NOTICE](NOTICE) for third-party attributions.
