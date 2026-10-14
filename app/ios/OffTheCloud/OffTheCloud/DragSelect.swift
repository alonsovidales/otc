// SPDX-License-Identifier: AGPL-3.0-or-later

import SwiftUI
import UIKit

// Issue #200: several photos or files picked in one gesture, as Google
// Photos does. A long press selects the item under the finger; dragging on
// without lifting selects every item from that one to the one under the
// finger, in the order they are shown, and dragging back gives the items
// left behind back their earlier state. The range adds to what was
// selected before; near the top or bottom edge the view scrolls on and the
// range follows. Images, People and Files (list and grid) share it, and
// Android's DragSelect.kt does the same (the web's dragSelect.ts with a
// mouse drag). VoiceOver keeps each item's own Select action.

enum DragSelect {
    /// How long a press is held before it selects: Images' and Files'
    /// (People keeps its own, longer one).
    static let pressDuration: TimeInterval = 0.25
    /// How far the finger may move before the press fires and still be a
    /// press (more, and it is the view's scroll).
    static let allowableMovement: CGFloat = 10
    /// The band along the top and bottom edges where the view scrolls by
    /// itself, and the speed it reaches at the edge itself (and past it).
    static let edgeZone: CGFloat = 56
    static let maxSpeed: CGFloat = 1200

    /// How fast the view scrolls with the finger at `y` (points down from
    /// the top of the visible part, which ends at `bottom`): toward the
    /// top (negative) or the bottom, faster the deeper into its edge's band,
    /// in points a second; 0 outside the bands.
    /// A view too short for two whole bands (a phone on its side with the
    /// keyboard up) has them a third of its height each.
    static func scrollSpeed(y: CGFloat, top: CGFloat = 0, bottom: CGFloat) -> CGFloat {
        let zone = min(edgeZone, (bottom - top) / 3)
        guard zone >= 8 else { return 0 }
        if y < top + zone { return -maxSpeed * min(1, (top + zone - y) / zone) }
        if y > bottom - zone { return maxSpeed * min(1, (y - (bottom - zone)) / zone) }
        return 0
    }

    /// Whether the edges may scroll yet: only once the finger, at `now`,
    /// has moved on from `pressed`, where the press fired, by more than a
    /// press may move - a long press held still near an edge doesn't set
    /// the view going by itself. Once armed, a drag stays armed.
    static func armed(from pressed: CGPoint, to now: CGPoint) -> Bool {
        hypot(now.x - pressed.x, now.y - pressed.y) > allowableMovement
    }

    /// A tap that is the end of a drag selection (the finger lifting) is
    /// not a tap on the item under it.
    @MainActor static var tapAllowed: Bool { CACurrentMediaTime() - lastEnd > 0.35 }
    @MainActor fileprivate static var lastEnd: CFTimeInterval = 0
}

/// The selection a drag makes from the one before it: the items from the
/// one pressed to the one under the finger, in the order shown, added to
/// what was selected before. Plain values, the same on every platform.
///
/// Items going from the list mid-drag (a refresh, a delete elsewhere): the
/// pressed one gone ends the drag before the finger lifts - the selection
/// stays as it stands, later moves change nothing, and the lift is still
/// no tap; the one under the finger gone leaves the selection as it stands
/// until the finger is over another item; a pick made before the press
/// gone leaves the drag too (forget), never to come back with a move.
struct DragSelection<Key: Hashable> {
    /// What was selected before the press (less what has gone since).
    private(set) var base: Set<Key>
    /// The item pressed.
    let start: Key
    /// The item under the finger now.
    private(set) var end: Key
    /// The pressed item has gone from the items shown: the drag is over,
    /// for good (even if it comes back).
    private(set) var isOver = false

    init(base: Set<Key>, start: Key) {
        self.base = base
        self.start = start
        end = start
    }

    /// Whether the drag goes on with the items shown now, `order`: not once
    /// the pressed item has gone from them.
    mutating func goesOn(in order: [Key]) -> Bool {
        if !isOver, !order.contains(start) { isOver = true }
        return !isOver
    }

    /// Picks that went from the items mid-drag (People's refresh dropping
    /// a face, Images' delete), as the view drops them from its selection:
    /// gone from what was selected before the press too, so the next move
    /// doesn't bring them back (Android's DragSelection.forget).
    mutating func forget(where gone: (Key) -> Bool) {
        guard base.contains(where: gone) else { return }
        base = base.filter { !gone($0) }
    }

