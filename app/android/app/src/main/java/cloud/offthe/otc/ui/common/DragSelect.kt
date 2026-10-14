// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import androidx.compose.foundation.gestures.ScrollableState
import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.gestures.awaitLongPressOrCancellation
import androidx.compose.foundation.gestures.scrollBy
import androidx.compose.foundation.lazy.LazyListLayoutInfo
import androidx.compose.foundation.lazy.grid.LazyGridLayoutInfo
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.Stable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.SideEffect
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshotFlow
import androidx.compose.runtime.withFrameNanos
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.hapticfeedback.HapticFeedbackType
import androidx.compose.ui.input.pointer.PointerEventPass
import androidx.compose.ui.input.pointer.PointerInputScope
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.layout.positionInRoot
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalHapticFeedback
import androidx.compose.ui.unit.IntSize
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.flow.collectLatest
import kotlin.math.max
import kotlin.math.min

// Issue #200: long press, then drag to select (iOS DragSelect.swift), as
// Google Photos does. A long press selects the item under the finger - the
// way into selection mode - and, the finger kept down, every item from that
// one to the one under the finger, in the order the screen shows them,
// joins the selection: dragging back shrinks the range, and what it passed
// over goes back to how it was before the drag. The range adds to what was
// already selected; a long press never takes anything out of it (a tap
// does, once something is selected). Near the top or bottom edge the list
// scrolls by itself, once the finger has moved on from the press, and the
// range follows. A press starts one only on an item itself (never a gap, a
// title, padding, ".." or a face's name); the pressed item going away
// mid-drag ends it as it stands, and so does a new Files listing (its
// selection stands). A pick that goes from the items mid-drag doesn't come
// back as the range moves on. The same rules on iOS and the web. Images,
// People and Files (list and grid) share it; TalkBack keeps each item's own
// Select action.

/**
 * The pure part of a drag: the selection [base] it started from (in its
 * order) and the item pressed, [start].
 */
class DragSelection<K>(base: List<K>, val start: K) {
    /** What was selected before the drag, in its order: less the picks gone since ([forget]). */
    var base: List<K> = base
        private set
    private var baseSet = base.toHashSet()

    /**
     * The pressed item went away mid-drag (a new listing, a delete
     * elsewhere): the drag is over, the selection stays as it was then and
     * the finger's later moves change nothing.
     */
    var over = false
        private set

    /**
     * Whether the drag goes on with the items shown now, [order]: not once
     * the pressed item has gone from them - for good, even if it comes
     * back. Asked at every move and scroll step, not only when the finger
     * reaches another item.
     */
    fun goesOn(order: List<K>): Boolean {
        if (!over && start !in order) over = true
        return !over
    }

    /**
     * Picks that went from the items mid-drag (People's refresh dropping a
     * face, Images' delete), as the view drops them from its selection:
     * gone from what was selected before the drag too, so the range moving
     * on doesn't bring them back.
     */
    fun forget(gone: (K) -> Boolean) {
        if (base.none(gone)) return
        base = base.filterNot(gone)
        baseSet = base.toHashSet()
    }

    /**
     * The items from the pressed one to [end], both included, in [order]
     * (display order); null when the selection should stay as it is: the
     * drag is [over], or [end] isn't in [order] (gone meanwhile) - until
     * the finger reaches another item.
     */
    fun range(order: List<K>, end: K): List<K>? {
        if (over) return null
        val s = order.indexOf(start)
        if (s < 0) { over = true; return null }
        val e = order.indexOf(end)
        if (e < 0) return null
        return order.subList(min(s, e), max(s, e) + 1).toList()
    }

    /**
     * The selection with the range at [end]: what was selected before the
     * drag, in its order, then the range's items it didn't hold, in display
     * order. Null: as it is ([range]).
     */
    fun apply(order: List<K>, end: K): List<K>? = range(order, end)?.let { r -> base + r.filter { it !in baseSet } }
}

/** The keys of these items ([key] of each), in their order, without copying them. */
fun <T, K> List<T>.keysView(key: (T) -> K): List<K> = object : AbstractList<K>() {
    override val size get() = this@keysView.size
    override fun get(index: Int) = key(this@keysView[index])
}

/** How deep the band at the top and bottom edge is where a drag scrolls the list. */
val DRAG_SELECT_EDGE = 56.dp

/** The shallowest band that still scrolls: a list too short for that has none. */
val DRAG_SELECT_MIN_EDGE = 8.dp

/** The fastest a drag scrolls the list, in dp per second: with the finger at the edge or past it. */
const val DRAG_SELECT_MAX_SPEED = 1200f

/**
 * How fast a drag scrolls the list with the finger at [y] (pixels per
 * second; negative: up), in a list whose visible part runs from [top] to
 * [bottom]: nothing outside the [zone] deep bands at its ends, then faster
 * the deeper into a band, up to [max] at the edge and past it. A list
 * shorter than three bands has a third of it for each, and none when that
 * is under [least] (the same on every platform).
 */
