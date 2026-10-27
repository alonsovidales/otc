// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  TourTests.swift
//  OffTheCloudTests
//
//  The first-run tour's rules and model (Tour.swift): its pages, when it
//  opens by itself, the mark kept on the phone, and moving between pages
//  while People comes and goes. Android's TourTest.kt has the same cases,
//  under the same names.
//

import Foundation
import Testing
@testable import OffTheCloud

private let all: [TourStep] = [.welcome, .images, .backup, .people, .collections, .files, .social, .post, .friends, .alerts, .done]
private let noFaces: [TourStep] = [.welcome, .images, .backup, .collections, .files, .social, .post, .friends, .alerts, .done]

/// A mark kept in memory: what was read and written.
private final class FakeSeen {
    var value: Int?
    var writable = true
    var writes: [Int] = []

    init(_ value: Int?) { self.value = value }

    var seen: TourSeen {
        TourSeen(read: { self.value }, write: { version in
            self.writes.append(version)
            guard self.writable else { return false }
            self.value = version
            return true
        })
    }
}

struct TourRulesTests {
    // T1
    @Test func theVersionIsOne() {
        #expect(TourRules.version == 1)
    }

    // T2
    @Test func peopleOnlyWhileFaceRecognitionIsOn() {
        #expect(TourRules.steps(faces: true) == all)
        #expect(TourRules.steps(faces: false) == noFaces)
        // Not known yet: left out, as the menu does.
        #expect(TourRules.steps(faces: nil) == noFaces)
        #expect(TourRules.steps(faces: true).count == 11)
        #expect(TourRules.steps(faces: false).count == 10)
    }

    // T3
    @Test func opensByItselfOnlyWhenEverythingIsReady() {
        #expect(TourRules.shouldShow(seenVersion: 0, syncChoicePending: false, signedIn: true, mainVisible: true))
        // Seen, this version or a later one.
        #expect(!TourRules.shouldShow(seenVersion: 1, syncChoicePending: false, signedIn: true, mainVisible: true))
        #expect(!TourRules.shouldShow(seenVersion: 2, syncChoicePending: false, signedIn: true, mainVisible: true))
        // The mark can't be read.
        #expect(!TourRules.shouldShow(seenVersion: nil, syncChoicePending: false, signedIn: true, mainVisible: true))
        // The sync question first.
        #expect(!TourRules.shouldShow(seenVersion: 0, syncChoicePending: true, signedIn: true, mainVisible: true))
        // Not signed in yet (or any more).
        #expect(!TourRules.shouldShow(seenVersion: 0, syncChoicePending: false, signedIn: false, mainVisible: true))
        // The connection screen, the Bluetooth setup, a card over MainView.
        #expect(!TourRules.shouldShow(seenVersion: 0, syncChoicePending: false, signedIn: true, mainVisible: false))
    }

    // T4
    @Test func nextAndBackFollowTheFixedOrder() {
        #expect(TourRules.after(.welcome, in: all) == .images)
        #expect(TourRules.after(.backup, in: all) == .people)
        #expect(TourRules.after(.backup, in: noFaces) == .collections)
        #expect(TourRules.after(.alerts, in: all) == .done)
        #expect(TourRules.after(.done, in: all) == nil)
        #expect(TourRules.before(.welcome, in: all) == nil)
        #expect(TourRules.before(.images, in: all) == .welcome)
        #expect(TourRules.before(.collections, in: all) == .people)
        #expect(TourRules.before(.collections, in: noFaces) == .backup)
        #expect(TourRules.before(.done, in: noFaces) == .alerts)
    }

    // T5
    @Test func aPageThatWentMovesToTheNextOne() {
        #expect(TourRules.place(.people, in: noFaces) == .collections)
        #expect(TourRules.place(.people, in: all) == .people)
        #expect(TourRules.place(.files, in: noFaces) == .files)
        // From a page that went, Next and Back go on from where it was.
        #expect(TourRules.after(.people, in: noFaces) == .collections)
        #expect(TourRules.before(.people, in: noFaces) == .backup)
    }
}

@MainActor
struct TourModelTests {
    // T6
    @Test func opensOnceAndIsMarkedSeenAsItOpens() {
        let fake = FakeSeen(0)
        let tour = TourModel(seen: fake.seen)
        tour.evaluate(syncChoicePending: false, signedIn: true, mainVisible: true)
        #expect(tour.shown)
        #expect(tour.step == .welcome)
        #expect(fake.value == 1)
        tour.close()
        // A relaunch, another sign-in: seen.
        tour.evaluate(syncChoicePending: false, signedIn: true, mainVisible: true)
        #expect(!tour.shown)
        let relaunched = TourModel(seen: fake.seen)
        relaunched.evaluate(syncChoicePending: false, signedIn: true, mainVisible: true)
        #expect(!relaunched.shown)
        #expect(fake.writes == [1])
    }

    // T7
    @Test func waitsForTheSyncQuestionWithoutMarkingIt() {
        let fake = FakeSeen(0)
        let tour = TourModel(seen: fake.seen)
        tour.evaluate(syncChoicePending: true, signedIn: true, mainVisible: true)
        tour.evaluate(syncChoicePending: false, signedIn: false, mainVisible: true)
        tour.evaluate(syncChoicePending: false, signedIn: true, mainVisible: false)
        #expect(!tour.shown)
        #expect(fake.writes.isEmpty)
        tour.evaluate(syncChoicePending: false, signedIn: true, mainVisible: true)
        #expect(tour.shown)
    }

