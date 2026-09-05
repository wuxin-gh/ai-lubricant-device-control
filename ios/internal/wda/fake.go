package wda

import (
	"context"
	"fmt"
	"sync"

	"device-control/ios/internal/screen"
)

// Fake is an in-memory wda.Client for command tests. Calls are recorded so a
// test can assert what the handler asked WDA to do, and canned responses let
// the test exercise each handler path without a real iPhone.
type Fake struct {
	mu sync.Mutex

	// Canned responses.
	SourceXML []byte
	Scale     int
	Screen    screen.ScreenInfo
	PNG       []byte
	Apps      []App
	Err       error // when set, every call returns Err

	// Recorded calls.
	Taps                  []tapCall
	DoubleTaps            []point
	Swipes                []swipeCall
	Scrolls               []string
	TypedText             []string
	SetValues             []setValueCall
	Keys                  []string
	KeyboardDismissed     bool
	HomeCount             int
	RecentsCount          int
	LaunchedApps          []string
	ListAppsIncludeSystem []bool
}

type tapCall struct {
	X, Y, DurationMS int
}
type point struct{ X, Y int }
type swipeCall struct {
	X1, Y1, X2, Y2, DurationMS int
}
type setValueCall struct {
	NodeID, Text string
}

func (f *Fake) fail() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Err
}

func (f *Fake) Source(ctx context.Context) (*screen.MultiWindowResult, error) {
	if err := f.fail(); err != nil {
		return nil, err
	}
	scale := f.Scale
	if scale <= 0 {
		scale = 1
	}
	return ParseSource(f.SourceXML, scale)
}

func (f *Fake) Screenshot(ctx context.Context) ([]byte, error) {
	if err := f.fail(); err != nil {
		return nil, err
	}
	return f.PNG, nil
}

func (f *Fake) ScreenInfo(ctx context.Context) (screen.ScreenInfo, int, error) {
	if err := f.fail(); err != nil {
		return screen.ScreenInfo{}, 0, err
	}
	return f.Screen, f.Scale, nil
}

func (f *Fake) Tap(ctx context.Context, x, y, durationMS int) error {
	if err := f.fail(); err != nil {
		return err
	}
	f.mu.Lock()
	f.Taps = append(f.Taps, tapCall{x, y, durationMS})
	f.mu.Unlock()
	return nil
}

func (f *Fake) DoubleTap(ctx context.Context, x, y int) error {
	if err := f.fail(); err != nil {
		return err
	}
	f.mu.Lock()
	f.DoubleTaps = append(f.DoubleTaps, point{x, y})
	f.mu.Unlock()
	return nil
}

func (f *Fake) Swipe(ctx context.Context, x1, y1, x2, y2, durationMS int) error {
	if err := f.fail(); err != nil {
		return err
	}
	f.mu.Lock()
	f.Swipes = append(f.Swipes, swipeCall{x1, y1, x2, y2, durationMS})
	f.mu.Unlock()
	return nil
}

func (f *Fake) Scroll(ctx context.Context, direction string) error {
	if err := f.fail(); err != nil {
		return err
	}
	f.mu.Lock()
	f.Scrolls = append(f.Scrolls, direction)
	f.mu.Unlock()
	return nil
}

func (f *Fake) TypeText(ctx context.Context, text string) error {
	if err := f.fail(); err != nil {
		return err
	}
	f.mu.Lock()
	f.TypedText = append(f.TypedText, text)
	f.mu.Unlock()
	return nil
}

func (f *Fake) SetValue(ctx context.Context, nodeID, text string) error {
	if err := f.fail(); err != nil {
		return err
	}
	f.mu.Lock()
	f.SetValues = append(f.SetValues, setValueCall{nodeID, text})
	f.mu.Unlock()
	return nil
}

func (f *Fake) Key(ctx context.Context, key string) error {
	if err := f.fail(); err != nil {
		return err
	}
	f.mu.Lock()
	f.Keys = append(f.Keys, key)
	f.mu.Unlock()
	return nil
}

func (f *Fake) DismissKeyboard(ctx context.Context) error {
	if err := f.fail(); err != nil {
		return err
	}
	f.mu.Lock()
	f.KeyboardDismissed = true
	f.mu.Unlock()
	return nil
}

func (f *Fake) Home(ctx context.Context) error {
	if err := f.fail(); err != nil {
		return err
	}
	f.mu.Lock()
	f.HomeCount++
	f.mu.Unlock()
	return nil
}

func (f *Fake) Recents(ctx context.Context) error {
	if err := f.fail(); err != nil {
		return err
	}
	f.mu.Lock()
	f.RecentsCount++
	f.mu.Unlock()
	return nil
}

func (f *Fake) ListApps(ctx context.Context, includeSystem bool) ([]App, error) {
	if err := f.fail(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.ListAppsIncludeSystem = append(f.ListAppsIncludeSystem, includeSystem)
	f.mu.Unlock()
	return f.Apps, nil
}

func (f *Fake) LaunchApp(ctx context.Context, bundleID string) error {
	if err := f.fail(); err != nil {
		return err
	}
	f.mu.Lock()
	f.LaunchedApps = append(f.LaunchedApps, bundleID)
	f.mu.Unlock()
	return nil
}

// Compile-time check that Fake satisfies Client.
var _ Client = (*Fake)(nil)

func (f *Fake) String() string {
	return fmt.Sprintf("fake wda: taps=%d scrolls=%d typed=%d", len(f.Taps), len(f.Scrolls), len(f.TypedText))
}
