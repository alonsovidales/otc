// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  StorageSection.swift
//  OffTheCloud
//
//  Settings › Storage (issue #208), as the web's MirrorDiskPanel.tsx: a
//  disk plugged in while the mirror lacks one, which the device didn't add
//  by itself because something is on it - most new disks and cards come
//  formatted (Status.mirror_candidates). Its owner may have it erased and
//  added: the confirmation says plainly that everything on that disk is
//  erased and that it becomes the mirror's second disk. The device's root
//  watcher checks everything again before it erases anything; then the
//  mirror copies every file onto the disk, its progress shown here.
//
//  Shown only while there is something to say: a disk offered, or one
//  asked for from here, until the copy onto it ends. The device offers
//  disks to the owner of the primary instance alone, never in a container
//  nor on a demo device. Two disks that look the same - one name, one
//  size - can't be chosen here: the owner couldn't tell which one would be
//  erased. Android's StorageSection.kt is the same.
//

import SwiftUI

/// A disk the device offers for the mirror (Status.mirror_candidates).
struct MirrorDisk: Equatable, Identifiable {
    let id: String
    let model: String
    let size: Int64

    init(id: String, model: String, size: Int64) {
        self.id = id
        self.model = model
        self.size = size
    }

    init(_ c: Msg_MirrorDiskCandidate) {
        self.init(id: c.id, model: c.model, size: c.size)
    }

    /// The disk as its owner knows it: what it calls itself and its size,
    /// in decimal units as on its label ("Generic Card Reader · 512.1 GB"),
    /// or the size alone when it gives no name. Made when shown.
    var name: String {
        let model = model.trimmingCharacters(in: .whitespacesAndNewlines)
        let size = MirrorDisks.sizeText(size)
        return model.isEmpty ? size : "\(model) · \(size)" // i18n-ignore: the disk's own name and its size, as on the web
    }
}

/// The disk asked for from here, until the copy onto it ends or it turns
/// out it wasn't added.
struct MirrorDiskAsked: Equatable {
    let disk: MirrorDisk
    /// When the device took the request (its Ack).
    let at: Date
    /// The mirror has started copying onto it.
    var copying: Bool
}

/// What a Status means for the disk asked for.
enum MirrorDiskStep: Equatable {
    /// Nothing new: still waiting, or still copying.
    case unchanged
    /// The mirror started copying onto it.
    case copying
    /// Over: a whole mirror (added and copied), or a copy that stopped -
    /// the mirror's own Alert says why.
    case done
    /// Nothing copying `notAddedAfter` after the Ack: it wasn't added (an
    /// Alert says why).
    case notAdded
}

/// What the device said to UseMirrorDisk.
enum MirrorDiskAnswer: Equatable {
    /// Taken (the Ack): its watcher takes it up within seconds.
    case taken
    /// Turned down (not_candidate, not_degraded, busy, not_in_container,
    /// demo): the device's words, nil when it gave none.
    case refused(String?)

    static func of(_ resp: Msg_RespEnvelope) -> MirrorDiskAnswer {
        if !resp.error, case .respAck(let ack) = resp.payload, ack.ok { return .taken }
        if resp.error && !resp.errorMessage.isEmpty { return .refused(resp.errorMessage) }
        if case .respAck(let ack) = resp.payload, !ack.errorMsg.isEmpty { return .refused(ack.errorMsg) }
        return .refused(nil)
    }
}

/// The section's rules, as the web's.
enum MirrorDisks {
    /// The device answers at once and its watcher acts within seconds: a
    /// disk asked for that is neither being copied onto nor in a whole
    /// mirror after this long wasn't added.
    static let notAddedAfter: TimeInterval = 20

    /// A size in bytes in decimal units with at most one decimal, as on a
    /// disk's label: "512.1 GB", "2 TB", "500 GB" ("512,1 GB" in Spanish:
    /// decimalText). A size that rounds up to 1000 of a unit is one of the
    /// next ("1 TB").
    static func sizeText(_ bytes: Int64) -> String {
        let units = ["B", "kB", "MB", "GB", "TB", "PB"] // i18n-ignore: unit symbols, the same in every language
        var n = Double(max(0, bytes))
        var unit = 0
        while n >= 1000 && unit < units.count - 1 {
            n /= 1000
            unit += 1
        }
        if unit == 0 { return "\(Int64(n)) \(units[0])" } // i18n-ignore: a size and its unit's symbol
        var tenths = (n * 10).rounded()
        if tenths >= 10_000 && unit < units.count - 1 {
            unit += 1
            tenths = (tenths / 1000).rounded()
        }
        let whole = tenths.truncatingRemainder(dividingBy: 10) == 0
        let number = decimalText(whole ? "%.0f" : "%.1f", tenths / 10)
        return "\(number) \(units[unit])" // i18n-ignore: a size and its unit's symbol
    }

    /// Another disk offered with the same name and size: neither gets a
    /// button, only the hint that they can't be told apart.
    static func lookAlike(_ d: MirrorDisk, among offered: [MirrorDisk]) -> Bool {
        offered.contains { $0.id != d.id && $0.model == d.model && $0.size == d.size }
    }

