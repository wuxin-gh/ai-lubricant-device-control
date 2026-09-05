package devicecontrol

import (
	"errors"
	"time"
)

// ErrNotPaired is returned by RunDevice when the credential file does not exist
// (the device has not been paired yet). Call Pair first.
var ErrNotPaired = errors.New("device-control: not paired (no credential; call Pair first)")

// ErrStartFailed wraps a transient failure to reach the device or launch WDA
// (go-ios discovery, WDA launch, port forward). It is the retry gate for
// RunDeviceWithRetry: a start failure is retried with backoff, while
// ErrNotPaired and the 4003/4004 fatal closes are terminal.
//
// errors.Is(err, ErrStartFailed) is the single place that distinguishes
// "the device will probably come back" from "stop trying".
var ErrStartFailed = errors.New("device-control: device start failed (no device / WDA down)")

// stateTick is the polling interval for the OnState watcher. wsclient.State()
// has no channel; the watcher polls at a coarse rate and only fires OnState on
// a transition, so the cost is negligible.
var stateTick = 100 * time.Millisecond
