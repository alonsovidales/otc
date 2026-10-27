// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  SettingsView.swift
//  OffTheCloud
//
//  Created by Alonso Vidales on 8/9/25.
//
//  Extended to also cover the web app's device Settings tab
//  (web/src/components/SettingsForm.tsx: password change, bridge secret) and a
//  native port of the status readout (web/src/components/StatusWidget.tsx),
//  alongside the original app-level connection/sync settings.

import SwiftUI
import Photos

/// Text a Settings model keeps for its screen - a toast, an error, a note -
/// worded each time the screen shows it, so it follows a change of language
/// (docs/i18n.md, "Each platform"): a String in a @Published property stays
/// in the language it was made in. `say` for our own words (an accessor),
/// `asIs` for the device's (already in the request's language), `error` for
/// an error's description (ours are LocalizedErrors worded when read). Two
/// are equal when they read the same, like the Strings they replace.
struct SettingsText: Equatable {
    private let make: () -> String

    var text: String { make() }

    static func say(_ make: @escaping () -> String) -> SettingsText { SettingsText(make: make) }

    static func asIs(_ text: String) -> SettingsText { SettingsText(make: { text }) }

    /// The device's words, or ours when it gave none.
    static func asIs(_ text: String, or ours: @escaping () -> String) -> SettingsText {
        text.isEmpty ? say(ours) : asIs(text)
    }

    static func error(_ error: Error) -> SettingsText { SettingsText(make: { error.localizedDescription }) }

    static func == (a: SettingsText, b: SettingsText) -> Bool { a.text == b.text }
}

@MainActor
final class DeviceSettingsViewModel: ObservableObject {
    private let ws = OTCConnection.shared

    @Published var oldKey = ""
    @Published var newKey = ""
    @Published var confirmKey = ""
    @Published var savingKey = false

    @Published var toast: SettingsText?

    // Issue #52: on by default - see db.sql's settings.face_recognition_
    // enabled doc comment for why turning this on never retroactively
    // processes anything already in the library.
    @Published var faceRecognitionEnabled = false
    @Published var savingFaceRecognition = false
    // Issue #181: the tagging model, on by default.
    @Published var imageTaggingEnabled = true
    @Published var savingImageTagging = false

    // Issue #73: full-library reprocess - re-runs tagging/face detection
    // against every already-uploaded photo/video (e.g. after a detection
    // fix), stripping existing tags/people first so they're recalculated
    // clean. Runs entirely server-side; this just starts it and polls for
    // progress the same way status.swift's StatusViewModel polls GetStatus.
    @Published var reprocessStatus = ""
    @Published var reprocessTotal: Int32 = 0
    @Published var reprocessProcessed: Int32 = 0
    @Published var startingReprocess = false
    // "resume": continue a stopped run from where it left off. "restart":
    // wipe everything and start fresh - always available for a first run,
    // and also offered *instead of* resume when a run was stopped, so the
    // owner isn't stuck only ever continuing (issue #73 follow-up).
    enum ReprocessConfirmAction { case resume, restart }
    @Published var reprocessConfirmAction: ReprocessConfirmAction? = nil
    private var reprocessPollTask: Task<Void, Never>?

    func loadReprocessStatus() async {
        guard let resp = try? await ws.request({ e in
            var req = ReqEnvelope()
            req.payload = .reqGetReprocessStatus(.init())
            e = req
        }) else { return }
        if case .respReprocessStatus(let s) = resp.payload {
            reprocessStatus = s.status
            reprocessTotal = s.total
            reprocessProcessed = s.processed
        }
    }

    // Self-rescheduling: loads once, and if that leaves status "running",
    // schedules another load in 1.5s - same idle-when-nothing's-happening
    // shape as OTCConnection's own reconnect backoff, just on a fixed
    // interval since this only ever needs to run while a job is active.
    func pollReprocessStatusWhileRunning() {
        reprocessPollTask?.cancel()
        reprocessPollTask = Task {
            await loadReprocessStatus()
            while !Task.isCancelled && reprocessStatus == "running" {
                try? await Task.sleep(nanoseconds: 1_500_000_000)
                guard !Task.isCancelled else { return }
                await loadReprocessStatus()
            }
        }
    }

    func stopPollingReprocessStatus() {
        reprocessPollTask?.cancel()
        reprocessPollTask = nil
    }

    // Rounded, clamped to [0, 100] - total can be 0 right when a run has
    // just started and the very first status poll hasn't landed yet.
    var reprocessPercent: Int {
        guard reprocessTotal > 0 else { return 0 }
        let pct = Int((Double(reprocessProcessed) / Double(reprocessTotal) * 100).rounded())
        return max(0, min(100, pct))
    }

    func startReprocess(forceRestart: Bool) async {
        reprocessConfirmAction = nil
        startingReprocess = true
        defer { startingReprocess = false }
        do {
            var req = Msg_StartReprocess()
            req.forceRestart = forceRestart
            let resp = try await ws.request { e in
                var envelope = ReqEnvelope()
                envelope.payload = .reqStartReprocess(req)
                e = envelope
            }
            if case .respAck(let ack) = resp.payload, ack.ok {
                pollReprocessStatusWhileRunning()
            } else if case .respAck(let ack) = resp.payload {
                showToast(.asIs(ack.errorMsg, or: { S.appSettingsReprocessStartFailed }))
            }
        } catch {
            showToast(.say { S.appSettingsReprocessStartError })
        }
    }

