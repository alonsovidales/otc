// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  TourTests.swift
//  OffTheCloudTests
//
//  The first-run tour's rules and model (Tour.swift): its steps, when it
//  opens by itself, the mark kept on the phone, moving between steps while
//  People and Users come and go, the section each step shows, what it
//  rings, the holes and where the card goes. Android's TourTest.kt has the
//  same cases, under the same names (T1-T19).
//

import CoreGraphics
import Foundation
import Testing
@testable import OffTheCloud

private let all: [TourStep] = [.welcome, .search, .dates, .select, .viewer, .people, .collections, .files, .upload,
                               .social, .post, .friends, .backup, .users, .alerts]
private let noFaces: [TourStep] = all.filter { $0 != .people }
private let noOwner: [TourStep] = all.filter { $0 != .users }
private let fewest: [TourStep] = all.filter { $0 != .people && $0 != .users }

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
    @Test func theVersionIsTwo() {
        #expect(TourRules.version == 2)
    }

    // T2
    @Test func peopleWithFaceRecognitionAndUsersForTheOwnerOnly() {
        #expect(TourRules.steps(faces: true, owner: true) == all)
        #expect(TourRules.steps(faces: false, owner: true) == noFaces)
        #expect(TourRules.steps(faces: true, owner: false) == noOwner)
        // Not known yet: left out, as the menu and Settings do.
        #expect(TourRules.steps(faces: nil, owner: nil) == fewest)
        #expect(TourRules.steps(faces: true, owner: true).count == 15)
        #expect(TourRules.steps(faces: false, owner: false).count == 13)
    }

    // T3
    @Test func opensByItselfOnlyWhenEverythingIsReady() {
        #expect(TourRules.shouldShow(seenVersion: 0, syncChoicePending: false, signedIn: true, mainVisible: true))
        // A phone that saw the first tour sees this one.
        #expect(TourRules.shouldShow(seenVersion: 1, syncChoicePending: false, signedIn: true, mainVisible: true))
        // Seen, this version or a later one.
        #expect(!TourRules.shouldShow(seenVersion: 2, syncChoicePending: false, signedIn: true, mainVisible: true))
        #expect(!TourRules.shouldShow(seenVersion: 3, syncChoicePending: false, signedIn: true, mainVisible: true))
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
        #expect(TourRules.after(.welcome, in: all) == .search)
        #expect(TourRules.after(.viewer, in: all) == .people)
        #expect(TourRules.after(.viewer, in: noFaces) == .collections)
        #expect(TourRules.after(.backup, in: all) == .users)
        #expect(TourRules.after(.backup, in: noOwner) == .alerts)
        #expect(TourRules.after(.alerts, in: all) == nil)
        #expect(TourRules.before(.welcome, in: all) == nil)
        #expect(TourRules.before(.search, in: all) == .welcome)
        #expect(TourRules.before(.collections, in: all) == .people)
        #expect(TourRules.before(.collections, in: noFaces) == .viewer)
        #expect(TourRules.before(.alerts, in: fewest) == .backup)
    }

    // T5
    @Test func aStepThatWentMovesToTheNextOne() {
        #expect(TourRules.place(.people, in: noFaces) == .collections)
        #expect(TourRules.place(.people, in: all) == .people)
        #expect(TourRules.place(.users, in: noOwner) == .alerts)
        #expect(TourRules.place(.files, in: fewest) == .files)
        // From a step that went, Next and Back go on from where it was.
        #expect(TourRules.after(.people, in: noFaces) == .collections)
        #expect(TourRules.before(.people, in: noFaces) == .viewer)
    }

    // T15
    @Test func eachStepShowsItsSectionButNeverAlerts() {
        let narrow: [TourStep: AppSection?] = [
            .welcome: nil, .search: .images, .dates: .images, .select: .images, .viewer: .images,
            .people: .images, .collections: .images, .files: .files, .upload: .files, .social: .social,
            .post: .social, .friends: .social, .backup: .settings, .users: .settings, .alerts: .settings,
        ]
        for (step, section) in narrow { #expect(TourRules.section(step, wide: false) == section, "\(step)") }
        // The wide layout's pages, from the menu.
        #expect(TourRules.section(.people, wide: true) == .people)
        #expect(TourRules.section(.collections, wide: true) == .collections)
        #expect(TourRules.section(.friends, wide: true) == .friends)
        #expect(TourRules.section(.search, wide: true) == .images)
        // Opening Alerts marks every alert read: no step shows it.
        for step in TourStep.allCases {
            #expect(TourRules.section(step, wide: false) != .alerts)
            #expect(TourRules.section(step, wide: true) != .alerts)
        }
    }

    // T16
    @Test func eachStepRingsItsOwnControls() {
        #expect(TourRules.targets(.welcome, wide: false) == [.sections])
        #expect(TourRules.targets(.search, wide: false) == [.search])
        #expect(TourRules.targets(.dates, wide: true) == [.scrubber])
        #expect(TourRules.targets(.select, wide: false) == [.firstPhoto])
        #expect(TourRules.targets(.people, wide: false) == [.peopleButton])
        #expect(TourRules.targets(.people, wide: true) == [.firstFace])
        #expect(TourRules.targets(.collections, wide: false) == [.collectionsButton])
        #expect(TourRules.targets(.collections, wide: true) == [.firstCollection])
        #expect(TourRules.targets(.files, wide: false) == [.viewOptions])
        #expect(TourRules.targets(.upload, wide: false) == [.upload, .folderLock])
        #expect(TourRules.targets(.social, wide: false) == [.like, .comment])
        #expect(TourRules.targets(.post, wide: true) == [.newPost])
        #expect(TourRules.targets(.friends, wide: false) == [.friendsButton])
        #expect(TourRules.targets(.friends, wide: true) == [.address, .addFriend])
        #expect(TourRules.targets(.backup, wide: false) == [.wifiOnly])
        #expect(TourRules.targets(.users, wide: false) == [.addUser, .bridgeAccess])
        // Show the tour too, where the card can keep clear of it.
        #expect(TourRules.targets(.alerts, wide: false) == [.alertsSection, .settingsSection])
        #expect(TourRules.targets(.alerts, wide: true) == [.alertsSection, .settingsSection, .showTour])
        // Settings scrolls to what its steps ring: high on a narrow window,
        // whose card is at the foot.
        #expect(TourRules.settingsScroll(.backup, wide: false)?.target == .wifiOnly)
        #expect(TourRules.settingsScroll(.users, wide: false)?.target == .addUser)
        #expect((TourRules.settingsScroll(.backup, wide: false)?.at ?? 1) < 0.3)
        #expect(TourRules.settingsScroll(.alerts, wide: false)?.target == .showTour)
        #expect(TourRules.settingsScroll(.alerts, wide: true)?.target == .showTour)
        #expect(TourRules.settingsScroll(.search, wide: false) == nil)
    }

    // T17
    @Test func holesArePaddedKeptInsideAndMergedWhenTheyTouch() {
        let bounds = CGRect(x: 0, y: 0, width: 400, height: 800)
        // Padded by 6.
        #expect(TourRules.holes([CGRect(x: 100, y: 100, width: 50, height: 20)], in: bounds)
            == [CGRect(x: 94, y: 94, width: 62, height: 32)])
        // Off screen: none; empty: none.
        #expect(TourRules.holes([CGRect(x: 500, y: 100, width: 50, height: 20)], in: bounds).isEmpty)
        #expect(TourRules.holes([CGRect(x: 10, y: 10, width: 0, height: 20)], in: bounds).isEmpty)
        // Kept 2 inside the window.
        #expect(TourRules.holes([CGRect(x: 0, y: 760, width: 400, height: 40)], in: bounds)
            == [CGRect(x: 2, y: 754, width: 396, height: 44)])
        // Two that touch are one; two apart stay two.
        let near = TourRules.holes([CGRect(x: 10, y: 10, width: 40, height: 40), CGRect(x: 55, y: 10, width: 40, height: 40)], in: bounds)
        #expect(near == [CGRect(x: 4, y: 4, width: 97, height: 52)])
        let apart = TourRules.holes([CGRect(x: 10, y: 10, width: 40, height: 40), CGRect(x: 200, y: 300, width: 40, height: 40)], in: bounds)
        #expect(apart.count == 2)
    }

    // T18
    @Test func theCardKeepsOffTheRings() {
        let area = CGRect(x: 0, y: 50, width: 400, height: 750)
        let card = CGSize(width: 380, height: 300)
        // A narrow window: across the foot, at most the window's width.
        let foot = TourRules.cardFrame(holes: [], card: card, area: area, sheet: true)
        #expect(foot == CGRect(x: 12, y: 488, width: 376, height: 300))
        // At the top when the foot would cover a ring (the bottom bar).
        let bar = [CGRect(x: 0, y: 740, width: 400, height: 60)]
        let top = TourRules.cardFrame(holes: bar, card: card, area: area, sheet: true)
        #expect(top.minY == 62)
        #expect(TourRules.covered(top, holes: bar) == 0)
        // A ring at the top leaves it at the foot.
        #expect(TourRules.cardFrame(holes: [CGRect(x: 10, y: 60, width: 300, height: 40)], card: card, area: area, sheet: true).minY == 488)
        // Rings at the top and the foot: in the middle.
        let both = [CGRect(x: 10, y: 60, width: 300, height: 40), CGRect(x: 0, y: 740, width: 400, height: 60)]
        let middle = TourRules.cardFrame(holes: both, card: card, area: area, sheet: true)
        #expect(middle.minY == 275)
        #expect(TourRules.covered(middle, holes: both) == 0)
        // Taller than the window: cut to it.
        let tall = TourRules.cardFrame(holes: [], card: CGSize(width: 380, height: 2000), area: area, sheet: true)
        #expect(tall.height == 726)
        // A wide window: in the middle with nothing rung, else beside the rings.
        let wideArea = CGRect(x: 0, y: 0, width: 1000, height: 700)
        let centre = TourRules.cardFrame(holes: [], card: card, area: wideArea, sheet: false)
        #expect(centre == CGRect(x: 310, y: 200, width: 380, height: 300))
        let left = [CGRect(x: 20, y: 100, width: 200, height: 60)]
        let beside = TourRules.cardFrame(holes: left, card: card, area: wideArea, sheet: false)
        #expect(beside.minX == 220 + TourRules.cardGap)
        #expect(TourRules.covered(beside, holes: left) == 0)
        // Rings everywhere: the corner that covers the least.
        let everywhere = [CGRect(x: 0, y: 0, width: 1000, height: 330), CGRect(x: 0, y: 370, width: 600, height: 330)]
        let corner = TourRules.cardFrame(holes: everywhere, card: card, area: wideArea, sheet: false)
        #expect(corner.maxX == 988)
        #expect(corner.maxY == 688)
    }
}

