// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  FriendshipsView.swift
//  OffTheCloud
//
//  Native port of the web app's Friends screen (web/src/components/
//  FriendshipsManager.tsx): send a friend request by domain and manage
//  incoming/outgoing friendships. Issue #84: "Your Profile" editing used to
//  live here too (this was the only place it was reachable at all) - moved
//  to the top of Settings instead (see ProfileEditor.swift), so this is
//  just friend management now, presented from a button in SocialFeedView's
//  own toolbar rather than a top-level tab of its own.

import SwiftUI

@MainActor
final class FriendshipsViewModel: ObservableObject {
    private let ws = OTCConnection.shared

    @Published var targetDomain = ""
    @Published var sendingRequest = false

    @Published var friendships: [Msg_Friendship] = []
    @Published var loadingFriendships = false

    @Published var toast: FriendsToast?

    func reloadFriendships() async {
        loadingFriendships = true
        defer { loadingFriendships = false }
        do {
            let resp = try await ws.request { $0.payload = .reqFriendshipsList(Msg_FriendshipsList()) }
            if case .respFriendships(let f) = resp.payload {
                friendships = f.friendships
            }
        } catch {
            showToast(.loadFailed)
        }
    }

    /// Issue #142: friends are always <name>.off-the.cloud (the device
    /// refuses anything else), so the box takes only the name. Accepts a
    /// bare name, the full domain or a link to it; nil when it isn't a
    /// device name. Same rule as the web's friendDomain.ts and Android's.
    static func friendDomain(_ input: String) -> String? {
        var s = input.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        if let r = s.range(of: "://") { s = String(s[r.upperBound...]) }
        s = String(s.prefix { $0 != "/" && $0 != "?" && $0 != "#" })
        let tld = "." + SecretsStore.bridgeDomain
        if s.hasSuffix(tld) { s = String(s.dropLast(tld.count)) }
        guard s.range(of: "^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$", options: .regularExpression) != nil else { return nil }
        return s + tld
    }

    func sendFriendRequest() async {
        guard !targetDomain.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return }
        guard let domain = Self.friendDomain(targetDomain) else {
            showToast(.nameInvalid)
            return
        }
        sendingRequest = true
        defer { sendingRequest = false }
        var req = Msg_FriendshipRequest()
        req.domain = domain
        do {
            let resp = try await ws.request { $0.payload = .reqFriendshipRequest(req) }
            if case .respAck(let ack) = resp.payload, ack.ok {
                // Issue #140: "accepted" - they already had this device as a
                // friend and re-linked it, nothing left to accept.
                showToast(ack.code == "accepted" ? .friendsAgain : .requestSent)
                targetDomain = ""
                await reloadFriendships()
            } else if case .respAck(let ack) = resp.payload {
                showToast(ack.errorMsg.isEmpty ? .requestFailed : .device(ack.errorMsg))
            } else {
                showToast(.unexpectedResponse)
            }
        } catch {
            showToast(.sendError)
        }
    }

    func changeStatus(_ f: Msg_Friendship, to status: Msg_FriendShipStatus) async {
        var req = Msg_ChangeFriendStatus()
        req.domain = f.originProfile.domain
        req.status = status
        do {
            let resp = try await ws.request { $0.payload = .reqChangeFriendStatus(req) }
            if case .respAck(let ack) = resp.payload, ack.ok {
                await reloadFriendships()
                showToast(.statusUpdated)
            } else if case .respAck(let ack) = resp.payload {
                showToast(ack.errorMsg.isEmpty ? .updateFailed : .device(ack.errorMsg))
            } else {
                showToast(.unexpectedResponse)
            }
        } catch {
            showToast(.updateError)
        }
    }

    // Issue #25: removes the request from this device and, best effort,
    // from the other one - the device does the asking.
    func deleteFriendship(_ f: Msg_Friendship) async {
        var req = Msg_DeleteFriendship()
        req.domain = f.originProfile.domain
        do {
            let resp = try await ws.request { $0.payload = .reqDeleteFriendship(req) }
            if case .respAck(let ack) = resp.payload, ack.ok {
                await reloadFriendships()
                showToast(f.sent ? .requestCancelled : .requestDeleted)
            } else if resp.error {
                showToast(.device(resp.errorMessage))
            } else {
                showToast(.deleteFailed)
            }
        } catch {
            showToast(.deleteError)
        }
    }

    // Issue #174: removes an accepted or blocked friend. deleteTheirData
    // drops everything that friend left on this device (their posts and
    // photos, their comments and likes anywhere); askThemToDeleteMine asks
    // their device to drop what this one shared, and until it confirms the
    // friendship stays, marked leaving. Both false is "Remove Now" for a
    // leaving friend: stop waiting and just drop the friendship.
    func removeFriend(_ f: Msg_Friendship, deleteTheirData: Bool, askThemToDeleteMine: Bool) async {
        var req = Msg_DeleteFriendship()
        req.domain = f.originProfile.domain
        req.deleteTheirData = deleteTheirData
        req.askThemToDeleteMine = askThemToDeleteMine
        do {
            let resp = try await ws.request { $0.payload = .reqDeleteFriendship(req) }
            if case .respAck(let ack) = resp.payload, ack.ok {
                await reloadFriendships()
                // Still listed as leaving: their device was offline and will
                // be asked the next time it connects.
                let leaving = friendships.contains { $0.originProfile.domain == f.originProfile.domain && $0.leaving }
                showToast(leaving ? .waitingForTheirDevice : .removed)
            } else if case .respAck(let ack) = resp.payload {
                showToast(ack.errorMsg.isEmpty ? .removeFailed : .device(ack.errorMsg))
            } else if resp.error {
                showToast(.device(resp.errorMessage))
            } else {
                showToast(.removeFailed)
            }
        } catch {
            showToast(.removeError)
        }
    }

    private func showToast(_ m: FriendsToast) {
        toast = m
        Task { [weak self] in
            try? await Task.sleep(nanoseconds: 2_500_000_000)
            if self?.toast == m { self?.toast = nil }
        }
    }
}

