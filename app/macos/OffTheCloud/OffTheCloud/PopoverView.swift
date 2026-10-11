// SPDX-License-Identifier: AGPL-3.0-or-later

import SwiftUI
import AppKit

struct PopoverView: View {
    // Bound straight to the shared singletons rather than injected via
    // .environmentObject() — MenuBarExtra(.window)'s content view has a
    // real quirk where @EnvironmentObject doesn't reliably pick up changes
    // that happened before its first render (here: folders restored from
    // disk at launch, before the popover was ever opened), and a one-shot
    // `.id()` forced-refresh worked around that but also defeated the
    // *normal* re-diffing on later state changes (e.g. toggling Settings),
    // which is what let the list "reappear" before this fix existed.
    // @ObservedObject on the singleton sidesteps the whole issue: this view
    // always reads the object's live state directly, so there's no snapshot
    // to go stale in the first place.
    @ObservedObject private var settings = SettingsStore.shared
    @ObservedObject private var sync = SyncModel.shared
    @ObservedObject private var login = LoginItemSettings.shared

    @State private var showSettings = false
    // Issue #47: remote → local sync — browse the device's tree, then pick
    // a local destination for it.
    @State private var showRemotePicker = false
    // The three ways to add a folder, each with its explanation, then the
    // options of the one picked - inline, for the same reason as the
    // remote picker below.
    @State private var showAddChooser = false
    // Issue #184: the new-device wizard lives in its own window.
    @Environment(\.openWindow) private var openWindow

    var body: some View {
        // Issue #47: swapped in *inline*, not as a .sheet() — a sheet
        // presented from a MenuBarExtra(.window) popover has a real quirk
        // where any state change inside it (here: tapping a directory row)
        // makes the popover's own window lose key status and auto-close,
        // since MenuBarExtra(.window) closes itself on exactly that
        // transition. Staying inside the one already-open, already-key
        // popover window (same trick showSettings already uses below)
        // sidesteps the whole problem.
        if showAddChooser {
            AddFolderChooser(
                outOfImagesSupported: sync.outOfImagesSupported,
                uploadOnlySupported: sync.uploadOnlySupported,
                onBackup: { keepOut in showAddChooser = false; sync.addBackupFolder(outOfImages: keepOut) },
                onSyncLocal: { uploadOnly, keepOut in
                    showAddChooser = false
                    sync.addFolder(uploadOnly: uploadOnly, outOfImages: keepOut)
                },
                // No options: a folder from the device keeps what it has there.
                onSyncDevice: { showAddChooser = false; showRemotePicker = true },
                onCancel: { showAddChooser = false }
            )
            // The main panel's own margins and width, which the chooser
            // lacked - its header ran into the window's edges.
            .padding(12)
            .frame(width: 360)
        } else if showRemotePicker {
            RemoteFolderPickerView(
                onChoose: { remotePath in
                    showRemotePicker = false
                    chooseLocalDestinationAndAdd(remotePath: remotePath)
                },
                onCancel: { showRemotePicker = false }
            )
        } else {
            mainContent
        }
    }

