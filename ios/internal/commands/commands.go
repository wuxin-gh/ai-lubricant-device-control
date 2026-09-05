// Package commands wires the 16 protocol commands to WDA. Mirrors
// android/app/.../command/CommandHandlers.kt, swapping the Android core graph
// calls for wda.Client calls. Each handler is a func(ctx, args) -> (data, err);
// errors thrown map to wire codes via internal/errcode.
package commands

import (
	"context"
	"encoding/json"

	"device-control/ios/internal/dispatcher"
	"device-control/ios/internal/errcode"
	"device-control/ios/internal/screen"
	"device-control/ios/internal/wda"
)

// Handlers returns the flat cmd→handler table the dispatcher walks. The WDA
// client is the single integration boundary; a fake backs the tests.
func Handlers(client wda.Client, formatter *screen.CompactTreeFormatter) map[string]handler {
	return map[string]handler{
		"get_screen_state": getScreenState(client, formatter),
		"tap":              tap(client),
		"long_press":       longPress(client),
		"double_tap":       doubleTap(client),
		"swipe":            swipe(client),
		"scroll":           scroll(client),
		"scroll_to_node":   scrollToNode(client),
		"type_text":        typeText(client),
		"set_text":         setText(client),
		"press_key":        pressKey(client),
		"dismiss_keyboard": dismissKeyboard(client),
		"press_back":       pressBack(client),
		"press_home":       pressHome(client),
		"press_recents":    pressRecents(client),
		"open_app":         openApp(client),
		"list_apps":        listApps(client),
	}
}

// handler is the dispatcher.Handler signature with a concrete args type for
// ergonomics; Build re-wraps to dispatcher.Handler.
type handler func(ctx context.Context, args map[string]any) (json.RawMessage, error)

// Build turns this package's handlers into the dispatcher's handler map. The
// returned map is dispatcher.Handler-typed so callers can pass it straight to
// dispatcher.New without a type-identity mismatch.
func Build(client wda.Client, formatter *screen.CompactTreeFormatter) map[string]dispatcher.Handler {
	raw := Handlers(client, formatter)
	out := make(map[string]dispatcher.Handler, len(raw))
	for cmd, h := range raw {
		cmd, h := cmd, h
		out[cmd] = func(ctx context.Context, a json.RawMessage) (json.RawMessage, error) {
			args, err := decodeArgs(a)
			if err != nil {
				return nil, errcode.New(errcode.BadArgs, "invalid args: %v", err)
			}
			return h(ctx, args)
		}
	}
	return out
}