    /// The finger is over `key` now; false when nothing changes: it
    /// already was, or the drag is over.
    @discardableResult
    mutating func move(to key: Key) -> Bool {
        guard !isOver, key != end else { return false }
        end = key
        return true
    }

    /// The items from the one pressed to the one under the finger, either
    /// way round, as `order` (the order shown) has them; nil when the drag
    /// is over or either of the two has gone from `order` meanwhile - the
    /// selection then stays as it stands.
    func range(in order: [Key]) -> [Key]? {
        guard !isOver, let a = order.firstIndex(of: start),
              let b = start == end ? a : order.firstIndex(of: end) else { return nil }
        return Array(order[min(a, b)...max(a, b)])
    }

    /// The selection with the drag: what was selected before, and the
    /// range. An item the range passed over and left again is back as it
    /// was. Nil: the selection stays as it stands (range(in:)).
    func selection(in order: [Key]) -> Set<Key>? {
        range(in: order).map(base.union)
    }

    /// The same for a selection kept in the order it was made (People's):
    /// the earlier picks as they were (`earlier`: the base in that order;
    /// those forgotten since left out), then the range's new ones in the
    /// order shown.
    func selection(keeping earlier: [Key], in order: [Key]) -> [Key]? {
        guard let range = range(in: order) else { return nil }
        let kept = earlier.filter { base.contains($0) }
        let had = Set(kept)
        return kept + range.filter { !had.contains($0) }
    }
}

/// Which item a point is over, from where the items are drawn (all in one
/// coordinate space).
enum DragSelectHit {
    /// Where a press starts a drag: the item the point is inside; nil
    /// anywhere else - a gap between tiles, past the end of a short row,
    /// padding, a title, a frameless row such as Files' "..". The scroll
    /// view then keeps the touch.
    static func item<Key>(containing p: CGPoint, in frames: [Key: CGRect]) -> Key? {
        frames.first { $0.value.contains(p) }?.key
    }

    /// Where the finger is during a drag: the item it is inside; else,
    /// beside items at its height (a gap between tiles, past the end of a
    /// short row, between months side by side), the nearest of them along
    /// the row; nil over a title, between rows or past them all - the drag
    /// then keeps the item it was last over.
    static func item<Key>(at p: CGPoint, in frames: [Key: CGRect]) -> Key? {
        var best: (key: Key, distance: CGFloat, minX: CGFloat)?
        for (key, f) in frames where f.minY <= p.y && p.y < f.maxY {
            if p.x >= f.minX && p.x < f.maxX { return key }
            let d = p.x < f.minX ? f.minX - p.x : p.x - f.maxX
            // Equally near: the one before it in the row, always the same.
            if best == nil || d < best!.distance || (d == best!.distance && f.minX < best!.minX) {
                best = (key, d, f.minX)
            }
        }
        return best?.key
    }
}

/// The coordinate space the items' frames and the finger are compared in:
/// the grid's own (a name on its lazy stack, where frames stay put while it
/// scrolls), or the screen's - a List's rows don't see a name given to the
/// List.
enum DragSelectSpace {
    case named(String)
    case global

    func frame(_ g: GeometryProxy) -> CGRect {
        switch self {
        case .named(let name): return g.frame(in: .named(name))
        case .global: return g.frame(in: .global)
        }
    }

    func location(_ c: UIGestureRecognizerRepresentableCoordinateSpaceConverter) -> CGPoint {
        switch self {
        case .named(let name): return c.location(in: .named(name))
        case .global: return c.location(in: .global)
        }
    }
}

/// Where each item on screen is drawn, for DragSelectHit: recorded by the
/// items themselves (dragSelectItem), in the grid's coordinate space. A
/// class, so recording draws nothing again. Only the items on screen
/// count, each at its latest frame - one off screen may have kept a frame
/// from before the grid changed. Counted: rows built again (a new width, a
/// new page) show an item's new view before its old one goes.
final class DragSelectFrames<Key: Hashable> {
    private var frames: [Key: CGRect] = [:]
    private var shown: [Key: Int] = [:]

