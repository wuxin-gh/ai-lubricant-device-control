// Module device-control/ios is the iOS device-side endpoint of the device-control
// protocol. It is NOT an on-device iOS app: it is a host-side Go driver that talks
// to an iPhone over USB/LAN via go-ios, drives WebDriverAgent (WDA) on the device,
// and dials a device-control server exactly like the Android app does.
//
// See ios/README.md and ../PLAN.md for the architecture and the hard constraints
// (non-jailbroken iOS has no equivalent of Android's accessibility-service control;
// the farm/host-driver shape is the only viable one).
module device-control/ios

go 1.26.0

require (
	github.com/coder/websocket v1.8.15
	github.com/danielpaulus/go-ios v1.3.2
	golang.org/x/image v0.45.0
)

require (
	github.com/Masterminds/semver v1.5.0 // indirect
	github.com/cenkalti/backoff v2.2.1+incompatible // indirect
	github.com/google/btree v1.1.2 // indirect
	github.com/google/uuid v1.1.2 // indirect
	github.com/grandcat/zeroconf v1.0.0 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/miekg/dns v1.1.57 // indirect
	github.com/pierrec/lz4 v2.6.1+incompatible // indirect
	github.com/quic-go/quic-go v0.59.1 // indirect
	github.com/songgao/water v0.0.0-20200317203138-2b4b6d7c09d8 // indirect
	github.com/tadglines/go-pkgs v0.0.0-20210623144937-b983b20f54f9 // indirect
	github.com/vishvananda/netlink v1.3.1 // indirect
	github.com/vishvananda/netns v0.0.5 // indirect
	go.mozilla.org/pkcs7 v0.9.0 // indirect
	golang.org/x/crypto v0.54.0 // indirect
	golang.org/x/exp v0.0.0-20230725093048-515e97ebf090 // indirect
	golang.org/x/mod v0.38.0 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	golang.org/x/time v0.5.0 // indirect
	golang.org/x/tools v0.48.0 // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
	gvisor.dev/gvisor v0.0.0-20240405191320-0878b34101b5 // indirect
	howett.net/plist v1.0.1 // indirect
	software.sslmate.com/src/go-pkcs12 v0.7.2 // indirect
)
