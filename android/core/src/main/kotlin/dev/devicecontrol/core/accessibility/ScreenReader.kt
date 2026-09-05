package dev.devicecontrol.core.accessibility

import dev.devicecontrol.core.CoreException
import javax.inject.Inject

/**
 * A rendered screen state, ready to be placed in a `get_screen_state` response.
 *
 * @property tsv The compact TSV tree (protocol v0 §8.2 / §8.7 when paginated).
 * @property screenInfo Screen dimensions/density/orientation at capture time.
 * @property cursor Cursor to pass back for the NEXT page (`<snapshotId>.<page>`), or null when
 *   this was the last (or only) page. Mirrors the trailing `note:` line the formatter emits.
 * @property page 1-based page number. Always 1 for a single-page read.
 * @property totalPages Total pages in this snapshot. 1 when the tree fits in one page.
 */
data class RenderedScreen(
    val tsv: String,
    val screenInfo: ScreenInfo,
    val cursor: String?,
    val page: Int,
    val totalPages: Int,
)

/**
 * Thrown when a pagination cursor no longer resolves to a retained snapshot — the snapshot was
 * replaced by a newer read, or the cursor is malformed. Callers map this to the wire error code
 * `stale_node` (protocol v0 §8.7): the fix is a fresh cursor-less read, not a retry.
 */
class StaleCursorException(
    message: String,
) : Exception(message)

/**
 * Reads the live accessibility tree and renders it as the compact TSV of protocol v0 §8.2.
 *
 * This is the one entry point for "what is on screen right now". It exists because the
 * clear→read→populate sequence has ordering constraints that must not leak to callers:
 *
 * 1. The framework node cache MUST be dropped before reading, or a WebView whose DOM changed via
 *    JavaScript serves stale nodes ([AccessibilityServiceProvider.clearFrameworkNodeCache]).
 * 2. Every window's tree is parsed into ONE accumulating node map, and
 *    [AccessibilityNodeCache.populate] is called ONCE at the end — a per-window populate would
 *    leave earlier windows' nodes unresolvable for the rest of the read.
 * 3. The whole critical section holds [AccessibilityTreeLock], so an overlapping read (the server
 *    may have up to 8 calls in flight) cannot clear the cache mid-traversal and yield a torn tree.
 *
 * Get any of those wrong and the symptom is not a crash but intermittently wrong `node_id`
 * resolution — i.e. tapping the wrong element. Hence one class, not a documented recipe.
 */
