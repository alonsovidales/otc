// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import SwiftProtobuf
import Testing
@testable import OffTheCloud

/// Waits (`seconds` at most) for `done`.
@MainActor
private func waitUntil(_ seconds: Double = 3, _ done: () -> Bool) async {
    let end = Date() + seconds
    while !done() && Date() < end {
        try? await Task.sleep(nanoseconds: 10_000_000)
    }
}

private func refusal(_ code: String, _ message: String) -> Msg_RespEnvelope {
    var r = Msg_RespEnvelope()
    r.error = true
    r.errorCode = code
    r.errorMessage = message
    return r
}

private func ack(_ ok: Bool = true, code: String = "", message: String = "") -> Msg_RespEnvelope {
    var a = Msg_Ack()
    a.ok = ok
    a.code = code
    a.errorMsg = message
    var r = Msg_RespEnvelope()
    r.payload = .respAck(a)
    return r
}

/// A collection's Rating, its pure rules (Rating.swift; Android's
/// RatingRules, the same cases).
struct RatingRulesTests {
    @Test func showMapsToTheRatingFilter() {
        #expect(RatingShow.all.filter == nil)
        #expect(RatingShow.five.filter?.minStars == 5)
        #expect(RatingShow.four.filter?.minStars == 4)
        #expect(RatingShow.three.filter?.minStars == 3)
        #expect(RatingShow.two.filter?.minStars == 2)
        // Rated: one star or more.
        #expect(RatingShow.rated.filter?.minStars == 1)
        let unrated = RatingShow.unrated.filter
        #expect(unrated?.unrated == true && unrated?.minStars == 0 && unrated?.withComment == false)
        let commented = RatingShow.commented.filter
        #expect(commented?.withComment == true && commented?.minStars == 0 && commented?.unrated == false)
        // In the menu's order.
        #expect(RatingShow.allCases == [.all, .five, .four, .three, .two, .rated, .unrated, .commented])
    }

    @Test func eitherChoiceNotTheDefaultRanksTheGrid() {
        #expect(!RatingRules.ranked(on: true, sort: .date, show: .all))
        #expect(RatingRules.ranked(on: true, sort: .rating, show: .all))
        #expect(RatingRules.ranked(on: true, sort: .date, show: .unrated))
        // Rating off: nothing applies.
        #expect(!RatingRules.ranked(on: false, sort: .rating, show: .five))
    }

    @Test func whatAPageSends() {
        #expect(RatingRules.query(on: true, sort: .date, show: .all) == nil)
        #expect(RatingRules.query(on: false, sort: .rating, show: .four) == nil)
        let byRating = RatingRules.query(on: true, sort: .rating, show: .all)
        #expect(byRating?.byRating == true && byRating?.filter == nil)
        let four = RatingRules.query(on: true, sort: .date, show: .four)
        #expect(four?.byRating == false && four?.filter?.minStars == 4)
        var sp = Msg_SearchPhotos()
        RatingRules.query(on: true, sort: .rating, show: .commented)?.apply(to: &sp)
        #expect(sp.byRating && sp.hasRating && sp.rating.withComment)
        var plain = Msg_SearchPhotos()
        byRating?.apply(to: &plain)
        #expect(plain.byRating && !plain.hasRating)
    }

    @Test func tappingTheCurrentStarClearsIt() {
        #expect(RatingRules.tapped(3, current: 0) == 3)
        #expect(RatingRules.tapped(5, current: 3) == 5)
        #expect(RatingRules.tapped(1, current: 4) == 1)
        #expect(RatingRules.tapped(3, current: 3) == 0)
        #expect(RatingRules.tapped(0, current: 2) == 0)
        // Kept within 0...5.
        #expect(RatingRules.tapped(9, current: 0) == 5)
        #expect(RatingRules.tapped(9, current: 5) == 0)
    }