/// A short message at the top of the Friends screen, kept as what happened
/// and worded when it shows, so it follows a change of language.
enum FriendsToast: Equatable {
    case loadFailed, nameInvalid, friendsAgain, requestSent, requestFailed, unexpectedResponse, sendError
    case statusUpdated, updateFailed, updateError
    case requestCancelled, requestDeleted, deleteFailed, deleteError
    case waitingForTheirDevice, removed, removeFailed, removeError
    /// The device's own words, already in the app's language.
    case device(String)

    var text: String {
        switch self {
        case .loadFailed: return S.appSocialFriendsLoadFailed
        case .nameInvalid: return S.appSocialFriendsNameInvalid
        case .friendsAgain: return S.appSocialFriendsFriendsAgain
        case .requestSent: return S.appSocialFriendsRequestSent
        case .requestFailed: return S.appSocialFriendsRequestFailed
        case .unexpectedResponse: return S.appSocialFriendsUnexpectedResponse
        case .sendError: return S.appSocialFriendsSendError
        case .statusUpdated: return S.appSocialFriendsStatusUpdated
        case .updateFailed: return S.appSocialFriendsUpdateFailed
        case .updateError: return S.appSocialFriendsUpdateError
        case .requestCancelled: return S.appSocialFriendsRequestCancelled
        case .requestDeleted: return S.appSocialFriendsRequestDeleted
        case .deleteFailed: return S.appSocialFriendsDeleteFailed
        case .deleteError: return S.appSocialFriendsDeleteError
        case .waitingForTheirDevice: return S.appSocialFriendsWaitingTheirDevice
        case .removed: return S.appSocialFriendsRemoved
        case .removeFailed: return S.appSocialFriendsRemoveFailed
        case .removeError: return S.appSocialFriendsRemoveError
        case .device(let words): return words
        }
    }
}

struct FriendshipsView: View {
    @StateObject private var vm = FriendshipsViewModel()
    // Issue #84: this is a sheet presented from Social now, not a tab of
    // its own - it needs its own way to close, same as NewPostPickerView.
    @Environment(\.dismiss) private var dismiss
    /// false: a page of the wide layout's menu (AppMenu.swift), which has
    /// nothing to close.
    private let showsDone: Bool

    init(showsDone: Bool = true) {
        self.showsDone = showsDone
    }