    private var mainContent: some View {
        VStack(spacing: 12) {
            HStack {
                Text(S.deskMainTitle)
                    .font(.headline)
                Spacer()
                // Issue #193: the device's own web app, in the browser -
                // the address only; the web app asks for the password.
                // otc-sync's tray has the same item at the top.
                if settings.ready, let web = SettingsStore.webAppURL(fromDomain: settings.domain) {
                    Button {
                        NSWorkspace.shared.open(web)
                    } label: {
                        Label(S.deskMainOpenWebApp, systemImage: "globe")
                            .foregroundStyle(Color.accentColor)
                    }
                    .buttonStyle(.borderless)
                    .help(S.deskMainOpenWebAppTip(address: web.absoluteString))
                }
                // Settings inline inside the popover
                Button {
                    showSettings.toggle()
                } label: {
                    Image(systemName: "gearshape.fill")
                }
                .buttonStyle(.plain)
            }

            // Connection status
            HStack(spacing: 8) {
                Circle()
                    .fill(statusColor)
                    .frame(width: 8, height: 8)
                Text(sync.overallStatus.text)
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                Spacer()
                // Issue #69: the icon in words, for anyone who noticed
                // it change colour.
                if sync.raidHealth != .unknown {
                    StorageStatusView(health: sync.raidHealth, status: sync.deviceStatus)
                }
            }

            // Issue #183: a major or critical device update waiting.
            if let alert = sync.updateAlert {
                UpdateAlertView(alert: alert)
            }

            // Nothing configured: the settings are the first thing shown.
            if showSettings || !settings.ready {
                SettingsInlineView(onSetUpDevice: openSetupWizard)
            }

            // Folders list — no ScrollView: the panel itself grows to fit
            // however many folders there are, so they're all visible at
            // once (per your call). MenuBarExtra(.window) sizes the popover
            // from this content's own ideal height, so a plain VStack with
            // no height cap is exactly what makes that "fit everything, no
            // scrolling" behavior happen.
            VStack(spacing: 8) {
                if sync.folders.isEmpty && sync.remoteFolders.isEmpty {
                    Text(S.deskMainNoFolders)
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                        .padding(.top, 24)
                } else {
                    ForEach(sync.folders) { f in
                        FolderRow(folder: f,
                                  images: sync.outOfImagesState(for: f.id),
                                  imagesNote: sync.outOfImagesNotes[f.id],
                                  setOutOfImages: { sync.setOutOfImages(f.id, keepOut: $0) },
                                  remove: { sync.removeFolder(f) })
                    }
                    // Issue #47: remote → local mirrors, shown alongside
                    // the (local → remote) upload folders above — same row
                    // style, a down-arrow instead of a plain folder icon is
                    // the only thing distinguishing direction.
                    ForEach(sync.remoteFolders) { f in
                        RemoteFolderRow(folder: f,
                                        uploadOnly: sync.uploadOnlyState(for: f.id),
                                        uploadOnlyNote: sync.uploadOnlyNotes[f.id],
                                        images: sync.outOfImagesState(for: f.id),
                                        imagesNote: sync.outOfImagesNotes[f.id],
                                        setOutOfImages: { sync.setOutOfImages(f.id, keepOut: $0) },
                                        remove: { sync.removeRemoteFolder(f) })
                    }
                }
            }
            .padding(.vertical, 4)

            // Start at login, offered once (App Store guideline 2.4.5:
            // never without consent). Below the folders, above the actions:
            // the top belongs to the status and the device's alerts, and a
            // one-time suggestion shouldn't push them down. Not while
            // Settings is open, whose checkbox is the same choice.
            if login.offerDue && settings.ready && !showSettings {
                LoginItemOffer { login.choose($0) }
            }

            HStack {
                Button {
                    showAddChooser = true
                } label: {
                    Label(S.deskMainAddFolder, systemImage: "plus.circle.fill")
                }
                .buttonStyle(.borderless)
                .fixedSize()
                Spacer()
                // App Store review: a menu bar app (LSUIElement - no Dock
                // icon, no app menu) must still offer a way to quit, and
                // this popover is its only UI. ⌘Q works while it is open,
                // as in any app. Same as the tray's "Quit" in otc-sync.
                Button {
                    NSApplication.shared.terminate(nil)
                } label: {
                    Label(S.macosPopoverQuit, systemImage: "power")
                }
                .buttonStyle(.borderless)
                .keyboardShortcut("q", modifiers: .command)
                .help(S.macosPopoverQuitTip)
            }
        }
        .padding(12)
        .frame(width: 360) // similar to OneDrive panel
        .onAppear {
            // Start binding only once; safe if already bound.
            sync.bind(settings: settings)
            SetupWizardSession.shared.bringToFront()
        }
        // onAppear alone isn't guaranteed on every reopening of a
        // MenuBarExtra window; its becoming key is.
        .onReceive(NotificationCenter.default.publisher(for: NSWindow.didBecomeKeyNotification)) { note in
            let wizard = SetupWizardSession.shared
            // The popover is the app's only untitled window (open panels and
            // alerts are titled, and must stay in front of the wizard).
            if let w = note.object as? NSWindow, w !== wizard.window, !w.styleMask.contains(.titled) {
                wizard.bringToFront()
                // Issue #190: the home network, if it answers now.
                sync.popoverOpened()
                // A change made in System Settings > Login Items.
                login.refresh()
            }
        }
    }

    /// Opens the wizard in front: a menu bar app isn't the active app.
    private func openSetupWizard() {
        openWindow(id: "setup")
        NSApp.activate()
    }

    /// Same NSOpenPanel SyncModel.addFolder() uses for a local folder — the
    /// destination for the remote directory just picked, created if it
    /// doesn't already exist so a fresh empty folder is a one-click option.
    private func chooseLocalDestinationAndAdd(remotePath: String) {
        let panel = NSOpenPanel()
        panel.canCreateDirectories = true
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        panel.prompt = S.commonChoose
        panel.message = S.macosPopoverChooseDownload(path: remotePath)
        if runFolderPanel(panel) == .OK, let url = panel.url {
            sync.addRemoteFolder(remotePath: remotePath, localURL: url)
        }
    }

    private var statusColor: Color {
        switch sync.overallStatus {
        case .connected: return .green
        case .disconnected, .connecting: return .yellow
        case .missingSettings, .wrongPassword, .tooManyAttempts: return .red
        case .notConnected, .offline: return .gray
        }
    }
}

// A one-way backup: an up-arrow badge (the two-way rows have circling
// arrows) and "Backup" under the name.
struct FolderRow: View {
    let folder: SyncModel.TrackedFolder
    /// Issue #192: how it stands with Images, the device's refusal of its
    /// last request to show it, and what confirming the eye button asks.
    let images: OutOfImagesState
    let imagesNote: String?
    let setOutOfImages: (Bool) -> Void
    let remove: () -> Void
    @State private var confirmImages = false

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 8) {
                Image(systemName: "folder.fill")
                    .foregroundStyle(Color.accentColor)
                    .overlay(alignment: .bottomTrailing) {
                        Image(systemName: "arrow.up.circle.fill")
                            .font(.system(size: 9))
                            .foregroundStyle(.orange)
                            .background(Circle().fill(.white))
                            .offset(x: 3, y: 3)
                    }
                VStack(alignment: .leading, spacing: 1) {
                    Text(folder.url.lastPathComponent)
                        .lineLimit(1)
                    Text(S.macosFoldersBackupLine)
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }
                Spacer()
                FolderStateView(state: folder.state, watchingLabel: S.deskMainFolderBackedUp)
                OutOfImagesButton(state: images, note: imagesNote) { confirmImages = true }
                Button(role: .destructive) {
                    remove()
                } label: {
                    Image(systemName: "minus.circle")
                }.buttonStyle(.plain)
            }
            if confirmImages {
                OutOfImagesConfirmation(name: folder.url.lastPathComponent, keepOut: !images.isKeptOut,
                                        onCancel: { confirmImages = false },
                                        onConfirm: { keepOut in confirmImages = false; setOutOfImages(keepOut) })
            }
        }
        .padding(8)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 10))
    }
}