    @Test func aCommentIsTrimmedAndKeptWithinTheLimit() {
        #expect(RatingRules.tidy("  Brighter, please \n") == "Brighter, please")
        #expect(RatingRules.tidy(" \n ") == "")
        let long = String(repeating: "a", count: 2001)
        #expect(RatingRules.clip(long).count == 2000)
        #expect(RatingRules.clip(String(repeating: "a", count: 2000)).count == 2000)
        // Counted as the device counts (Unicode scalars), never cutting a
        // character in two: a two-scalar emoji past the limit goes whole.
        let emoji = String(repeating: "a", count: 1999) + "👍🏽"
        #expect(emoji.unicodeScalars.count == 2001)
        #expect(RatingRules.clip(emoji) == String(repeating: "a", count: 1999))
        #expect(RatingRules.clip("ok 👍🏽") == "ok 👍🏽")
    }

    @Test func saveIsOnlyForAChangedComment() {
        #expect(!RatingRules.changed(draft: "nice", saved: "nice"))
        #expect(!RatingRules.changed(draft: "  nice \n", saved: "nice"))
        #expect(RatingRules.changed(draft: "nicer", saved: "nice"))
        #expect(RatingRules.changed(draft: "", saved: "nice"))
        #expect(!RatingRules.changed(draft: "  ", saved: ""))
    }

    @Test func aMarkFromTheDevice() {
        var r = Msg_PhotoRating()
        r.stars = 4
        r.comment = "Crop it"
        r.fromLink = true
        #expect(RatingMark(r) == RatingMark(stars: 4, comment: "Crop it", fromLink: true))
        r.stars = 9
        #expect(RatingMark(r).stars == 5)
        r.stars = -1
        #expect(RatingMark(r).stars == 0)
        #expect(RatingMark.none.isEmpty)
        #expect(!RatingMark(stars: 0, comment: "x").isEmpty)
        #expect(RatingMark(stars: 0, comment: "x").hasComment)
    }

    @Test func theRequestCarriesBothStarsAndComment() {
        let r = RatingRules.request(group: "g1", path: "/p/0.jpg", RatingMark(stars: 2, comment: "hm"))
        #expect(r.groupID == "g1" && r.path == "/p/0.jpg" && r.stars == 2 && r.comment == "hm")
    }

    @Test func howSetPhotoRatingWasAnswered() {
        var stored = Msg_PhotoRating()
        stored.stars = 3
        stored.comment = "ok"
        var ok = Msg_RespEnvelope()
        ok.payload = .respPhotoRating(stored)
        #expect(RatingRules.answer(ok) == .saved(RatingMark(stars: 3, comment: "ok")))
        #expect(RatingRules.answer(nil) == .refused(nil))
        #expect(RatingRules.answer(refusal("unknown_payload", "")) == .needsUpdate)
        #expect(RatingRules.answer(refusal("", "unknown payload")) == .needsUpdate)
        #expect(RatingRules.answer(refusal("rating_off", "Rating is off for this collection.")) == .off("Rating is off for this collection."))
        #expect(RatingRules.answer(refusal("rating_off", "")) == .off(nil))
        #expect(RatingRules.answer(refusal("not_in_collection", "The photo isn't in this collection any more.")) == .refused("The photo isn't in this collection any more."))
        // The bridge answering for a device it can't reach has no words of
        // the device's.
        #expect(RatingRules.answer(ack(false, code: LoadProblem.deviceUnreachable, message: "device unreachable")) == .refused(nil))
    }

    @Test func howSetImageGroupRatingWasAnswered() {
        #expect(RatingRules.toggled(ack()) == .done)
        #expect(RatingRules.toggled(nil) == .refused(nil))
        #expect(RatingRules.toggled(refusal("unknown_payload", "")) == .needsUpdate)
        #expect(RatingRules.toggled(ack(false, message: "database is down")) == .refused("database is down"))
        #expect(RatingRules.toggled(ack(false, code: LoadProblem.deviceUnreachable)) == .refused(nil))
        #expect(RatingRules.toggled(refusal("", "  ")) == .refused(nil))
    }