    // Cancelling doesn't wait for the worker to actually wind down (it
    // notices between files - see files_manager.CancelReprocess) - just
    // requests it and refreshes status, same as starting.
    @Published var cancellingReprocess = false
    func stopReprocess() async {
        cancellingReprocess = true
        defer { cancellingReprocess = false }
        do {
            let resp = try await ws.request { e in
                var req = ReqEnvelope()
                req.payload = .reqStopReprocess(.init())
                e = req
            }
            if case .respAck(let ack) = resp.payload, ack.ok {
                // Deliberately does NOT stop polling here, and does not
                // force one extra status read either: this Ack just means
                // the cancellation was *requested* - the worker only
                // notices between files (see files_manager.
                // CancelReprocess), which can take several seconds on a
                // slow file. Reading status right now would very likely
                // still see "running" and, if that got treated as fresh
                // truth while the poll loop was torn down, the screen
                // would freeze on "running" forever with nothing left to
                // ever notice the real "stopped" that follows a few
                // seconds later - exactly the bug this comment replaced.
                // The existing pollReprocessStatusWhileRunning loop
                // (already active, since this button only shows while
                // status is "running") keeps ticking every 1.5s and exits
                // itself the moment it actually observes "stopped".
            } else if case .respAck(let ack) = resp.payload {
                showToast(.asIs(ack.errorMsg, or: { S.appSettingsReprocessStopFailed }))
            }
        } catch {
            showToast(.say { S.appSettingsReprocessStopError })
        }
    }

    func loadSettings() async {
        do {
            let resp = try await ws.request { $0.payload = .reqGetSettings(Msg_GetSettings()) }
            if case .respSettings(let s) = resp.payload {
                faceRecognitionEnabled = s.faceRecognitionEnabled
                imageTaggingEnabled = s.imageTaggingEnabled
                // People shows only while it is on (FaceRecognition.swift).
                FaceRecognition.shared.set(s.faceRecognitionEnabled)
                LanguageSettings.shared.deviceSaid(settings: s)
            }
        } catch { /* leave blank - the rest of the screen still works */ }
    }

    func toggleFaceRecognition(_ enabled: Bool) async {
        savingFaceRecognition = true
        defer { savingFaceRecognition = false }
        var req = Msg_SetFaceRecognitionEnabled()
        req.enabled = enabled
        do {
            let resp = try await ws.request { $0.payload = .reqSetFaceRecognitionEnabled(req) }
            if case .respAck(let ack) = resp.payload, ack.ok {
                faceRecognitionEnabled = enabled
                // People comes and goes at once.
                FaceRecognition.shared.set(enabled)
            } else {
                // Revert the optimistic toggle - see the Toggle binding below.
                faceRecognitionEnabled = !enabled
                if case .respAck(let ack) = resp.payload {
                    showToast(.asIs(ack.errorMsg, or: { S.appSettingsErrorUpdateFailed }))
                }
            }
        } catch {
            faceRecognitionEnabled = !enabled
            showToast(.say { S.appSettingsErrorSetting })
        }
    }

    func toggleImageTagging(_ enabled: Bool) async {
        savingImageTagging = true
        defer { savingImageTagging = false }
        var req = Msg_SetImageTaggingEnabled()
        req.enabled = enabled
        do {
            let resp = try await ws.request { $0.payload = .reqSetImageTaggingEnabled(req) }
            if case .respAck(let ack) = resp.payload, ack.ok {
                imageTaggingEnabled = enabled
            } else {
                imageTaggingEnabled = !enabled
                if case .respAck(let ack) = resp.payload {
                    showToast(.asIs(ack.errorMsg, or: { S.appSettingsErrorUpdateFailed }))
                } else if resp.error {
                    showToast(.asIs(resp.errorMessage, or: { S.appSettingsErrorUpdateFailed }))
                }
            }
        } catch {
            imageTaggingEnabled = !enabled
            showToast(.say { S.appSettingsErrorSetting })
        }
    }

    /// Encrypts old/new password with this connection's public key (same
    /// RSA-OAEP flow used at sign-in, see PwCrypto.swift / issue #2) and
    /// updates the locally stored password on success so future
    /// reconnects authenticate with the new one.
    func changePassword(secrets: SecretsStore) async {
        guard !oldKey.isEmpty, !newKey.isEmpty else { showToast(.say { S.appSettingsPasswordFillAll }); return }
        guard newKey == confirmKey else { showToast(.say { S.appSettingsPasswordMismatch }); return }

        savingKey = true
        defer { savingKey = false }
        do {
            let pubKeyResp = try await ws.request { $0.payload = .reqGetPubKey(Msg_GetPubKey()) }
            guard case .respPubKey(let pubKey) = pubKeyResp.payload else {
                showToast(.say { S.appSettingsPasswordNoKey })
                return
            }
            let encOld = try PwCrypto.encryptPassword(oldKey, pubKeyDER: pubKey.publicKey)
            let encNew = try PwCrypto.encryptPassword(newKey, pubKeyDER: pubKey.publicKey)

            var req = Msg_ChangeKey()
            req.oldKey = encOld
            req.newKey = encNew
            let resp = try await ws.request { $0.payload = .reqChangeKey(req) }
            if case .respAck(let ack) = resp.payload, ack.ok {
                secrets.saveCredentials(endpoint: secrets.endpoint, password: newKey)
                oldKey = ""; newKey = ""; confirmKey = ""
                showToast(.say { S.appSettingsPasswordChanged })
            } else if case .respAck(let ack) = resp.payload {
                showToast(.asIs(ack.errorMsg, or: { S.appSettingsPasswordFailed }))
            } else {
                showToast(.say { S.appSettingsErrorUnexpected })
            }
        } catch {
            showToast(.say { S.appSettingsPasswordError(detail: error.localizedDescription) })
        }
    }