// Issue #47: remote → local mirror — same row shape as FolderRow, a
// down-arrow badge on the folder icon and the remote path as a subtitle
// are the only things distinguishing direction. Shares FolderStateView
// with FolderRow rather than re-deriving the same progress/watching/error
// display for this direction too.
struct RemoteFolderRow: View {
    let folder: SyncModel.RemoteFolder
    /// Issue #132: its upload-only request on its way, and the device's
    /// refusal of it (UploadOnlyBadge).
    let uploadOnly: UploadOnlyRequestState
    let uploadOnlyNote: String?
    /// Issue #192: as FolderRow's.
    let images: OutOfImagesState
    let imagesNote: String?
    let setOutOfImages: (Bool) -> Void
    let remove: () -> Void
    @State private var confirmImages = false

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 8) {
                Image(systemName: "folder.fill")
                    .foregroundStyle(Color.accentColor)
                    .overlay(alignment: .bottomTrailing) {
                        // Every folder is two-way.
                        Image(systemName: "arrow.triangle.2.circlepath.circle.fill")
                            .font(.system(size: 9))
                            .foregroundStyle(.blue)
                            .background(Circle().fill(.white))
                            .offset(x: 3, y: 3)
                    }
                VStack(alignment: .leading, spacing: 1) {
                    Text(folder.localURL.lastPathComponent)
                        .lineLimit(1)
                    Text(folder.remotePath)
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                        .truncationMode(.head)
                        .help(folder.remotePath)
                }
                Spacer()
                FolderStateView(state: folder.state, watchingLabel: S.deskMainFolderSynced)
                UploadOnlyBadge(state: uploadOnly, note: uploadOnlyNote)
                OutOfImagesButton(state: images, note: imagesNote) { confirmImages = true }
                Button(role: .destructive) {
                    remove()
                } label: {
                    Image(systemName: "minus.circle")
                }.buttonStyle(.plain)
            }
            if confirmImages {
                OutOfImagesConfirmation(name: folder.localURL.lastPathComponent, keepOut: !images.isKeptOut,
                                        onCancel: { confirmImages = false },
                                        onConfirm: { keepOut in confirmImages = false; setOutOfImages(keepOut) })
            }
        }
        .padding(8)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 10))
    }
}

/// Issue #192's words, the same on every client (otc-sync's tray/images.go,
/// iOS and Android's OutOfImagesText). Buttons are Title Case here, as the
/// Mac's other buttons; the checkbox is sentence case, as "Start at login".
/// Computed: a view that reads them shows the language in effect.
enum OutOfImagesText {
    static var toggle: String { S.deskMainImagesKeepOut }
    static var keep: String { S.deskMainImagesKeepButton }
    static var show: String { S.deskMainImagesShowButton }
    static var state: String { S.deskFoldersImagesKeptOut }
    /// The add flow's: the folder may be on the device already (one synced
    /// from it, or added again), and keeping it out deletes the tags and
    /// faces found there (otc-sync's engine.OutOfImagesAddCaption).
    static var addCaption: String { S.deskMainImagesAddCaption }
    static var addInfo: String { S.macosImagesAddInfo }
    static var keepMessage: String { S.deskMainImagesKeepMessage }
    static var showMessage: String { S.deskMainImagesShowMessage }
    static var needsUpdate: String { S.deskMainImagesNeedsUpdate }
    static func keepTitle(_ name: String) -> String { S.deskMainImagesKeepTitle(name: name) }
    static func showTitle(_ name: String) -> String { S.deskMainImagesShowTitle(name: name) }
    static func byParent(_ parent: String) -> String { S.deskMainImagesByParent(parent: parent) }
}

/// Issue #132's words, the same on every client (otc-sync's
/// engine/upload_only.go, with "this computer" for "this Mac").
enum UploadOnlyText {
    static var toggle: String { S.deskMainUploadOnly }
    static var caption: String { S.deskMainUploadOnlyCaption }
    /// What it means for a two-way folder, in one sentence, and how it is
    /// undone, which brings back what was deleted here meanwhile
    /// (SyncPaths.baselines) - the add flow's (i).
    static var addInfo: String { S.macosUploadOnlyAddInfo }
    /// The backup's fixed line: always on.
    static var backup: String { S.deskMainUploadOnlyBackup }
    static var needsUpdate: String { S.deskMainUploadOnlyNeedsUpdate }
    static var making: String { S.deskMainUploadOnlyMaking }
    static var lifting: String { S.deskMainUploadOnlyLifting }
}

