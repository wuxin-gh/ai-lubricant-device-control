package dev.devicecontrol.core.accessibility

import io.mockk.every
import io.mockk.mockk
import io.mockk.verify
import io.mockk.verifyOrder
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertFalse
import org.junit.jupiter.api.Assertions.assertNotNull
import org.junit.jupiter.api.Assertions.assertNull
import org.junit.jupiter.api.Assertions.assertSame
import org.junit.jupiter.api.Assertions.assertThrows
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.BeforeEach
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Nested
import org.junit.jupiter.api.Test
import android.view.accessibility.AccessibilityNodeInfo
import android.view.accessibility.AccessibilityWindowInfo
import dev.devicecontrol.core.CoreException

@DisplayName("ScreenReader")
class ScreenReaderTest {
    private lateinit var provider: AccessibilityServiceProvider
    private lateinit var parser: AccessibilityTreeParser
    private lateinit var nodeCache: AccessibilityNodeCache
    private lateinit var snapshotCache: ScreenStateSnapshotCache
    private lateinit var reader: ScreenReader

    private val screenInfo =
        ScreenInfo(width = 1080, height = 2400, densityDpi = 420, orientation = "portrait")

    @BeforeEach
    fun setUp() {
        provider = mockk(relaxed = true)
        parser = mockk(relaxed = true)
        nodeCache = mockk(relaxed = true)
        snapshotCache = ScreenStateSnapshotCacheImpl()
        every { provider.isReady() } returns true
        every { provider.getScreenInfo() } returns screenInfo
        reader =
            ScreenReader(
                provider = provider,
                parser = parser,
                nodeCache = nodeCache,
                merger = WebViewNodeMerger(),
                formatter = CompactTreeFormatter(),
                snapshotCache = snapshotCache,
            )
    }

    private fun node(
        id: String,
        text: String? = null,
        children: List<AccessibilityNodeData> = emptyList(),
    ) = AccessibilityNodeData(
        id = id,
        text = text,
        bounds = BoundsData(0, 0, 100, 100),
        visible = true,
        children = children,
    )

    /** A window whose root is a distinct mock, so recycle() calls can be attributed. */
    private fun mockWindow(
        id: Int,
        type: Int = AccessibilityWindowInfo.TYPE_APPLICATION,
        focused: Boolean = false,
        pkg: String? = "com.example",
        title: String? = "Title",
        layer: Int = 0,
        root: AccessibilityNodeInfo? = mockk(relaxed = true),
    ): AccessibilityWindowInfo {
        val window = mockk<AccessibilityWindowInfo>(relaxed = true)
        every { window.id } returns id
        every { window.type } returns type
        every { window.isFocused } returns focused
        every { window.layer } returns layer
        every { window.title } returns title
        every { window.root } returns root
        if (root != null) {
            every { root.packageName } returns pkg
            every { root.refresh() } returns true
        }
        return window
    }

