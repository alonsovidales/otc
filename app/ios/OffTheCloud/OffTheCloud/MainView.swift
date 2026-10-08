// SPDX-License-Identifier: AGPL-3.0-or-later

import SwiftUI

struct MainView: View {
    @EnvironmentObject var secrets: SecretsStore
    @EnvironmentObject var upload: UploadModel
    @EnvironmentObject var notifications: NotificationsModel
    @EnvironmentObject var social: SocialFeedViewModel
    @EnvironmentObject var updateAlert: UpdateAlertModel
    // Issue #78: bare-TabView-with-no-selection couldn't be switched
    // programmatically at all - a tapped notification needs to be able to
    // jump to Social or Profile on its own, not just rely on the user
    // already being there. Issue #79: Social (not Notifications) is the
    // default landing tab, and it stays that even when the timeline is
    // empty - an empty feed shows its own invitation to post (see
    // SocialFeedView) rather than being something to route away from.
    @State private var selectedTab = 1
    // Issue #56: the bridge's verdict on whether this device is reachable
    // at all. Observed rather than stored so this clears itself as soon as
    // OTCConnection's own reconnect succeeds.
    @ObservedObject private var connection = OTCConnection.shared
    /// Issue #151: set once connecting has kept failing for a few seconds -
    /// see the .task below.
    @State private var showConnectionProblem = false
    // The Images search sends Files to a folder, a file or a list of
    // results (TopSearch.swift): Files is shown for it.
    @ObservedObject private var filesNav = FilesNav.shared
    @Environment(\.scenePhase) private var scenePhase

    // The wide layout (AppMenu.swift): a window 600 points wide or more
    // has the web's top bar and menu instead of the bottom bar. The tabs
    // stay the same views either way, so turning the phone keeps each
    // section as it was.
    //
    // The photo library and its search are here, not in Images: the top
    // bar searches from any section, and the menu's People and
    // Collections pages show the same library.
    @StateObject private var gallery: PhotoGalleryVM
    @StateObject private var search = TopSearchModel()
    @ObservedObject private var faces = FaceRecognition.shared
    /// The window's size, safe areas included.
    @State private var windowSize: CGSize
    /// Whether the keyboard shows: the search's panel may rise over its field.
    @State private var keyboardUp = false
    /// People, Collections or Friends shown as a page over the tabs (a wide
    /// window only; a narrow one has them as sheets).
    @State private var page: AppSection?
    /// Social's Friends sheet - here so that it can become the Friends page.
    @State private var friendsSheet = false
    /// The menu button's choice on a full-width window: the whole menu or
    /// the rail.
    @AppStorage(WideLayout.openKey) private var menuOpen = true
    /// The whole menu laid over the page (the menu button below 1024).
    @State private var drawerOpen = false
    @State private var storageDetails = false
    @State private var revealStorage = false
    @StateObject private var storage = StatusViewModel()
    @FocusState private var topSearchFocused: Bool

    init(deviceID: String) {
        _gallery = StateObject(wrappedValue: PhotoGalleryVM(deviceID: deviceID, localPhotosFolder: nil))
        // Laid out for the window from the first frame: launched on its
        // side, the phone doesn't show the bottom bar first.
        let window = UIApplication.shared.connectedScenes
            .compactMap { ($0 as? UIWindowScene)?.keyWindow }
            .first
        _windowSize = State(initialValue: window?.bounds.size ?? .zero)
    }

    private var menu: WideLayout.Menu { WideLayout.menu(width: windowSize.width, open: menuOpen) }
    private var wide: Bool { menu != .none }
    private var fullWidth: Bool { windowSize.width >= WideLayout.fullWidth }