    private func showToast(_ m: SettingsText) {
        toast = m
        Task { [weak self] in
            try? await Task.sleep(nanoseconds: 2_500_000_000)
            if self?.toast == m { self?.toast = nil }
        }
    }
}

@MainActor
final class StatusViewModel: ObservableObject {
    private let ws = OTCConnection.shared
    @Published var status: Msg_Status?
    @Published var error: SettingsText?
    /// The last ask's error, worded now (the menu's storage line reads it too).
    var errorText: String? { error?.text }
    private var pollTask: Task<Void, Never>?

    func start() {
        guard pollTask == nil else { return }
        pollTask = Task { [weak self] in
            while let self, !Task.isCancelled {
                await self.fetch()
                try? await Task.sleep(nanoseconds: 5_000_000_000)
            }
        }
    }

    func stop() {
        pollTask?.cancel()
        pollTask = nil
    }

    private func fetch() async {
        do {
            let resp = try await ws.request { $0.payload = .reqGetStatus(Msg_GetStatus()) }
            if resp.error {
                error = .asIs(resp.errorMessage)
                status = nil
            } else if case .respStatus(let s) = resp.payload {
                status = s
                error = nil
                LanguageSettings.shared.deviceSaid(status: s)
            }
        } catch {
            self.error = .error(error)
        }
    }
}

struct SettingsView: View {
    @EnvironmentObject var secrets: SecretsStore
    @EnvironmentObject var upload: UploadModel
    @EnvironmentObject var notifications: NotificationsModel
    @StateObject private var device = DeviceSettingsViewModel()
    @StateObject private var status = StatusViewModel()
    // Settings > Users; its sheet is presented here (usersManagementSheets).
    @StateObject private var users = UsersManagementViewModel()
    // Issue #190: the route line under Connection.
    @ObservedObject private var connection = OTCConnection.shared
    // The Language row's value.
    @ObservedObject private var language = LanguageSettings.shared
    @State private var confirmLogout = false
    // Whether the app may read the photo library (full or limited): the
    // Authorize Photos Access button shows only while it can't.
    @State private var photosAuthorized = SettingsView.photosAuthorizedNow()
    // Only the photos picked in iOS's prompt: the sync says so.
    @State private var photosLimited = SettingsView.photosLimitedNow()
    // The albums the Albums row counts from (PhotoSources.albums): nil
    // until they are known. What is off is counted as it changes, without
    // reading the library again.
    @State private var albumIDs: [String]?
    // Bumped on coming back to the app, so the Albums row counts again.
    @State private var resumed = 0
    // The password change's fields, for Next on the keyboard.
    @FocusState private var newKeyFocused: Bool
    @FocusState private var confirmKeyFocused: Bool