@MainActor
struct TourModelTests {
    private func model(_ seen: FakeSeen, owner: Bool? = nil) -> TourModel {
        TourModel(seen: seen.seen, role: { owner })
    }

    // T6
    @Test func opensOnceAndIsMarkedSeenAsItOpens() {
        let fake = FakeSeen(0)
        let tour = model(fake)
        tour.evaluate(syncChoicePending: false, signedIn: true, mainVisible: true)
        #expect(tour.shown)
        #expect(tour.step == .welcome)
        #expect(fake.value == 2)
        tour.close()
        // A relaunch, another sign-in: seen.
        tour.evaluate(syncChoicePending: false, signedIn: true, mainVisible: true)
        #expect(!tour.shown)
        let relaunched = model(fake)
        relaunched.evaluate(syncChoicePending: false, signedIn: true, mainVisible: true)
        #expect(!relaunched.shown)
        #expect(fake.writes == [2])
        // A phone that saw the first tour sees this one, once.
        let older = FakeSeen(1)
        let upgraded = model(older)
        upgraded.evaluate(syncChoicePending: false, signedIn: true, mainVisible: true)
        #expect(upgraded.shown)
        #expect(older.value == 2)
    }

    // T7
    @Test func waitsForTheSyncQuestionWithoutMarkingIt() {
        let fake = FakeSeen(0)
        let tour = model(fake)
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
        let a = model(unreadable)
        a.evaluate(syncChoicePending: false, signedIn: true, mainVisible: true)
        #expect(!a.shown)
        #expect(unreadable.writes.isEmpty)

        let unwritable = FakeSeen(0)
        unwritable.writable = false
        let b = model(unwritable)
        b.evaluate(syncChoicePending: false, signedIn: true, mainVisible: true)
        #expect(!b.shown)
        #expect(unwritable.writes == [2])
    }