    @Test func everyTextReadsAsWritten() {
        #expect(S.appPhotosRatingTitle == "Rating")
        #expect(S.appPhotosRatingOn == "Rating on")
        #expect(S.appPhotosRatingOff == "Rating off")
        #expect(S.appPhotosRatingTurnedOn == "Rating on: rate photos with stars and comments")
        #expect(RatingSort.allCases.map(\.title) == ["Date", "Rating"])
        #expect(RatingShow.allCases.map(\.title) == [
            "All photos", "5 stars", "4 stars or more", "3 stars or more", "2 stars or more",
            "Rated", "Not rated", "With a comment",
        ])
        #expect(S.appPhotosRatingShow == "Show")
        #expect(S.appPhotosRatingNoMatch == "No photos match this rating")
        #expect(S.appPhotosRatingCommentPlaceholder == "Add a comment")
        #expect(S.appPhotosRatingFromLink == "From the shared link")
        #expect(RatingRules.starsText(1) == "1 star")
        #expect(RatingRules.starsText(4) == "4 stars")
        #expect(RatingRules.starsText(0) == "Not rated")
        #expect(S.appFilesGalleryRating == "Let people rate the photos")
        #expect(S.appFilesGalleryRatingNote == "Anyone with the link can give stars and leave comments. You see them in this collection.")
        #expect(S.appSettingsLinksRating == "Rating on")
        #expect(S.appPhotosRatingNeedsUpdate == "Your device needs an update to rate photos.")
    }
}

/// A Rating alert (NotificationRating): its tap is a deep link to the
/// collection it names; one that can't be opened says why in Alerts.
@MainActor
struct RatingAlertTests {
    @Test func aTapOpensTheCollectionItNames() {
        let model = NotificationsModel()
        var n = Msg_Notification()
        n.type = .notificationRating
        n.title = "Ana rated 3 photos in Wedding"
        n.groupID = "g1"
        model.handleTap(n)
        #expect(model.pendingDeepLink == .collection(id: "g1"))
        // Without a collection: nothing to open.
        model.pendingDeepLink = nil
        n.groupID = ""
        model.handleTap(n)
        #expect(model.pendingDeepLink == nil)
    }

    @Test func aCollectionThatCantBeOpenedIsSaidInAlerts() {
        let model = NotificationsModel()
        model.say(.ratingGone)
        #expect(model.notice == .ratingGone)
        #expect(AlertsNotice.ratingGone.text == "This collection no longer exists.")
        #expect(AlertsNotice.ratingOpenFailed.text == "Couldn't open the collection. Try again.")
    }
}

/// The device, for a rated collection: its photos with their ratings, and
/// what it was asked.
@MainActor
final class RatingDevice {
    struct Photo {
        var path: String
        var hash: String
        var mark: RatingMark
    }

    var photos = [
        Photo(path: "/p/0.jpg", hash: "h0", mark: RatingMark(stars: 4, comment: "nice")),
        Photo(path: "/p/1.jpg", hash: "h1", mark: .none),
        Photo(path: "/p/2.jpg", hash: "h2", mark: RatingMark(stars: 0, comment: "Crop it", fromLink: true)),
    ]
    /// The collection's Rating as the device holds it.
    var rating = true
    var searches: [Msg_SearchPhotos] = []
    var bucketAsks = 0
    var toggles: [Msg_SetImageGroupRating] = []
    var rates: [Msg_SetPhotoRating] = []
    var groupLists = 0
    /// How SetImageGroupRating / SetPhotoRating are refused: nil is answered.
    var toggleRefusal: (code: String, message: String)?
    var rateRefusal: (code: String, message: String)?

    var group: Msg_ImageGroup {
        var g = Msg_ImageGroup()
        g.id = "g1"
        g.name = "Wedding"
        g.fileCount = Int32(photos.count)
        g.rating = rating
        return g
    }