    var body: some View {
        NavigationStack {
            // Issue #183: an update alert (or the critical banner) lands
            // here scrolled to the update section.
            ScrollViewReader { proxy in
            Form {
                // Issue #84: first thing in Settings, not bundled in with
                // friend management on its own tab (which used to be the
                // only place this was reachable at all).
                ProfileEditorSection()

                // The language of every app signed in to the device
                // (LanguageView); only while this build has more than one.
                if language.pickerShown {
                    Section {
                        NavigationLink {
                            LanguageView()
                        } label: {
                            LabeledContent(S.appSettingsLanguage, value: language.currentName)
                        }
                    }
                }

                // The first-run tour again, from its first page (Tour.swift).
                Section(header: Text(S.appTourSettingsTitle), footer: Text(S.appTourSettingsFooter)) {
                    Button(S.appTourShow) { TourModel.shared.replay() }
                }

                Section(header: Text(S.appSettingsStatusTitle)) {
                    StatusSectionContent(vm: status)
                }

                // The device's accounts, right under its status (the
                // owner's most used part of Settings).
                UsersManagementSection(vm: users)

                // Issue #122: the only place upload progress is shown. There
                // used to be a hairline over every tab as well (issue #14);
                // a background sync isn't worth announcing everywhere.
                // Also while a run waits: for Wi-Fi, or room on the device.
                if upload.totalPending > 0 || upload.isUploading || upload.halt != nil {
                    Section(header: Text(S.appSettingsUploadsTitle)) {
                        UploadDetail(upload: upload)
                    }
                }

                // Issue #180: galleries and zip links shared from Images
                // and Files - when they expire, how often they were opened,
                // and a delete that stops them working.
                Section(header: Text(S.appSettingsSharingTitle)) {
                    NavigationLink(S.appSettingsLinksTitle) { SharedLinksView() }
                }

                // Issue #94: in-place updates. Renders nothing on a
                // non-primary instance - see UpdateSection.
                UpdateSection()
                    .id(Self.updateSectionID)

                Section(header: Text(S.appSettingsPasswordTitle)) {
                    PasswordField(title: S.appSettingsPasswordCurrent, text: $device.oldKey, submitLabel: .next,
                                  onSubmit: { newKeyFocused = true })
                    PasswordField(title: S.appSettingsPasswordNew, text: $device.newKey, contentType: .newPassword, submitLabel: .next,
                                  onSubmit: { confirmKeyFocused = true }, focus: $newKeyFocused)
                    PasswordField(title: S.appSettingsPasswordConfirm, text: $device.confirmKey, contentType: .newPassword,
                                  onSubmit: changePassword, focus: $confirmKeyFocused)
                    Button(device.savingKey ? S.appSettingsPasswordChanging : S.appSettingsPasswordChange, action: changePassword)
                    .buttonStyle(.bordered)
                    .disabled(device.savingKey || device.oldKey.isEmpty || device.newKey.isEmpty)
                }

                Section(header: Text(S.appSettingsSyncTitle)) {
                    Toggle(S.appSettingsSyncWifiOnly, isOn: $secrets.wifiOnly)
                    Toggle(S.appSettingsSyncVideos, isOn: $secrets.includeVideos)
                    Toggle(S.iosSettingsSyncIcloud, isOn: $secrets.downloadFromiCloud)
                    // The albums whose photos aren't backed up (AlbumsView).
                    if photosAuthorized {
                        NavigationLink {
                            AlbumsView()
                        } label: {
                            LabeledContent(S.iosSettingsAlbums, value: albumIDs.map {
                                PhotoSources.rowValue(existing: $0, skipped: secrets.skippedAlbums)
                            } ?? "")
                        }
                    }
                    if photosLimited {
                        LimitedAccessNotice { resumed += 1 }
                    }
                    // Only while the app can't read the library.
                    if !photosAuthorized {
                        AuthorizePhotosButton { refreshPhotosAuthorized() }
                    }
                }

                Section(footer: Text(S.appSettingsSyncFooter)) {
                    Button(S.appSettingsSyncAll) {
                        Task {
                            // The watermark PhotoSync syncs from - cleared,
                            // so the run below starts at the beginning
                            // (it used to be set to now here, which made
                            // this button sync nothing at all). A run going
                            // goes through once more from there (IOS-05).
                            PhotoSync.shared.syncAll()
                            try? await PhotoSync.shared.runForeground()
                        }
                    }
                    .buttonStyle(.bordered)
                    Button(S.appSettingsSyncFromNow) {
                        PhotoSync.shared.syncFromNow()
                    }
                    .buttonStyle(.bordered)
                }

                FreeUpSpaceSection()

                // This phone's own, as the sync options: how much room the
                // grids' thumbnails may take here (ThumbDiskCache).
                ThumbCacheSection()

                Section(
                    header: Text(S.appSettingsTaggingTitle),
                    footer: Text(S.appSettingsTaggingFooter)
                ) {
                    Toggle(
                        S.appSettingsTaggingTitle,
                        isOn: Binding(
                            get: { device.imageTaggingEnabled },
                            set: { newValue in Task { await device.toggleImageTagging(newValue) } }
                        )
                    )
                    .disabled(device.savingImageTagging)
                }

                Section(
                    header: Text(S.appSettingsFacesTitle),
                    footer: Text(S.appSettingsFacesFooter)
                ) {
                    Toggle(
                        S.appSettingsFacesToggle,
                        isOn: Binding(
                            get: { device.faceRecognitionEnabled },
                            set: { newValue in Task { await device.toggleFaceRecognition(newValue) } }
                        )
                    )
                    .disabled(device.savingFaceRecognition)
                }

                ConnectionSettingsSection(secrets: secrets) { confirmLogout = true }

                // Settings › Wi-Fi (release 140): the network the device
                // uses. Primary-only, where the device manages its own
                // Wi-Fi - see WifiSection.
                WifiSection()

                // Issue #80: Tailscale Funnel as an alternative to the
                // bridge. Primary-only too - see TailscaleSection.
                TailscaleSection()

                BridgeAccountSection()

                Section(
                    header: Text(S.appSettingsReprocessTitle),
                    footer: Text(S.appSettingsReprocessFooter)
                ) {
                    if device.reprocessStatus == "running" {
                        VStack(alignment: .leading, spacing: 6) {
                            ProgressView(value: device.reprocessTotal > 0 ? Double(device.reprocessProcessed) / Double(device.reprocessTotal) : 0)
                                .progressViewStyle(.linear)
                            HStack {
                                Text(S.appSettingsReprocessRunning(percent: device.reprocessPercent, done: Int(device.reprocessProcessed), total: Int(device.reprocessTotal)))
                                    .font(.caption)
                                    .foregroundColor(.secondary)
                                Spacer()
                                Button(device.cancellingReprocess ? S.appSettingsReprocessStopping : S.commonCancel) {
                                    Task { await device.stopReprocess() }
                                }
                                .buttonStyle(.bordered)
                                .controlSize(.small)
                                .tint(.red)
                                .disabled(device.cancellingReprocess)
                            }
                        }
                    } else if device.reprocessStatus == "stopped" {
                        Text(S.appSettingsReprocessStopped(percent: device.reprocessPercent, done: Int(device.reprocessProcessed), total: Int(device.reprocessTotal)))
                            .font(.caption).foregroundColor(.secondary)
                        HStack {
                            Button(device.startingReprocess ? S.appSettingsReprocessStarting : S.appSettingsReprocessResume) {
                                device.reprocessConfirmAction = .resume
                            }
                            .buttonStyle(.bordered)
                            .disabled(device.startingReprocess)
                            Spacer()
                            Button(S.appSettingsReprocessStartOver) {
                                device.reprocessConfirmAction = .restart
                            }
                            .buttonStyle(.bordered)
                            .disabled(device.startingReprocess)
                        }
                    } else {
                        Button(device.startingReprocess ? S.appSettingsReprocessStarting : S.appSettingsReprocessAll) {
                            device.reprocessConfirmAction = .restart
                        }
                        .buttonStyle(.bordered)
                        .disabled(device.startingReprocess)
                        if device.reprocessStatus == "completed" {
                            Text(S.appSettingsReprocessCompleted(count: Int(device.reprocessProcessed)))
                                .font(.caption).foregroundColor(.secondary)
                        } else if device.reprocessStatus == "failed" {
                            Text(S.appSettingsReprocessFailed(count: Int(device.reprocessProcessed)))
                                .font(.caption).foregroundColor(.red)
                        }
                    }
                }

                // The device's logs, live, with Share and "Send to us".
                // Renders nothing on a non-primary instance - see
                // LogsSection.
                LogsSection()
            }
            // No nav title (issue #19): the tab bar already labels this
            // screen "Settings". Still .inline so there's no big empty
            // title bar left behind.
            .navigationBarTitleDisplayMode(.inline)
            // That bar shows nothing, so an iPad window's controls would
            // lie over the first section (AppMenu.swift).
            .modifier(ScrollClearsWindowControls())
            .overlay(alignment: .top) {
                if let toast = device.toast {
                    Text(toast.text)
                        .padding(.horizontal, 12).padding(.vertical, 8)
                        .background(.ultraThinMaterial, in: Capsule())
                        .padding(.top, 8)
                }
            }
            .onAppear { scrollToUpdatesIfAsked(proxy) }
            .onChange(of: notifications.pendingDeepLink) { _, _ in scrollToUpdatesIfAsked(proxy) }
            }
        }
        .task {
            await device.loadSettings()
            status.start()
            // Covers both "a run is already in progress" (this screen was
            // reopened while it's going) and "was interrupted" (status is
            // still 'running' from a server restart - the poll loop just
            // sees it hasn't advanced until the user starts it again,
            // which is fine, that's Reprocess's own resume path to drive).
            await device.loadReprocessStatus()
            if device.reprocessStatus == "running" {
                device.pollReprocessStatusWhileRunning()
            }
        }
        .onDisappear {
            status.stop()
            device.stopPollingReprocessStatus()
        }
        .usersManagementSheets(users)
        // Back from iOS Settings, where access may just have been given.
        .onReceive(NotificationCenter.default.publisher(for: UIApplication.didBecomeActiveNotification)) { _ in
            refreshPhotosAuthorized()
            resumed += 1
        }
        // The albums listed, read off the main thread whenever the list may
        // have changed (not when one is turned off or on).
        .task(id: AlbumsRow(includeVideos: secrets.includeVideos, authorized: photosAuthorized, resumed: resumed)) {
            guard photosAuthorized else { return }
            let includeVideos = secrets.includeVideos
            let existing = await Task.detached(priority: .utility) {
                PhotoSources.albums(includeVideos: includeVideos).map(\.id)
            }.value
            // Replaced by a newer read meanwhile: that one's list wins.
            guard !Task.isCancelled else { return }
            albumIDs = existing
        }
        .alert(S.appSettingsConnectionLogOutTitle, isPresented: $confirmLogout) {
            Button(S.appSettingsConnectionLogOutConfirm, role: .destructive) { logOut() }
            Button(S.commonCancel, role: .cancel) {}
        } message: {
            Text(S.appSettingsConnectionLogOutMessage)
        }
        .confirmationDialog(
            device.reprocessConfirmAction == .resume
                ? S.appSettingsReprocessConfirmResume
                : S.appSettingsReprocessConfirmRestart,
            isPresented: Binding(
                get: { device.reprocessConfirmAction != nil },
                set: { if !$0 { device.reprocessConfirmAction = nil } }
            ),
            titleVisibility: .visible
        ) {
            Button(device.reprocessConfirmAction == .resume ? S.appSettingsReprocessResumeButton : S.appSettingsReprocessRestartButton, role: .destructive) {
                Task { await device.startReprocess(forceRestart: device.reprocessConfirmAction == .restart) }
            }
            Button(S.commonCancel, role: .cancel) {}
        }
    }
}