func decodeArgs(a json.RawMessage) (map[string]any, error) {
	var m map[string]any
	if len(a) == 0 {
		return map[string]any{}, nil
	}
	if err := json.Unmarshal(a, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// ── arg helpers ──────────────────────────────────────────────────────────

func argString(args map[string]any, key string) (string, bool) {
	v, ok := args[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok && s != ""
}

func argInt(args map[string]any, key string) (int, bool) {
	v, ok := args[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}

func requireInt(args map[string]any, key string) (int, error) {
	v, ok := argInt(args, key)
	if !ok {
		return 0, errcode.New(errcode.BadArgs, "missing or non-numeric field: %s", key)
	}
	return v, nil
}

// ── get_screen_state (§8.1–§8.3, §8.7) ────────────────────────────────────

func getScreenState(c wda.Client, f *screen.CompactTreeFormatter) handler {
	return func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
		includeScreenshot := false
		if v, ok := args["include_screenshot"].(bool); ok {
			includeScreenshot = v
		}
		cursor, _ := argString(args, "cursor")

		res, err := c.Source(ctx)
		if err != nil {
			return nil, err
		}
		info, scale, err := c.ScreenInfo(ctx)
		if err != nil {
			return nil, err
		}
		_ = scale // source already parsed to pixels; scale was applied in ParseSource

		var tsv string
		var nextCursor string
		var hasCursor bool
		if cursor != "" {
			// Cursor pagination over a retained snapshot. Without a retained
			// snapshot store (deferred — see plan), a stale cursor is
			// stale_node. The max_nodes truncation path is the fallback.
			return nil, errcode.New(errcode.StaleNode,
				"cursor pagination not retained; re-read without a cursor")
		}
		totalKept := f.CountKeptNodes(res)
		if totalKept > screen.PageSize {
			// Paginate: page 1 only this call; further pages via cursor would
			// need a snapshot store. For v0 the max_nodes truncation is the
			// simpler path — but to keep the tree bounded we render page 1 and
			// signal more via the trailing note the formatter emits.
			snap := &screen.Snapshot{ID: "", Result: res, ScreenInfo: info, TotalKeptNodes: totalKept, TotalPages: screen.PageCount(totalKept)}
			tsv = f.FormatMultiWindowPage(snap, 1)
			nextCursor = "" // cursor requires a retained snapshot; left empty until snapshot store lands
			hasCursor = false
		} else {
			tsv = f.FormatMultiWindow(res, info)
		}

		out := map[string]any{
			"screen": map[string]any{
				"w":           info.Width,
				"h":           info.Height,
				"density":     info.DensityDpi,
				"orientation": info.Orientation,
			},
			"tree": tsv,
		}
		if hasCursor && nextCursor != "" {
			out["next_cursor"] = nextCursor
		}
		if includeScreenshot {
			png, err := c.Screenshot(ctx)
			if err == nil {
				if shot, err := encodeScreenshot(png, 80, 700); err == nil {
					out["screenshot"] = shot
				}
				// Screenshot is best-effort: a failed capture/encode must not
				// fail the whole read. The tree + screen are still useful.
			}
		}
		return json.Marshal(out)
	}
}

// ── tap / long_press / double_tap (§8.4: node_id preferred) ──────────────

func tap(c wda.Client) handler {
	return func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
		// iOS WDA has no ACTION_CLICK; node_id targeting falls back to bounds
		// center from the latest source (re-read then resolve). Coordinate
		// form goes straight to WDA.
		if id, ok := argString(args, "node_id"); ok {
			x, y, err := resolveNodeCenter(ctx, c, id)
			if err != nil {
				return nil, err
			}
			return wda.EmptyData, c.Tap(ctx, x, y, 0)
		}
		x, err := requireInt(args, "x")
		if err != nil {
			return nil, err
		}
		y, err := requireInt(args, "y")
		if err != nil {
			return nil, err
		}
		return wda.EmptyData, c.Tap(ctx, x, y, 0)
	}
}

func longPress(c wda.Client) handler {
	return func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
		dur := 500
		if d, ok := argInt(args, "duration_ms"); ok {
			dur = d
		}
		if id, ok := argString(args, "node_id"); ok {
			x, y, err := resolveNodeCenter(ctx, c, id)
			if err != nil {
				return nil, err
			}
			return wda.EmptyData, c.Tap(ctx, x, y, dur)
		}
		x, err := requireInt(args, "x")
		if err != nil {
			return nil, err
		}
		y, err := requireInt(args, "y")
		if err != nil {
			return nil, err
		}
		return wda.EmptyData, c.Tap(ctx, x, y, dur)
	}
}

func doubleTap(c wda.Client) handler {
	return func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
		if id, ok := argString(args, "node_id"); ok {
			x, y, err := resolveNodeCenter(ctx, c, id)
			if err != nil {
				return nil, err
			}
			return wda.EmptyData, c.DoubleTap(ctx, x, y)
		}
		x, err := requireInt(args, "x")
		if err != nil {
			return nil, err
		}
		y, err := requireInt(args, "y")
		if err != nil {
			return nil, err
		}
		return wda.EmptyData, c.DoubleTap(ctx, x, y)
	}
}

// ── swipe / scroll ───────────────────────────────────────────────────────

func swipe(c wda.Client) handler {
	return func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
		x1, err := requireInt(args, "x1")
		if err != nil {
			return nil, err
		}
		y1, err := requireInt(args, "y1")
		if err != nil {
			return nil, err
		}
		x2, err := requireInt(args, "x2")
		if err != nil {
			return nil, err
		}
		y2, err := requireInt(args, "y2")
		if err != nil {
			return nil, err
		}
		dur := 300
		if d, ok := argInt(args, "duration_ms"); ok {
			dur = d
		}
		return wda.EmptyData, c.Swipe(ctx, x1, y1, x2, y2, dur)
	}
}