    var body: some View {
        layout
            .environment(\.wideLayout, wide)
            .onGeometryChange(for: CGSize.self) { proxy in
                CGSize(
                    width: proxy.size.width + proxy.safeAreaInsets.leading + proxy.safeAreaInsets.trailing,
                    height: proxy.size.height + proxy.safeAreaInsets.top + proxy.safeAreaInsets.bottom
                )
            } action: { windowSize = $0 }
            .keyboardShown($keyboardUp)
            .onChange(of: wide) { _, isWide in layoutChanged(wide: isWide) }
            .onChange(of: fullWidth) { _, _ in drawerOpen = false }
            .onChange(of: scenePhase) { _, _ in pollStorage() }
            .onAppear { pollStorage() }
            .onDisappear { storage.stop() }
            .onChange(of: topSearchFocused) { _, focused in
                guard focused else { return }
                search.open = true
                drawerOpen = false
                gallery.loadLibraryOnce()
            }
            .onChange(of: search.query) { _, _ in
                if topSearchFocused && !search.typed.isEmpty { search.open = true }
            }
            // Face recognition turned off: no People page.
            .onChange(of: faces.isOn) { _, on in
                if !on && page == .people { show(.images) }
            }
            .modifier(MainChrome(
                secrets: secrets,
                notifications: notifications,
                connection: connection,
                showConnectionProblem: $showConnectionProblem,
                filesSent: filesNav.sent,
                scenePhase: scenePhase,
                onFilesSent: { show(.files) },
                onDeepLink: deepLink
            ))
    }

    /// Both layouts: the bottom bar's, or the web's top bar and menu beside
    /// the same tabs. The tabs keep their place in the tree either way.
    private var layout: some View {
        // Issue #183: a critical device update, over every tab until it
        // is installed. Update goes the same way as an update alert does.
        // Above the tabs in the layout rather than a .safeAreaInset on
        // them: the TabView doesn't hand that inset on to its navigation
        // stacks, so the banner used to lie over the navigation bar and
        // the top of each tab's content, covering the toolbar buttons and
        // the header of a post opened from a notification, while the feed
        // still measured its full height (and so let a vertical photo run
        // under the tab bar - see SocialFeedView's feedViewportHeight).
        // Here it takes its height from the tabs instead. On a wide window
        // it sits under the top bar, beside the menu, as on the web.
        VStack(spacing: 0) {
            if wide {
                WideTopBar(
                    menuShown: menu == .full || drawerOpen,
                    fullWidth: fullWidth,
                    // The page's own action: a new post, on the feed.
                    action: currentSection == .social
                        ? TopBarAction(label: "New post") { social.composeRequests += 1 }
                        : nil,
                    actionIconOnly: windowSize.width < 700,
                    onMenu: toggleMenu
                ) {
                    TopBarSearchField(vm: gallery, search: search, focused: $topSearchFocused, actions: searchActions)
                        .anchorPreference(key: SearchFieldAnchorKey.self, value: .bounds) { $0 }
                }
                .zIndex(1)
            } else if let alert = updateAlert.critical {
                criticalBanner(alert)
                    // Its red carries on up behind the status bar, which the
                    // navigation bar no longer reaches while it shows.
                    .background(Color.red.ignoresSafeArea(edges: .top))
            }
            HStack(spacing: 0) {
                if wide {
                    sideMenu(menu == .full ? .full : .rail)
                        .background(MenuStyle.bar.ignoresSafeArea(edges: [.leading, .bottom]))
                        // The keyboard doesn't squeeze the menu.
                        .ignoresSafeArea(.keyboard)
                        .accessibilityHidden(drawerOpen)
                }
                VStack(spacing: 0) {
                    if wide, let alert = updateAlert.critical {
                        criticalBanner(alert)
                    }
                    ZStack {
                        // Under a page, out of VoiceOver's reach as well.
                        // Said to each tab (LazyTab): hiding the TabView
                        // itself leaves what its tabs show reachable.
                        tabs
                            .environment(\.tabsCovered, wide && page != nil)
                        if wide, let page {
                            pageView(page)
                                .background(Color(.systemBackground))
                        }
                    }
                }
                // Under the menu laid over it: out of reach, as the web's inert.
                .accessibilityHidden(drawerOpen)
            }
            // The whole menu over the page, from the menu button of a
            // window too narrow for it beside the page (the web's drawer).
            .overlay(alignment: .leading) { drawer }
        }
        // The panel hangs from the field itself, both edges lined up with
        // it as the web's (left: 0; right: 0 under the field). The space
        // here ends above the keyboard while it shows.
        .overlayPreferenceValue(SearchFieldAnchorKey.self, alignment: .topLeading) { anchor in
            GeometryReader { proxy in
                if let anchor {
                    searchPanel(under: proxy[anchor], height: proxy.size.height)
                }
            }
        }
    }

