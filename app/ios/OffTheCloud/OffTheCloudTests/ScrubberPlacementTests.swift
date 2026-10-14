// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import CoreGraphics
import UIKit
import Testing
@testable import OffTheCloud

/// Issue #198: the date scrubber at the screen's side on a phone turned on
/// its side (ScrubberPlacement, PhotoGallery.swift).
struct ScrubberPlacementTests {
    private typealias Gaps = ScrubberPlacement.Gaps

    @Test func uprightPhoneAndIPadAreAsBefore() {
        // Held upright the grid reaches the screen's side, and an iPad has
        // no band down its sides: nothing moves, nothing is shortened -
        // whichever side the camera is on.
        for camera in [false, true] {
            #expect(ScrubberPlacement(Gaps(trailing: 0, top: 120, bottom: 0), cameraAtTrailing: camera) == ScrubberPlacement())
            #expect(ScrubberPlacement(Gaps(trailing: 0, top: 0, bottom: 0), cameraAtTrailing: camera) == ScrubberPlacement())
            #expect(ScrubberPlacement(Gaps(trailing: 0.4, top: 64, bottom: 20), cameraAtTrailing: camera) == ScrubberPlacement())
        }
        let none = ScrubberPlacement()
        #expect(none.reach == 0 && none.top == 0 && none.bottom == 0)
    }

    @Test func onItsSideItReachesTheScreensEdge() {
        // An iPhone 15 on its side: the band is 59 pt, the grid starts
        // under the 64 pt top bar and ends over the 21 pt home indicator.
        // The island floats clear of the edge: all the way on either side.
        for camera in [false, true] {
            let p = ScrubberPlacement(Gaps(trailing: 59, top: 64, bottom: 21), cameraAtTrailing: camera)
            #expect(p.reach == 59)
            // The top bar already keeps the track off the top corner...
            #expect(p.top == 0)
            // ...the bottom end comes up to 64 pt from the screen's bottom.
            #expect(p.bottom == 43)
            #expect(p.bottom + 21 == ScrubberPlacement.cornerClearance)
        }
        // The Pros' and the Air's wider bands too.
        #expect(ScrubberPlacement(Gaps(trailing: 62, top: 64, bottom: 21), cameraAtTrailing: true).reach == 62)
        #expect(ScrubberPlacement(Gaps(trailing: 68, top: 64, bottom: 21), cameraAtTrailing: true).reach == 68)
        // A notch phone's band, with the notch on the other side: all the way.
        let other = ScrubberPlacement(Gaps(trailing: 47, top: 64, bottom: 21), cameraAtTrailing: false)
        #expect(other.reach == 47 && other.bottom == 43)
    }

    @Test func besideANotchOnlyToTheNotch() {
        // An iPhone 14 (or 13, 16e) with its notch on the scrubber's side:
        // the notch runs 33.75 pt in from the edge along most of the
        // track. The scrubber stops at its inner edge - its handle and
        // grab strip, at the scrubber's outer edge, never under it.
        let p = ScrubberPlacement(Gaps(trailing: 47, top: 64, bottom: 21), cameraAtTrailing: true)
        #expect(p.reach == 11)
        #expect(47 - p.reach >= 33.75)
        #expect(47 - p.reach == ScrubberPlacement.notchDepth)
        // That far in, the corners don't reach the track: not shortened.
        #expect(p.top == 0 && p.bottom == 0)
        // Every notch phone: X/XS/11 Pro (44 pt band, 30 pt notch), XR/11
        // (48, 33), 12 and 13 mini (50, 33 and 36) - clear of the notch,
        // and never past the band.
        for (band, notch) in [(44, 30), (47, 32), (47, 33.75), (48, 33), (50, 33), (50, 36)] as [(CGFloat, CGFloat)] {
            let q = ScrubberPlacement(Gaps(trailing: band, top: 64, bottom: 21), cameraAtTrailing: true)
            #expect(q.reach > 0 && q.reach < band)
            #expect(band - q.reach >= notch)
        }
        // A band no wider than the notch: the scrubber stays on the grid.
        #expect(ScrubberPlacement(Gaps(trailing: 36, top: 64, bottom: 21), cameraAtTrailing: true) == ScrubberPlacement())
        #expect(ScrubberPlacement(Gaps(trailing: 30, top: 0, bottom: 0), cameraAtTrailing: true) == ScrubberPlacement())
        // The limit between a notch's band and an island's.
        #expect(ScrubberPlacement.notchBandLimit > 50 && ScrubberPlacement.notchBandLimit < 59)
    }

    @Test func whichSideTheCameraIsOn() {
        // Turned to landscapeLeft the phone's top - its camera - is at the
        // right, the scrubber's side; to landscapeRight, at the left.
        #expect(ScrubberPlacement.cameraAtTrailing(.landscapeLeft, rightToLeft: false))
        #expect(!ScrubberPlacement.cameraAtTrailing(.landscapeRight, rightToLeft: false))
        // Right to left the scrubber is at the left.
        #expect(!ScrubberPlacement.cameraAtTrailing(.landscapeLeft, rightToLeft: true))
        #expect(ScrubberPlacement.cameraAtTrailing(.landscapeRight, rightToLeft: true))
        // Upright: no camera beside the scrubber.
        #expect(!ScrubberPlacement.cameraAtTrailing(.portrait, rightToLeft: false))
        #expect(!ScrubberPlacement.cameraAtTrailing(.portraitUpsideDown, rightToLeft: false))
        // Not known yet: as if it were, so a notch is never reached under.
        #expect(ScrubberPlacement.cameraAtTrailing(.unknown, rightToLeft: false))
    }

    @Test func endsKeepClearOfTheCorners() {
        // A grid reaching the very top or bottom of the screen: the track
        // ends the whole clearance in.
        let p = ScrubberPlacement(Gaps(trailing: 62, top: 0, bottom: 0), cameraAtTrailing: false)
        #expect(p.top == ScrubberPlacement.cornerClearance)
        #expect(p.bottom == ScrubberPlacement.cornerClearance)
        // Already far enough from them: nothing taken off.
        let q = ScrubberPlacement(Gaps(trailing: 62, top: 140, bottom: 80), cameraAtTrailing: false)
        #expect(q.top == 0 && q.bottom == 0)
        // A grid reported above the screen's top (mid-rotation) is taken as
        // at it, never as a longer track.
        #expect(ScrubberPlacement(Gaps(trailing: 59, top: -30, bottom: 21), cameraAtTrailing: false).top == ScrubberPlacement.cornerClearance)
    }

    @Test func clearanceLeavesTheHandleOnScreen() {
        // The handle is 10 pt across at the very edge, its centre at the
        // track's end at most. Along the side of the roundest of these
        // screens (the iPhone Air's mask, in the simulator), the screen's
        // edge is this far in from the side at a distance from its
        // bottom: from the clearance on, the handle loses at most half a
        // point.
        let airInset: [(fromBottom: CGFloat, inset: CGFloat)] = [(44, 4.0), (52, 2.0), (56, 1.5), (60, 1.0), (64, 0.5), (68, 0.2)]
        for (d, inset) in airInset where d >= ScrubberPlacement.cornerClearance {
            #expect(inset <= 0.5)
        }
        // And it is above the home indicator, with room to spare.
        #expect(ScrubberPlacement.cornerClearance - 5 > 21)
    }
}