    func attach(_ vm: PhotoGalleryVM) {
        vm.pageRetry.delay = { _ in 0.05 }
        vm.bucketRetry.delay = { _ in 0.05 }
        vm.bigThumbDelay = { _ in 0.05 }
        vm.problemOf = { LoadProblem.of(resp: $0, error: $1, offline: false, statusCode: nil) }
        vm.ratingDefaults = UserDefaults(suiteName: "rating-\(UUID().uuidString)")!
        let hearts = FavouritesModel()
        hearts.request = { _ in throw CancellationError() }
        vm.favourites = hearts
        vm.photoRequest = { [weak self] payload, _ in
            guard let self else { throw CancellationError() }
            var r = Msg_RespEnvelope()
            switch payload {
            case .reqSearchPhotos(let sp):
                self.searches.append(sp)
                var lof = Msg_ListOfFiles()
                lof.files = self.photos.enumerated().map { i, p in
                    var f = Msg_File()
                    f.path = p.path
                    f.hash = p.hash
                    f.mime = "image/jpeg"
                    f.created = Google_Protobuf_Timestamp(date: Date(timeIntervalSince1970: 1_780_000_000 - Double(i) * 3600))
                    if self.rating, !p.mark.isEmpty {
                        var pr = Msg_PhotoRating()
                        pr.stars = Int32(p.mark.stars)
                        pr.comment = p.mark.comment
                        pr.fromLink = p.mark.fromLink
                        f.rating = pr
                    }
                    return f
                }
                r.payload = .respListOfFiles(lof)
            case .reqPhotoDateBuckets:
                self.bucketAsks += 1
                r.payload = .respPhotoDateBuckets(Msg_RespPhotoDateBuckets())
            case .reqSetPhotoRating(let s):
                self.rates.append(s)
                if let no = self.rateRefusal { return refusal(no.code, no.message) }
                guard let i = self.photos.firstIndex(where: { $0.path == s.path }) else { return refusal("not_in_collection", "") }
                self.photos[i].mark = RatingMark(stars: Int(s.stars), comment: s.comment.trimmingCharacters(in: .whitespacesAndNewlines))
                var pr = Msg_PhotoRating()
                pr.stars = s.stars
                pr.comment = self.photos[i].mark.comment
                r.payload = .respPhotoRating(pr)
            default:
                throw CancellationError()
            }
            return r
        }
        vm.libraryRequest = { [weak self] payload, _ in
            guard let self else { throw CancellationError() }
            switch payload {
            case .reqSetImageGroupRating(let s):
                self.toggles.append(s)
                if let no = self.toggleRefusal { return refusal(no.code, no.message) }
                self.rating = s.on
                return ack()
            case .reqListImageGroups:
                self.groupLists += 1
                var gs = Msg_ImageGroups()
                gs.groups = [self.group]
                var r = Msg_RespEnvelope()
                r.payload = .respImageGroups(gs)
                return r
            case .reqGetInstanceRole:
                var role = Msg_RespInstanceRole()
                role.isPrimary = true
                var r = Msg_RespEnvelope()
                r.payload = .respInstanceRole(role)
                return r
            default:
                throw CancellationError()
            }
        }
        vm.peopleRequest = { _, _ in throw CancellationError() }
    }
}

extension ImagesNotificationTests {
@MainActor
@Suite(.serialized)
struct RatingCollectionTests {
    private func model(rating: Bool = true) async -> (PhotoGalleryVM, RatingDevice) {
        let vm = PhotoGalleryVM(deviceID: "test", localPhotosFolder: nil, thumbCache: testThumbCache())
        let device = RatingDevice()
        device.rating = rating
        device.attach(vm)
        vm.openGroup(device.group)
        await waitUntil { vm.items.count == 3 && !vm.loading }
        return (vm, device)
    }

    @Test func theTilesCarryTheirRatings() async {
        let (vm, device) = await model()
        #expect(vm.ratingOn)
        #expect(vm.items.map(\.rating) == [RatingMark(stars: 4, comment: "nice"), .none, RatingMark(stars: 0, comment: "Crop it", fromLink: true)])
        // Date and All photos: nothing more is asked, in date order.
        #expect(device.searches.last?.hasRating == false && device.searches.last?.byRating == false)
        #expect(!vm.ranked)
        #expect(device.bucketAsks > 0)
    }

