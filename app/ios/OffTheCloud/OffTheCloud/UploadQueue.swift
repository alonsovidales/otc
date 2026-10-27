// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation

/// Where the picked files' uploads are: `done` is the number of the one
/// going now (from 1) - the ones finished and the next - of `total` picked,
/// the line over Files' bar ("Uploading 2 of 5…"). Android's UploadProgress.
struct UploadProgress: Equatable {
    let done: Int
    let total: Int
}

/// Files' uploads, as Android's UploadQueue: at most `slots` at once (every
/// picked file used to start its own upload at the same moment), the others
/// waiting their turn, counted for the progress line; files picked while
/// some go join the same count. Cancel stops them all - what waits never
/// goes, what goes stops at its next chunk - and none of it counts as
/// failed: `send` reports its own failures and says nothing for a
/// cancellation. `onChange` gets the progress, nil once nothing is left.
@MainActor
final class UploadQueue<Item> {
    private let slots: Int
    private let send: @MainActor (Item) async -> Void
    private let onChange: @MainActor (UploadProgress?) -> Void
    private var waiting: [Item] = []
    private var running: [UUID: Task<Void, Never>] = [:]
    private var total = 0
    private var finished = 0
    /// Bumped by cancel(): what ends after it counts for nothing.
    private var generation = 0

    init(slots: Int, send: @escaping @MainActor (Item) async -> Void, onChange: @escaping @MainActor (UploadProgress?) -> Void) {
        self.slots = slots
        self.send = send
        self.onChange = onChange
    }

    /// Whether any is waiting or going.
    var busy: Bool { !waiting.isEmpty || !running.isEmpty }

    /// Queued, behind those already picked.
    func add(_ items: [Item]) {
        guard !items.isEmpty else { return }
        waiting.append(contentsOf: items)
        total += items.count
        report()
        pump()
    }

    /// Cancel: nothing more goes up; nothing is reported for it.
    func cancel() {
        guard busy else { return }
        generation &+= 1
        waiting.removeAll()
        for task in running.values { task.cancel() }
        running.removeAll()
        total = 0
        finished = 0
        onChange(nil)
    }

    private func pump() {
        while running.count < slots, !waiting.isEmpty {
            let item = waiting.removeFirst()
            let id = UUID()
            let generation = self.generation
            running[id] = Task { [weak self] in
                await self?.send(item)
                self?.ended(id, generation: generation)
            }
        }
    }

    private func ended(_ id: UUID, generation: Int) {
        guard generation == self.generation, running.removeValue(forKey: id) != nil else { return }
        finished += 1
        if running.isEmpty && waiting.isEmpty {
            total = 0
            finished = 0
            onChange(nil)
        } else {
            report()
            pump()
        }
    }

    private func report() {
        onChange(UploadProgress(done: min(finished + 1, total), total: total))
    }
}
