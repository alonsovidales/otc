// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
@testable import OffTheCloud

/// Settings › Storage (issue #208, StorageSection.swift): a disk that isn't
/// blank offered for the mirror, as the web's MirrorDiskPanel. Android's
/// StorageSectionTest is the same, case for case.
struct MirrorDiskRulesTests {
    private let gb: Int64 = 1_000_000_000

    @Test func sizesAreDecimalWithAtMostOneDecimal() {
        #expect(MirrorDisks.sizeText(512_110_190_592) == "512.1 GB")
        #expect(MirrorDisks.sizeText(500 * gb) == "500 GB")
        #expect(MirrorDisks.sizeText(2_000 * gb) == "2 TB")
        #expect(MirrorDisks.sizeText(4_000_787_030_016) == "4 TB")
        #expect(MirrorDisks.sizeText(1_500 * gb) == "1.5 TB")
        #expect(MirrorDisks.sizeText(31_914_983_424) == "31.9 GB")
        #expect(MirrorDisks.sizeText(64_023_257_088) == "64 GB")
        #expect(MirrorDisks.sizeText(250_059_350_016) == "250.1 GB")
        // A size that rounds up to 1000 of a unit is one of the next.
        #expect(MirrorDisks.sizeText(999_960_000_000) == "1 TB")
        #expect(MirrorDisks.sizeText(999_940_000_000) == "999.9 GB")
        // Small ones, for completeness.
        #expect(MirrorDisks.sizeText(0) == "0 B")
        #expect(MirrorDisks.sizeText(999) == "999 B")
        #expect(MirrorDisks.sizeText(1_000) == "1 kB")
        #expect(MirrorDisks.sizeText(16_400_000) == "16.4 MB")
        #expect(MirrorDisks.sizeText(-5) == "0 B")
    }

    @Test func aDiskIsNamedByItsModelAndSizeOrItsSizeAlone() {
        #expect(MirrorDisk(id: "a", model: "Generic Card Reader", size: 512_110_190_592).name == "Generic Card Reader · 512.1 GB")
        #expect(MirrorDisk(id: "a", model: "", size: 2_000 * gb).name == "2 TB")
        #expect(MirrorDisk(id: "a", model: "   ", size: 2_000 * gb).name == "2 TB")
        #expect(MirrorDisk(id: "a", model: " Samsung PSSD T7 ", size: 1_000 * gb).name == "Samsung PSSD T7 · 1 TB")
    }

    @Test func twoDisksOfOneModelAndSizeGetNoButton() {
        let a = MirrorDisk(id: "a", model: "Generic Card Reader", size: 64 * gb)
        let b = MirrorDisk(id: "b", model: "Generic Card Reader", size: 64 * gb)
        let c = MirrorDisk(id: "c", model: "Generic Card Reader", size: 128 * gb)
        let d = MirrorDisk(id: "d", model: "Samsung PSSD T7", size: 64 * gb)
        let offered = [a, b, c, d]
        #expect(MirrorDisks.lookAlike(a, among: offered))
        #expect(MirrorDisks.lookAlike(b, among: offered))
        // Another size, or another name: a button each.
        #expect(!MirrorDisks.lookAlike(c, among: offered))
        #expect(!MirrorDisks.lookAlike(d, among: offered))
        // Alone, never.
        #expect(!MirrorDisks.lookAlike(a, among: [a]))
        // Two nameless disks of one size can't be told apart either.
        let e = MirrorDisk(id: "e", model: "", size: 64 * gb)
        let f = MirrorDisk(id: "f", model: "", size: 64 * gb)
        #expect(MirrorDisks.lookAlike(e, among: [e, f]))
    }

    @Test func theSectionShowsOnlyWithSomethingToSay() {
        let d = MirrorDisk(id: "a", model: "X", size: gb)
        #expect(!MirrorDisks.shown(offered: [], asked: nil, notAdded: nil))
        #expect(MirrorDisks.shown(offered: [d], asked: nil, notAdded: nil))
        #expect(MirrorDisks.shown(offered: [], asked: MirrorDiskAsked(disk: d, at: Date(), copying: false), notAdded: nil))
        #expect(MirrorDisks.shown(offered: [], asked: nil, notAdded: d))
    }