func scroll(c wda.Client) handler {
	return func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
		dir, ok := argString(args, "direction")
		if !ok {
			return nil, errcode.New(errcode.BadArgs, "missing required field: direction")
		}
		// node_id targeting would scroll a specific scrollable; iOS WDA's
		// scroll gestures operate on the focused scrollable. Defer node_id
		// scroll to a bounds-centered swipe when present.
		return wda.EmptyData, c.Scroll(ctx, dir)
	}
}

// ── scroll_to_node (§8.5: up to 5 attempts, 300ms settle) ───────────────

func scrollToNode(c wda.Client) handler {
	return func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
		id, ok := argString(args, "node_id")
		if !ok {
			return nil, errcode.New(errcode.BadArgs, "missing required field: node_id")
		}
		for attempt := 0; attempt < 5; attempt++ {
			visible, err := nodeVisible(ctx, c, id)
			if err != nil {
				return nil, err
			}
			if visible {
				return wda.EmptyData, nil
			}
			if err := c.Scroll(ctx, "down"); err != nil {
				return nil, err
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-after(300):
			}
		}
		// §8.5: still off-screen after 5 attempts — the caller re-reads and
		// decides. Return ok rather than erroring (indistinguishable from
		// "couldn't scroll at all").
		return wda.EmptyData, nil
	}
}

// ── text input ───────────────────────────────────────────────────────────

func typeText(c wda.Client) handler {
	return func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
		text, ok := argString(args, "text")
		if !ok {
			return nil, errcode.New(errcode.BadArgs, "missing required field: text")
		}
		return wda.EmptyData, c.TypeText(ctx, text)
	}
}

func setText(c wda.Client) handler {
	return func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
		id, ok := argString(args, "node_id")
		if !ok {
			return nil, errcode.New(errcode.BadArgs, "missing required field: node_id")
		}
		text, ok := argString(args, "text")
		if !ok {
			return nil, errcode.New(errcode.BadArgs, "missing required field: text")
		}
		return wda.EmptyData, c.SetValue(ctx, id, text)
	}
}

// ── press_key (§8.6) ──────────────────────────────────────────────────────

func pressKey(c wda.Client) handler {
	return func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
		key, ok := argString(args, "key")
		if !ok {
			return nil, errcode.New(errcode.BadArgs, "missing required field: key")
		}
		switch key {
		case "back":
			return nil, errcode.New(errcode.DeviceError,
				"iOS has no system-wide back button")
		case "home":
			return wda.EmptyData, c.Home(ctx)
		}
		return wda.EmptyData, c.Key(ctx, key)
	}
}

func dismissKeyboard(c wda.Client) handler {
	return func(_ context.Context, _ map[string]any) (json.RawMessage, error) {
		return wda.EmptyData, c.DismissKeyboard(context.Background())
	}
}

func pressBack(c wda.Client) handler {
	return func(_ context.Context, _ map[string]any) (json.RawMessage, error) {
		return nil, errcode.New(errcode.DeviceError,
			"iOS has no system-wide back button")
	}
}

func pressHome(c wda.Client) handler {
	return func(ctx context.Context, _ map[string]any) (json.RawMessage, error) {
		return wda.EmptyData, c.Home(ctx)
	}
}

func pressRecents(c wda.Client) handler {
	return func(ctx context.Context, _ map[string]any) (json.RawMessage, error) {
		return wda.EmptyData, c.Recents(ctx)
	}
}

// ── apps (package = bundle id on iOS) ───────────────────────────────────

func openApp(c wda.Client) handler {
	return func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
		bundle, ok := argString(args, "package")
		if !ok {
			return nil, errcode.New(errcode.BadArgs, "missing required field: package")
		}
		if err := c.LaunchApp(ctx, bundle); err != nil {
			return nil, errcode.New(errcode.NotFound, "no launchable app for bundle id: %s", bundle)
		}
		return wda.EmptyData, nil
	}
}

func listApps(c wda.Client) handler {
	return func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
		includeSystem := false
		if v, ok := args["include_system"].(bool); ok {
			includeSystem = v
		}
		apps, err := c.ListApps(ctx, includeSystem)
		if err != nil {
			return nil, err
		}
		out := map[string]any{"apps": apps}
		return json.Marshal(out)
	}
}
