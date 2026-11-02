// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  Rating.swift
//  OffTheCloud
//
//  A collection's Rating (the owner, 2026-11-02; devices from release
//  163): while it is on, each photo of the collection has 1 to 5 stars and
//  a comment, the same for the owner and for anyone rating through a
//  shared link made with it (PhotoRating.from_link). The Android app and
//  the web follow the same rules:
//    - The open collection's header has a star button that turns it on
//      and off (SetImageGroupRating; off keeps the ratings, hidden). The
//      first time it is turned on a one-line note says what it does.
//    - While it is on, the header also has Sort by (Date / Rating) and
//      Show (All photos, 5 stars, 4, 3, 2 stars or more, Rated, Not rated,
//      With a comment), both back to the defaults whenever another
//      collection opens. Either one not the default makes the grid ranked
//      (SearchPhotos.rating / by_rating): no month titles, no scrubber.
//    - Tiles show "★ 4" and a speech bubble for a comment (File.rating).
//    - The viewer, only inside such a collection (never Files', the map's
//      or a memory's), has a Rate button opening a panel: five stars
//      (tapping the current one clears them) that save at once with the
//      comment as the box has it, and a comment box saved with Save, with
//      a star, or by itself when the viewer moves to another photo or
//      closes. One change at a time is sent, in order; refused, the
//      photo's rating goes back to what the device holds and the device's
//      words are said.
//    - A device from before it answers unknown_payload: it needs an update
//      (DeviceNeedsUpdate).
//

import Combine
import SwiftUI

/// A photo's stars and comment in the open collection (File.rating, the
/// answer to SetPhotoRating); `none` for a photo with neither.
struct RatingMark: Hashable, Sendable {
    /// 1-5; 0: none.
    var stars: Int
    /// "" for none.
    var comment: String
    /// The last change came through the collection's shared link.
    var fromLink: Bool

    static let none = RatingMark()

    init(stars: Int = 0, comment: String = "", fromLink: Bool = false) {
        self.stars = RatingRules.stars(stars)
        self.comment = comment
        self.fromLink = fromLink
    }

    init(_ r: Msg_PhotoRating) {
        self.init(stars: Int(r.stars), comment: r.comment, fromLink: r.fromLink)
    }

    var isEmpty: Bool { stars == 0 && comment.isEmpty }
    var hasComment: Bool { !comment.isEmpty }
}

/// Sort by in a rated collection.
enum RatingSort: String, CaseIterable, Identifiable, Sendable {
    /// By the date taken, the newest first (the default).
    case date
    /// The most stars first, those without stars last.
    case rating

    var id: String { rawValue }

    var title: String {
        switch self {
        case .date: return S.appPhotosRatingSortDate
        case .rating: return S.appPhotosRatingSortRating
        }
    }
}

/// Show in a rated collection: which of its photos the grid keeps.
enum RatingShow: String, CaseIterable, Identifiable, Sendable {
    case all, five, four, three, two, rated, unrated, commented

    var id: String { rawValue }

    var title: String {
        switch self {
        case .all: return S.appPhotosRatingShowAll
        case .five: return S.appPhotosRatingShowFive
        case .four: return S.appPhotosRatingShowFour
        case .three: return S.appPhotosRatingShowThree
        case .two: return S.appPhotosRatingShowTwo
        case .rated: return S.appPhotosRatingShowRated
        case .unrated: return S.appPhotosRatingShowUnrated
        case .commented: return S.appPhotosRatingShowCommented
        }
    }

    /// SearchPhotos.rating for it; nil for every photo (left unset).
    var filter: Msg_RatingFilter? {
        var f = Msg_RatingFilter()
        switch self {
        case .all: return nil
        case .five: f.minStars = 5
        case .four: f.minStars = 4
        case .three: f.minStars = 3
        case .two: f.minStars = 2
        case .rated: f.minStars = 1
        case .unrated: f.unrated = true
        case .commented: f.withComment = true
        }
        return f
    }
}

/// What a page of a rated collection asks for besides the other filters.
struct RatingQuery: Equatable {
    var filter: Msg_RatingFilter?
    var byRating: Bool

    func apply(to sp: inout Msg_SearchPhotos) {
        if let filter { sp.rating = filter }
        sp.byRating = byRating
    }
}

/// How a SetPhotoRating was answered.
enum RatingAnswer: Equatable {
    /// Stored as this.
    case saved(RatingMark)
    /// A device from before Rating (unknown_payload).
    case needsUpdate
    /// The collection's Rating is off (turned off elsewhere): the device's
    /// words, if any.
    case off(String?)
    /// Refused (the photo left the collection...) or no answer: the
    /// device's words, if any.
    case refused(String?)
}