class ScreenReader
    @Inject
    constructor(
        private val provider: AccessibilityServiceProvider,
        private val parser: AccessibilityTreeParser,
        private val nodeCache: AccessibilityNodeCache,
        private val merger: WebViewNodeMerger,
        private val formatter: CompactTreeFormatter,
        private val snapshotCache: ScreenStateSnapshotCache,
    ) {
        /**
         * Reads every on-screen window and repopulates the id→node cache from that read.
         *
         * The returned trees are the raw parse — WebView subtrees are NOT yet collapsed, because
         * node-targeted commands must resolve against the un-merged tree (merging rewrites text and
         * drops non-interactive nodes). Pass the result to [render] for the wire format, or use
         * [MultiWindowResult.windows] directly for node targeting.
         *
         * @throws CoreException.PermissionDenied if the accessibility service is not connected.
         */
        fun readFresh(): MultiWindowResult {
            if (!provider.isReady()) {
                throw CoreException.PermissionDenied(
                    "Accessibility service not enabled. Please enable it in Android Settings.",
                )
            }
            return synchronized(AccessibilityTreeLock.monitor) { readFreshLocked() }
        }

        /**
         * Convenience for node-targeted commands: the window list from a fresh read.
         *
         * Node actions need the parsed trees to walk in parallel with the live ones, and they need
         * the node cache to have just been populated from the same read — so they must go through
         * this rather than reusing an older list.
         */
        fun readFreshWindows(): List<WindowData> = readFresh().windows

        /** Current screen dimensions, density (integer DPI) and orientation. */
        fun screenInfo(): ScreenInfo = provider.getScreenInfo()

        /**
         * Renders [result] as the compact TSV.
         *
         * With a null [cursor] this is a first read: if the tree exceeds one page a snapshot is
         * retained so later pages can be served without re-reading the screen. With a non-null
         * [cursor] (`<snapshotId>.<page>`) the retained snapshot is re-rendered at that page — no
         * screen read happens, so the content is as of the original capture.
         *
         * @throws StaleCursorException if [cursor] is malformed or its snapshot is gone.
         */
        fun render(
            result: MultiWindowResult,
            cursor: String? = null,
        ): RenderedScreen {
            if (cursor != null) return renderCursor(cursor)

            val merged = merger.merge(result)
            val screen = screenInfo()
            val totalKept = formatter.countKeptNodes(merged)
            val totalPages = pageCount(totalKept)

            if (totalPages <= 1) {
                return RenderedScreen(
                    tsv = formatter.formatMultiWindow(merged, screen),
                    screenInfo = screen,
                    cursor = null,
                    page = 1,
                    totalPages = 1,
                )
            }

            // Base36 of the capture instant: short, monotonic enough to read in a log, and only
            // ever compared for equality by the single-slot cache.
            val snapshot =
                ScreenStateSnapshot(
                    id = System.currentTimeMillis().toString(SNAPSHOT_ID_RADIX),
                    result = merged,
                    screenInfo = screen,
                    totalKeptNodes = totalKept,
                    totalPages = totalPages,
                )
            snapshotCache.store(snapshot)
            return renderPage(snapshot, page = 1)
        }

        /** Reads and renders in one step — the common `get_screen_state` path. */
        fun readAndRender(cursor: String? = null): RenderedScreen =
            if (cursor != null) renderCursor(cursor) else render(readFresh())

        private fun renderCursor(cursor: String): RenderedScreen {
            val separator = cursor.lastIndexOf(CURSOR_SEPARATOR)
            if (separator <= 0 || separator == cursor.length - 1) {
                throw StaleCursorException("Malformed cursor '$cursor'; expected '<snapshot>.<page>'")
            }
            val snapshotId = cursor.substring(0, separator)
            val page =
                cursor.substring(separator + 1).toIntOrNull()
                    ?: throw StaleCursorException("Malformed cursor '$cursor'; page is not a number")

            val snapshot =
                snapshotCache.get(snapshotId)
                    ?: throw StaleCursorException(
                        "Snapshot '$snapshotId' is no longer retained; re-read without a cursor",
                    )
            if (page < 1 || page > snapshot.totalPages) {
                throw StaleCursorException(
                    "Page $page out of range 1..${snapshot.totalPages} for snapshot '$snapshotId'",
                )
            }
            return renderPage(snapshot, page)
        }

        private fun renderPage(
            snapshot: ScreenStateSnapshot,
            page: Int,
        ): RenderedScreen =
            RenderedScreen(
                tsv = formatter.formatMultiWindowPage(snapshot, page),
                screenInfo = snapshot.screenInfo,
                cursor = if (page < snapshot.totalPages) "${snapshot.id}$CURSOR_SEPARATOR${page + 1}" else null,
                page = page,
                totalPages = snapshot.totalPages,
            )

        /**
         * The clear→read→populate critical section. Caller holds [AccessibilityTreeLock].
         *
         * Falls back to the single active window when the multi-window API yields nothing, marking
         * the result [MultiWindowResult.degraded] so the caller can tell the difference between "one
         * window is on screen" and "we could only see one window".
         */
        private fun readFreshLocked(): MultiWindowResult {
            provider.clearFrameworkNodeCache()

            val accumulated = mutableMapOf<String, CachedNode>()
            val windows = provider.getAccessibilityWindows()
            val parsed = mutableListOf<WindowData>()

            try {
                for (window in windows) {
                    val root = window.root ?: continue
                    try {
                        // Virtual hierarchies (Compose/WebView) can hand back stale roots even
                        // after the framework cache drop; refresh forces a live re-read.
                        root.refresh()
                        parsed.add(parseWindow(window, root, accumulated))
                    } finally {
                        @Suppress("DEPRECATION")
                        root.recycle()
                    }
                }
            } finally {
                for (window in windows) {
                    @Suppress("DEPRECATION")
                    window.recycle()
                }
            }

            if (parsed.isNotEmpty()) {
                nodeCache.populate(accumulated)
                return MultiWindowResult(windows = parsed, degraded = false)
            }

            return readActiveWindowOnly(accumulated)
        }

        private fun parseWindow(
            window: android.view.accessibility.AccessibilityWindowInfo,
            root: android.view.accessibility.AccessibilityNodeInfo,
            accumulated: MutableMap<String, CachedNode>,
        ): WindowData {
            val windowId = window.id
            val tree = parser.parseTree(root, "$ROOT_PARENT_PREFIX$windowId", accumulated)
            val focused = window.isFocused
            // The framework node carries the package; the parsed tree does not. The window's own
            // package is the root's — fall back to the provider's tracked current package.
            val packageName = root.packageName?.toString() ?: provider.getCurrentPackageName()
            return WindowData(
                windowId = windowId,
                windowType = AccessibilityTreeParser.mapWindowType(window.type),
                packageName = packageName,
                title = window.title?.toString(),
                // The activity name is only meaningful for the focused app window; the accessibility
                // event that carries it does not distinguish other windows.
                activityName = if (focused) provider.getCurrentActivityName() else null,
                layer = window.layer,
                focused = focused,
                tree = tree,
            )
        }

        /**
         * Degraded path: `getWindows()` gave us nothing usable, so read `rootInActiveWindow`.
         *
         * This loses system dialogs, permission popups and the IME window. The result is flagged
         * [MultiWindowResult.degraded] and the formatter emits a `note:DEGRADED` line so the caller
         * knows the view is partial rather than the screen being empty.
         */
        private fun readActiveWindowOnly(accumulated: MutableMap<String, CachedNode>): MultiWindowResult {
            val root =
                provider.getRootNode()
                    ?: throw CoreException.ActionFailed("No accessibility window is available to read")

            val tree =
                try {
                    root.refresh()
                    parser.parseTree(root, "${ROOT_PARENT_PREFIX}0", accumulated)
                } finally {
                    @Suppress("DEPRECATION")
                    root.recycle()
                }

            nodeCache.populate(accumulated)
            return MultiWindowResult(
                windows =
                    listOf(
                        WindowData(
                            windowId = 0,
                            windowType = WINDOW_TYPE_APPLICATION,
                            packageName = provider.getCurrentPackageName(),
                            title = null,
                            activityName = provider.getCurrentActivityName(),
                            layer = 0,
                            focused = true,
                            tree = tree,
                        ),
                    ),
                degraded = true,
            )
        }

        private fun pageCount(totalKept: Int): Int =
            if (totalKept <= 0) {
                1
            } else {
                (totalKept + CompactTreeFormatter.PAGE_SIZE - 1) / CompactTreeFormatter.PAGE_SIZE
            }

        companion object {
            /**
             * Prefix for the synthetic parent id each window's root hashes against. Including the
             * window id keeps node_ids distinct across windows that hold structurally identical
             * subtrees (e.g. two instances of the same activity side by side).
             */
            private const val ROOT_PARENT_PREFIX = "root_w"
            private const val WINDOW_TYPE_APPLICATION = "APPLICATION"
            private const val SNAPSHOT_ID_RADIX = 36
            internal const val CURSOR_SEPARATOR = '.'
        }
    }