    // T9
    @Test func replayStartsAtTheFirstStepAndLeavesTheMark() {
        let fake = FakeSeen(2)
        let tour = model(fake)
        tour.replay()
        #expect(tour.shown)
        #expect(tour.step == .welcome)
        tour.next(in: all)
        tour.next(in: all)
        #expect(tour.step == .dates)
        tour.close()
        tour.replay()
        #expect(tour.step == .welcome)
        #expect(fake.writes.isEmpty)
        #expect(fake.value == 2)
    }

    // T10
    @Test func nextWalksToTheLastAndDoneCloses() {
        let tour = model(FakeSeen(2))
        tour.replay()
        tour.back(in: fewest)
        #expect(tour.step == .welcome)
        for _ in 0..<(fewest.count - 1) { tour.next(in: fewest) }
        #expect(tour.step == .alerts)
        #expect(tour.shown)
        tour.back(in: fewest)
        #expect(tour.step == .backup)
        tour.next(in: fewest)
        tour.next(in: fewest)
        #expect(!tour.shown)
    }

    // T11
    @Test func peopleGoingWhileShownMovesOn() {
        let tour = model(FakeSeen(2))
        tour.replay()
        for _ in 0..<5 { tour.next(in: all) }
        #expect(tour.step == .people)
        // Face recognition turned off meanwhile: the step shown is the next.
        #expect(TourRules.place(tour.step, in: noFaces) == .collections)
        tour.next(in: noFaces)
        #expect(tour.step == .files)
        tour.back(in: noFaces)
        tour.back(in: noFaces)
        #expect(tour.step == .viewer)
    }