    var body: some View {
        NavigationStack {
            List {
                // Requests waiting for an answer come first: a tapped
                // friend-request alert lands here to answer it.
                // Issue #174: a leaving friendship is never waiting for an
                // answer, whatever its status.
                let waiting = vm.friendships.filter { $0.status == .pending && !$0.sent && !$0.leaving }
                if !waiting.isEmpty {
                    Section(S.appSocialFriendsWaitingAnswer) {
                        ForEach(waiting, id: \.originProfile.domain) { f in
                            let who = HStack(spacing: 12) {
                                avatarView(data: f.originProfile.hasImage ? f.originProfile.image : nil, size: 44)
                                VStack(alignment: .leading) {
                                    WholeWordText(f.originProfile.name.isEmpty ? S.appSocialFriendsNoName : f.originProfile.name, alignment: .leading).font(.headline)
                                    Text(f.originProfile.domain).font(.caption).foregroundColor(.secondary)
                                }
                            }
                            let answer = Group {
                                Button { Task { await vm.deleteFriendship(f) } } label: { WholeWordText(S.commonDecline, twoLines: true) }
                                    .buttonStyle(.bordered).controlSize(.small)
                                Button { Task { await vm.changeStatus(f, to: .accepted) } } label: { WholeWordText(S.commonAccept, twoLines: true) }
                                    .buttonStyle(.borderedProminent).controlSize(.small)
                            }
                            // Beside the name while every word fits; else on a
                            // line of their own under it (Annehmen, Accepter
                            // were broken in the middle on a narrow phone).
                            ViewThatFits(in: .horizontal) {
                                SmallestIsIdeal {
                                    HStack(spacing: 12) {
                                        who
                                        Spacer()
                                        answer
                                    }
                                }
                                VStack(alignment: .leading, spacing: 8) {
                                    who
                                    HStack(spacing: 12) { answer }
                                        .padding(.leading, 56)
                                }
                            }
                            .padding(.vertical, 4)
                        }
                    }
                }

                Section(S.appSocialFriendsAddHeading) {
                    HStack {
                        // Issue #142: the name only; the suffix is fixed.
                        TextField(S.appSocialFriendsNamePlaceholder, text: $vm.targetDomain)
                            .autocapitalization(.none)
                            .disableAutocorrection(true)
                            .keyboardType(.URL)
                            .multilineTextAlignment(.trailing)
                        Text(verbatim: ".\(SecretsStore.bridgeDomain)")
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                            .fixedSize()
                        Button(S.commonSend) { Task { await vm.sendFriendRequest() } }
                            .disabled(vm.sendingRequest || vm.targetDomain.trimmingCharacters(in: .whitespaces).isEmpty)
                    }
                }

                Section {
                    if vm.friendships.isEmpty {
                        Text(S.appSocialFriendsEmpty).foregroundColor(.secondary)
                    } else {
                        ForEach(vm.friendships.filter { !($0.status == .pending && !$0.sent && !$0.leaving) }, id: \.originProfile.domain) { f in
                            FriendRow(
                                f: f,
                                onChange: { status in Task { await vm.changeStatus(f, to: status) } },
                                onDelete: { Task { await vm.deleteFriendship(f) } },
                                onRemove: { theirs, mine in
                                    Task { await vm.removeFriend(f, deleteTheirData: theirs, askThemToDeleteMine: mine) }
                                }
                            )
                        }
                    }
                } header: {
                    HStack {
                        Text(S.appSocialFriendsListHeading)
                        Spacer()
                        if vm.loadingFriendships {
                            ProgressView()
                        } else {
                            Button(S.commonRefresh) { Task { await vm.reloadFriendships() } }
                                .font(.caption)
                        }
                    }
                }
            }
            .listStyle(.insetGrouped)
            // Issue #84: now a sheet (see SocialFeedView's own toolbar
            // button), so it needs a real title of its own - the tab bar
            // used to supply "Profile" for free.
            .navigationTitle(S.appSocialFriendsTitle)
            .navigationBarTitleDisplayMode(.inline)
            // The wide layout's page: its menu names it, as the web's
            // does, so no title bar.
            .toolbar(showsDone ? .automatic : .hidden, for: .navigationBar)
            .overlay(alignment: .topLeading) {
                if !showsDone { HiddenPageHeading(title: S.appShellSectionFriends) }
            }
            .toolbar {
                if showsDone {
                    ToolbarItem(placement: .cancellationAction) {
                        Button(S.commonDone) { dismiss() }
                    }
                }
            }
            .overlay(alignment: .top) {
                if let toast = vm.toast {
                    Text(toast.text)
                        .padding(.horizontal, 12).padding(.vertical, 8)
                        .background(.ultraThinMaterial, in: Capsule())
                        .padding(.top, 8)
                        .transition(.move(edge: .top).combined(with: .opacity))
                }
            }
        }
        .task { await vm.reloadFriendships() }
    }
}

