// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
@testable import OffTheCloud

/// Files' uploads (IOS-08, A3): two at a time, "Uploading N of M" as they
/// go, files picked meanwhile joining the count, and Cancel stopping them
/// all without one reported as failed. Android's UploadQueueTest is the
/// same.
@MainActor
struct UploadQueueTests {
    private func until(_ done: () -> Bool) async {
        for _ in 0..<150 where !done() {
            try? await Task.sleep(for: .milliseconds(20))
        }
    }

    /// A stand-in upload: each name waits for its own go (or a cancel).
    @MainActor
    final class Uploads {
        var open: Set<String> = []
        var now = 0
        var most = 0
        var started: [String] = []
        var finished: [String] = []
        var cancelled: [String] = []

        func send(_ name: String) async {
            started.append(name)
            now += 1
            most = max(most, now)
            defer { now -= 1 }
            while !open.contains(name) {
                if Task.isCancelled { cancelled.append(name); return }
                try? await Task.sleep(for: .milliseconds(5))
            }
            finished.append(name)
        }
    }

    @Test func twoAtATimeCountedAsTheyGo() async {
        let up = Uploads()
        var lines: [UploadProgress?] = []
        let queue = UploadQueue<String>(slots: 2, send: { await up.send($0) }, onChange: { lines.append($0) })
        queue.add(["a", "b", "c", "d", "e"])
        await until { up.started.count == 2 }
        try? await Task.sleep(for: .milliseconds(50))
        #expect(up.started.count == 2)
        #expect(lines.last == UploadProgress(done: 1, total: 5))
        up.open.insert("a")
        await until { up.started.count == 3 }
        #expect(lines.last == UploadProgress(done: 2, total: 5))
        // Picked meanwhile: the same count.
        queue.add(["f"])
        #expect(lines.last == UploadProgress(done: 2, total: 6))
        up.open.formUnion(["b", "c", "d", "e", "f"])
        await until { lines.last == .some(nil) }
        #expect(up.finished.sorted() == ["a", "b", "c", "d", "e", "f"])
        #expect(up.most == 2)
        #expect(!queue.busy)
    }

    @Test func cancelStopsThemAllAndNoneIsAFailure() async {
        let up = Uploads()
        var lines: [UploadProgress?] = []
        let queue = UploadQueue<String>(slots: 2, send: { await up.send($0) }, onChange: { lines.append($0) })
        queue.add(["a", "b", "c", "d"])
        await until { up.started.count == 2 }
        queue.cancel()
        #expect(lines.last == .some(nil))
        await until { up.cancelled.count == 2 }
        // What went stopped; what waited never went.
        #expect(Set(up.cancelled) == ["a", "b"])
        #expect(up.started.count == 2)
        #expect(up.finished.isEmpty)
        #expect(!queue.busy)
        // The next pick counts from one again.
        queue.add(["e"])
        #expect(lines.last == UploadProgress(done: 1, total: 1))
        up.open.insert("e")
        await until { lines.last == .some(nil) }
        #expect(up.finished == ["e"])
    }

    @Test func theLineAndAnUnreadableFile() {
        #expect(S.appFilesUploading(done: 2, total: 5) == "Uploading 2 of 5…")
        #expect(S.appFilesUploadFailed(detail: S.appFilesCannotRead(name: "photo.jpg")) == "Upload failed: cannot read \u{2068}photo.jpg\u{2069}")
    }
}