/// A two-way folder's upload-only request on its way, on its row: a lock
/// whose tooltip says so - or that the device needs an update, or refused
/// it. Nothing once the device has it (the tray's title says the same).
struct UploadOnlyBadge: View {
    let state: UploadOnlyRequestState
    let note: String?

    var body: some View {
        if let help {
            Image(systemName: "lock")
                .foregroundStyle(note != nil || state == .unsupported ? Color.orange : Color.secondary)
                .help(help)
                .accessibilityLabel(help)
        }
    }

    private var help: String? {
        if let note { return note }
        switch state {
        case .making: return UploadOnlyText.making
        case .lifting: return UploadOnlyText.lifting
        case .unsupported: return UploadOnlyText.needsUpdate
        case .none: return nil
        }
    }
}

/// The eye on a folder's row (issue #192): open when the folder is shown
/// in Images, crossed out when kept out; a click asks first (the row's
/// OutOfImagesConfirmation). Not there while the state isn't known, nor
/// for a device that can't; disabled when a folder above keeps it out.
/// It is the row's only sign of it - a line under the name crowded the
/// popover - so its tooltip carries the rest: a request on its way, or the
/// device's refusal to show it while a folder above still keeps it out.
struct OutOfImagesButton: View {
    let state: OutOfImagesState
    let note: String?
    let action: () -> Void

    var body: some View {
        switch state {
        case .unknown, .unsupported:
            EmptyView()
        default:
            Button(action: action) {
                Image(systemName: state.isKeptOut ? "eye.slash.fill" : "eye")
                    .foregroundStyle(state.isKeptOut ? Color.accentColor : Color.secondary)
            }
            .buttonStyle(.plain)
            .disabled(isByParent)
            .help(help)
            .accessibilityLabel(help)
        }
    }

    private var isByParent: Bool {
        if case .keptOutBy = state { return true }
        return false
    }

    private var help: String {
        switch state {
        case .keptOutBy(let parent): return note ?? OutOfImagesText.byParent(parent)
        case .keptOut: return OutOfImagesText.state
        case .keeping: return S.deskFoldersImagesKeeping
        case .showing: return S.deskFoldersImagesShowing
        default: return OutOfImagesText.keep
        }
    }
}

/// Asked inside the row, not as an .alert(): an alert takes key status from
/// the MenuBarExtra(.window) popover, which then closes (as Settings'
/// Disconnect confirmation). Keeping a folder out deletes the tags and
/// faces found in it; showing it again may search it for faces.
struct OutOfImagesConfirmation: View {
    let name: String
    let keepOut: Bool
    let onCancel: () -> Void
    let onConfirm: (Bool) -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(keepOut ? OutOfImagesText.keepTitle(name) : OutOfImagesText.showTitle(name))
                .font(.callout.bold())
                .fixedSize(horizontal: false, vertical: true)
            Text(keepOut ? OutOfImagesText.keepMessage : OutOfImagesText.showMessage)
                .font(.footnote)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            HStack {
                Spacer()
                Button(S.commonCancel, action: onCancel)
                Button(keepOut ? OutOfImagesText.keep : OutOfImagesText.show) { onConfirm(keepOut) }
                    .buttonStyle(.borderedProminent)
            }
            .controlSize(.small)
        }
    }
}

// Issue #37: folders are watched/polled, not perpetually rescanned, so
// "N%" only means something during the initial/periodic reconcile pass —
// otherwise it's just idle, up-to-date. `watchingLabel` is the only thing
// that differs between the upload direction ("Watching", event-driven) and
// the download direction ("Synced", polling-driven — see SyncModel's
// RemoteFolder doc comment for why there's no watcher on that side).
struct FolderStateView: View {
    let state: SyncModel.FolderState
    let watchingLabel: String

    /// The file a pass is on: "Checking 12 of 340" or "165 of 6,420" in
    /// full, and the file name shortened in the middle - shortening the
    /// whole string cut the count and the name alike ("165/6…513.jpg").
    /// "Checking…" before the pass knows which file it is on.
    @ViewBuilder
    private func fileLabel(_ file: SyncModel.FileProgress?, font: Font) -> some View {
        HStack(spacing: 4) {
            if let file {
                Text(Self.countText(file))
                    .monospacedDigit()
                    .fixedSize()
            }
            Text(file?.name ?? S.deskMainChecking)
                .lineLimit(1)
                .truncationMode(.middle)
        }
        .font(font)
        .foregroundStyle(.secondary)
    }

    /// "Checking 12 of 340", "165 of 6,420": the numbers in the app's locale.
    private static func countText(_ file: SyncModel.FileProgress) -> String {
        let done = file.index.formatted(.number.locale(L10n.shared.locale))
        let total = file.total.formatted(.number.locale(L10n.shared.locale))
        return file.checking ? S.macosFoldersCheckingCount(done: done, total: total)
            : S.macosFoldersCount(done: done, total: total)
    }