private struct FriendRow: View {
    let f: Msg_Friendship
    let onChange: (Msg_FriendShipStatus) -> Void
    let onDelete: () -> Void
    /// Issue #174: (deleteTheirData, askThemToDeleteMine).
    let onRemove: (Bool, Bool) -> Void
    @State private var confirmingDelete = false
    @State private var removing = false
    @State private var confirmingRemoveNow = false

    // Issue #25: a pending request can be removed by either side - the
    // sender withdraws it, the receiver declines it.
    private var canDelete: Bool { f.status == .pending && !f.leaving }
    private var deleteLabel: String { f.sent ? S.appSocialFriendsCancelRequest : S.appSocialFriendsDeleteRequest }
    // Issue #174: an accepted or blocked friend can be removed, with a
    // choice of what gets deleted on each side.
    private var canRemove: Bool { !f.leaving && (f.status == .accepted || f.status == .blocked) }
    private var name: String {
        f.originProfile.name.isEmpty ? f.originProfile.domain : f.originProfile.name
    }

    var body: some View {
        // The buttons beside the name while its words, the address and the
        // labels fit whole, as the waiting rows; else on lines of their own
        // under it.
        ViewThatFits(in: .horizontal) {
            SmallestIsIdeal {
                HStack(spacing: 12) {
                    who(wholeDomain: true)
                    Spacer()
                    HStack(spacing: 8) { actions }
                }
            }
            VStack(alignment: .leading, spacing: 8) {
                who(wholeDomain: false)
                WholeWordButtons(spacing: 8) { actions }
                    .padding(.leading, 56)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .controlSize(.small)
        .padding(.vertical, 4)
        .confirmationDialog(
            f.sent ? S.appSocialFriendsCancelConfirmTitle : S.appSocialFriendsDeleteConfirmTitle,
            isPresented: $confirmingDelete,
            titleVisibility: .visible
        ) {
            Button(deleteLabel, role: .destructive) { onDelete() }
        } message: {
            Text(f.sent ? S.appSocialFriendsCancelConfirmText : S.appSocialFriendsDeleteConfirmText)
        }
        .confirmationDialog(S.appSocialFriendsRemoveNowTitle(name: name), isPresented: $confirmingRemoveNow, titleVisibility: .visible) {
            Button(S.appSocialFriendsRemoveNow, role: .destructive) { onRemove(false, false) }
        } message: {
            Text(S.appSocialFriendsRemoveNowText)
        }
        .sheet(isPresented: $removing) {
            RemoveFriendSheet(name: name) { theirs, mine in onRemove(theirs, mine) }
        }
    }

    private func who(wholeDomain: Bool) -> some View {
        let domain = f.originProfile.domain.isEmpty ? S.appSocialFriendsNoDomain : f.originProfile.domain
        return HStack(spacing: 12) {
            avatarView(data: f.originProfile.hasImage ? f.originProfile.image : nil, size: 44)
            VStack(alignment: .leading) {
                WholeWordText(f.originProfile.name.isEmpty ? S.appSocialFriendsNoName : f.originProfile.name, alignment: .leading)
                    .font(.headline)
                Group {
                    if wholeDomain { Text(domain).fixedSize() } else { Text(domain) }
                }
                .font(.caption).foregroundColor(.secondary)
                WholeWordText(statusLabel, alignment: .leading)
                    .font(.caption2).foregroundColor(.secondary)
                if f.leaving {
                    Text(S.appSocialFriendsLeavingNote)
                        .font(.caption2).foregroundColor(.secondary)
                }
            }
        }
    }

    // Issue #179: every action is a button on the row, as on the web.
    // Removing is a trash icon: the row ran to a second line of buttons.
    @ViewBuilder private var actions: some View {
        if f.leaving {
            // Issue #174: nothing else to do with a leaving friend
            // but stop waiting for their device.
            Button(role: .destructive) { confirmingRemoveNow = true } label: { WholeWordText(S.appSocialFriendsRemoveNow, twoLines: true) }
                .buttonStyle(.bordered)
        } else {
            ForEach(actionOptions, id: \.0) { opt in
                if opt.1 == .accepted {
                    Button { onChange(opt.1) } label: { WholeWordText(opt.0, twoLines: true) }.buttonStyle(.borderedProminent)
                } else {
                    Button { onChange(opt.1) } label: { WholeWordText(opt.0, twoLines: true) }.buttonStyle(.bordered)
                }
            }
            if canDelete {
                Button(role: .destructive) { confirmingDelete = true } label: {
                    WholeWordText(f.sent ? S.appSocialFriendsCancelRequestButton : S.commonDecline, twoLines: true)
                }
                    .buttonStyle(.bordered)
            }
            if canRemove {
                Button(role: .destructive) { removing = true } label: {
                    Label(S.appSocialFriendsRemoveButton, systemImage: "trash").labelStyle(.iconOnly)
                }
                    .buttonStyle(.bordered)
            }
        }
    }

    /// The status line, " (sent)" included for a request this side sent:
    /// one text per case, so a language can word each as a whole.
    private var statusLabel: String {
        if f.leaving { return S.appSocialFriendsStatusLeaving }
        switch f.status {
        case .accepted: return f.sent ? S.appSocialFriendsStatusAcceptedSent : S.appSocialFriendsStatusAccepted
        case .blocked: return f.sent ? S.appSocialFriendsStatusBlockedSent : S.appSocialFriendsStatusBlocked
        default: return f.sent ? S.appSocialFriendsStatusPendingSent : S.appSocialFriendsStatusPending
        }
    }

    private var actionOptions: [(String, Msg_FriendShipStatus)] {
        // The sender does not decide the status; the receiver does.
        if f.sent || f.leaving { return [] }
        switch f.status {
        case .pending: return [(S.commonAccept, .accepted), (S.appSocialFriendsBlock, .blocked)]
        // No way back to pending: that was for debugging. Block stops new
        // syncs; removing (the trash) is the other way out.
        case .accepted: return [(S.appSocialFriendsBlock, .blocked)]
        case .blocked: return [(S.commonAccept, .accepted)]
        default: return []
        }
    }
}

/// Issue #174: what removing a friend deletes, on each side - both off by
/// default, so a plain removal keeps everything where it is.
private struct RemoveFriendSheet: View {
    let name: String
    let onConfirm: (Bool, Bool) -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var deleteTheirData = false
    @State private var askThemToDeleteMine = false

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    Toggle(S.iosFriendsDeleteTheirs(name: name), isOn: $deleteTheirData)
                } footer: {
                    Text(S.appSocialFriendsDeleteTheirsText)
                }
                Section {
                    Toggle(S.appSocialFriendsAskTheirs(name: name), isOn: $askThemToDeleteMine)
                } footer: {
                    Text(S.appSocialFriendsAskTheirsText)
                }
                Section {
                    Button(S.iosFriendsRemoveFriend, role: .destructive) {
                        onConfirm(deleteTheirData, askThemToDeleteMine)
                        dismiss()
                    }
                    .frame(maxWidth: .infinity)
                }
            }
            .navigationTitle(S.appSocialFriendsRemoveTitle(name: name))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button(S.commonCancel) { dismiss() }
                }
            }
        }
        .presentationDetents([.medium, .large])
    }
}