    private func criticalBanner(_ alert: Msg_UpdateAlert) -> some View {
        CriticalUpdateBanner(alert: alert) {
            notifications.pendingDeepLink = .updates
        }
    }

    private var tabs: some View {
        // Every tab is wrapped in LazyTab - see its doc comment for why
        // the ones not showing must not be built at launch. A wide window
        // hides the bar: the menu switches the tabs.
        TabView(selection: Binding(
            get: { selectedTab },
            set: { tab in
                // Files picked in the tab bar - from another tab, or its own
                // button while a search's results show - is the folder, as
                // Android's tab bar and the wide layout's menu have it.
                if tab == AppSection.files.tab { filesNav.leaveSearch() }
                selectedTab = tab
            }
        )) {
            // Issue #78 follow-up: notifications became a section of
            // its own (was a header/toolbar bell+sheet) - leftmost tab,
            // per the user's explicit ask ("in the ios app, it should
            // be at the left").
            LazyTab(tag: 0, selection: $selectedTab) {
                NotificationsListView(model: notifications)
            }
            .toolbar(wide ? .hidden : .automatic, for: .tabBar)
            .tabItem { Label("Alerts", systemImage: "bell.fill") }
            .badge(notifications.unacknowledgedCount)
            .tag(0)

            LazyTab(tag: 1, selection: $selectedTab) {
                SocialFeedView(showingFriendships: $friendsSheet)
            }
            .toolbar(wide ? .hidden : .automatic, for: .tabBar)
            .tabItem { Label("Social", systemImage: "bubble.left.and.bubble.right") }
            .tag(1)

            // Issue #84: Friendships moved from here into a sheet
            // presented by SocialFeedView's own toolbar (tag 2 left
            // unused rather than renumbering everything after it -
            // same convention as a removed proto field).
            LazyTab(tag: 3, selection: $selectedTab) {
                FilesExplorerView(initialPath: "/")
            }
            .toolbar(wide ? .hidden : .automatic, for: .tabBar)
            .tabItem { Label("Files", systemImage: "folder") }
            .tag(3)

            LazyTab(tag: 4, selection: $selectedTab) {
                PhotoGalleryView(vm: gallery, search: search)
            }
            .toolbar(wide ? .hidden : .automatic, for: .tabBar)
            .tabItem { Label("Images", systemImage: "photo.on.rectangle") }
            .tag(4)

            LazyTab(tag: 5, selection: $selectedTab) {
                SettingsView()
            }
            .toolbar(wide ? .hidden : .automatic, for: .tabBar)
            .tabItem { Label("Settings", systemImage: "gearshape") }
            .tag(5)
        }
    }

    // MARK: The wide layout (AppMenu.swift)

    /// People, Collections and Friends as pages of their own.
    @ViewBuilder
    private func pageView(_ p: AppSection) -> some View {
        switch p {
        case .people:
            NavigationStack {
                PeopleView(vm: gallery, onOpenPhotos: { show(.images) })
            }
        case .collections:
            NavigationStack {
                CollectionsPage(vm: gallery, onOpenPhotos: { show(.images) })
            }
        case .friends:
            FriendshipsView(showsDone: false)
        default:
            EmptyView()
        }
    }

    /// The section the menu marks: a collection open in Images is still
    /// the Collections section, as on the web.
    private var currentSection: AppSection {
        if let page { return page }
        if selectedTab == AppSection.images.tab && gallery.activeGroup != nil { return .collections }
        return AppSection.forTab(selectedTab)
    }

    private func sideMenu(_ layout: WideLayout.Menu) -> some View {
        SideMenu(
            layout: layout,
            groups: AppSection.menu(faces: faces.isOn),
            current: currentSection,
            unread: notifications.unacknowledgedCount,
            storage: StorageSummary(status: storage.status, error: storage.errorText),
            short: windowSize.height < WideLayout.shortHeight,
            details: $storageDetails,
            revealStorage: $revealStorage,
            onPick: pickFromMenu,
            onStorage: showStorageDetails
        )
    }