    /// How the disk asked for goes, from a Status.
    static func step(_ asked: MirrorDiskAsked, raidState: Msg_RaidState, now: Date) -> MirrorDiskStep {
        if raidState == .raidSyncing { return asked.copying ? .unchanged : .copying }
        if raidState == .raidInSync || asked.copying { return .done }
        if now.timeIntervalSince(asked.at) >= notAddedAfter { return .notAdded }
        return .unchanged
    }

    /// Whether the section shows: a disk offered, one asked for, or one
    /// that wasn't added.
    static func shown(offered: [MirrorDisk], asked: MirrorDiskAsked?, notAdded: MirrorDisk?) -> Bool {
        asked != nil || notAdded != nil || !offered.isEmpty
    }
}

@MainActor
final class MirrorDiskViewModel: ObservableObject {
    /// The disk asked for from here, until the copy onto it ends.
    @Published private(set) var asked: MirrorDiskAsked?
    /// The disk asked for that the device didn't add.
    @Published private(set) var notAdded: MirrorDisk?
    /// The disk whose confirmation shows.
    @Published var confirming: MirrorDisk?
    /// A request on its way: the buttons wait.
    @Published private(set) var sending = false
    /// The device turned the disk down (or nothing answered).
    @Published private(set) var failure: SettingsText?

    private let send: DeviceNeedsUpdate.Send
    /// The clock (tests set their own).
    var now: () -> Date = Date.init

    init(send: @escaping DeviceNeedsUpdate.Send = DeviceNeedsUpdate.viaDevice) {
        self.send = send
    }

    /// Settings' Status asks every 2 s while the device takes the disk up,
    /// every 5 s otherwise.
    var fastPoll: Bool { asked.map { !$0.copying } ?? false }

    /// The disks a Status offers, in its order.
    static func offered(_ s: Msg_Status?) -> [MirrorDisk] {
        (s?.mirrorCandidates ?? []).map(MirrorDisk.init)
    }

    /// Each Status Settings gets.
    func sawStatus(_ s: Msg_Status?) {
        guard let a = asked, let s else { return }
        switch MirrorDisks.step(a, raidState: s.raidState, now: now()) {
        case .unchanged:
            break
        case .copying:
            var copying = a
            copying.copying = true
            asked = copying
        case .done:
            asked = nil
        case .notAdded:
            notAdded = a.disk
            asked = nil
        }
    }

    /// Erase `d` and add it to the mirror (after the confirmation).
    func use(_ d: MirrorDisk) async {
        sending = true
        failure = nil
        defer { sending = false }
        var req = Msg_UseMirrorDisk()
        req.disk = d.id
        do {
            let resp = try await send(.reqUseMirrorDisk(req))
            switch MirrorDiskAnswer.of(resp) {
            case .taken:
                notAdded = nil
                asked = MirrorDiskAsked(disk: d, at: now(), copying: false)
            case .refused(let words):
                failure = words.map { .asIs($0) } ?? .say { S.appSettingsStorageFailed }
            }
        } catch {
            failure = .error(error)
        }
    }
}

struct StorageSection: View {
    // Both owned by SettingsView, whose Status poll feeds the model.
    @ObservedObject var model: MirrorDiskViewModel
    @ObservedObject var status: StatusViewModel

    var body: some View {
        let offered = MirrorDiskViewModel.offered(status.status)
        if MirrorDisks.shown(offered: offered, asked: model.asked, notAdded: model.notAdded) {
            Section(header: Text(S.appSettingsStorageTitle)) {
                if let asked = model.asked, !asked.copying {
                    HStack(spacing: 8) {
                        ProgressView()
                        Text(S.appSettingsStorageAdding(disk: asked.disk.name))
                            .foregroundStyle(.secondary)
                    }
                    .accessibilityElement(children: .combine)
                }

                if let asked = model.asked, asked.copying, let s = status.status {
                    VStack(alignment: .leading, spacing: 6) {
                        Text(S.appSettingsStorageCopying(disk: asked.disk.name))
                        ProgressView(value: Double(min(max(s.raidSyncPercent, 0), 100)), total: 100)
                        Text(raidStateLabel(s))
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                }

                if let gone = model.notAdded {
                    Text(S.appSettingsStorageNotAdded(disk: gone.name))
                        .foregroundStyle(.red)
                }

                if model.asked == nil {
                    ForEach(offered) { d in
                        VStack(alignment: .leading, spacing: 8) {
                            Text(S.appSettingsStorageNewDisk(disk: d.name))
                            if MirrorDisks.lookAlike(d, among: offered) {
                                Text(S.appSettingsStorageLookAlike)
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                            } else {
                                Button(S.appSettingsStorageUse, role: .destructive) {
                                    model.confirming = d
                                }
                                .buttonStyle(.bordered)
                                .disabled(model.sending)
                            }
                        }
                    }
                }

                if let failure = model.failure {
                    Text(failure.text).font(.caption).foregroundStyle(.red)
                }
            }
            // Cancel is the default; erasing is the red button.
            .alert(S.appSettingsStorageConfirmTitle, isPresented: Binding(
                get: { model.confirming != nil },
                set: { if !$0 { model.confirming = nil } }
            ), presenting: model.confirming) { d in
                Button(S.commonCancel, role: .cancel) {}
                Button(S.appSettingsStorageConfirm, role: .destructive) {
                    Task { await model.use(d) }
                }
            } message: { d in
                Text(S.appSettingsStorageConfirmText(disk: d.name))
            }
        }
    }
}