fun dragScrollSpeed(y: Float, top: Float, bottom: Float, zone: Float, max: Float, least: Float): Float {
    if (bottom <= top || zone <= 0f) return 0f
    val z = min(zone, (bottom - top) / 3f)
    if (z < least) return 0f
    return when {
        y < top + z -> -max * ((top + z - y) / z).coerceAtMost(1f)
        y > bottom - z -> max * ((y - (bottom - z)) / z).coerceAtMost(1f)
        else -> 0f
    }
}

/**
 * Where a finger at [p] points in a list [size] big whose bottom
 * [bottomInset] pixels something floats over: inside what is shown of it.
 */
fun clampToShown(p: Offset, size: IntSize, bottomInset: Float): Offset {
    if (size.width <= 0 || size.height <= 0) return p
    val bottom = max(0f, size.height - bottomInset - 1f)
    return Offset(p.x.coerceIn(0f, size.width - 1f), p.y.coerceIn(0f, bottom))
}

/**
 * The key of a list's item under [y] (pixels from the list's top), or null
 * over none (its content padding, past its end).
 */
fun LazyListLayoutInfo.keyAt(y: Float): Any? {
    // Items are placed from the start of the content: past any padding before it.
    val at = y + viewportStartOffset
    return visibleItemsInfo.firstOrNull { at >= it.offset && at < it.offset + it.size }?.key
}

/**
 * The key of a vertical grid's item under ([x], [y]) (pixels from the
 * grid's top left), or null over none - a gap, the padding. [left]: the
 * grid's content padding on its left, in pixels (a vertical grid places
 * its items from there, already mirrored right to left). [inside]: whether
 * a point in an item (pixels from its own top left, in an item that big)
 * is on what counts - all of it unless said otherwise (People: the face).
 */
fun LazyGridLayoutInfo.keyAt(
    x: Float, y: Float, left: Float,
    inside: (Offset, IntSize) -> Boolean = { _, _ -> true },
): Any? {
    val cx = x - left
    val cy = y + viewportStartOffset
    val item = visibleItemsInfo.firstOrNull {
        cx >= it.offset.x && cx < it.offset.x + it.size.width && cy >= it.offset.y && cy < it.offset.y + it.size.height
    } ?: return null
    return item.key.takeIf { inside(Offset(cx - item.offset.x, cy - item.offset.y), item.size) }
}

/**
 * A list's drag to select: what is under the finger ([hit]), what a drag
 * does, and where it is. Made by [rememberDragSelect], put on the list with
 * [dragSelect].
 */
@Stable
class DragSelectState<K : Any> internal constructor() {
    internal var hit: (Offset) -> K? = { null }
    internal var start: (Offset) -> K? = { null }
    internal var onBegin: (K) -> Unit = {}
    internal var onMove: (K) -> Boolean = { true }
    internal var onEnd: () -> Unit = {}
    internal var goesOn: () -> Boolean = { true }
    internal var bottomInset = 0f
    internal var slop = 0f

    /** A drag is going on: the long press fired and the finger is still down. */
    var dragging by mutableStateOf(false)
        private set

    /**
     * The drag is over with the finger still down (the pressed item went
     * away, a new listing ended it): its moves change nothing and the edges
     * don't scroll, but the lift is still the drag's, no tap.
     */
    internal var over by mutableStateOf(false)
        private set

    // The finger, in the list's coordinates, while dragging.
    internal var pointer: Offset? = null
    private var from = Offset.Zero
    private var last: K? = null
    // The edges scroll only once the finger has moved: a long press near one
    // doesn't set the list going by itself.
    internal var armed = false
    internal var size = IntSize.Zero
    // Where the list's top is on the screen, and how far it moved down
    // during the drag (a bar appearing above it): the items are scrolled
    // back under the finger.
    private var top: Float? = null
    internal var shift = 0f

    internal fun begin(key: K, at: Offset) {
        pointer = at
        from = at
        last = key
        armed = false
        over = false
        shift = 0f
        dragging = true
        onBegin(key)
    }

    /**
     * Whether the drag goes on: asked of the view model at every move and
     * scroll step, as the pressed item may go (a new listing, a delete)
     * while the finger stays over the same one. Once it says no, the drag
     * is [over] until the lift.
     */
    internal fun goingOn(): Boolean {
        if (!over && !goesOn()) over = true
        return !over
    }

    internal fun moveTo(p: Offset) {
        pointer = p
        if (!goingOn()) return
        if (!armed && (p - from).getDistance() > slop) armed = true
        // Past an edge (over the field above, the bar below) the range
        // follows what scrolls in at that edge, as far as the list shows.
        val k = hit(clampToShown(p, size, bottomInset)) ?: return
        if (k == last) return
        last = k
        if (!onMove(k)) over = true
    }

    /** The list scrolled under the finger: the item there now. */
    internal fun rehit() { pointer?.let(::moveTo) }

    internal fun end() {
        if (!dragging) return
        dragging = false
        over = false
        pointer = null
        last = null
        onEnd()
    }

    internal fun placed(size: IntSize, topInRoot: Float) {
        this.size = size
        val before = top
        top = topInRoot
        if (dragging && before != null) shift += topInRoot - before
    }
}