    @Nested
    @DisplayName("readFresh")
    inner class ReadFreshTests {
        @Test
        @DisplayName("throws PermissionDenied when the service is not ready")
        fun throwsWhenNotReady() {
            every { provider.isReady() } returns false
            assertThrows(CoreException.PermissionDenied::class.java) { reader.readFresh() }
            // Must fail before touching the framework at all.
            verify(exactly = 0) { provider.clearFrameworkNodeCache() }
        }

        @Test
        @DisplayName("drops the framework cache before reading windows")
        fun clearsFrameworkCacheFirst() {
            every { provider.getAccessibilityWindows() } returns listOf(mockWindow(1))
            every { parser.parseTree(any(), any(), any()) } returns node("node_a", "hello")

            reader.readFresh()

            verifyOrder {
                provider.clearFrameworkNodeCache()
                provider.getAccessibilityWindows()
            }
        }

        @Test
        @DisplayName("populates the node cache exactly once for a multi-window read")
        fun populatesNodeCacheOnce() {
            every { provider.getAccessibilityWindows() } returns
                listOf(mockWindow(1), mockWindow(2), mockWindow(3))
            every { parser.parseTree(any(), any(), any()) } returns node("node_a", "hello")

            reader.readFresh()

            // One atomic swap after ALL windows are parsed — a per-window populate would leave
            // earlier windows' ids unresolvable for the remainder of the read.
            verify(exactly = 1) { nodeCache.populate(any()) }
        }

        @Test
        @DisplayName("accumulates every window's nodes into the single populate call")
        fun accumulatesAllWindowsIntoOneMap() {
            every { provider.getAccessibilityWindows() } returns listOf(mockWindow(1), mockWindow(2))
            // Simulate the parser's out-param behaviour: each call adds its own entry.
            var n = 0
            every { parser.parseTree(any(), any(), any()) } answers {
                val map = thirdArg<MutableMap<String, CachedNode>?>()
                map?.put("node_${n++}", mockk(relaxed = true))
                node("node_a", "hello")
            }

            reader.readFresh()

            verify { nodeCache.populate(match { it.size == 2 }) }
        }

        @Test
        @DisplayName("passes a window-scoped root parent id so ids stay distinct across windows")
        fun usesWindowScopedRootParentId() {
            every { provider.getAccessibilityWindows() } returns listOf(mockWindow(7), mockWindow(9))
            every { parser.parseTree(any(), any(), any()) } returns node("node_a", "hello")

            reader.readFresh()

            verify { parser.parseTree(any(), "root_w7", any()) }
            verify { parser.parseTree(any(), "root_w9", any()) }
        }

        @Test
        @DisplayName("refreshes each root before parsing to defeat stale virtual hierarchies")
        fun refreshesRootBeforeParsing() {
            val root = mockk<AccessibilityNodeInfo>(relaxed = true)
            every { root.packageName } returns "com.example"
            every { root.refresh() } returns true
            every { provider.getAccessibilityWindows() } returns listOf(mockWindow(1, root = root))
            every { parser.parseTree(any(), any(), any()) } returns node("node_a", "hello")

            reader.readFresh()

            verifyOrder {
                root.refresh()
                parser.parseTree(root, "root_w1", any())
            }
        }

        @Test
        @DisplayName("carries window metadata through to WindowData")
        fun carriesWindowMetadata() {
            every { provider.getCurrentActivityName() } returns ".MainActivity"
            every { provider.getAccessibilityWindows() } returns
                listOf(
                    mockWindow(
                        id = 4,
                        type = AccessibilityWindowInfo.TYPE_INPUT_METHOD,
                        focused = true,
                        pkg = "com.keyboard",
                        title = "Keyboard",
                        layer = 3,
                    ),
                )
            every { parser.parseTree(any(), any(), any()) } returns node("node_a", "hello")

            val window = reader.readFresh().windows.single()

            assertEquals(4, window.windowId)
            assertEquals("INPUT_METHOD", window.windowType)
            assertEquals("com.keyboard", window.packageName)
            assertEquals("Keyboard", window.title)
            assertEquals(3, window.layer)
            assertTrue(window.focused)
            assertEquals(".MainActivity", window.activityName)
        }

        @Test
        @DisplayName("omits activityName for non-focused windows")
        fun omitsActivityNameForUnfocusedWindows() {
            every { provider.getCurrentActivityName() } returns ".MainActivity"
            every { provider.getAccessibilityWindows() } returns listOf(mockWindow(1, focused = false))
            every { parser.parseTree(any(), any(), any()) } returns node("node_a", "hello")

            // The accessibility event that carries the activity name does not identify which window
            // it belongs to, so attributing it to an unfocused window would be a fabrication.
            assertNull(reader.readFresh().windows.single().activityName)
        }

        @Test
        @DisplayName("skips windows with no root without failing the whole read")
        fun skipsRootlessWindows() {
            every { provider.getAccessibilityWindows() } returns
                listOf(mockWindow(1, root = null), mockWindow(2))
            every { parser.parseTree(any(), any(), any()) } returns node("node_a", "hello")

            val result = reader.readFresh()

            assertEquals(1, result.windows.size)
            assertEquals(2, result.windows.single().windowId)
            assertFalse(result.degraded)
        }
    }