    func record(_ key: Key, _ frame: CGRect) { frames[key] = frame }
    func appeared(_ key: Key) { shown[key, default: 0] += 1 }
    func disappeared(_ key: Key) {
        guard let n = shown[key] else { return }
        shown[key] = n > 1 ? n - 1 : nil
    }

    /// The item on screen a press at `p` is inside: where a drag starts.
    func item(containing p: CGPoint) -> Key? {
        frames.first { shown[$0.key] != nil && $0.value.contains(p) }?.key
    }

    /// The item on screen the finger at `p` is over during a drag, the
    /// nearest in its row beside them (DragSelectHit.item(at:in:)).
    func item(at p: CGPoint) -> Key? {
        DragSelectHit.item(at: p, in: frames.filter { shown[$0.key] != nil })
    }
}

extension View {
    /// This view is the item `key` for the drag selection over `frames`:
    /// where it is drawn in the coordinate space `space` (the one the
    /// DragSelectGesture reports in).
    func dragSelectItem<Key: Hashable>(_ key: Key, in frames: DragSelectFrames<Key>, space: DragSelectSpace) -> some View {
        onGeometryChange(for: CGRect.self) { space.frame($0) } action: { frames.record(key, $0) }
            .onAppear { frames.appeared(key) }
            .onDisappear { frames.disappeared(key) }
    }
}

/// The press and drag, on the grid or list holding the items. A UIKit long
/// press rather than SwiftUI's: a scroll started before it fires stays the
/// scroll view's (SwiftUI's own gestures inside a ScrollView or List hold
/// its scrolling up), and once it fires the scroll view stops following the
/// finger. It starts only on an item itself (DragSelectFrames.item(containing:)):
/// a press on a month's title, a gap, padding or Files' ".." leaves the
/// scroll alone. Once the finger has moved on from where the press fired
/// (DragSelect.armed), near the top and bottom edges of the scroll view it
/// scrolls it (DragSelect.scrollSpeed), and reports the finger again after
/// each step, as the items move under it. A drag over before the lift (its
/// pressed item gone, or Files listed again) is no longer followed, and its
/// lift is still no tap.
struct DragSelectGesture: UIGestureRecognizerRepresentable {
    /// The coordinate space the items' frames are recorded in.
    let space: DragSelectSpace
    var minimumDuration: TimeInterval = DragSelect.pressDuration
    var enabled = true
    /// The bottom of the scroll view a bar is laid over (Images' selection
    /// bar): its edge band starts above it.
    var bottomBand: CGFloat = 0
    /// A tap of the device's own: People plays its own on every pick.
    var haptic = true
    /// Whether a press at this point selects: it is inside an item
    /// (DragSelectFrames.item(containing:), never the nearest one).
    let canStart: (CGPoint) -> Bool
    /// The press fired at this point.
    let began: (CGPoint) -> Void
    /// The finger is at this point now (it moved, or the view scrolled
    /// under it); false once the drag is over (DragSelection.goesOn, or a
    /// new Files listing): the finger is no longer followed nor the view
    /// scrolled, and the lift still ends it.
    let moved: (CGPoint) -> Bool
    /// The finger lifted, or the press was cancelled.
    let ended: () -> Void

    func makeCoordinator(converter: CoordinateSpaceConverter) -> Coordinator {
        Coordinator(gesture: self, converter: converter)
    }

    func makeUIGestureRecognizer(context: Context) -> UILongPressGestureRecognizer {
        let r = UILongPressGestureRecognizer()
        r.minimumPressDuration = minimumDuration
        r.allowableMovement = DragSelect.allowableMovement
        r.delegate = context.coordinator
        r.isEnabled = enabled
        return r
    }

    func updateUIGestureRecognizer(_ recognizer: UILongPressGestureRecognizer, context: Context) {
        context.coordinator.gesture = self
        recognizer.minimumPressDuration = minimumDuration
        if recognizer.isEnabled != enabled { recognizer.isEnabled = enabled }
    }

    func handleUIGestureRecognizerAction(_ recognizer: UILongPressGestureRecognizer, context: Context) {
        let c = context.coordinator
        switch recognizer.state {
        case .began:
            c.begin(recognizer)
        case .changed:
            c.fingerMoved()
        case .ended, .cancelled, .failed:
            c.end()
        default:
            break
        }
    }