    var body: some View {
        switch state {
        case .scanning(let progress, let file) where progress == 0:
            // A flat 0% bar right as a folder starts (or on the very
            // first pass over it) reads as stalled/broken, not "working" —
            // an indeterminate spinner says "something's happening" without
            // implying a number that hasn't actually moved yet. Once real
            // progress exists (bytesDone > 0) the case below takes over.
            HStack(spacing: 6) {
                ProgressView()
                    .controlSize(.small)
                fileLabel(file, font: .caption)
                    .frame(maxWidth: 170, alignment: .trailing)
            }
        case .scanning(let progress, let file):
            VStack(alignment: .trailing, spacing: 2) {
                HStack(spacing: 6) {
                    ProgressView(value: progress)
                        .progressViewStyle(.linear)
                        .frame(width: 100)
                        .tint(progressColor(progress))
                    Text("\(Int(progress * 100))%")
                        .monospacedDigit()
                        .foregroundStyle(.secondary)
                        .frame(width: 36, alignment: .trailing)
                }
                // Shown so a folder with a couple of huge files (e.g.
                // drone video) doesn't look stuck at a low percentage for
                // the minutes it can genuinely take to send just one of
                // them — issue #37's original complaint. The percentage
                // itself can't move *during* that one file's transfer (no
                // per-byte upload progress over the wire), so the spinner
                // is what actually says "still alive" in the meantime.
                if let file {
                    HStack(spacing: 4) {
                        ProgressView()
                            .controlSize(.mini)
                        fileLabel(file, font: .caption2)
                    }
                    .frame(maxWidth: 170, alignment: .trailing)
                }
            }
        case .watching:
            Image(systemName: "checkmark.circle.fill")
                .foregroundStyle(.green)
            Text(watchingLabel)
                .font(.caption)
                .foregroundStyle(.secondary)
        case .error(let problem):
            Image(systemName: "exclamationmark.triangle.fill")
                .foregroundStyle(.red)
            Text(problem.text)
                .font(.caption)
                .foregroundStyle(.secondary)
                .lineLimit(1)
        }
    }

    private func progressColor(_ p: Double) -> Color {
        if p < 0.20 { return .red }
        if p < 0.90 { return .yellow }
        return .green
    }
}

/// Settings: connected (configured), the device and a Disconnect button;
/// not configured, the device and password fields with Connect. Below,
/// the Language picker (only once more than English ships), then always
/// "Start at login" (StartAtLoginToggle) with "Set Up a New Device…" (issue #184) at its
/// right, a button the size of Disconnect's.
struct SettingsInlineView: View {
    var onSetUpDevice: () -> Void

    @ObservedObject private var settings = SettingsStore.shared
    @ObservedObject private var sync = SyncModel.shared
    @ObservedObject private var login = LoginItemSettings.shared
    // Edited locally and applied with the Connect button - not bound to
    // the store, which would reconnect on every keystroke and spend one
    // of the device's five password attempts per minute per character
    // typed (issue #117's lock-out, seen live as "Wrong password").
    @State private var domain = ""
    @State private var password = ""
    @State private var confirmDisconnect = false

    private var deviceLabel: String {
        SettingsStore.bridgeName(fromDomain: settings.domain).map { "\($0).\(SettingsStore.bridgeDomain)" } ?? settings.domain
    }

    var body: some View {
        VStack(spacing: 8) {
            HStack {
                Text(S.commonSettings).font(.subheadline.bold())
                Spacer()
            }
            if settings.ready && confirmDisconnect {
                disconnectConfirmation
            } else if settings.ready {
                connected
            } else {
                connectForm
            }
            // The user's language for every app; hidden while English is
            // the only language that ships.
            LanguagePicker()
            HStack(spacing: 8) {
                // The real state; a change records the owner's choice.
                StartAtLoginToggle(login: login)
                Spacer()
                Button(S.deskMainSetUpDevice, action: onSetUpDevice)
                    .controlSize(.small)
            }
            StartAtLoginNote(login: login)
        }
        .padding(8)
        .background(.thinMaterial, in: RoundedRectangle(cornerRadius: 10))
        .onAppear { login.refresh() }
    }

    // Inline, not an .alert(): an alert takes key status from the
    // MenuBarExtra(.window) popover, which closes itself on exactly that
    // and takes the alert with it (same quirk as the remote folder picker).
    private var disconnectConfirmation: some View {
        VStack(alignment: .leading, spacing: 8) {
            Label(S.deskMainDisconnectTitle(device: deviceLabel), systemImage: "exclamationmark.triangle.fill")
                .font(.callout.bold())
                .foregroundStyle(.orange)
            Text(S.macosSettingsDisconnectMessage)
                .font(.footnote)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            HStack {
                Spacer()
                Button(S.commonCancel) { confirmDisconnect = false }
                    .keyboardShortcut(.cancelAction)
                Button(S.commonDisconnect, role: .destructive) {
                    confirmDisconnect = false
                    domain = ""
                    password = ""
                    sync.disconnect()
                }
                .buttonStyle(.borderedProminent)
                .tint(.red)
            }
            .controlSize(.small)
        }
    }