extension SettingsView {
    fileprivate static let updateSectionID = "updates"

    /// Change Password, and Go on the confirmation's keyboard.
    fileprivate func changePassword() {
        guard !device.savingKey, !device.oldKey.isEmpty, !device.newKey.isEmpty else { return }
        Task { await device.changePassword(secrets: secrets) }
    }

    /// Full or limited access to the photo library: either lets the sync run.
    fileprivate static func photosAuthorizedNow() -> Bool {
        let status = PHPhotoLibrary.authorizationStatus(for: .readWrite)
        return status == .authorized || status == .limited
    }

    /// Access to only the photos picked in iOS's prompt.
    fileprivate static func photosLimitedNow() -> Bool {
        PHPhotoLibrary.authorizationStatus(for: .readWrite) == .limited
    }

    fileprivate func refreshPhotosAuthorized() {
        photosAuthorized = Self.photosAuthorizedNow()
        photosLimited = Self.photosLimitedNow()
    }

    /// What the albums the Albums row counts from depend on.
    fileprivate struct AlbumsRow: Equatable {
        let includeVideos: Bool
        let authorized: Bool
        let resumed: Int
    }

    /// Issue #183: consumes a `.updates` deep link. The short wait lets a
    /// tab that was just switched to (and built) lay out first.
    fileprivate func scrollToUpdatesIfAsked(_ proxy: ScrollViewProxy) {
        guard notifications.pendingDeepLink == .updates else { return }
        notifications.pendingDeepLink = nil
        Task {
            try? await Task.sleep(for: .milliseconds(300))
            withAnimation { proxy.scrollTo(Self.updateSectionID, anchor: .top) }
        }
    }