/// A row of buttons that never breaks a label inside a word: one line, as an
/// HStack, while every label's words fit whole; else whole buttons wrapped onto
/// more lines, as Android's FlowRow. German, French, Spanish and Dutch labels
/// (Blockieren, Ac-cepter, Blo-quear) used to be broken in the middle on a
/// narrow phone; English keeps its one line.
private struct WholeWordButtons<Content: View>: View {
    var spacing: CGFloat
    @ViewBuilder var content: Content

    var body: some View {
        ViewThatFits(in: .horizontal) {
            SmallestIsIdeal { HStack(spacing: spacing) { content } }
            FlowRow(spacing: spacing) { content }
        }
    }
}

/// A text whose smallest width is its longest word: hidden copies of its
/// words, each on one line, hold it open. Squeezed, Text hyphenates German,
/// French, Spanish and Dutch (An-nehmen, So-phie), which let a row "fit" by
/// splitting its words; English isn't hyphenated, so it lays out as before.
/// Also the selection bar's labels (SelectionActionBar.swift).
///
/// With twoLines, a label of three words or more is held open at its best
/// split into two lines instead, so a row of buttons "fits" only while no
/// label needs a third line (French "Remettre / en / attente" was a word a
/// line on an iPhone SE); one or two words lay out as before.
struct WholeWordText: View {
    let text: String
    var alignment: Alignment = .center
    var twoLines = false

    init(_ text: String, alignment: Alignment = .center, twoLines: Bool = false) {
        self.text = text
        self.alignment = alignment
        self.twoLines = twoLines
    }

    private var words: [String] { text.split(separator: " ").map(String.init) }