    private var drawer: some View {
        ZStack(alignment: .leading) {
            drawerContent
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .leading)
    }

    @ViewBuilder
    private var drawerContent: some View {
        if wide && drawerOpen {
            Color.black.opacity(0.5)
                .ignoresSafeArea(edges: [.horizontal, .bottom])
                .ignoresSafeArea(.keyboard)
                .contentShape(Rectangle())
                .onTapGesture { closeDrawer() }
                .accessibilityLabel("Close the menu")
                .accessibilityAddTraits(.isButton)
                .transition(.opacity)
        }
        if wide && drawerOpen {
            sideMenu(.full)
                .frame(maxHeight: .infinity)
                .background(
                    UnevenRoundedRectangle(bottomTrailingRadius: 16, topTrailingRadius: 16, style: .continuous)
                        .fill(MenuStyle.drawer)
                        .shadow(color: .black.opacity(0.3), radius: 12, x: 4)
                        .ignoresSafeArea(edges: [.leading, .bottom])
                )
                .ignoresSafeArea(.keyboard)
                .transition(.move(edge: .leading))
        }
    }

    /// The search's suggestions, under the top bar's field (`field`, in the
    /// panel's own coordinates, whose foot is `height` down - the
    /// keyboard's top while it shows). With the keyboard up on a short
    /// window (a phone turned sideways) it rises over the field instead,
    /// its foot just above the keyboard (SearchPanelPlace).
    @ViewBuilder
    private func searchPanel(under field: CGRect, height: CGFloat) -> some View {
        if wide && search.open && !search.typed.isEmpty {
            ZStack(alignment: .topLeading) {
                // A tap anywhere else closes it, as on the web.
                Color.clear
                    .contentShape(Rectangle())
                    .padding(.top, WideLayout.topBarHeight)
                    .onTapGesture { searchActions.finish() }
                SearchPanelLayout(
                    place: SearchPanelPlace(
                        under: field.maxY + 6,
                        bottom: height,
                        // Just under the status bar: the top of this space.
                        ceiling: 4,
                        keyboardUp: keyboardUp,
                        cap: min(windowSize.height * 0.7, 640)
                    ),
                    x: field.minX,
                    width: max(field.width, 320)
                ) {
                    TopSearchPanel(
                        suggestions: searchActions.suggestions(),
                        typed: search.typed,
                        alreadyIn: gallery.chips.contains { FoldedText($0).text == FoldedText(search.typed).text },
                        selectedPeople: gallery.selectedPeople,
                        face: gallery.face(for:),
                        onPick: searchActions.pick,
                        moved: search.moved,
                        fitsRows: true,
                        background: MenuStyle.drawer
                    )
                    .padding(.vertical, 6)
                    .background(MenuStyle.drawer)
                    .clipShape(RoundedRectangle(cornerRadius: 16, style: .continuous))
                    .overlay(RoundedRectangle(cornerRadius: 16, style: .continuous).stroke(MenuStyle.line, lineWidth: 1))
                    .shadow(color: .black.opacity(0.22), radius: 24, y: 10)
                }
            }
        }
    }

    private var searchActions: TopSearchActions {
        TopSearchActions(
            vm: gallery,
            search: search,
            faces: faces.isOn,
            filesNav: filesNav,
            focus: { topSearchFocused = $0 },
            showPhotos: { show(.images) }
        )
    }

    /// Shows a section: its tab, or on a wide window a page over the tabs.
    private func show(_ s: AppSection) {
        if s.isPage && wide {
            page = s
        } else {
            page = nil
            selectedTab = s.tab
        }
    }

    /// The menu, as the web's: Images is the whole library (whatever was
    /// searched for is left), Files the folder rather than a search's results.
    private func pickFromMenu(_ s: AppSection) {
        if s == .images { gallery.showAll() }
        if s == .files { filesNav.leaveSearch() }
        show(s)
        if drawerOpen { closeDrawer() }
    }