    /// Log Out (the same as SettingsView.kt's logOut): stop everything that
    /// talks to the device, forget what it told us, wipe what this phone
    /// stores. RootView shows onboarding the moment the secrets clear, and
    /// MainView going away takes every per-tab view model with it.
    fileprivate func logOut() {
        status.stop()
        device.stopPollingReprocessStatus()
        AppLogOut.run(secrets: secrets)
    }
}

/// Settings › Connection (IOS-03): the fields edit drafts, seeded from what
/// is saved, and only Save Connection writes them, as on Android - every
/// reconnect dials what is saved, emptying the device name used to send the
/// app back to Welcome at once, and turning a sync switch saved a half-typed
/// address and password with it. What is saved changing underneath (a
/// password changed above, the bridge left) shows in the fields.
private struct ConnectionSettingsSection: View {
    @ObservedObject var secrets: SecretsStore
    @ObservedObject private var connection = OTCConnection.shared
    var onLogOut: () -> Void
    @State private var endpoint: String
    @State private var password: String
    /// Bumped when the saved address changes: the fields are made again
    /// from the new draft, their name and mode with it.
    @State private var fieldsID = 0
    @FocusState private var passwordFocused: Bool

    init(secrets: SecretsStore, onLogOut: @escaping () -> Void) {
        self.secrets = secrets
        self.onLogOut = onLogOut
        _endpoint = State(initialValue: secrets.endpoint)
        _password = State(initialValue: secrets.password)
    }

    var body: some View {
        Section(header: Text(S.appSettingsConnectionTitle)) {
            // Issue #121: name for the bridge, or a custom address.
            ConnectionEndpointFields(endpoint: $endpoint, onSubmit: { passwordFocused = true })
                .id(fieldsID)
            PasswordField(title: S.commonPassword, text: $password, onSubmit: save, focus: $passwordFocused)
            // Issue #190: which way the app reaches the device now.
            if let route = connection.route {
                Label(route.description, systemImage: route == .home ? "house" : "cloud")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
            Text(S.appSettingsConnectionDeviceId(id: secrets.deviceId))
                .font(.caption)
                .foregroundColor(.secondary)
            Button(S.appSettingsConnectionSave, action: save)
                .buttonStyle(.bordered)
                .disabled(!canSave)
            Button(S.appSettingsConnectionLogOut, role: .destructive, action: onLogOut)
                .buttonStyle(.bordered)
        }
        .onChange(of: secrets.endpoint) { _, saved in
            endpoint = saved
            fieldsID &+= 1
        }
        .onChange(of: secrets.password) { _, saved in password = saved }
    }

    private var trimmed: String { endpoint.trimmingCharacters(in: .whitespacesAndNewlines) }

    /// Never an empty address or password: that would be Log Out without
    /// the wipe.
    private var canSave: Bool { !trimmed.isEmpty && !password.isEmpty }

    private func save() {
        guard canSave else { return }
        secrets.saveCredentials(endpoint: trimmed, password: password)
        OTCConnection.shared.invalidate()
    }
}

/// Log Out, shared by Settings and the "device isn't available" card
/// (whose close button is how a phone leaves a device that died, to set a
/// new one up): stop everything that talks to the device, forget what it
/// told us, wipe what this phone stores. RootView shows the connection
/// screen the moment the secrets clear. Same as LogOut.kt.
@MainActor
enum AppLogOut {
    /// `keepDevice`: leaving from the "isn't available"/"can't connect"
    /// screens - everything is wiped as for Log Out, but the device's
    /// address and password are kept for the connection screen to fill in
    /// (SecretsStore.lastDevice): `lastDevice` when the card's form has
    /// them (it edits drafts, IOS-03), else the saved ones, as Android's
    /// logOut(lastDevice =). Settings' Log Out forgets them too.
    static func run(secrets: SecretsStore, unregisterPush: Bool = true, keepDevice: Bool = false,
                    lastDevice: (String, String)? = nil) {
        let last = lastDevice ?? (secrets.endpoint, secrets.password)
        Task {
            // A photo sync in progress owns the connection: stop it first,
            // or the request below queues behind its uploads and Log Out
            // looks like it does nothing.
            PhotoSync.shared.cancel()
            // Issue #131: tell the device to forget this phone's push
            // token first, best effort - it goes on pushing to a phone
            // that is no longer signed in otherwise. Everything below
            // happens either way, and within seconds. Skipped when the
            // device can't be reached anyway.
            if unregisterPush, let token = UserDefaults.standard.string(forKey: "apnsToken"), !token.isEmpty {
                _ = try? await withThrowingTaskGroup(of: Void.self) { group in
                    group.addTask {
                        _ = try await OTCConnection.shared.request { req in
                            var un = Msg_UnregisterApnsToken()
                            un.token = token
                            req.payload = .reqUnregisterApnsToken(un)
                        }
                    }
                    group.addTask { try await Task.sleep(for: .seconds(5)) }
                    try await group.next()
                    group.cancelAll()
                }
            }
            // Again, with no await before the wipe: a sync that started
            // during the wait above (a Photos change) still had the
            // signed-in secrets, and would write its watermark and asset
            // cache after it.
            PhotoSync.shared.cancel()
            // The tour closes; that it was seen stays (Tour.swift), so the
            // next sign-in doesn't show it again.
            TourModel.shared.reset()
            NotificationsModel.shared.reset()
            UpdateAlertModel.shared.reset()
            UploadModel.shared.reset()
            SocialFeedViewModel.shared.reset()
            FaceRecognition.shared.reset()
            FilesNav.shared.reset()
            OTCConnection.shared.reset()
            MediaStream.reset()
            SyncScheduler.cancel()
            AssetSyncCache.shared.clear()
            // The device's thumbnails: none kept, none stored until the
            // next sign-in (secrets.logOut empties Caches too). The
            // viewer's full-size photos go as well.
            ThumbDiskCache.shared.forget()
            FullPhotoCache.shared.clear()
            GridThumbCache.removeAll()
            secrets.logOut()
            // The language stays as chosen (its own defaults survive the
            // wipe); only what belonged to this device goes. Leaving from
            // the cards, a change the device never got goes to the next
            // device (a new one set up from the connection screen).
            LanguageSettings.shared.loggedOut(carryPending: keepDevice)
            // Its size went with the defaults: back to 1 GB.
            ThumbDiskCache.shared.limitBytes = ThumbCacheLimit.thumbnailBytes(of: ThumbCacheLimit.stored())
            FullPhotoCache.shared.limitBytes = ThumbCacheLimit.fullPhotoBytes(of: ThumbCacheLimit.stored())
            if keepDevice {
                SecretsStore.saveLastDevice(endpoint: last.0, password: last.1)
            } else {
                SecretsStore.clearLastDevice()
            }
        }
    }
}

/// Settings' "Thumbnail Cache": the most the grids' thumbnails and the
/// viewer's full-size photos may take on this phone together
/// (ThumbCacheLimit.choices, 1 GB unless chosen; a tenth of it for the
/// photos), what they take now, and a button that deletes them. Android's
/// ThumbCacheSettings.
@MainActor
final class ThumbCacheSettings: ObservableObject {
    @Published private(set) var limit: Int64
    /// What the thumbnails and the full-size photos take: nil until the
    /// thumbnail cache has counted its own (or while no device is signed
    /// in to).
    @Published private(set) var used: Int64?
    @Published private(set) var clearing = false
    private let cache: ThumbDiskCache
    private let fullPhotos: FullPhotoCache
    private let defaults: UserDefaults
    /// Lets the decoded tiles go too (tests keep theirs).
    private let clearDecoded: () -> Void