    @Test func showAndSortAskTheDeviceAndRankTheGrid() async {
        let (vm, device) = await model()
        let asked = device.searches.count
        vm.setRatingShow(.four)
        await waitUntil { device.searches.count > asked && !vm.loading }
        #expect(device.searches.last?.rating.minStars == 4)
        #expect(device.searches.last?.byRating == false)
        // Ranked as a tag search: no months, no scrubber.
        #expect(vm.ranked)
        #expect(!vm.showScrubber)
        let again = device.searches.count
        vm.setRatingSort(.rating)
        await waitUntil { device.searches.count > again && !vm.loading }
        #expect(device.searches.last?.byRating == true)
        #expect(device.searches.last?.rating.minStars == 4)
        // The same choice again asks nothing.
        let same = device.searches.count
        vm.setRatingSort(.rating)
        try? await Task.sleep(nanoseconds: 100_000_000)
        #expect(device.searches.count == same)
    }

    @Test func noMatchOffersAllPhotosAgain() async {
        let (vm, device) = await model()
        device.photos = []
        vm.setRatingShow(.five)
        await waitUntil { vm.gridBody == .empty }
        #expect(vm.emptyKind == .ratingNoMatch)
        vm.setRatingShow(.all)
        #expect(vm.ratingShow == .all)
        await waitUntil { !vm.loading }
        #expect(vm.emptyKind == .collection)
    }

    @Test func withoutRatingSortAndShowDoNothing() async {
        let (vm, device) = await model(rating: false)
        #expect(!vm.ratingOn)
        #expect(vm.items.allSatisfy { $0.rating == .none })
        let asked = device.searches.count
        vm.setRatingShow(.five)
        vm.setRatingSort(.rating)
        try? await Task.sleep(nanoseconds: 100_000_000)
        #expect(device.searches.count == asked)
        #expect(vm.ratingShow == .all && vm.ratingSort == .date)
    }

    @Test func anotherCollectionStartsFromDateAndAllPhotos() async {
        let (vm, device) = await model()
        vm.setRatingShow(.rated)
        vm.setRatingSort(.rating)
        var other = device.group
        other.id = "g2"
        vm.openGroup(other)
        #expect(vm.ratingShow == .all && vm.ratingSort == .date)
        vm.setRatingShow(.rated)
        vm.leaveGroup()
        #expect(vm.ratingShow == .all)
    }

    @Test func theStarTurnsRatingOnAndSaysWhatItDoesOnce() async {
        let (vm, device) = await model(rating: false)
        let asked = device.searches.count
        await vm.toggleRating()
        #expect(device.toggles.map(\.id) == ["g1"])
        #expect(device.toggles.map(\.on) == [true])
        #expect(vm.activeGroup?.rating == true)
        #expect(vm.ratingToast)
        // Its photos again, now with their ratings.
        await waitUntil { device.searches.count > asked && vm.items.first?.rating.stars == 4 }
        #expect(vm.items.first?.rating == RatingMark(stars: 4, comment: "nice"))
        // Off: the ratings hidden; Sort by and Show back to the defaults.
        vm.setRatingShow(.five)
        await vm.toggleRating()
        #expect(device.toggles.map(\.on) == [true, false])
        #expect(vm.activeGroup?.rating == false)
        #expect(vm.ratingShow == .all)
        await waitUntil { vm.items.count == 3 && vm.items.allSatisfy { $0.rating == .none } }
        // On again: no note the second time.
        vm.ratingToast = false
        await vm.toggleRating()
        #expect(vm.activeGroup?.rating == true)
        #expect(!vm.ratingToast)
    }

    @Test func anOlderDeviceNeedsAnUpdateToRate() async throws {
        let (vm, device) = await model(rating: false)
        device.toggleRefusal = ("unknown_payload", "this device's software is older than the app")
        await vm.toggleRating()
        #expect(vm.activeGroup?.rating == false)
        #expect(!vm.showAlert)
        let ask = try #require(vm.needsUpdate)
        #expect(ask.title == S.appPhotosRatingNeedsUpdate)
        #expect(ask.canUpdate)
    }

