package dev.devicecontrol.app.command

import android.content.Context
import android.content.Intent
import android.content.pm.ApplicationInfo
import android.content.pm.PackageManager
import android.os.Build
import android.view.KeyEvent
import dev.devicecontrol.app.CoreGraph
import dev.devicecontrol.core.CoreException
import dev.devicecontrol.core.accessibility.AccessibilityNodeData
import dev.devicecontrol.core.accessibility.BoundsData
import dev.devicecontrol.core.accessibility.ScrollDirection
import dev.devicecontrol.core.screencapture.ScreenshotData
import kotlinx.coroutines.delay
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.doubleOrNull
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.longOrNull
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonArray
import kotlinx.serialization.json.putJsonObject

/**
 * The 16 command handlers wired into [CommandDispatcher].
 *
 * Each handler is a suspend lambda `suspend (JsonObject) -> JsonElement` returning the
 * `data` for an `ok=true` response. Handlers throw on failure — generally
 * [CommandException] with an explicit wire code, or let a [CoreException] / generic
 * JVM exception bubble — and [CommandDispatcher.mapException] turns the thrown
 * exception into an `ok=false` frame. Keeping the dispatch table as pure business
 * logic and putting error framing in the protocol layer means a handler author
 * never has to think about the wire envelope.
 *
 * Plan.md §1: no default server address, no hard-coded packages. All state flows
 * through [CoreGraph]; handlers never reach for endpoints or bundle identifiers.
 */