    @MainActor
    final class Coordinator: NSObject, UIGestureRecognizerDelegate {
        var gesture: DragSelectGesture
        private let converter: CoordinateSpaceConverter
        private weak var recognizer: UILongPressGestureRecognizer?
        private weak var scrollView: UIScrollView?
        private var link: CADisplayLink?
        private var lastTick: CFTimeInterval = 0
        private var active = false
        /// The drag goes on: false once `moved` said it is over, until the
        /// lift.
        private var following = false
        /// Where the press fired (in the window), and whether the finger
        /// has moved on from there since (DragSelect.armed): only then do
        /// the edges scroll - a long press near one doesn't set the view
        /// going by itself.
        private var pressedAt = CGPoint.zero
        private var armed = false

        init(gesture: DragSelectGesture, converter: CoordinateSpaceConverter) {
            self.gesture = gesture
            self.converter = converter
        }

        private var location: CGPoint { gesture.space.location(converter) }

        // Only over an item: a press on a title or a gap is no selection,
        // and the scroll view keeps the touch.
        func gestureRecognizerShouldBegin(_ r: UIGestureRecognizer) -> Bool {
            gesture.canStart(location)
        }

        func begin(_ r: UILongPressGestureRecognizer) {
            let p = location
            // Asked again: the delegate may not have been (or the grid
            // changed in between).
            guard gesture.canStart(p) else {
                r.isEnabled = false
                r.isEnabled = gesture.enabled
                return
            }
            active = true
            following = true
            armed = false
            recognizer = r
            pressedAt = r.location(in: nil)
            scrollView = Self.scrollView(under: r)
            if gesture.haptic { UIImpactFeedbackGenerator(style: .medium).impactOccurred() }
            gesture.began(p)
            let l = CADisplayLink(target: self, selector: #selector(tick(_:)))
            l.add(to: .main, forMode: .common)
            link = l
            lastTick = CACurrentMediaTime()
        }

        func fingerMoved() {
            guard active, following else { return }
            if !armed, let r = recognizer {
                armed = DragSelect.armed(from: pressedAt, to: r.location(in: nil))
            }
            report()
        }

        /// Tells where the finger is; a drag over by then stops being
        /// followed (no more reports, no more scrolling).
        private func report() {
            guard !gesture.moved(location) else { return }
            following = false
            link?.invalidate()
            link = nil
        }

        func end() {
            link?.invalidate()
            link = nil
            guard active else { return }
            active = false
            following = false
            DragSelect.lastEnd = CACurrentMediaTime()
            gesture.ended()
        }

        /// Scrolls on while the finger is in an edge band, then reports
        /// it again: the items moved under it.
        @objc private func tick(_ l: CADisplayLink) {
            let now = CACurrentMediaTime()
            let dt = min(now - lastTick, 1.0 / 30)
            lastTick = now
            // The grid went away under the finger: nothing more to follow.
            guard let r = recognizer, r.view?.window != nil else { return end() }
            guard active, following, armed, let sv = scrollView else { return }
            let inset = sv.adjustedContentInset
            let y = r.location(in: sv).y - sv.bounds.minY
            let speed = DragSelect.scrollSpeed(y: y, top: inset.top, bottom: sv.bounds.height - inset.bottom - gesture.bottomBand)
            guard speed != 0 else { return }
            let minY = -inset.top
            let maxY = max(minY, sv.contentSize.height + inset.bottom - sv.bounds.height)
            let to = min(maxY, max(minY, sv.contentOffset.y + speed * CGFloat(dt)))
            guard abs(to - sv.contentOffset.y) > 0.01 else { return }
            sv.setContentOffset(CGPoint(x: sv.contentOffset.x, y: to), animated: false)
            report()
        }

        /// The view scrolling the items under the finger: the innermost
        /// scroll view there that scrolls up and down.
        private static func scrollView(under r: UIGestureRecognizer) -> UIScrollView? {
            guard let window = r.view?.window else { return nil }
            var v = window.hitTest(r.location(in: window), with: nil)
            while let view = v {
                if let sv = view as? UIScrollView, sv.isScrollEnabled,
                   sv.contentSize.width <= sv.bounds.width + 1 || sv.contentSize.height > sv.bounds.height {
                    return sv
                }
                v = view.superview
            }
            return nil
        }
    }
}