    @Nested
    @DisplayName("degraded fallback")
    inner class DegradedTests {
        @Test
        @DisplayName("falls back to the active window and flags degraded when getWindows is empty")
        fun fallsBackToActiveWindow() {
            val root = mockk<AccessibilityNodeInfo>(relaxed = true)
            every { root.refresh() } returns true
            every { provider.getAccessibilityWindows() } returns emptyList()
            every { provider.getRootNode() } returns root
            every { provider.getCurrentPackageName() } returns "com.example"
            every { parser.parseTree(any(), any(), any()) } returns node("node_a", "hello")

            val result = reader.readFresh()

            assertTrue(result.degraded)
            assertEquals(1, result.windows.size)
            assertEquals("APPLICATION", result.windows.single().windowType)
            assertEquals("com.example", result.windows.single().packageName)
            verify { parser.parseTree(root, "root_w0", any()) }
            verify(exactly = 1) { nodeCache.populate(any()) }
        }

        @Test
        @DisplayName("falls back when every window is rootless")
        fun fallsBackWhenAllWindowsRootless() {
            val root = mockk<AccessibilityNodeInfo>(relaxed = true)
            every { root.refresh() } returns true
            every { provider.getAccessibilityWindows() } returns listOf(mockWindow(1, root = null))
            every { provider.getRootNode() } returns root
            every { parser.parseTree(any(), any(), any()) } returns node("node_a", "hello")

            assertTrue(reader.readFresh().degraded)
        }

        @Test
        @DisplayName("fails with ActionFailed when no window is readable at all")
        fun failsWhenNothingReadable() {
            every { provider.getAccessibilityWindows() } returns emptyList()
            every { provider.getRootNode() } returns null

            assertThrows(CoreException.ActionFailed::class.java) { reader.readFresh() }
            // Nothing was read, so the cache must be left alone rather than emptied.
            verify(exactly = 0) { nodeCache.populate(any()) }
        }

        @Test
        @DisplayName("emits the DEGRADED note in the rendered output")
        fun rendersDegradedNote() {
            val root = mockk<AccessibilityNodeInfo>(relaxed = true)
            every { root.refresh() } returns true
            every { provider.getAccessibilityWindows() } returns emptyList()
            every { provider.getRootNode() } returns root
            every { parser.parseTree(any(), any(), any()) } returns node("node_a", "hello")

            val rendered = reader.render(reader.readFresh())

            assertTrue(rendered.tsv.startsWith(CompactTreeFormatter.DEGRADATION_NOTE))
        }
    }

    @Nested
    @DisplayName("render — single page")
    inner class SinglePageTests {
        private fun resultWith(keptNodes: Int): MultiWindowResult {
            val children = (0 until keptNodes).map { node("node_$it", "row $it") }
            return MultiWindowResult(
                windows =
                    listOf(
                        WindowData(
                            windowId = 1,
                            windowType = "APPLICATION",
                            tree = node("node_root", children = children),
                        ),
                    ),
            )
        }

        @Test
        @DisplayName("returns no cursor and one page when the tree fits")
        fun noCursorWhenTreeFits() {
            val rendered = reader.render(resultWith(5))

            assertNull(rendered.cursor)
            assertEquals(1, rendered.page)
            assertEquals(1, rendered.totalPages)
            assertEquals(screenInfo, rendered.screenInfo)
        }

        @Test
        @DisplayName("retains no snapshot for a single-page read")
        fun retainsNoSnapshotForSinglePage() {
            val spyCache = mockk<ScreenStateSnapshotCache>(relaxed = true)
            val r =
                ScreenReader(provider, parser, nodeCache, WebViewNodeMerger(), CompactTreeFormatter(), spyCache)

            r.render(resultWith(5))

            verify(exactly = 0) { spyCache.store(any()) }
        }

        @Test
        @DisplayName("renders exactly one page at the page-size boundary")
        fun boundaryAtExactlyPageSize() {
            // The root has no text and isn't interactive, so it's filtered out — kept == children.
            // Exactly PAGE_SIZE kept rows must still be a single page.
            val rendered = reader.render(resultWith(CompactTreeFormatter.PAGE_SIZE))

            assertEquals(1, rendered.totalPages)
            assertNull(rendered.cursor)
        }

        @Test
        @DisplayName("spills to a second page one node past the boundary")
        fun boundaryOneNodePastPageSize() {
            // The root node is not kept (no text, not interactive), so kept == children count.
            val rendered = reader.render(resultWith(CompactTreeFormatter.PAGE_SIZE + 1))

            assertEquals(2, rendered.totalPages)
            assertNotNull(rendered.cursor)
            assertTrue(rendered.cursor!!.endsWith(".2"))
        }

        @Test
        @DisplayName("treats an empty tree as one page")
        fun emptyTreeIsOnePage() {
            val rendered =
                reader.render(
                    MultiWindowResult(
                        windows =
                            listOf(
                                WindowData(windowId = 1, windowType = "APPLICATION", tree = node("node_root")),
                            ),
                    ),
                )

            assertEquals(1, rendered.totalPages)
            assertNull(rendered.cursor)
        }
    }