    private func toggleMenu() {
        if fullWidth {
            withAnimation(.easeInOut(duration: 0.2)) { menuOpen.toggle() }
        } else if drawerOpen {
            closeDrawer()
        } else {
            closeSearch()
            withAnimation(.easeOut(duration: 0.25)) { drawerOpen = true }
        }
    }

    /// The menu laid over the page takes the place of the search's panel.
    private func closeSearch() {
        if search.open || topSearchFocused { searchActions.finish() }
    }

    private func closeDrawer() {
        withAnimation(.easeIn(duration: 0.2)) { drawerOpen = false }
    }

    /// The rail's storage ring: the whole menu, at the storage details.
    private func showStorageDetails() {
        storageDetails = true
        revealStorage = true
        if fullWidth {
            withAnimation(.easeInOut(duration: 0.2)) { menuOpen = true }
        } else {
            closeSearch()
            withAnimation(.easeOut(duration: 0.25)) { drawerOpen = true }
        }
    }

    /// The window crossed 600 points (turned, folded, resized): each section
    /// stays where it was. A sheet that is a page of the wide layout's menu
    /// becomes that page, and back.
    private func layoutChanged(wide isWide: Bool) {
        drawerOpen = false
        // The field it belonged to is gone; what was typed stays.
        search.open = false
        if isWide {
            if friendsSheet {
                friendsSheet = false
                page = .friends
            } else if gallery.showPeople {
                // The page carries on with what the sheet was doing.
                gallery.peoplePage.handOff()
                gallery.showPeople = false
                page = .people
            } else if gallery.showGroups {
                gallery.showGroups = false
                page = .collections
            }
        } else if let p = page {
            // The sheet carries on with what the page was doing.
            if p == .people && faces.isOn { gallery.peoplePage.handOff() }
            page = nil
            selectedTab = p.tab
            // Once the narrow layout is up, over the tab that opens it.
            DispatchQueue.main.async {
                switch p {
                case .people:
                    gallery.showPeople = faces.isOn
                case .collections:
                    Task { await gallery.loadGroups() }
                    gallery.showGroups = true
                case .friends:
                    friendsSheet = true
                default:
                    break
                }
            }
        }
        pollStorage()
    }

    /// The menu's storage, polled while it shows.
    private func pollStorage() {
        if wide && scenePhase == .active {
            storage.start()
        } else {
            storage.stop()
        }
    }

    private func deepLink(_ link: NotificationsModel.DeepLink?) {
        switch link {
        case .post:
            show(.social)
        case .friendRequests:
            // A wide window has Friends in its menu: the page answers it.
            // A narrow one opens Social, whose sheet does.
            if wide {
                show(.friends)
                notifications.pendingDeepLink = nil
            } else {
                show(.social)
            }
        case .updates:
            // Issue #183: an update alert or the critical banner -
            // Settings scrolls itself to its update section.
            show(.settings)
        case nil:
            break
        }
    }
}

/// What both layouts share around them: the switches to Files and from
/// notifications, the face recognition question, and the cards over
/// everything while the device can't be reached.
private struct MainChrome: ViewModifier {
    let secrets: SecretsStore
    @ObservedObject var notifications: NotificationsModel
    @ObservedObject var connection: OTCConnection
    @Binding var showConnectionProblem: Bool
    let filesSent: Int
    let scenePhase: ScenePhase
    let onFilesSent: () -> Void
    let onDeepLink: (NotificationsModel.DeepLink?) -> Void