    @Test func aRefusedStarButtonSaysTheDevicesWords() async {
        let (vm, device) = await model(rating: false)
        device.toggleRefusal = ("", "database is down")
        await vm.toggleRating()
        #expect(vm.activeGroup?.rating == false)
        #expect(vm.notice?.text == "database is down")
        device.toggleRefusal = ("", "")
        await vm.toggleRating()
        #expect(vm.notice?.text == S.appPhotosRatingToggleFailed)
    }

    @Test func onlyARatedCollectionsViewerRates() async {
        let (vm, device) = await model(rating: false)
        vm.open(index: 0)
        #expect(!vm.viewerRates)
        vm.tapStar(2)
        try? await Task.sleep(nanoseconds: 100_000_000)
        #expect(device.rates.isEmpty)
        #expect(vm.openRating == .none)
        vm.closeModal()
    }

    @Test func starsSaveAtOnceWithTheCommentInTheBox() async {
        let (vm, device) = await model()
        vm.open(index: 0)
        #expect(vm.viewerRates)
        #expect(vm.ratingDraft.text == "nice")
        // Typed but not saved: the stars go with the box's comment (the web's
        // and Android's rule), which is then saved - Save goes grey.
        vm.ratingDraft.text = "  nicer \n"
        #expect(RatingRules.changed(draft: vm.ratingDraft.text, saved: vm.openRating.comment))
        vm.tapStar(2)
        #expect(vm.openRating == RatingMark(stars: 2, comment: "nicer"))
        #expect(vm.ratingDraft.text == "nicer")
        #expect(!RatingRules.changed(draft: vm.ratingDraft.text, saved: vm.openRating.comment))
        await waitUntil { device.rates.count == 1 }
        #expect(device.rates.first?.groupID == "g1")
        #expect(device.rates.first?.path == "/p/0.jpg")
        #expect(device.rates.first?.stars == 2)
        #expect(device.rates.first?.comment == "nicer")
        // The current star again: none, the comment kept.
        vm.tapStar(2)
        #expect(vm.openRating.stars == 0)
        await waitUntil { device.rates.count == 2 }
        #expect(device.rates.last?.stars == 0)
        #expect(device.rates.last?.comment == "nicer")
        // Emptied, then a star: the comment goes with it.
        vm.ratingDraft.text = ""
        vm.tapStar(4)
        await waitUntil { device.rates.count == 3 }
        #expect(device.rates.last?.stars == 4)
        #expect(device.rates.last?.comment == "")
        // Closing sends nothing more: the box was saved.
        vm.closeModal()
        try? await Task.sleep(nanoseconds: 100_000_000)
        #expect(device.rates.count == 3)
    }

    @Test func aRefusedRatingGoesBackWithTheDevicesWords() async {
        let (vm, device) = await model()
        device.rateRefusal = ("not_in_collection", "The photo isn't in this collection any more.")
        vm.open(index: 0)
        vm.tapStar(1)
        #expect(vm.openRating.stars == 1)
        await waitUntil { device.rates.count == 1 && vm.openRating.stars == 4 }
        #expect(vm.openRating == RatingMark(stars: 4, comment: "nice"))
        #expect(vm.notice?.text == "The photo isn't in this collection any more.")
        #expect(vm.alertShown(overViewer: true))
        vm.closeModal()
    }