class CommandHandlers(
    private val graph: CoreGraph,
    private val context: Context,
) {
    /** The flat `cmd → handler` table the dispatcher walks. */
    val dispatch: Map<String, suspend (JsonObject) -> JsonElement> =
        listOf(
            "get_screen_state" to ::getScreenState,
            "tap" to ::tap,
            "long_press" to ::longPress,
            "double_tap" to ::doubleTap,
            "swipe" to ::swipe,
            "scroll" to ::scroll,
            "scroll_to_node" to ::scrollToNode,
            "type_text" to ::typeText,
            "set_text" to ::setText,
            "press_key" to ::pressKey,
            "dismiss_keyboard" to ::dismissKeyboard,
            "press_back" to ::pressBack,
            "press_home" to ::pressHome,
            "press_recents" to ::pressRecents,
            "open_app" to ::openApp,
            "list_apps" to ::listApps,
        ).associate { it.first to it.second }

    // ── get_screen_state ──────────────────────────────────────────────────────────
    // §8.1 + §8.2 + §8.3 + §8.7. include_screenshot defaults false; cursor defaults null.
    // max_nodes is accepted but v0 lets the device pick its own bound — we use cursor
    // pagination (§8.7), so max_nodes is advisory and currently ignored.

    private suspend fun getScreenState(args: JsonObject): JsonElement {
        val includeScreenshot = args["include_screenshot"]?.jsonPrimitive?.booleanOrNull ?: false
        val cursor = args["cursor"]?.let { if (it is JsonNull) null else it.jsonPrimitive.contentOrNull }

        val screen = graph.screenReader.readFresh()
        val rendered = graph.screenReader.render(screen, cursor)

        return buildJsonObject {
            putJsonObject("screen") {
                put("w", rendered.screenInfo.width)
                put("h", rendered.screenInfo.height)
                put("density", rendered.screenInfo.densityDpi)
                put("orientation", rendered.screenInfo.orientation)
            }
            put("tree", rendered.tsv)
            if (rendered.cursor != null) {
                put("next_cursor", rendered.cursor)
            }
            if (includeScreenshot) {
                val shot = graph.screenCapture.captureScreenshot().getOrElse {
                    // Screenshot is best-effort; a failed capture must not fail the whole
                    // read. The tree + screen are still useful on their own.
                    return@buildJsonObject
                }
                put("screenshot", shot.toJson())
            }
        }
    }

    // ── tap / long_press / double_tap ────────────────────────────────────────────
    // §8.4: node_id preferred over coords when present.

    private suspend fun tap(args: JsonObject): JsonElement {
        val windows = graph.screenReader.readFreshWindows()
        val nodeId = args["node_id"]?.jsonPrimitive?.contentOrNull
        if (nodeId != null) {
            graph.actionExecutor.clickNode(nodeId, windows).getOrThrow()
        } else {
            val x = requireFloat(args, "x")
            val y = requireFloat(args, "y")
            graph.actionExecutor.tap(x, y).getOrThrow()
        }
        return buildJsonObject {}
    }

    private suspend fun longPress(args: JsonObject): JsonElement {
        val windows = graph.screenReader.readFreshWindows()
        val nodeId = args["node_id"]?.jsonPrimitive?.contentOrNull
        if (nodeId != null) {
            graph.actionExecutor.longClickNode(nodeId, windows).getOrThrow()
        } else {
            val x = requireFloat(args, "x")
            val y = requireFloat(args, "y")
            val duration = args["duration_ms"]?.jsonPrimitive?.longOrNull ?: 500L
            graph.actionExecutor.longPress(x, y, duration).getOrThrow()
        }
        return buildJsonObject {}
    }

    private suspend fun doubleTap(args: JsonObject): JsonElement {
        val windows = graph.screenReader.readFreshWindows()
        val nodeId = args["node_id"]?.jsonPrimitive?.contentOrNull
        if (nodeId != null) {
            // Core only has coordinate-based doubleTap — resolve node to bounds center first.
            val node = graph.elementFinder.findNodeById(windows, nodeId)
                ?: throw CommandException(ErrorCode.STALE_NODE, "node_id not found: $nodeId")
            val (cx, cy) = node.bounds.center()
            graph.actionExecutor.doubleTap(cx, cy).getOrThrow()
        } else {
            val x = requireFloat(args, "x")
            val y = requireFloat(args, "y")
            graph.actionExecutor.doubleTap(x, y).getOrThrow()
        }
        return buildJsonObject {}
    }

    // ── swipe ────────────────────────────────────────────────────────────────────

    private suspend fun swipe(args: JsonObject): JsonElement {
        val x1 = requireFloat(args, "x1")
        val y1 = requireFloat(args, "y1")
        val x2 = requireFloat(args, "x2")
        val y2 = requireFloat(args, "y2")
        val duration = args["duration_ms"]?.jsonPrimitive?.longOrNull ?: 300L
        graph.actionExecutor.swipe(x1, y1, x2, y2, duration).getOrThrow()
        return buildJsonObject {}
    }

    // ── scroll ───────────────────────────────────────────────────────────────────

    private suspend fun scroll(args: JsonObject): JsonElement {
        val direction = parseDirection(args["direction"])
        val nodeId = args["node_id"]?.jsonPrimitive?.contentOrNull
        if (nodeId != null) {
            val windows = graph.screenReader.readFreshWindows()
            graph.actionExecutor.scrollNode(nodeId, direction, windows).getOrThrow()
        } else {
            graph.actionExecutor.scroll(direction).getOrThrow()
        }
        return buildJsonObject {}
    }

    // ── scroll_to_node ───────────────────────────────────────────────────────────
    // §8.5: ARC-MCP recovery — up to 5 scroll attempts, 300 ms settle each, until the
    // target node is visible. core has no single method for this; composed here.

    private suspend fun scrollToNode(args: JsonObject): JsonElement {
        val nodeId = args["node_id"]?.jsonPrimitive?.contentOrNull
            ?: throw CommandException(ErrorCode.BAD_ARGS, "missing required field: node_id")

        val initWindows = graph.screenReader.readFreshWindows()
        val node = graph.elementFinder.findNodeById(initWindows, nodeId)
            ?: throw CommandException(ErrorCode.STALE_NODE, "node_id not found: $nodeId")

        // Already on-screen? No-op (§8.5).
        if (node.visible) return buildJsonObject {}

        val scrollable = findNearestScrollableAncestor(nodeId, initWindows)
            ?: throw CommandException(
                ErrorCode.STALE_NODE,
                "node_id $nodeId has no scrollable ancestor",
            )

        repeat(MAX_SCROLL_ATTEMPTS) { attempt ->
            graph.actionExecutor.scrollNode(scrollable, ScrollDirection.DOWN, initWindows)
            delay(SETTLE_MS)
            val refreshed = graph.screenReader.readFreshWindows()
            val current = graph.elementFinder.findNodeById(refreshed, nodeId)
                ?: throw CommandException(
                    ErrorCode.STALE_NODE,
                    "node_id $nodeId vanished during scroll (attempt ${attempt + 1})",
                )
            if (current.visible) return buildJsonObject {}
        }
        // Still off-screen after 5 attempts: §8.5 says the caller gives up. We return ok
        // rather than erroring — the command did what it could; the caller re-reads and
        // decides. (An error here would be indistinguishable from "couldn't scroll at all".)
        return buildJsonObject {}
    }

    // ── text input ────────────────────────────────────────────────────────────────

    private suspend fun typeText(args: JsonObject): JsonElement {
        val text = args["text"]?.jsonPrimitive?.contentOrNull
            ?: throw CommandException(ErrorCode.BAD_ARGS, "missing required field: text")
        requireAccessibilityIme("type_text")
        graph.typeInputController.commitText(text, newCursorPosition = 1)
        return buildJsonObject {}
    }

    private suspend fun setText(args: JsonObject): JsonElement {
        val nodeId = args["node_id"]?.jsonPrimitive?.contentOrNull
            ?: throw CommandException(ErrorCode.BAD_ARGS, "missing required field: node_id")
        val text = args["text"]?.jsonPrimitive?.contentOrNull
            ?: throw CommandException(ErrorCode.BAD_ARGS, "missing required field: text")
        val windows = graph.screenReader.readFreshWindows()
        graph.actionExecutor.setTextOnNode(nodeId, text, windows).getOrThrow()
        return buildJsonObject {}
    }

    // ── press_key ─────────────────────────────────────────────────────────────────
    // §8.6 enumeration. back/home have dedicated commands (press_back/press_home) but
    // are also accepted here for convenience. Keys that affect the focused editor
    // (enter, delete, tab, ...) route through the IME input connection so they land on
    // the current field, not anywhere arbitrary.

    private suspend fun pressKey(args: JsonObject): JsonElement {
        val key = args["key"]?.jsonPrimitive?.contentOrNull
            ?: throw CommandException(ErrorCode.BAD_ARGS, "missing required field: key")
        when (key.lowercase()) {
            "back" -> graph.actionExecutor.pressBack().getOrThrow()
            "home" -> graph.actionExecutor.pressHome().getOrThrow()
            else -> {
                val keyCode = parseKeyCode(key)
                    ?: throw CommandException(ErrorCode.UNSUPPORTED, "unknown key: $key")
                requireAccessibilityIme("press_key '$key'")
                val down = KeyEvent(KeyEvent.ACTION_DOWN, keyCode)
                val up = KeyEvent(KeyEvent.ACTION_UP, keyCode)
                graph.typeInputController.sendKeyEvent(down)
                graph.typeInputController.sendKeyEvent(up)
            }
        }
        return buildJsonObject {}
    }

    private suspend fun dismissKeyboard(@Suppress("UNUSED_PARAMETER") args: JsonObject): JsonElement {
        graph.actionExecutor.dismissKeyboard().getOrThrow()
        return buildJsonObject {}
    }

    private suspend fun pressBack(@Suppress("UNUSED_PARAMETER") args: JsonObject): JsonElement {
        graph.actionExecutor.pressBack().getOrThrow()
        return buildJsonObject {}
    }

    private suspend fun pressHome(@Suppress("UNUSED_PARAMETER") args: JsonObject): JsonElement {
        graph.actionExecutor.pressHome().getOrThrow()
        return buildJsonObject {}
    }

    private suspend fun pressRecents(@Suppress("UNUSED_PARAMETER") args: JsonObject): JsonElement {
        graph.actionExecutor.pressRecents().getOrThrow()
        return buildJsonObject {}
    }

    // ── apps ──────────────────────────────────────────────────────────────────────
    // core does NOT implement open_app or list_apps (PLAN.md step 8). Use
    // PackageManager directly; the manifest declares QUERY_ALL_PACKAGES for this.

    private suspend fun openApp(args: JsonObject): JsonElement {
        val pkg = args["package"]?.jsonPrimitive?.contentOrNull
            ?: throw CommandException(ErrorCode.BAD_ARGS, "missing required field: package")
        val intent = context.packageManager.getLaunchIntentForPackage(pkg)
            ?: throw CommandException(
                ErrorCode.NOT_FOUND,
                "no launchable activity found for package: $pkg",
            )
        intent.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        context.startActivity(intent)
        return buildJsonObject {}
    }

    private suspend fun listApps(args: JsonObject): JsonElement {
        val includeSystem = args["include_system"]?.jsonPrimitive?.booleanOrNull ?: false
        val pm = context.packageManager
        val resolved = pm.queryIntentActivities(
            Intent(Intent.ACTION_MAIN).addCategory(Intent.CATEGORY_LAUNCHER),
            PackageManager.MATCH_ALL,
        )
        val apps = resolved.mapNotNull {
            val pkg = it.activityInfo.packageName
            if (!includeSystem && isSystemPackage(pm, pkg)) null
            else buildJsonObject {
                put("package", pkg)
                put("name", it.loadLabel(pm).toString())
            }
        }
        return buildJsonObject {
            putJsonArray("apps") {
                apps.forEach { add(it) }
            }
        }
    }

    // ── internals ─────────────────────────────────────────────────────────────────

    /**
     * Fails with the accurate error when the accessibility IME is unusable.
     *
     * Two different situations look the same to [TypeInputController.isReady] but need
     * different codes, because only one of them is fixable by the caller:
     *
     * - Below API 33 `android.accessibilityservice.InputMethod` does not exist, so no
     *   amount of focusing will ever make typing work → `unsupported`. Saying `not_ready`
     *   here would invite an endless focus-and-retry loop.
     * - On API 33+ a false [TypeInputController.isReady] means no editable field has
     *   input started → `not_ready`, which the caller fixes by tapping a field first.
     *
     * These commands are also dropped from the registered capability set below API 33
     * (see [dev.devicecontrol.app.protocol.Capabilities.forApiLevel]), so a conforming
     * server never sends them. This guard covers the non-conforming server and the
     * partially-supported `press_key` keys that stay declared for back/home.
     */
    private fun requireAccessibilityIme(command: String) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) {
            throw CommandException(
                ErrorCode.UNSUPPORTED,
                "$command needs the accessibility IME, added in Android 13 (API 33); " +
                    "this device runs API ${Build.VERSION.SDK_INT}",
            )
        }
        if (!graph.typeInputController.isReady()) {
            throw CommandException(
                ErrorCode.NOT_READY,
                "input method not available; focus an editable field first",
            )
        }
    }

    /**
     * Finds the nearest scrollable ancestor of [nodeId] by walking the parsed trees.
     * `AccessibilityNodeData` has no parent pointer, so this scans for the node and
     * then walks up by repeatedly finding the parent that contains it. For the
     * common single-window case this is one tree; multi-window scans all roots.
     *
     * Returns the scrollable ancestor's `node_id`, or null if the node itself or none
     * of its ancestors are scrollable.
     */
    private fun findNearestScrollableAncestor(
        nodeId: String,
        windows: List<dev.devicecontrol.core.accessibility.WindowData>,
    ): String? {
        for (window in windows) {
            val found = walkForScrollableAncestor(window.tree, nodeId, null)
            if (found != null) return found
        }
        return null
    }

    /**
     * Recursive walk. Returns the nearest scrollable ancestor of [targetId] found in
     * the subtree rooted at [node], or null. [lastScrollable] is the most recent
     * scrollable ancestor seen on the path down to [targetId].
     */
    private fun walkForScrollableAncestor(
        node: AccessibilityNodeData,
        targetId: String,
        lastScrollable: String?,
    ): String? {
        if (node.id == targetId) return lastScrollable
        val nextScrollable = if (node.scrollable) node.id else lastScrollable
        for (child in node.children) {
            val found = walkForScrollableAncestor(child, targetId, nextScrollable)
            if (found != null) return found
        }
        return null
    }

    private fun parseDirection(value: JsonElement?): ScrollDirection {
        val s = value?.jsonPrimitive?.contentOrNull?.lowercase()
            ?: throw CommandException(ErrorCode.BAD_ARGS, "missing required field: direction")
        return when (s) {
            "up" -> ScrollDirection.UP
            "down" -> ScrollDirection.DOWN
            "left" -> ScrollDirection.LEFT
            "right" -> ScrollDirection.RIGHT
            else -> throw CommandException(ErrorCode.BAD_ARGS, "unsupported direction: $s")
        }
    }

    private fun requireFloat(args: JsonObject, name: String): Float {
        val v = args[name]?.jsonPrimitive?.doubleOrNull
            ?: throw CommandException(ErrorCode.BAD_ARGS, "missing or non-numeric field: $name")
        return v.toFloat()
    }

    private fun BoundsData.center(): Pair<Float, Float> =
        ((left + right) / 2f) to ((top + bottom) / 2f)

    private fun ScreenshotData.toJson(): JsonObject = buildJsonObject {
        put("format", format)
        put("w", width)
        put("h", height)
        put("b64", data)
    }

    private fun parseKeyCode(key: String): Int? = when (key.lowercase()) {
        "enter", "return" -> KeyEvent.KEYCODE_ENTER
        "tab" -> KeyEvent.KEYCODE_TAB
        "delete", "backspace" -> KeyEvent.KEYCODE_DEL
        "escape" -> KeyEvent.KEYCODE_ESCAPE
        "space" -> KeyEvent.KEYCODE_SPACE
        "dpad_up" -> KeyEvent.KEYCODE_DPAD_UP
        "dpad_down" -> KeyEvent.KEYCODE_DPAD_DOWN
        "dpad_left" -> KeyEvent.KEYCODE_DPAD_LEFT
        "dpad_right" -> KeyEvent.KEYCODE_DPAD_RIGHT
        "dpad_center" -> KeyEvent.KEYCODE_DPAD_CENTER
        else -> null
    }

    private fun isSystemPackage(pm: PackageManager, pkg: String): Boolean {
        return try {
            val info = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
                pm.getPackageInfo(
                    pkg,
                    PackageManager.PackageInfoFlags.of(PackageManager.GET_META_DATA.toLong()),
                )
            } else {
                @Suppress("DEPRECATION")
                pm.getPackageInfo(pkg, PackageManager.GET_META_DATA)
            }
            val appInfo = info.applicationInfo ?: return false
            (appInfo.flags and ApplicationInfo.FLAG_SYSTEM) != 0
        } catch (_: PackageManager.NameNotFoundException) {
            false
        }
    }

    companion object {
        private const val MAX_SCROLL_ATTEMPTS = 5
        private const val SETTLE_MS = 300L
    }
}

/** Convenience so `Result.getOrThrow()` rethrows core's typed failures verbatim. */
private fun <T> Result<T>.getOrThrow(): T = getOrElse { throw it }