    // T8
    @Test func aMarkThatCantBeReadOrKeptOpensNothing() {
        let unreadable = FakeSeen(nil)
        let a = TourModel(seen: unreadable.seen)
        a.evaluate(syncChoicePending: false, signedIn: true, mainVisible: true)
        #expect(!a.shown)
        #expect(unreadable.writes.isEmpty)

        let unwritable = FakeSeen(0)
        unwritable.writable = false
        let b = TourModel(seen: unwritable.seen)
        b.evaluate(syncChoicePending: false, signedIn: true, mainVisible: true)
        #expect(!b.shown)
        #expect(unwritable.writes == [1])
    }

    // T9
    @Test func replayStartsAtTheFirstPageAndLeavesTheMark() {
        let fake = FakeSeen(1)
        let tour = TourModel(seen: fake.seen)
        tour.replay()
        #expect(tour.shown)
        #expect(tour.step == .welcome)
        tour.next(in: all)
        tour.next(in: all)
        #expect(tour.step == .backup)
        tour.close()
        tour.replay()
        #expect(tour.step == .welcome)
        #expect(fake.writes.isEmpty)
        #expect(fake.value == 1)
    }

    // T10
    @Test func nextWalksToDoneAndDoneCloses() {
        let tour = TourModel(seen: FakeSeen(1).seen)
        tour.replay()
        tour.back(in: noFaces)
        #expect(tour.step == .welcome)
        for _ in 0..<9 { tour.next(in: noFaces) }
        #expect(tour.step == .done)
        #expect(tour.shown)
        tour.back(in: noFaces)
        #expect(tour.step == .alerts)
        tour.next(in: noFaces)
        tour.next(in: noFaces)
        #expect(!tour.shown)
    }

    // T11
    @Test func peopleGoingWhileShownMovesOn() {
        let tour = TourModel(seen: FakeSeen(1).seen)
        tour.replay()
        for _ in 0..<3 { tour.next(in: all) }
        #expect(tour.step == .people)
        // Face recognition turned off meanwhile: the page shown is the next.
        #expect(TourRules.place(tour.step, in: noFaces) == .collections)
        tour.next(in: noFaces)
        #expect(tour.step == .files)
        tour.back(in: noFaces)
        tour.back(in: noFaces)
        #expect(tour.step == .backup)
    }

    // T12
    @Test func logOutClosesItAndTheMarkStays() {
        let fake = FakeSeen(0)
        let tour = TourModel(seen: fake.seen)
        tour.evaluate(syncChoicePending: false, signedIn: true, mainVisible: true)
        tour.next(in: all)
        tour.reset()
        #expect(!tour.shown)
        #expect(tour.step == .welcome)
        // The next sign-in, on this device or another: not again.
        tour.evaluate(syncChoicePending: false, signedIn: true, mainVisible: true)
        #expect(!tour.shown)
        #expect(fake.value == 1)
    }

    // T13
    @Test func theMarkIsKeptInDefaultsOfItsOwn() {
        let name = "cloud.off-the.OffTheCloud.test.\(UUID().uuidString)"
        defer { UserDefaults().removePersistentDomain(forName: name) }
        let seen = TourSeen(defaults: UserDefaults(suiteName: name))
        #expect(seen.read() == 0)
        #expect(seen.write(1))
        #expect(TourSeen(defaults: UserDefaults(suiteName: name)).read() == 1)
        // None to be had: nothing read, nothing kept.
        let none = TourSeen(defaults: nil)
        #expect(none.read() == nil)
        #expect(!none.write(1))
        // Apart from the app's own domain, which Log Out removes.
        #expect(TourSeen.suiteName == "cloud.off-the.OffTheCloud.tour")
        #expect(TourSeen.suiteName != Bundle.main.bundleIdentifier)
        #expect(TourSeen.key == "seenVersion")
    }
}

/// The pages' texts with their labels filled in, in English.
struct TourTextTests {
    private func plain(_ s: AttributedString) -> String { String(s.characters) }

    // T14
    @Test func theLabelsAreTheirOwnKeys() {
        let b = AttributeContainer()
        #expect(plain(S.appTourBackupText(settings: S.appShellSectionSettings, sync: S.appSettingsSyncTitle, b: b))
            == "This phone can copy its photos and videos to your device by itself. Choose what it sends in Settings › Sync Options.")
        #expect(plain(S.appTourPostStep3(publish: S.appSocialComposePublish, b: b)) == "Write a caption, then tap Publish.")
        #expect(plain(S.appTourFriendsAdd(suffix: SecretsStore.bridgeDomain, heading: S.appSocialFriendsAddHeading, send: S.commonSend, b: b))
            == "To add a friend, type their device's name, the part before .off-the.cloud, under Add a friend and tap Send.")
        #expect(plain(S.appTourDoneText(show: S.appTourShow, settings: S.appShellSectionSettings, b: b))
            == "See this tour again any time: tap Show the tour in Settings.")
        #expect(S.appTourProgress(step: 3, total: 11) == "Step 3 of 11")
    }
}