    private var connected: some View {
        HStack(spacing: 8) {
            Image(systemName: "externaldrive.connected.to.line.below")
                .foregroundStyle(.secondary)
            VStack(alignment: .leading, spacing: 2) {
                Text(deviceLabel).font(.callout)
                // Issue #190: "Connected over your home network" or
                // "Connected through off-the.cloud".
                Text(sync.connectionStatus).font(.footnote).foregroundStyle(.secondary)
            }
            Spacer()
            if sync.overallStatus != .connected {
                // After "Wrong password" nothing else would try again.
                Button(S.macosSettingsRetry) { sync.reconnectNow() }
                    .controlSize(.small)
            }
            Button(S.macosSettingsDisconnectButton) { confirmDisconnect = true }
                .controlSize(.small)
        }
    }

    private var connectForm: some View {
        VStack(spacing: 8) {
            // Issue #121: the device's name is all the bridge needs; a
            // custom address is behind the toggle.
            DeviceAddressFields(domain: $domain)
            SecureField(S.commonPassword, text: $password)
                .textFieldStyle(.roundedBorder)
                .onSubmit { if canConnect { connect() } }
            HStack {
                Text(S.macosSettingsConnectHint)
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                Spacer()
                Button(S.commonConnect) { connect() }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.small)
                    .disabled(!canConnect)
            }
        }
    }

    private var canConnect: Bool { !domain.isEmpty && !password.isEmpty }

    private func connect() {
        settings.apply(domain: domain, password: password)
    }
}

/// Issue #121: how the device's address is entered - its bridge name, or
/// behind a toggle, any address. Mirrors the iOS app's
/// ConnectionEndpointFields. An existing custom address opens the form in
/// custom mode, so nothing set up on purpose is silently rewritten.
struct DeviceAddressFields: View {
    /// The stored value - a bare bridge host, or a full URL.
    @Binding var domain: String

    @State private var deviceName = ""
    @State private var customAddress = false
    @State private var loaded = false

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            if customAddress {
                TextField(S.macosSettingsAddressPlaceholder, text: $domain)
                    .textFieldStyle(.roundedBorder)
                    .disableAutocorrection(true)
            } else {
                HStack(spacing: 4) {
                    TextField(S.macosSettingsDeviceNamePlaceholder, text: $deviceName)
                        .textFieldStyle(.roundedBorder)
                        .disableAutocorrection(true)
                        .onChange(of: deviceName) { _, name in
                            domain = name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
                                ? "" : SettingsStore.bridgeDomain(forName: name)
                        }
                    Text(".\(SettingsStore.bridgeDomain)")
                        .foregroundStyle(.secondary)
                        .fixedSize()
                }
            }
            Toggle(S.macosSettingsCustomAddress, isOn: $customAddress)
                .toggleStyle(.checkbox)
                .font(.footnote)
                .onChange(of: customAddress) { _, custom in
                    if !custom {
                        deviceName = SettingsStore.bridgeName(fromDomain: domain) ?? ""
                        domain = deviceName.isEmpty ? "" : SettingsStore.bridgeDomain(forName: deviceName)
                    }
                }
            if customAddress {
                Text(S.macosSettingsCustomAddressHint)
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .onAppear {
            guard !loaded else { return }
            loaded = true
            if let name = SettingsStore.bridgeName(fromDomain: domain) {
                deviceName = name
            } else if !domain.isEmpty {
                customAddress = true
            }
        }
    }
}

/// "Add Folder", in two steps (otc-sync's tray does the same,
/// addfolder.go): first the kind of folder, each with an (i) that says in
/// plain words what it does - the old menu's two entries didn't, and every
/// folder being two-way came as a surprise; then the options that kind has
/// - a backup: Keep out of Images (upload only it always is, a fixed line);
/// a two-way folder from this Mac: Upload only and Keep out of Images - and
/// "Choose Folder…", which only then opens the folder picker. A folder from
/// the device has no options (it keeps what it has there): its kind goes
/// straight to the device's folders.
struct AddFolderChooser: View {
    /// The kinds that have a second step.
    enum Kind { case backup, local }
    /// Whether the device can keep folders out of Images (issue #192) and
    /// make them upload only (issue #132): nil while not known - offered,
    /// and sent once it is connected.
    let outOfImagesSupported: Bool?
    let uploadOnlySupported: Bool?
    let onBackup: (_ outOfImages: Bool) -> Void
    let onSyncLocal: (_ uploadOnly: Bool, _ outOfImages: Bool) -> Void
    let onSyncDevice: () -> Void
    let onCancel: () -> Void
    @State private var kind: Kind?
    /// Which (i) is open.
    @State private var open: String?
    @State private var keepOut = false
    @State private var uploadOnly = false

    private var canKeepOut: Bool { outOfImagesSupported != false }
    private var canUploadOnly: Bool { uploadOnlySupported != false }

    // The kinds' words (the tray's, with "this Mac" for "this computer").
    static var backupTitle: String { S.macosAddBackupTitle }
    static var backupSubtitle: String { S.macosAddBackupSubtitle }
    static var localTitle: String { S.macosAddLocalTitle }
    static var localSubtitle: String { S.macosAddLocalSubtitle }

    var body: some View {
        if let kind {
            optionsStep(kind)
        } else {
            kindStep
        }
    }

    // MARK: Step 1: the kind