/// How a SetImageGroupRating was answered.
enum RatingToggleAnswer: Equatable {
    case done
    case needsUpdate
    case refused(String?)
}

/// Rating's rules, apart from any screen (tests read them as Android's
/// RatingRules).
enum RatingRules {
    /// The most characters a comment keeps (the device counts runes).
    static let maxComment = 2000
    /// Whether the note about Rating was shown (UserDefaults, as
    /// files.viewMode: Log Out forgets it with the app's defaults).
    static let introducedKey = "rating.introduced"

    /// Stars kept within 0...5.
    static func stars(_ n: Int) -> Int { max(0, min(5, n)) }

    /// The stars after a tap on star `n` (1-5) of a photo with `current`:
    /// that many, or none when it is the current one.
    static func tapped(_ n: Int, current: Int) -> Int {
        let n = stars(n)
        return n == stars(current) ? 0 : n
    }

    /// A comment as it is kept: at most maxComment characters as the device
    /// counts them (Unicode scalars), never cutting a character in two.
    static func clip(_ text: String) -> String {
        guard text.unicodeScalars.count > maxComment else { return text }
        var kept = 0
        var end = text.startIndex
        for c in text.indices {
            let n = text[c].unicodeScalars.count
            if kept + n > maxComment { break }
            kept += n
            end = text.index(after: c)
        }
        return String(text[..<end])
    }

    /// A comment as it is sent: without the spaces and lines at its ends,
    /// within the limit.
    static func tidy(_ text: String) -> String {
        clip(text.trimmingCharacters(in: .whitespacesAndNewlines))
    }

    /// The comment box holds something else than the comment stored: Save
    /// is enabled.
    static func changed(draft: String, saved: String) -> Bool {
        tidy(draft) != saved
    }

    /// Sort by or Show not the default in a rated collection: the grid is
    /// ranked (no month titles, no scrubber).
    static func ranked(on: Bool, sort: RatingSort, show: RatingShow) -> Bool {
        on && (sort != .date || show != .all)
    }

    /// What a page sends for them; nil when it sends nothing more (Rating
    /// off, or both the defaults).
    static func query(on: Bool, sort: RatingSort, show: RatingShow) -> RatingQuery? {
        guard ranked(on: on, sort: sort, show: show) else { return nil }
        return RatingQuery(filter: show.filter, byRating: sort == .rating)
    }

    /// A photo's stars in words, for a screen reader: "4 stars", or "Not
    /// rated".
    static func starsText(_ n: Int) -> String {
        n > 0 ? S.appPhotosRatingStars(count: stars(n)) : S.appPhotosRatingShowUnrated
    }

    /// The request that sets a photo's rating.
    static func request(group: String, path: String, _ mark: RatingMark) -> Msg_SetPhotoRating {
        var r = Msg_SetPhotoRating()
        r.groupID = group
        r.path = path
        r.stars = Int32(mark.stars)
        r.comment = mark.comment
        return r
    }

    static func answer(_ resp: Msg_RespEnvelope?) -> RatingAnswer {
        guard let resp else { return .refused(nil) }
        if !resp.error, case .respPhotoRating(let r) = resp.payload { return .saved(RatingMark(r)) }
        if ErrorCodes.isUnknownPayload(code: resp.errorCode, message: resp.errorMessage) { return .needsUpdate }
        if resp.errorCode == "rating_off" { return .off(words(resp.errorMessage)) }
        if case .respAck(let a) = resp.payload {
            // The bridge's own "device unreachable" isn't the device's answer.
            if a.code == LoadProblem.deviceUnreachable { return .refused(nil) }
            return .refused(words(a.errorMsg))
        }
        return .refused(words(resp.errorMessage))
    }

    static func toggled(_ resp: Msg_RespEnvelope?) -> RatingToggleAnswer {
        guard let resp else { return .refused(nil) }
        if resp.error {
            if ErrorCodes.isUnknownPayload(code: resp.errorCode, message: resp.errorMessage) { return .needsUpdate }
            return .refused(words(resp.errorMessage))
        }
        guard case .respAck(let a) = resp.payload else { return .refused(nil) }
        if a.ok { return .done }
        if a.code == LoadProblem.deviceUnreachable { return .refused(nil) }
        return .refused(words(a.errorMsg))
    }