    func body(content: Content) -> some View {
        content
        // Issue #122: no upload indicator over the tabs any more. The
        // full detail lives in Settings > Uploads, and a sync running in
        // the background is not something every screen needs to announce.
        // A like/comment or friend-request notification is handled by
        // SocialFeedView itself now (it observes
        // `notifications.pendingDeepLink` directly, presenting its own
        // Friendships sheet for the latter since issue #84 removed that
        // tab) - this just switches to Social either way, so that view is
        // actually on screen to react to it. A wide window shows its
        // Friends page instead.
        .onChange(of: filesSent) { _, _ in onFilesSent() }
        // Whether face recognition is on decides whether People shows
        // (FaceRecognition.swift): asked once signed in, and again each
        // time the app comes back, as it may have changed on the web.
        .task { FaceRecognition.shared.refresh() }
        .onChange(of: scenePhase) { _, phase in
            if phase == .active { FaceRecognition.shared.refresh() }
        }
        .onChange(of: notifications.pendingDeepLink) { _, link in onDeepLink(link) }
        // Issue #56: covers the tabs rather than sitting inside one of
        // them - while the device can't be reached there is nothing behind
        // this worth interacting with, since every tab's content comes
        // from that device. Only for the two verdicts the bridge gives us
        // explicitly; an ordinary dropped connection stays silent and
        // reconnects in the background as it always did, because that
        // recovers in a second or two and is not worth a full-screen
        // interruption.
        .overlay {
            if let code = connection.statusCode {
                DeviceUnreachableView(message: connection.lastError ?? "", code: code) {
                    AppLogOut.run(secrets: secrets, unregisterPush: false, keepDevice: true)
                }
                    .transition(.opacity)
            } else if showConnectionProblem {
                // Everything else that stops the app connecting - a wrong
                // address or password, an unreachable host - with the
                // settings to fix it. See ConnectionProblemView.
                ConnectionProblemView {
                    AppLogOut.run(secrets: secrets, unregisterPush: false, keepDevice: true)
                }
                    .transition(.opacity)
            }
        }
        .animation(.default, value: connection.statusCode)
        .animation(.default, value: showConnectionProblem)
        // Issue #151: one failed attempt is not a connection problem - at
        // launch the first try often goes out before the network is up,
        // and the next one works. The card with the connection settings
        // is shown only once connecting has kept failing for a few
        // seconds, retried meanwhile.
        .task(id: connection.connectionFailed) {
            guard connection.connectionFailed else { showConnectionProblem = false; return }
            for _ in 0..<3 {
                try? await Task.sleep(for: .seconds(2.5))
                if Task.isCancelled || !connection.connectionFailed { return }
                _ = try? await OTCConnection.shared.ensureConnected()
            }
            if !Task.isCancelled { showConnectionProblem = connection.connectionFailed }
        }
    }
}


private struct TabsCoveredKey: EnvironmentKey {
    static let defaultValue = false
}

private extension EnvironmentValues {
    /// A page of the wide layout (People, Collections, Friends) lies over
    /// the tabs: what they show is out of VoiceOver's reach too.
    var tabsCovered: Bool {
        get { self[TabsCoveredKey.self] }
        set { self[TabsCoveredKey.self] = newValue }
    }
}

/// A tab whose content is not built until the tab is first selected.
///
/// TabView constructs every tab's view tree up front, whether or not it
/// is showing. That was the remaining cold-launch cost after the feed's
/// PostBox fix: each tab still held generated protobuf values in its views
/// - the Files rows' Msg_File, the people chips' Msg_Person, the alerts
/// list's [Msg_Notification] - and AttributeGraph builds a layout
/// descriptor for every one of those types by recursively walking its
/// fields (see PostBox's doc comment for the trace that showed this).
/// Doing it for five tabs' worth of types at once, before the first
/// frame, is what a cold start was waiting on; the runtime then caches
/// it, which is why only a cold start ever paid.
///
/// Deferring the build changes nothing else: a tab that isn't showing
/// never had its onAppear fire at launch anyway, so each one still loads
/// its data the first time it is selected, exactly as before. Once built,
/// a tab stays built, so switching back is instant.
private struct LazyTab<Content: View>: View {
    let tag: Int
    @Binding var selection: Int
    @ViewBuilder let content: () -> Content

    @State private var built = false
    @Environment(\.tabsCovered) private var covered

    var body: some View {
        if built || selection == tag {
            content()
                .onAppear { built = true }
                .accessibilityHidden(covered)
        } else {
            // Something has to occupy the slot so the tab item exists;
            // it is never seen, since the tab isn't selected.
            Color.clear
        }
    }
}