    @Test func theCommentSavesWithSaveAndWhenMovingOnOrClosing() async {
        let (vm, device) = await model()
        vm.open(index: 1)
        #expect(vm.ratingDraft.text == "")
        vm.ratingDraft.text = "  Brighter, please \n"
        vm.saveComment()
        // Shown at once, tidied.
        #expect(vm.openRating.comment == "Brighter, please")
        #expect(vm.ratingDraft.text == "Brighter, please")
        await waitUntil { device.rates.count == 1 }
        #expect(device.rates.last?.path == "/p/1.jpg")
        #expect(device.rates.last?.comment == "Brighter, please")
        #expect(device.rates.last?.stars == 0)
        // Typed, then on to the next photo: saved.
        vm.ratingDraft.text = "Warmer"
        vm.next()
        #expect(vm.ratingDraft.text == "Crop it")
        await waitUntil { device.rates.count == 2 }
        #expect(device.rates.last?.path == "/p/1.jpg")
        #expect(device.rates.last?.comment == "Warmer")
        // A visitor's comment: from the link, until it is changed here.
        #expect(vm.openRating.fromLink)
        // Typed, then closed: saved.
        vm.ratingDraft.text = "Crop it tighter"
        vm.closeModal()
        await waitUntil { device.rates.count == 3 }
        #expect(device.rates.last?.path == "/p/2.jpg")
        #expect(device.rates.last?.comment == "Crop it tighter")
        await waitUntil { vm.items[2].rating.comment == "Crop it tighter" }
        #expect(vm.items[2].rating.fromLink == false)
        // Nothing typed: nothing sent.
        vm.open(index: 0)
        vm.next()
        vm.closeModal()
        try? await Task.sleep(nanoseconds: 100_000_000)
        #expect(device.rates.count == 3)
    }

    @Test func aFilteredGridIsAskedAgainAsTheViewerCloses() async {
        let (vm, device) = await model()
        vm.setRatingShow(.rated)
        await waitUntil { device.searches.last?.hasRating == true && !vm.loading }
        vm.open(index: 0)
        vm.tapStar(4)
        await waitUntil { device.rates.count == 1 }
        let asked = device.searches.count
        vm.closeModal()
        await waitUntil { device.searches.count > asked }
        #expect(device.searches.count > asked)
        #expect(device.searches.last?.rating.minStars == 1)
    }

    @Test func ratingTurnedOffElsewhereIsSaidAndTheGridFollowsOnClose() async {
        let (vm, device) = await model()
        device.rateRefusal = ("rating_off", "Rating is off for this collection.")
        vm.open(index: 0)
        vm.tapStar(5)
        await waitUntil { vm.notice != nil }
        #expect(vm.notice?.text == "Rating is off for this collection.")
        #expect(vm.activeGroup?.rating == false)
        #expect(vm.openRating.stars == 4)
        let asked = device.searches.count
        device.rating = false
        vm.closeModal()
        await waitUntil { device.searches.count > asked }
        await waitUntil { !vm.loading && vm.items.count == 3 }
        #expect(vm.items.allSatisfy { $0.rating == .none })
    }

    /// A Rating alert's tap opens its collection in Images, as from
    /// Collections - the grid, not the map; one deleted since, or a list
    /// that didn't come, opens nothing.
    @Test func anAlertOpensItsCollection() async {
        let (vm, device) = await model()
        vm.leaveGroup()
        #expect(vm.activeGroup == nil)
        vm.showMap = true
        let lists = device.groupLists
        #expect(await vm.openGroup(id: "g1") == .opened)
        #expect(device.groupLists > lists)
        #expect(vm.activeGroup?.id == "g1")
        #expect(!vm.showMap)
        await waitUntil { vm.items.count == 3 && !vm.loading }
        #expect(vm.items.first?.rating.stars == 4)
        // Deleted since: nothing opens.
        #expect(await vm.openGroup(id: "gone") == .gone)
        #expect(vm.activeGroup?.id == "g1")
        // No list: the one here decides.
        vm.groupsRetry.delay = { _ in 60 }
        vm.libraryRequest = { _, _ in throw CancellationError() }
        #expect(await vm.openGroup(id: "other") == .failed)
        vm.leaveGroup()
        #expect(await vm.openGroup(id: "g1") == .opened)
        #expect(vm.activeGroup?.id == "g1")
        #expect(!vm.showAlert)
    }

    @Test func aSharedLinkWithRatingsListsTheCollectionsAgain() async {
        let (vm, device) = await model(rating: false)
        device.rating = true
        let lists = device.groupLists
        NotificationCenter.default.post(name: .otcCollectionRatingChanged, object: "g1")
        await waitUntil { vm.activeGroup?.rating == true }
        #expect(device.groupLists > lists)
        await waitUntil { vm.items.first?.rating.stars == 4 }
        #expect(vm.items.first?.rating.stars == 4)
    }
}
}