    // T12
    @Test func logOutClosesItAndTheMarkStays() {
        let fake = FakeSeen(0)
        let tour = model(fake, owner: true)
        tour.evaluate(syncChoicePending: false, signedIn: true, mainVisible: true)
        tour.next(in: all)
        tour.reset()
        #expect(!tour.shown)
        #expect(tour.step == .welcome)
        #expect(tour.owner == nil)
        // The next sign-in, on this device or another: not again.
        tour.evaluate(syncChoicePending: false, signedIn: true, mainVisible: true)
        #expect(!tour.shown)
        #expect(fake.value == 2)
    }

    // T13
    @Test func theMarkIsKeptInDefaultsOfItsOwn() {
        let name = "cloud.off-the.OffTheCloud.test.\(UUID().uuidString)"
        defer { UserDefaults().removePersistentDomain(forName: name) }
        let seen = TourSeen(defaults: UserDefaults(suiteName: name))
        #expect(seen.read() == 0)
        #expect(seen.write(2))
        #expect(TourSeen(defaults: UserDefaults(suiteName: name)).read() == 2)
        // None to be had: nothing read, nothing kept.
        let none = TourSeen(defaults: nil)
        #expect(none.read() == nil)
        #expect(!none.write(2))
        // Apart from the app's own domain, which Log Out removes.
        #expect(TourSeen.suiteName == "cloud.off-the.OffTheCloud.tour")
        #expect(TourSeen.suiteName != Bundle.main.bundleIdentifier)
        #expect(TourSeen.key == "seenVersion")
    }

