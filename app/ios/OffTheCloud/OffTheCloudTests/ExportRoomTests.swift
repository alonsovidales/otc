// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
@testable import OffTheCloud

/// Issue #210: room on the phone for the sync's copies. A copy that doesn't
/// fit waits for the others and fails only when it doesn't fit alone; one
/// under way stops once the phone is down to the floor. A free-space reading
/// stands in for the phone's.
struct ExportRoomTests {
    private static let gib: Int64 = 1 << 30
    private let floor = ExportRoom.floorBytes

    /// Free space the test sets.
    private final class Free: @unchecked Sendable {
        private let lock = NSLock()
        private var value: Int64?
        init(_ v: Int64?) { value = v }
        func set(_ v: Int64?) { lock.withLock { value = v } }
        func get() -> Int64? { lock.withLock { value } }
    }

    @Test func aCopyFitsWithTheFloorLeft() {
        #expect(ExportRoom.fits(size: 3 * Self.gib, free: 3 * Self.gib + floor, held: 0))
        #expect(!ExportRoom.fits(size: 3 * Self.gib, free: 3 * Self.gib + floor - 1, held: 0))
        // Room held by copies under way counts against it.
        #expect(!ExportRoom.fits(size: Self.gib, free: 2 * Self.gib + floor, held: Self.gib + 1))
        #expect(ExportRoom.mustStop(free: floor - 1))
        #expect(!ExportRoom.mustStop(free: floor))
    }

    @Test func aCopyThatDoesntFitAloneSaysWhatItNeeds() async {
        let room = ExportRoom(free: { 2 * Self.gib })
        do {
            _ = try await room.take(4 * Self.gib, filename: "IMG_0001.MOV")
            Issue.record("took room it doesn't have")
        } catch let short as ExportNoRoom {
            #expect(short == ExportNoRoom(filename: "IMG_0001.MOV", size: 4 * Self.gib, needed: 4 * Self.gib + floor))
        } catch {
            Issue.record("unexpected \(error)")
        }
    }

    @Test func anUnknownSizeTakesNothingAndAnUnreadableRoomStopsNothing() async throws {
        let room = ExportRoom(free: { nil })
        #expect(try await room.take(nil, filename: "a") == 0)
        // No reading: it goes, as copies always did.
        #expect(try await room.take(9 * Self.gib, filename: "b") == 9 * Self.gib)
        #expect(!room.copyMustStop())
    }

    @Test func aCopyWaitsForAnotherToLeaveRoom() async throws {
        let free = Free(5 * Self.gib)
        let room = ExportRoom(free: { free.get() })
        let first = try await room.take(3 * Self.gib, filename: "a")
        // 3 GB more doesn't fit beside the first: it waits, not fails.
        let second = Task { try await room.take(3 * Self.gib, filename: "b") }
        try await Task.sleep(for: .milliseconds(300))
        room.release(first)
        #expect(try await second.value == 3 * Self.gib)
    }

    @Test func aWaitingCopyIsCancelled() async throws {
        let room = ExportRoom(free: { 4 * Self.gib })
        let first = try await room.take(3 * Self.gib, filename: "a")
        let second = Task { try await room.take(3 * Self.gib, filename: "b") }
        second.cancel()
        await #expect(throws: CancellationError.self) { try await second.value }
        room.release(first)
    }

    @Test func aSizeLearntFromACopyCutShortIsUsedNextTime() {
        let room = ExportRoom(free: { 0 })
        #expect(room.size(of: "v", given: nil) == nil)
        room.learn("v", written: 2 * Self.gib)
        room.learn("v", written: Self.gib)
        #expect(room.size(of: "v", given: nil) == 2 * Self.gib)
        // PhotoKit's own size wins.
        #expect(room.size(of: "v", given: 5 * Self.gib) == 5 * Self.gib)
        #expect(room.size(of: "v", given: 0) == 2 * Self.gib)
    }

    @Test func aCopyUnderWayStopsAtTheFloor() {
        let free = Free(floor)
        let room = ExportRoom(free: { free.get() })
        #expect(!room.copyMustStop())
        free.set(floor - 1)
        #expect(room.copyMustStop())
    }

    @Test func settingsSaysWhatWaitsAndTheRoomItNeeds() {
        let short = ExportNoRoom(filename: "IMG_0001.MOV", size: 3 * Self.gib, needed: 3 * Self.gib + floor)
        let halt = SyncHalt.phoneFull(short)
        #expect(halt.title == S.iosUploadsPhoneFull)
        #expect(halt.detail?.contains("IMG_0001.MOV") == true)
        #expect(halt.detail?.contains(formatByteCount(short.needed)) == true)
    }
}