    var body: some View {
        if twoLines && words.count > 2 {
            TwoLineFloor {
                ForEach(1..<words.count, id: \.self) { i in
                    Text(words[..<i].joined(separator: " ")).fixedSize().hidden()
                    Text(words[i...].joined(separator: " ")).fixedSize().hidden()
                }
                Text(text)
            }
        } else {
            ZStack(alignment: alignment) {
                ForEach(Array(Set(words)).sorted(), id: \.self) { word in
                    Text(word).fixedSize().hidden()
                }
                Text(text)
            }
        }
    }
}

/// WholeWordText's two-line hold: the subviews are each split's two halves,
/// on one line each, then the text. Its smallest width is the narrowest
/// split's wider half - the narrowest the text can be in two lines without
/// breaking a word - and the text is laid out in the width it gets.
private struct TwoLineFloor: Layout {
    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        guard let text = subviews.last else { return .zero }
        let halves = subviews.dropLast().map { $0.sizeThatFits(.unspecified).width }
        let floor = stride(from: 0, to: halves.count - 1, by: 2)
            .map { max(halves[$0], halves[$0 + 1]) }
            .min() ?? 0
        guard let width = proposal.width else { return text.sizeThatFits(proposal) }
        // The text's own width in what it gets, never below the floor: a
        // label offered a whole line stays as wide as its words.
        let size = text.sizeThatFits(ProposedViewSize(width: max(width, floor), height: proposal.height))
        return CGSize(width: max(size.width, floor), height: size.height)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        for view in subviews.dropLast() {
            view.place(at: bounds.origin, proposal: .zero)
        }
        subviews.last?.place(at: CGPoint(x: bounds.midX, y: bounds.midY), anchor: .center,
                             proposal: ProposedViewSize(width: bounds.width, height: bounds.height))
    }
}

/// Offers ViewThatFits its content's smallest width as the ideal one, and
/// otherwise lays the content out as it is. A Text's smallest width is its
/// longest unbreakable piece - a whole word, with WholeWordText - so an HStack
/// of buttons "fits" exactly when no label in it has to break inside a word.
struct SmallestIsIdeal: Layout {
    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        guard let content = subviews.first else { return .zero }
        if proposal.width == nil {
            return content.sizeThatFits(ProposedViewSize(width: 0, height: proposal.height))
        }
        return content.sizeThatFits(proposal)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        // The proposal it was measured with, not its measured size: a Text
        // laid out in exactly its own width can wrap by a rounding hair.
        subviews.first?.place(at: bounds.origin, anchor: .topLeading, proposal: proposal)
    }
}

/// Views at their own width, left to right, a new line when the next doesn't
/// fit; one wider than a whole line gets the line's width.
private struct FlowRow: Layout {
    var spacing: CGFloat

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let frames = arrange(width: proposal.width, subviews)
        return CGSize(width: frames.map(\.maxX).max() ?? 0, height: frames.map(\.maxY).max() ?? 0)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        for (view, frame) in zip(subviews, arrange(width: bounds.width, subviews)) {
            view.place(at: CGPoint(x: bounds.minX + frame.minX, y: bounds.minY + frame.minY),
                       anchor: .topLeading, proposal: ProposedViewSize(frame.size))
        }
    }

    private func arrange(width: CGFloat?, _ subviews: Subviews) -> [CGRect] {
        let line = width ?? .infinity
        var frames: [CGRect] = []
        var x: CGFloat = 0, y: CGFloat = 0, lineHeight: CGFloat = 0
        for view in subviews {
            var size = view.sizeThatFits(.unspecified)
            if size.width > line { size = view.sizeThatFits(ProposedViewSize(width: line, height: nil)) }
            if x > 0 && x + size.width > line {
                x = 0
                y += lineHeight + spacing
                lineHeight = 0
            }
            frames.append(CGRect(origin: CGPoint(x: x, y: y), size: size))
            x += size.width + spacing
            lineHeight = max(lineHeight, size.height)
        }
        return frames
    }
}

/// Shared avatar rendering — used by the profile editor and the friends list.
@ViewBuilder
func avatarView(data: Data?, size: CGFloat) -> some View {
    Group {
        if let data, let ui = UIImage(data: data) {
            Image(uiImage: ui).resizable().scaledToFill()
        } else {
            Image(systemName: "person.crop.circle.fill")
                .resizable()
                .foregroundColor(.secondary)
        }
    }
    .frame(width: size, height: size)
    .clipShape(Circle())
}