    @Nested
    @DisplayName("render — pagination")
    inner class PaginationTests {
        private fun paginatedResult(keptNodes: Int): MultiWindowResult {
            val children = (0 until keptNodes).map { node("node_$it", "row $it") }
            return MultiWindowResult(
                windows =
                    listOf(
                        WindowData(
                            windowId = 1,
                            windowType = "APPLICATION",
                            tree = node("node_root", children = children),
                        ),
                    ),
            )
        }

        @Test
        @DisplayName("retains a snapshot and hands back a cursor for the next page")
        fun retainsSnapshotAndReturnsCursor() {
            val first = reader.render(paginatedResult(450))

            assertEquals(1, first.page)
            assertEquals(3, first.totalPages)
            val cursor = assertNotNull(first.cursor).let { first.cursor!! }
            assertTrue(cursor.endsWith(".2"))
            // The snapshot must be resolvable by the id embedded in that cursor.
            assertNotNull(snapshotCache.get(cursor.substringBeforeLast('.')))
        }

        @Test
        @DisplayName("serves a later page from the retained snapshot without re-reading the screen")
        fun servesLaterPageWithoutReReading() {
            val first = reader.render(paginatedResult(450))

            val second = reader.readAndRender(first.cursor)

            assertEquals(2, second.page)
            assertEquals(3, second.totalPages)
            // No screen read happened for the cursor path.
            verify(exactly = 0) { provider.getAccessibilityWindows() }
        }

        @Test
        @DisplayName("drops the cursor on the last page")
        fun lastPageHasNoCursor() {
            val first = reader.render(paginatedResult(450))
            val second = reader.readAndRender(first.cursor)
            val third = reader.readAndRender(second.cursor)

            assertEquals(3, third.page)
            assertNull(third.cursor)
        }

        @Test
        @DisplayName("reports the snapshot's screen info, not a fresh read, for later pages")
        fun laterPagesReportSnapshotScreenInfo() {
            val first = reader.render(paginatedResult(450))
            // Screen rotates between pages; the retained page must stay self-consistent.
            every { provider.getScreenInfo() } returns
                ScreenInfo(width = 2400, height = 1080, densityDpi = 420, orientation = "landscape")

            val second = reader.readAndRender(first.cursor)

            assertEquals("portrait", second.screenInfo.orientation)
        }
    }

    @Nested
    @DisplayName("cursor validation")
    inner class CursorValidationTests {
        @Test
        @DisplayName("rejects a cursor whose snapshot was replaced")
        fun rejectsReplacedSnapshot() {
            val stale = "abc.2"
            // Nothing was ever stored under "abc".
            assertThrows(StaleCursorException::class.java) { reader.readAndRender(stale) }
        }

        @Test
        @DisplayName("rejects a cursor with no page component")
        fun rejectsCursorWithoutPage() {
            assertThrows(StaleCursorException::class.java) { reader.readAndRender("abc") }
        }

        @Test
        @DisplayName("rejects a cursor with a trailing separator")
        fun rejectsTrailingSeparator() {
            assertThrows(StaleCursorException::class.java) { reader.readAndRender("abc.") }
        }

        @Test
        @DisplayName("rejects a cursor with a non-numeric page")
        fun rejectsNonNumericPage() {
            assertThrows(StaleCursorException::class.java) { reader.readAndRender("abc.x") }
        }

        @Test
        @DisplayName("rejects a page beyond the snapshot's range")
        fun rejectsOutOfRangePage() {
            val children = (0 until 450).map { node("node_$it", "row $it") }
            val first =
                reader.render(
                    MultiWindowResult(
                        windows =
                            listOf(
                                WindowData(
                                    windowId = 1,
                                    windowType = "APPLICATION",
                                    tree = node("node_root", children = children),
                                ),
                            ),
                    ),
                )
            val snapshotId = first.cursor!!.substringBeforeLast('.')

            assertThrows(StaleCursorException::class.java) { reader.readAndRender("$snapshotId.99") }
            assertThrows(StaleCursorException::class.java) { reader.readAndRender("$snapshotId.0") }
        }
    }

    @Nested
    @DisplayName("readFreshWindows")
    inner class ReadFreshWindowsTests {
        @Test
        @DisplayName("returns the un-merged windows so node targeting resolves")
        fun returnsUnmergedWindows() {
            every { provider.getAccessibilityWindows() } returns listOf(mockWindow(1))
            every { parser.parseTree(any(), any(), any()) } returns node("node_a", "hello")

            val windows = reader.readFreshWindows()

            // Node actions walk the real tree in parallel with THIS tree; if it had been merged the
            // structure would no longer line up and every node action would miss.
            assertEquals(1, windows.size)
            assertEquals("node_a", windows.single().tree.id)
            verify(exactly = 1) { nodeCache.populate(any()) }
        }
    }
}