    init(cache: ThumbDiskCache = .shared, fullPhotos: FullPhotoCache = .shared,
         defaults: UserDefaults = .standard,
         clearDecoded: @escaping () -> Void = {
             GridThumbCache.removeAll()
             NotificationCenter.default.post(name: .otcThumbnailCacheCleared, object: nil)
         }) {
        self.cache = cache
        self.fullPhotos = fullPhotos
        self.defaults = defaults
        self.clearDecoded = clearDecoded
        limit = ThumbCacheLimit.stored(defaults)
    }

    /// "Using 120 MB of 1 GB"; nil until counted.
    var usageText: String? {
        used.map { ThumbCacheLimit.usage($0, of: limit) }
    }

    func refresh() async {
        let thumbnails = await cache.usage()
        let photos = await fullPhotos.usage()
        used = thumbnails.map { $0 + photos }
    }

    /// Another size: kept, and the least recently used thumbnails and
    /// photos over their shares of it deleted at once.
    func choose(_ bytes: Int64) async {
        guard ThumbCacheLimit.choices.contains(bytes), bytes != limit else { return }
        limit = bytes
        ThumbCacheLimit.save(bytes, defaults)
        cache.limitBytes = ThumbCacheLimit.thumbnailBytes(of: bytes)
        fullPhotos.limitBytes = ThumbCacheLimit.fullPhotoBytes(of: bytes)
        await refresh()
    }

    /// "Clear thumbnail cache": every thumbnail on the phone goes, decoded
    /// ones too (GridThumbCache, Files' grid, the viewer's big ones:
    /// otcThumbnailCacheCleared), and the full-size photos kept for the
    /// session; the grids ask the device for them again.
    func clearAll() async {
        clearing = true
        defer { clearing = false }
        cache.clear()
        fullPhotos.clear()
        clearDecoded()
        await refresh()
    }
}

/// With the other settings of this phone (the sync options).
struct ThumbCacheSection: View {
    @StateObject private var model = ThumbCacheSettings()