    @Test func aDiskAskedForIsFollowedThroughTheStatus() {
        let d = MirrorDisk(id: "a", model: "X", size: gb)
        let t0 = Date(timeIntervalSince1970: 1_800_000_000)
        let waiting = MirrorDiskAsked(disk: d, at: t0, copying: false)
        // Still degraded a moment later: waiting.
        #expect(MirrorDisks.step(waiting, raidState: .raidDegraded, now: t0 + 5) == .unchanged)
        // The mirror copies onto it.
        #expect(MirrorDisks.step(waiting, raidState: .raidSyncing, now: t0 + 5) == .copying)
        let copying = MirrorDiskAsked(disk: d, at: t0, copying: true)
        #expect(MirrorDisks.step(copying, raidState: .raidSyncing, now: t0 + 600) == .unchanged)
        // Whole: done. A copy that stopped: done too (its Alert says why).
        #expect(MirrorDisks.step(copying, raidState: .raidInSync, now: t0 + 900) == .done)
        #expect(MirrorDisks.step(copying, raidState: .raidDegraded, now: t0 + 900) == .done)
        #expect(MirrorDisks.step(waiting, raidState: .raidInSync, now: t0 + 3) == .done)
        // Nothing copying 20 s after the Ack: it wasn't added.
        #expect(MirrorDisks.step(waiting, raidState: .raidDegraded, now: t0 + 19.9) == .unchanged)
        #expect(MirrorDisks.step(waiting, raidState: .raidDegraded, now: t0 + 20) == .notAdded)
        #expect(MirrorDisks.step(waiting, raidState: .raidUnknown, now: t0 + 25) == .notAdded)
    }

    @Test func theDevicesAnswers() {
        var ok = Msg_RespEnvelope()
        var a = Msg_Ack()
        a.ok = true
        ok.payload = .respAck(a)
        #expect(MirrorDiskAnswer.of(ok) == .taken)

        var refused = Msg_RespEnvelope()
        refused.error = true
        refused.errorCode = "not_candidate"
        refused.errorMessage = "That disk isn't offered any more."
        var no = Msg_Ack()
        no.code = "not_candidate"
        no.errorMsg = "That disk isn't offered any more."
        refused.payload = .respAck(no)
        #expect(MirrorDiskAnswer.of(refused) == .refused("That disk isn't offered any more."))

        var ackOnly = Msg_RespEnvelope()
        var busy = Msg_Ack()
        busy.errorMsg = "Another disk was just asked for."
        ackOnly.payload = .respAck(busy)
        #expect(MirrorDiskAnswer.of(ackOnly) == .refused("Another disk was just asked for."))

        #expect(MirrorDiskAnswer.of(Msg_RespEnvelope()) == .refused(nil))
    }

    @Test func everyTextReadsAsTheWebs() {
        #expect(S.appSettingsStorageTitle == "Storage")
        #expect(S.appSettingsStorageNewDisk(disk: "2 TB") == "New disk found: \u{2068}2 TB\u{2069}. It has data on it, so it wasn't added to the mirror.")
        #expect(S.appSettingsStorageUse == "Use it for the mirror")
        #expect(S.appSettingsStorageConfirmTitle == "Erase this disk and use it for the mirror?")
        #expect(S.appSettingsStorageConfirm == "Erase and use it")
        #expect(S.appSettingsStorageLookAlike == "Another disk plugged in looks just like this one, so neither can be chosen here: unplug the one to keep first.")
        #expect(S.appSettingsStorageFailed == "The disk could not be used for the mirror.")
    }
}

/// The model Settings feeds with its Status answers.
@MainActor
struct MirrorDiskViewModelTests {
    private let disk = MirrorDisk(id: "d1", model: "Generic Card Reader", size: 64_000_000_000)

    private func status(_ state: Msg_RaidState, percent: Float = 0, offering: [MirrorDisk] = []) -> Msg_Status {
        var s = Msg_Status()
        s.raidState = state
        s.raidSyncPercent = percent
        s.mirrorCandidates = offering.map { d in
            var c = Msg_MirrorDiskCandidate()
            c.id = d.id
            c.model = d.model
            c.size = d.size
            return c
        }
        return s
    }

