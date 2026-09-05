// Module path is intentionally domain-less: this is a reference server users
// build from a clone, not a library published for `go get`. Retarget it if this
// ever needs to be importable.
module device-control/server

go 1.24

require github.com/coder/websocket v1.8.12