    private static func words(_ s: String) -> String? {
        let t = s.trimmingCharacters(in: .whitespacesAndNewlines)
        return t.isEmpty ? nil : t
    }
}

/// The comment box's text in the viewer, apart from the view model: a
/// key typed redraws the panel, not Images' whole grid.
@MainActor
final class RatingDraft: ObservableObject {
    @Published var text = ""
    /// The photo it is about; nil while the viewer rates nothing.
    var path: String?
}

extension Notification.Name {
    /// A collection's Rating may have changed elsewhere in the app (a
    /// shared link made with "Let people rate the photos" turns it on):
    /// Images lists the collections again.
    static let otcCollectionRatingChanged = Notification.Name("otcCollectionRatingChanged")
}

/// The stars' colours: the menu's gold as it is drawn on dark (tiles, the
/// viewer), and as on the page (the collection's chip).
enum RatingStyle {
    static let star = Color(red: 1, green: 0.784, blue: 0.341)
    static let starOnPage = MenuStyle.gold
}

// MARK: - Images' side (PhotoGalleryVM)

extension PhotoGalleryVM {
    /// The open collection's Rating is on: its photos carry their stars and
    /// comments, and Sort by and Show apply.
    var ratingOn: Bool { activeGroup?.rating == true }

    /// Sorted by Rating or showing some photos only: ranked, as a tag
    /// search is.
    var ratingRanked: Bool { RatingRules.ranked(on: ratingOn, sort: ratingSort, show: ratingShow) }

    /// What the next page asks for besides the other filters.
    var ratingQuery: RatingQuery? { RatingRules.query(on: ratingOn, sort: ratingSort, show: ratingShow) }

    /// The star in the open collection's header: Rating on or off. Done,
    /// its photos again (the listing carries the ratings only while it is
    /// on), from Date and All photos; turned on the first time, the note
    /// that says what it does. Refused: the device's words, else
    /// toggle_failed; a device from before it needs an update.
    func toggleRating() async {
        guard let g = activeGroup, !ratingBusy else { return }
        let on = !g.rating
        ratingBusy = true
        defer { ratingBusy = false }
        var s = Msg_SetImageGroupRating()
        s.id = g.id
        s.on = on
        let resp = try? await libraryRequest(.reqSetImageGroupRating(s), nil)
        switch RatingRules.toggled(resp) {
        case .done:
            setGroupRating(g.id, on: on)
            if on { introduceRating() }
        case .needsUpdate:
            let send = libraryRequest
            needsUpdate = await DeviceNeedsUpdate.asking(title: S.appPhotosRatingNeedsUpdate) { try await send($0, nil) }
        case .refused(let words):
            say(.ratingToggleFailed(words))
        }
    }

    /// The collection `id`'s Rating is now `on`: in the list, and the open
    /// one's grid again - once the viewer has closed if it is open.
    func setGroupRating(_ id: String, on: Bool) {
        if let i = groups.firstIndex(where: { $0.id == id }), groups[i].rating != on { groups[i].rating = on }
        guard var g = activeGroup, g.id == id, g.rating != on else { return }
        g.rating = on
        activeGroup = g
        ratingTurned()
    }

    /// The open collection's Rating went on or off (here, or as its list
    /// came again): Date and All photos, and its photos again.
    func ratingTurned() {
        resetRatingView()
        if openIndex == nil {
            restartSearch()
        } else {
            ratingReloadOnClose = true
        }
    }

    /// Sort by and Show as a collection opens: Date and All photos.
    func resetRatingView() {
        if ratingSort != .date { ratingSort = .date }
        if ratingShow != .all { ratingShow = .all }
    }

    func setRatingSort(_ sort: RatingSort) {
        guard ratingOn, sort != ratingSort else { return }
        ratingSort = sort
        restartSearch()
    }

    func setRatingShow(_ show: RatingShow) {
        guard ratingOn, show != ratingShow else { return }
        ratingShow = show
        restartSearch()
    }

    /// The first time Rating is turned on: a one-line note, a few seconds.
    private func introduceRating() {
        guard !ratingDefaults.bool(forKey: RatingRules.introducedKey) else { return }
        ratingDefaults.set(true, forKey: RatingRules.introducedKey)
        ratingToast = true
        Announce.polite(S.appPhotosRatingTurnedOn)
        ratingToastTask?.cancel()
        ratingToastTask = Task { [weak self] in
            try? await Task.sleep(nanoseconds: 4_000_000_000)
            guard !Task.isCancelled else { return }
            self?.ratingToast = false
        }
    }