/**
 * The drag to select of a list or grid scrolled by [scroll]. [hit]: the key
 * of the item at a point in the list's own coordinates, null where there is
 * none (a gap, a title, a header, an item that can't be selected) - the
 * last item reached stays. [start]: the same for where a long press may
 * start a drag - only on an item itself, never a gap, a title or padding;
 * [hit] unless an item has parts that don't start one (People: a face's
 * name and count). [onBegin]: the long press fired on an item (it is
 * selected); [onMove]: the finger reached another - false once the drag is
 * over (the pressed item went away); [onEnd]: it lifted; [goesOn]: whether
 * the drag goes on, asked at every move and scroll step - false once its
 * pressed item has gone or a new listing ended it. [bottomInset]:
 * how much of the list's bottom something floats over (the selection bar),
 * in pixels - the bottom band starts above it.
 */
@Composable
fun <K : Any> rememberDragSelect(
    scroll: ScrollableState,
    hit: (Offset) -> K?,
    onBegin: (K) -> Unit,
    onMove: (K) -> Boolean,
    onEnd: () -> Unit,
    goesOn: () -> Boolean,
    bottomInset: Float = 0f,
    start: (Offset) -> K? = hit,
): DragSelectState<K> {
    val state = remember { DragSelectState<K>() }
    val haptics = LocalHapticFeedback.current
    // What this composition knows: the layout the hit test reads, the view
    // model's calls, what floats over the list.
    SideEffect {
        state.hit = hit
        state.start = start
        state.onBegin = { k -> haptics.performHapticFeedback(HapticFeedbackType.LongPress); onBegin(k) }
        state.onMove = onMove
        state.onEnd = onEnd
        state.goesOn = goesOn
        state.bottomInset = bottomInset
    }
    val density = LocalDensity.current
    val zone = with(density) { DRAG_SELECT_EDGE.toPx() }
    val least = with(density) { DRAG_SELECT_MIN_EDGE.toPx() }
    val maxSpeed = with(density) { DRAG_SELECT_MAX_SPEED.dp.toPx() }
    LaunchedEffect(state, scroll, zone, least, maxSpeed) {
        snapshotFlow { state.dragging && !state.over }.collectLatest { on ->
            if (!on) return@collectLatest
            var last = 0L
            while (true) {
                val now = withFrameNanos { it }
                // A frame late (the app busy) doesn't jump the list.
                val dt = if (last == 0L) 0f else ((now - last) / 1e9f).coerceAtMost(0.05f)
                last = now
                val p = state.pointer ?: continue
                var moved = false
                val shift = state.shift
                if (shift != 0f) {
                    state.shift = 0f
                    moved = scroll.scrollBy(shift) != 0f
                }
                // Only once the finger has moved on from where the press
                // fired: a press held still in a band never scrolls.
                if (state.armed && dt > 0f) {
                    val v = dragScrollSpeed(p.y, 0f, state.size.height - state.bottomInset, zone, maxSpeed, least)
                    if (v != 0f) {
                        // Not a step more once the drag is over, the finger
                        // still on the same item.
                        if (!state.goingOn()) break
                        moved = scroll.scrollBy(v * dt) != 0f || moved
                    }
                }
                if (moved) state.rehit()
            }
        }
    }
    return state
}

/**
 * Puts [state]'s drag to select on this list or grid: a long press on an
 * item starts it, and from then on the finger's moves are the drag's alone
 * - the list doesn't scroll, pull to refresh doesn't pull and the item
 * under the finger isn't clicked when it lifts. Before the press fires,
 * scrolling and taps work as ever.
 */
fun <K : Any> Modifier.dragSelect(state: DragSelectState<K>, enabled: Boolean = true): Modifier =
    this.onGloballyPositioned { state.placed(it.size, it.positionInRoot().y) }
        .pointerInput(state, enabled) { if (enabled) detectDragSelect(state) }

private suspend fun <K : Any> PointerInputScope.detectDragSelect(state: DragSelectState<K>) {
    state.slop = viewConfiguration.touchSlop
    awaitEachGesture {
        // The item takes its own down (a tap opens it): seen all the same.
        // Only on an item itself: a press anywhere else starts nothing.
        val down = awaitFirstDown(requireUnconsumed = false)
        if (state.start(down.position) == null) return@awaitEachGesture
        // Scrolling first (the list consumed a move) cancels it.
        val press = awaitLongPressOrCancellation(down.id) ?: return@awaitEachGesture
        val key = state.start(press.position) ?: return@awaitEachGesture
        state.begin(key, press.position)
        try {
            while (true) {
                // Ahead of the list and the item: the moves and the lift
                // are the drag's (Initial pass, consumed).
                val event = awaitPointerEvent(PointerEventPass.Initial)
                val change = event.changes.firstOrNull { it.id == press.id }
                event.changes.forEach { it.consume() }
                if (change == null || !change.pressed) break
                state.moveTo(change.position)
            }
        } finally {
            state.end()
        }
    }
}