    private var kindStep: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                Text(S.macosAddTitle).font(.headline)
                Spacer()
                Button(S.commonCancel, action: onCancel).buttonStyle(.borderless)
            }
            kindRow("backup", icon: "arrow.up.circle.fill", tint: .orange,
                    title: Self.backupTitle, subtitle: Self.backupSubtitle,
                    info: S.macosAddBackupInfo,
                    action: { choose(.backup) })
            kindRow("local", icon: "arrow.triangle.2.circlepath.circle.fill", tint: .blue,
                    title: Self.localTitle, subtitle: Self.localSubtitle,
                    info: S.macosAddLocalInfo,
                    action: { choose(.local) })
            kindRow("device", icon: "arrow.down.circle.fill", tint: .green,
                    title: S.deskMainKindDevice,
                    subtitle: S.deskMainKindDeviceSubtitle,
                    info: S.macosAddDeviceInfo,
                    action: onSyncDevice)
        }
    }

    /// A kind picked: its options start unticked.
    private func choose(_ k: Kind) {
        keepOut = false
        uploadOnly = false
        open = nil
        kind = k
    }

    private func kindRow(_ id: String, icon: String, tint: Color, title: String, subtitle: String, info: String, action: @escaping () -> Void) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 10) {
                Button(action: action) {
                    HStack(spacing: 10) {
                        Image(systemName: icon)
                            .font(.title3)
                            .foregroundStyle(tint)
                        VStack(alignment: .leading, spacing: 1) {
                            Text(title)
                            Text(subtitle)
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        }
                        Spacer(minLength: 0)
                        // There is a next step: its options, or the
                        // device's folders.
                        Image(systemName: "chevron.right")
                            .font(.caption)
                            .foregroundStyle(.tertiary)
                    }
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                infoButton(id)
            }
            if open == id {
                infoText(info, leading: 34)
            }
        }
        .padding(8)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 10))
    }

    // MARK: Step 2: the options, then the folder

    private func optionsStep(_ k: Kind) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                Button {
                    open = nil
                    kind = nil
                } label: {
                    Label(S.commonBack, systemImage: "chevron.left")
                }
                .buttonStyle(.borderless)
                Spacer()
                Button(S.commonCancel, action: onCancel).buttonStyle(.borderless)
            }
            HStack(spacing: 10) {
                Image(systemName: k == .backup ? "arrow.up.circle.fill" : "arrow.triangle.2.circlepath.circle.fill")
                    .font(.title3)
                    .foregroundStyle(k == .backup ? Color.orange : Color.blue)
                VStack(alignment: .leading, spacing: 1) {
                    Text(k == .backup ? Self.backupTitle : Self.localTitle).font(.headline)
                    Text(k == .backup ? Self.backupSubtitle : Self.localSubtitle)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
            if k == .backup {
                // Not a choice: a backup is made upload only at every start.
                option("uploadOnly", isOn: .constant(true), title: UploadOnlyText.toggle,
                       caption: UploadOnlyText.backup + " " + UploadOnlyText.caption, info: nil, enabled: false)
            } else {
                option("uploadOnly", isOn: $uploadOnly, title: UploadOnlyText.toggle,
                       caption: canUploadOnly ? UploadOnlyText.caption : UploadOnlyText.needsUpdate,
                       info: UploadOnlyText.addInfo, enabled: canUploadOnly)
            }
            option("keepOut", isOn: $keepOut, title: OutOfImagesText.toggle,
                   caption: canKeepOut ? OutOfImagesText.addCaption : OutOfImagesText.needsUpdate,
                   info: OutOfImagesText.addInfo, enabled: canKeepOut)
            HStack {
                Spacer()
                Button(S.deskMainChooseFolder) {
                    switch k {
                    case .backup: onBackup(keepOut && canKeepOut)
                    case .local: onSyncLocal(uploadOnly && canUploadOnly, keepOut && canKeepOut)
                    }
                }
                .buttonStyle(.borderedProminent)
                .keyboardShortcut(.defaultAction)
            }
        }
    }

    private func option(_ id: String, isOn: Binding<Bool>, title: String, caption: String, info: String?, enabled: Bool) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .top, spacing: 10) {
                VStack(alignment: .leading, spacing: 1) {
                    Toggle(title, isOn: isOn)
                        .toggleStyle(.checkbox)
                        .disabled(!enabled)
                    // Outside the toggle, so a device that needs an update
                    // says so in full contrast.
                    Text(caption)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                        .padding(.leading, 20)
                }
                Spacer(minLength: 0)
                if info != nil {
                    infoButton(id)
                }
            }
            if let info, open == id {
                infoText(info, leading: 20)
            }
        }
        .padding(8)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 10))
    }

    private func infoButton(_ id: String) -> some View {
        Button {
            open = open == id ? nil : id
        } label: {
            Image(systemName: open == id ? "info.circle.fill" : "info.circle")
                .foregroundStyle(.secondary)
        }
        .buttonStyle(.borderless)
        .help(S.macosAddWhatThisDoes)
    }

    private func infoText(_ text: String, leading: CGFloat) -> some View {
        Text(text)
            .font(.caption)
            .foregroundStyle(.secondary)
            .fixedSize(horizontal: false, vertical: true)
            .padding(.leading, leading)
    }
}

/// The storage line: its health, how full the device is, and -
/// with the pointer over it - the device's storage in GB, CPU and memory
/// in a pop-up. otc-sync's tray shows the same (a text bar, and a submenu).
private struct StorageStatusView: View {
    let health: RaidHealth
    let status: Msg_Status?
    @State private var showLoad = false