    /// A shared link made with ratings turned a collection's Rating on (or
    /// may have): the collections again, and the open one's grid with them
    /// if it changed (loadGroups).
    func ratingChangedElsewhere() {
        guard !fixedList else { return }
        Task { await loadGroups() }
    }

    // MARK: The viewer

    /// The viewer rates the open photo: inside a collection whose Rating is
    /// on - never Files', the map's or a memory's viewer - a photo the
    /// device holds.
    var viewerRates: Bool {
        guard !fixedList, ratingOn, let i = openIndex, items.indices.contains(i) else { return false }
        return !items[i].isLocalOnly && !items[i].path.isEmpty
    }

    /// The open photo's stars and comment as stored.
    var openRating: RatingMark {
        guard let i = openIndex, items.indices.contains(i) else { return .none }
        return items[i].rating
    }

    /// Star `n` of the open photo tapped: that many stars, or none when it
    /// is the current one - shown at once, saved with the comment as the
    /// box has it (as the web and Android do), which is then saved too.
    func tapStar(_ n: Int) {
        guard viewerRates, let i = openIndex else { return }
        let path = items[i].path
        var mark = items[i].rating
        mark.stars = RatingRules.tapped(n, current: mark.stars)
        mark.fromLink = false
        if ratingDraft.path == path {
            mark.comment = RatingRules.tidy(ratingDraft.text)
            ratingDraft.text = mark.comment
        }
        rate(path, mark)
    }

    /// Save: the open photo's comment as the box has it.
    func saveComment() {
        guard let path = ratingDraft.path else { return }
        saveDraft(path)
    }

    /// The viewer shows another photo (or the first): the comment typed for
    /// the one before is saved, and the box holds this one's.
    func ratingOpened() {
        let path = viewerRates ? openPath : nil
        guard path != ratingDraft.path else { return }
        leaveDraft()
        if let path, let i = openIndex {
            ratingDraft.path = path
            ratingDraft.text = items[i].rating.comment
        }
    }

    /// The viewer closed: the comment typed is saved; sorted or filtered by
    /// rating, the grid is asked again once what was rated is saved (a
    /// change may have moved photos or taken them out).
    func ratingViewerClosed() {
        leaveDraft()
        let reload = ratingReloadOnClose || (ratingChangedInViewer && ratingRanked)
        ratingChangedInViewer = false
        ratingReloadOnClose = false
        guard reload, !fixedList else { return }
        let saving = ratingQueue
        Task { [weak self] in
            await saving?.value
            self?.restartSearch()
        }
    }

    /// The box's photo is left: its text saved if it changed, the box
    /// emptied.
    private func leaveDraft() {
        if let path = ratingDraft.path { saveDraft(path) }
        ratingDraft.path = nil
        ratingDraft.text = ""
    }

    private func saveDraft(_ path: String) {
        guard ratingOn, let it = items.first(where: { $0.path == path }) else { return }
        let text = RatingRules.tidy(ratingDraft.text)
        guard text != it.rating.comment else { return }
        if ratingDraft.path == path { ratingDraft.text = text }
        var mark = it.rating
        mark.comment = text
        mark.fromLink = false
        rate(path, mark)
    }

    /// `path`'s rating is `mark`: shown at once, sent after any change on
    /// its way (one at a time, in order).
    private func rate(_ path: String, _ mark: RatingMark) {
        guard let g = activeGroup, g.rating else { return }
        if ratingHeld[path] == nil, let was = items.first(where: { $0.path == path })?.rating { ratingHeld[path] = was }
        showRating(mark, at: path)
        ratingChangedInViewer = true
        let before = ratingQueue
        ratingQueue = Task { [weak self] in
            await before?.value
            await self?.sendRating(path, mark, group: g.id)
        }
    }

    private func sendRating(_ path: String, _ mark: RatingMark, group: String) async {
        let resp = try? await photoRequest(.reqSetPhotoRating(RatingRules.request(group: group, path: path, mark)), nil)
        guard activeGroup?.id == group else { return }
        switch RatingRules.answer(resp) {
        case .saved(let stored):
            ratingHeld[path] = stored
            // Only what this sent: a change made since goes after it.
            if let now = items.first(where: { $0.path == path })?.rating, now.stars == mark.stars, now.comment == mark.comment {
                showRating(stored, at: path)
            }
        case .needsUpdate:
            ratingRefused(path)
            let send = libraryRequest
            needsUpdate = await DeviceNeedsUpdate.asking(title: S.appPhotosRatingNeedsUpdate) { try await send($0, nil) }
        case .off(let words):
            ratingRefused(path)
            say(.ratingFailed(words))
            // Turned off elsewhere meanwhile.
            setGroupRating(group, on: false)
        case .refused(let words):
            ratingRefused(path)
            say(.ratingFailed(words))
        }
    }