    var body: some View {
        Section(
            header: Text(S.iosSettingsThumbsTitle),
            footer: Text(S.appSettingsThumbsFooter)
        ) {
            Picker(S.appSettingsThumbsMaxSize, selection: Binding(
                get: { model.limit },
                set: { v in Task { await model.choose(v) } }
            )) {
                ForEach(ThumbCacheLimit.choices, id: \.self) { bytes in
                    Text(ThumbCacheLimit.text(bytes)).tag(bytes)
                }
            }
            if let text = model.usageText {
                Text(text).foregroundStyle(.secondary)
            } else {
                HStack {
                    // Not counted yet: an ellipsis where the size goes.
                    Text(S.appShellThumbCacheUsage(used: "…", limit: ThumbCacheLimit.text(model.limit))).foregroundStyle(.secondary)
                    Spacer()
                    ProgressView()
                }
            }
            Button(S.appSettingsThumbsClear, role: .destructive) {
                Task { await model.clearAll() }
            }
            .buttonStyle(.bordered)
            .disabled(model.clearing || model.used == 0)
        }
        .task { await model.refresh() }
    }
}

/// A size the device reports in MB, readable: "4.6 GB", "984 GB", "820 MB"
/// ("4,6 GB" in Spanish). Same as sizeText in Android's SettingsView.kt.
func sizeText(_ mb: Double) -> String {
    if mb >= 100_000 { return decimalText("%.0f GB", mb / 1000) } // i18n-ignore: a size, the unit's symbol in every language
    if mb >= 1000 { return decimalText("%.1f GB", mb / 1000) } // i18n-ignore: a size, the unit's symbol in every language
    return decimalText("%.0f MB", mb) // i18n-ignore: a size, the unit's symbol in every language
}

// Issue #65: a plain, always-visible read of the RAID's own health -
// "in sync"/"syncing"/"degraded" - rather than something the user has to
// infer from whether an error banner happens to be showing.
private func raidStateLabel(_ s: Msg_Status) -> String {
    switch s.raidState {
    case .raidNone: return S.appSettingsStatusRaidNone
    case .raidInSync: return S.appSettingsStatusRaidInSync
    case .raidSyncing: return S.appSettingsStatusRaidSyncing(percent: Int(s.raidSyncPercent))
    case .raidDegraded: return S.appSettingsStatusRaidDegraded
    default: return S.appSettingsStatusRaidUnknown
    }
}

private struct StatusSectionContent: View {
    @ObservedObject var vm: StatusViewModel

    var body: some View {
        if let s = vm.status {
            let usedPct = s.raidSize > 0 ? Double(s.raidUsage) / Double(s.raidSize) * 100 : 0
            // Same threshold as the web bar (see StatusWidget.tsx): a
            // single fill color that itself says "getting full" - green
            // under 70%, amber 70-90%, red past that - rather than a fixed
            // tint that doesn't track how full the RAID actually is.
            let usedTint: Color = usedPct >= 90 ? .red : usedPct >= 70 ? .yellow : .green
            VStack(alignment: .leading, spacing: 6) {
                RaidUsageBar(percent: usedPct, tint: usedTint)
                // The % now lives inside the bar itself - just the MB
                // figures here, not a repeated "(Z%)" suffix.
                Text(S.appSettingsStatusRaidUsed(used: sizeText(Double(s.raidUsage)), total: sizeText(Double(s.raidSize))))
                    .font(.caption)
                Text(S.appSettingsStatusDisk(used: sizeText(Double(s.diskUsage)), total: sizeText(Double(s.diskSize))))
                    .font(.caption)
                Text(S.appSettingsStatusCpuMem(cpu: decimalText("%.1f", Double(s.cpuUsagePrc)), used: sizeText(Double(s.memUsage)), total: sizeText(Double(s.memSize))))
                    .font(.caption)
                Text(s.raidLevel.isEmpty
                     ? S.appSettingsStatusRaid(state: raidStateLabel(s), disks: Int(s.disks))
                     : S.appSettingsStatusRaidLevel(level: s.raidLevel, state: raidStateLabel(s), disks: Int(s.disks)))
                    .font(.caption)
                ForEach(s.errors, id: \.message) { e in
                    // The device's own words (in the request's language) after a sign.
                    Text(verbatim: "⚠️ \(e.message)").font(.caption).foregroundColor(.red)
                }
            }
        } else if let err = vm.errorText {
            Text(err).font(.caption).foregroundColor(.red)
        } else {
            ProgressView()
        }
    }
}

// A bigger, custom bar rather than the plain system ProgressView - matches
// the web version (StatusWidget.tsx/.css): tall enough to carry the % as a
// dark chip inside the bar's own right edge, so it reads whether that spot
// happens to sit over bare track or over the (light) fill color.
private struct RaidUsageBar: View {
    let percent: Double
    let tint: Color

    var body: some View {
        let clamped = min(max(percent, 0), 100)
        GeometryReader { geo in
            ZStack(alignment: .leading) {
                RoundedRectangle(cornerRadius: 7)
                    .fill(Color(.systemGray5))
                RoundedRectangle(cornerRadius: 7)
                    .fill(tint)
                    .frame(width: geo.size.width * clamped / 100)
                HStack {
                    Spacer()
                    Text(S.appSettingsStatusUsedPercent(percent: Int(clamped)))
                        .font(.system(size: 9, weight: .medium))
                        .monospacedDigit()
                        .foregroundColor(.white)
                        .padding(.horizontal, 4)
                        .padding(.vertical, 0.5)
                        .background(Color.black.opacity(0.35), in: RoundedRectangle(cornerRadius: 4))
                        .padding(.trailing, 3)
                }
            }
        }
        // Matches the bar height to the % chip's own height rather than
        // padding it out - see the web version (StatusWidget.css) for the
        // same 14px sizing.
        .frame(height: 14)
    }
}