    var body: some View {
        HStack(spacing: 0) {
            Text(health.summary)
                .foregroundStyle(health == .ok ? Color.secondary : Color.red)
            if let s = status?.storageUse {
                let frac = s.fraction
                Text(" · " + S.deskMainStorageUsed(percent: Int((frac * 100).rounded())))
                    .foregroundStyle(fullnessColor(frac) ?? Color.secondary)
                    .monospacedDigit()
            }
        }
        .font(.caption)
        .lineLimit(1)
        .contentShape(Rectangle())
        .onHover { showLoad = $0 && status != nil }
        .popover(isPresented: $showLoad, arrowEdge: .bottom) {
            if let s = status {
                DeviceLoadView(status: s)
            }
        }
    }
}

/// The device-update line (issue #183): red with a warning sign for a
/// critical update, a plain line for a major one; the summary as help.
/// otc-sync's tray shows the same line under the storage one.
private struct UpdateAlertView: View {
    let alert: Msg_UpdateAlert

    var body: some View {
        Group {
            if alert.level == "critical" {
                Label(S.deskMainUpdateCritical(version: alert.version),
                      systemImage: "exclamationmark.triangle.fill")
                    .foregroundStyle(Color.red)
            } else {
                Text(S.deskMainUpdateAvailable(version: alert.version))
                    .foregroundStyle(.secondary)
            }
        }
        .font(.caption)
        .fixedSize(horizontal: false, vertical: true)
        .help(alert.summary)
    }
}

private extension Msg_Status {
    /// Used and total, in the status's units (1.024 MB): the storage
    /// path's disk, or the OS disk on a device without one; nil until the
    /// device reports a size.
    var storageUse: (used: Int32, size: Int32, fraction: Double)? {
        let (used, size) = raidSize > 0 ? (raidUsage, raidSize) : (diskUsage, diskSize)
        guard size > 0 else { return nil }
        return (used, size, min(max(Double(used) / Double(size), 0), 1))
    }
}

/// How full storage is, in colour: orange past 75%, red past 90%, nil
/// (the usual colour) below. The storage line and the pop-up's bar.
private func fullnessColor(_ fraction: Double) -> Color? {
    fraction > 0.9 ? .red : fraction > 0.75 ? .orange : nil
}

/// An amount in the status's units (1.024 MB), as otc-sync's `sizeText`
/// writes it: one decimal under 100 GB ("8.5"), whole GB from 100 GB
/// ("474"), TB with one decimal from 1000 GB ("3.6"). The number is
/// written for the app's language (its canonical tag, as the texts' own
/// numbers: "8.5" in English whatever the region, "8,5" in Spanish).
private func sizeText(_ units: Int32) -> (number: String, tb: Bool) {
    let gb = Double(units) * 1.024 / 1000
    let locale = L10n.shared.current.locale
    if gb < 99.95 { return (String(format: "%.1f", locale: locale, gb), false) }
    if gb < 999.5 { return (String(format: "%.0f", locale: locale, gb), false) }
    return (String(format: "%.1f", locale: locale, gb / 1000), true)
}

/// "39.6 of 474 GB", "1.2 of 3.6 TB", or "39.6 GB of 3.6 TB" when the two
/// need different units - otc-sync's `usedOfTotal`.
private func usedOfTotal(_ used: Int32, _ size: Int32) -> String {
    let u = sizeText(used), s = sizeText(size)
    switch (u.tb, s.tb) {
    case (false, false): return S.deskMainUsedOfTotalGb(used: u.number, total: s.number)
    case (true, true): return S.deskMainUsedOfTotalTb(used: u.number, total: s.number)
    case (false, true): return S.deskMainUsedGbOfTotalTb(used: u.number, total: s.number)
    case (true, false): return S.deskMainUsedTbOfTotalGb(used: u.number, total: s.number)
    }
}

/// The pop-up: storage first, as it details the line it opens from,
/// then the device's load.
private struct DeviceLoadView: View {
    let status: Msg_Status

    var body: some View {
        let cpu = Double(status.cpuUsagePrc) / 100
        let mem = status.memSize > 0 ? Double(status.memUsage) / Double(status.memSize) : 0
        Grid(alignment: .leading, horizontalSpacing: 10, verticalSpacing: 8) {
            if let s = status.storageUse {
                GridRow {
                    Text(S.macosLoadStorage).font(.caption)
                    ProgressView(value: s.fraction).frame(width: 90)
                        .tint(fullnessColor(s.fraction))
                    Text(usedOfTotal(s.used, s.size))
                        .font(.caption).monospacedDigit()
                }
            }
            GridRow {
                Text(S.macosLoadCpu).font(.caption)
                ProgressView(value: min(max(cpu, 0), 1)).frame(width: 90)
                Text("\(Int((cpu * 100).rounded()))%")
                    .font(.caption).monospacedDigit()
            }
            GridRow {
                Text(S.macosLoadMemory).font(.caption)
                ProgressView(value: min(max(mem, 0), 1)).frame(width: 90)
                Text(usedOfTotal(status.memUsage, status.memSize))
                    .font(.caption).monospacedDigit()
            }
        }
        .padding(12)
    }
}