    /// A change the device didn't take: the photo shows what it holds.
    private func ratingRefused(_ path: String) {
        guard let held = ratingHeld[path] else { return }
        showRating(held, at: path)
    }

    /// Every tile of `path` shows `mark`.
    private func showRating(_ mark: RatingMark, at path: String) {
        var next = items
        var changed = false
        for i in next.indices where next[i].path == path && next[i].rating != mark {
            next[i].rating = mark
            changed = true
        }
        if changed { items = next }
    }
}

// MARK: - Views

/// The open collection's Sort by and Show, under its chip while its Rating
/// is on. Show other than All photos is marked, so a filtered grid reads as
/// one.
struct RatingControls: View {
    @ObservedObject var vm: PhotoGalleryVM

    var body: some View {
        HStack(spacing: 6) {
            SortByMenu(
                choices: RatingSort.allCases,
                selection: Binding(get: { vm.ratingSort }, set: { vm.setRatingSort($0) }),
                title: \.title
            )
            RatingShowMenu(selection: Binding(get: { vm.ratingShow }, set: { vm.setRatingShow($0) }))
            Spacer(minLength: 0)
        }
        .padding(.horizontal, 4)
    }
}

/// "Show": a menu of RatingShow, the one in use ticked; its label the
/// choice's name, in ember on a tinted capsule while it isn't All photos.
struct RatingShowMenu: View {
    @Binding var selection: RatingShow

    var body: some View {
        let active = selection != .all
        Menu {
            Section(S.appPhotosRatingShow) {
                ForEach(RatingShow.allCases) { c in
                    Toggle(c.title, isOn: Binding(get: { selection == c }, set: { if $0 { selection = c } }))
                }
            }
        } label: {
            HStack(spacing: 6) {
                Image(systemName: active ? "line.3.horizontal.decrease.circle.fill" : "line.3.horizontal.decrease.circle")
                    .font(.footnote.weight(.semibold))
                Text(selection.title)
                    .lineLimit(1)
                Image(systemName: "chevron.down")
                    .font(.caption2.weight(.semibold))
                    .foregroundStyle(active ? MenuStyle.ember : .secondary)
            }
            .font(.subheadline.weight(.medium))
            // As Sort by's beside it; the choice's capsule says it filters.
            .foregroundStyle(active ? MenuStyle.ember : Color.accentColor)
            .padding(.horizontal, 12)
            .padding(.vertical, 6)
            .background(active ? MenuStyle.ember.opacity(0.15) : .clear, in: Capsule())
            .frame(minHeight: 44)
            .contentShape(Rectangle())
        }
        .accessibilityLabel(S.appPhotosRatingShow)
        .accessibilityValue(selection.title)
    }
}

/// A tile's rating in a rated collection: "★ 4" and a speech bubble for a
/// comment; nothing for a photo with neither.
struct RatingBadge: View {
    let mark: RatingMark

    var body: some View {
        if !mark.isEmpty {
            HStack(spacing: 3) {
                if mark.stars > 0 {
                    Image(systemName: "star.fill").foregroundStyle(RatingStyle.star)
                    Text(verbatim: "\(mark.stars)")
                }
                if mark.hasComment {
                    Image(systemName: "text.bubble.fill")
                }
            }
            .font(.system(size: 11, weight: .semibold))
            .foregroundStyle(.white)
            .padding(.horizontal, 6)
            .padding(.vertical, 3)
            .background(.black.opacity(0.55), in: Capsule())
            // The tile says it (its accessibility value and content).
            .accessibilityHidden(true)
        }
    }
}

/// The viewer's Rate button: the photo's stars (an outlined star for none,
/// a speech bubble with a comment); opens and closes the rating panel.
struct RateButton: View {
    @ObservedObject var vm: PhotoGalleryVM
    /// The target's side in landscape; nil in portrait's row.
    let side: CGFloat?