    private func ack() -> Msg_RespEnvelope {
        var r = Msg_RespEnvelope()
        var a = Msg_Ack()
        a.ok = true
        r.payload = .respAck(a)
        return r
    }

    @Test func offeredDisksComeInTheDevicesOrder() {
        let other = MirrorDisk(id: "d2", model: "", size: 2_000_000_000_000)
        #expect(MirrorDiskViewModel.offered(status(.raidDegraded, offering: [disk, other])) == [disk, other])
        #expect(MirrorDiskViewModel.offered(nil).isEmpty)
    }

    /// What the model sent.
    @MainActor
    final class Sent {
        var payloads: [Msg_ReqEnvelope.OneOf_Payload] = []
    }

    @Test func afterTheAckItWaitsThenFollowsTheCopyToTheEnd() async {
        let sent = Sent()
        let answer = ack()
        let model = MirrorDiskViewModel { payload in
            sent.payloads.append(payload)
            return answer
        }
        var clock = Date(timeIntervalSince1970: 1_800_000_000)
        model.now = { clock }

        await model.use(disk)
        guard case .reqUseMirrorDisk(let req) = sent.payloads.first else {
            Issue.record("UseMirrorDisk not sent")
            return
        }
        #expect(req.disk == "d1")
        #expect(model.asked == MirrorDiskAsked(disk: disk, at: clock, copying: false))
        // The device takes it up: Status every 2 s meanwhile.
        #expect(model.fastPoll)
        #expect(model.failure == nil)

        clock += 4
        model.sawStatus(status(.raidDegraded))
        #expect(model.asked?.copying == false)

        clock += 2
        model.sawStatus(status(.raidSyncing, percent: 3))
        #expect(model.asked?.copying == true)
        #expect(!model.fastPoll)

        clock += 3600
        model.sawStatus(status(.raidSyncing, percent: 80))
        #expect(model.asked?.copying == true)

        model.sawStatus(status(.raidInSync))
        #expect(model.asked == nil)
        #expect(model.notAdded == nil)
    }

    @Test func nothingCopyingAfterTwentySecondsWasntAdded() async {
        let answer = ack()
        let model = MirrorDiskViewModel { _ in answer }
        var clock = Date(timeIntervalSince1970: 1_800_000_000)
        model.now = { clock }
        await model.use(disk)

        clock += 10
        model.sawStatus(status(.raidDegraded, offering: [disk]))
        #expect(model.asked != nil)
        clock += 10
        model.sawStatus(status(.raidDegraded, offering: [disk]))
        #expect(model.asked == nil)
        #expect(model.notAdded == disk)
        #expect(!model.fastPoll)

        // Asked again and taken: the note goes.
        await model.use(disk)
        #expect(model.notAdded == nil)
        #expect(model.asked != nil)
    }

    @Test func aRefusalSaysTheDevicesWordsOrOurs() async {
        let refusal: Msg_RespEnvelope = {
            var r = Msg_RespEnvelope()
            r.error = true
            r.errorCode = "busy"
            r.errorMessage = "Another disk was just asked for."
            return r
        }()
        let silent: Msg_RespEnvelope = {
            var r = refusal
            r.errorMessage = ""
            return r
        }()
        let sent = Sent()
        let model = MirrorDiskViewModel { payload in
            sent.payloads.append(payload)
            return sent.payloads.count == 1 ? refusal : silent
        }
        await model.use(disk)
        #expect(model.asked == nil)
        #expect(model.failure?.text == "Another disk was just asked for.")
        #expect(!model.sending)

        await model.use(disk)
        #expect(model.failure?.text == "The disk could not be used for the mirror.")
    }

    @Test func noAnswerSaysWhy() async {
        struct Gone: LocalizedError { var errorDescription: String? { "The connection went." } }
        let model = MirrorDiskViewModel { _ in throw Gone() }
        await model.use(disk)
        #expect(model.asked == nil)
        #expect(model.failure?.text == "The connection went.")
    }

    @Test func aStatusWithNothingAskedChangesNothing() {
        let model = MirrorDiskViewModel { _ in throw CancellationError() }
        model.sawStatus(status(.raidSyncing))
        model.sawStatus(nil)
        #expect(model.asked == nil)
        #expect(model.notAdded == nil)
    }
}