    // T19
    @Test func theUsersStepComesOnceTheOwnerIsKnown() async {
        let owner = model(FakeSeen(2), owner: true)
        #expect(owner.owner == nil)
        await owner.learnOwner()
        #expect(owner.owner == true)
        #expect(TourRules.steps(faces: false, owner: owner.owner).contains(.users))
        let other = model(FakeSeen(2), owner: false)
        await other.learnOwner()
        #expect(other.owner == false)
        #expect(!TourRules.steps(faces: false, owner: other.owner).contains(.users))
        // Not asked (no connection): stays unknown, and no Users step.
        let unknown = model(FakeSeen(2), owner: nil)
        await unknown.learnOwner()
        #expect(unknown.owner == nil)
    }
}

/// The steps' texts with their labels filled in, in English.
@MainActor
struct TourTextTests {
    private func plain(_ s: AttributedString) -> String { String(s.characters) }

    // T14
    @Test func theLabelsAreTheirOwnKeys() {
        #expect(plain(TourTexts.text(.select, wide: false, faces: false))
            == "Press and hold a photo, then drag across others to select them too. Then choose Share, Download, Add to collection or Delete in the bar at the bottom.")
        #expect(plain(TourTexts.text(.files, wide: false, faces: false))
            == "The button beside the folder's path switches between List, Grid and Storage, which shows what takes up space. Under Sort by, pick an order, and pick it again to reverse it.")
        #expect(plain(TourTexts.text(.post, wide: false, faces: false))
            == "Tap +, pick photos or videos from Phone (this phone) or Synced (already on your device), write a caption and tap Publish. It goes from your device to your friends' devices.")
        #expect(plain(TourTexts.text(.backup, wide: false, faces: false))
            == "This phone copies its photos and videos to your device by itself. Under Sync Options you choose what it sends, and Wi-Fi only keeps it off mobile data.")
        #expect(plain(TourTexts.text(.users, wide: false, faces: false))
            == "Give each person their own Off The Cloud on this same device: their own photos, files and private feed, separate from yours. In Settings › Users, type a username, tap Add and set their first password. Keep Request bridge access on to give them their own address.")
        #expect(plain(TourTexts.text(.alerts, wide: false, faces: false))
            == "Alerts counts what's new: likes, comments, friend requests and news from your device. Settings has your profile, password and language, and Show the tour to see this again.")
        // The layout's own words.
        #expect(plain(TourTexts.text(.welcome, wide: false, faces: false)).hasSuffix("in the bar at the bottom."))
        #expect(plain(TourTexts.text(.welcome, wide: true, faces: false)).hasSuffix("in the menu on the left."))
        #expect(plain(TourTexts.text(.people, wide: false, faces: true)).hasPrefix("The button next to the search opens People"))
        #expect(plain(TourTexts.text(.search, wide: false, faces: true)).contains("someone's name"))
        #expect(!plain(TourTexts.text(.search, wide: false, faces: false)).contains("someone's name"))
        #expect(S.appTourProgress(step: 3, total: 13) == "Step 3 of 13")
        // Every step has a title and a text.
        for step in TourStep.allCases {
            #expect(!TourTexts.title(step).isEmpty)
            #expect(!plain(TourTexts.text(step, wide: false, faces: true)).isEmpty)
            #expect(!plain(TourTexts.text(step, wide: true, faces: true)).isEmpty)
        }
    }
}