    var body: some View {
        let mark = vm.openRating
        Button {
            vm.ratingPanelOpen.toggle()
        } label: {
            HStack(spacing: 2) {
                Image(systemName: mark.stars > 0 ? "star.fill" : "star")
                    // A comment: a small bubble at the star's corner, so
                    // the button stays narrow enough for landscape's column.
                    .overlay(alignment: .topTrailing) {
                        if mark.hasComment {
                            Image(systemName: "text.bubble.fill")
                                .font(.system(size: 11, weight: .semibold))
                                .foregroundStyle(.white)
                                .offset(x: 7, y: -5)
                        }
                    }
                if mark.stars > 0 {
                    Text(verbatim: "\(mark.stars)").font(.headline)
                        .padding(.leading, mark.hasComment ? 6 : 0)
                }
            }
            .foregroundStyle(mark.stars > 0 ? RatingStyle.star : .white)
            .frame(minWidth: side, minHeight: side)
            .contentShape(Rectangle())
        }
        .accessibilityLabel(S.appPhotosRatingRate)
        .accessibilityValue(RatingRules.starsText(mark.stars))
        .accessibilityAddTraits(vm.ratingPanelOpen ? .isSelected : [])
    }
}

/// Five stars to tap; the current number filled. One element for VoiceOver
/// ("Rating, 4 stars"), swiped up and down.
struct RatingStars: View {
    let stars: Int
    let tap: (Int) -> Void
    /// Taps, for a light tick of the phone each (not a swipe to a photo
    /// with other stars).
    @State private var taps = 0

    var body: some View {
        HStack(spacing: 2) {
            ForEach(1...5, id: \.self) { n in
                Button {
                    taps += 1
                    tap(n)
                } label: {
                    Image(systemName: n <= stars ? "star.fill" : "star")
                        .font(.system(size: 26))
                        .foregroundStyle(n <= stars ? RatingStyle.star : Color.white.opacity(0.85))
                        .frame(width: 44, height: 44)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
            }
        }
        .sensoryFeedback(.selection, trigger: taps)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(S.appPhotosRatingTitle)
        .accessibilityValue(RatingRules.starsText(stars))
        .accessibilityAdjustableAction { direction in
            switch direction {
            case .increment: if stars < 5 { tap(stars + 1) }
            case .decrement: if stars > 0 { tap(stars - 1) }
            @unknown default: break
            }
        }
    }
}

/// The viewer's rating panel for the open photo: its stars, "From the
/// shared link" when a visitor rated it last, and the comment box with
/// Save (enabled once it differs from the comment stored).
struct RatingPanel: View {
    @ObservedObject var vm: PhotoGalleryVM
    @ObservedObject var draft: RatingDraft
    var focus: FocusState<Bool>.Binding

    var body: some View {
        let mark = vm.openRating
        VStack(alignment: .leading, spacing: 6) {
            RatingStars(stars: mark.stars, tap: vm.tapStar)
            if mark.fromLink {
                Text(S.appPhotosRatingFromLink)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .padding(.leading, 10)
            }
            HStack(alignment: .bottom, spacing: 8) {
                TextField(S.appPhotosRatingCommentPlaceholder, text: $draft.text, axis: .vertical)
                    .lineLimit(1...4)
                    .focused(focus)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 9)
                    .background(Color.white.opacity(0.12), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                    .onChange(of: draft.text) { _, text in
                        let kept = RatingRules.clip(text)
                        if kept != text { draft.text = kept }
                    }
                let canSave = RatingRules.changed(draft: draft.text, saved: mark.comment)
                Button(S.commonSave) {
                    focus.wrappedValue = false
                    vm.saveComment()
                }
                .buttonStyle(.borderedProminent)
                .tint(MenuStyle.ember)
                .foregroundStyle(canSave ? MenuStyle.emberInk : Color.white.opacity(0.5))
                .frame(minHeight: 44)
                .disabled(!canSave)
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        .foregroundStyle(.white)
        // The viewer is black whatever the phone's appearance.
        .environment(\.colorScheme, .dark)
    }
}

/// The one-line note the first time Rating is turned on.
struct RatingToastView: View {
    @ObservedObject var vm: PhotoGalleryVM

    var body: some View {
        if vm.ratingToast {
            let text = S.appPhotosRatingTurnedOn
            // One line where it fits, a size smaller if need be; else as
            // many as it takes.
            ViewThatFits(in: .horizontal) {
                Text(text).font(.subheadline).lineLimit(1)
                Text(text).font(.footnote).lineLimit(1)
                Text(text).font(.subheadline).multilineTextAlignment(.center)
            }
                .padding(.horizontal, 16)
                .padding(.vertical, 10)
                .background(.regularMaterial, in: Capsule())
                .padding(.horizontal, 16)
                .transition(.opacity)
                .onTapGesture { vm.ratingToast = false }
        }
    }
}
