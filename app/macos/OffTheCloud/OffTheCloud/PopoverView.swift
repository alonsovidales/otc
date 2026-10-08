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
    // The three ways to add a folder, each with its explanation - inline,
    // for the same reason as the remote picker below.
    @State private var showAddChooser = false
    // Issue #192: the chooser's "Keep out of Images", carried through the
    // remote picker to the folder it adds.
    @State private var addOutOfImages = false
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
                onBackup: { keepOut in showAddChooser = false; sync.addBackupFolder(outOfImages: keepOut) },
                onSyncLocal: { keepOut in showAddChooser = false; sync.addFolder(outOfImages: keepOut) },
                onSyncDevice: { keepOut in showAddChooser = false; addOutOfImages = keepOut; showRemotePicker = true },
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
                    chooseLocalDestinationAndAdd(remotePath: remotePath, outOfImages: addOutOfImages)
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
                Text("Off The Cloud — Sync")
                    .font(.headline)
                Spacer()
                // Issue #193: the device's own web app, in the browser -
                // the address only; the web app asks for the password.
                // otc-sync's tray has the same item at the top.
                if settings.ready, let web = SettingsStore.webAppURL(fromDomain: settings.domain) {
                    Button {
                        NSWorkspace.shared.open(web)
                    } label: {
                        Label("Open Web App", systemImage: "globe")
                            .foregroundStyle(Color.accentColor)
                    }
                    .buttonStyle(.borderless)
                    .help("Open \(web.absoluteString) in your browser")
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
                Text(sync.overallStatus)
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
                    Text("No folders yet — add one below.")
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
                    Label("Add Folder…", systemImage: "plus.circle.fill")
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
                    Label("Quit Off The Cloud", systemImage: "power")
                }
                .buttonStyle(.borderless)
                .keyboardShortcut("q", modifiers: .command)
                .help("Quit Off The Cloud - folders stop syncing until it is opened again")
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
    private func chooseLocalDestinationAndAdd(remotePath: String, outOfImages: Bool) {
        let panel = NSOpenPanel()
        panel.canCreateDirectories = true
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        panel.prompt = "Choose"
        panel.message = "Choose where to download “\(remotePath)” and keep it in sync."
        if runFolderPanel(panel) == .OK, let url = panel.url {
            sync.addRemoteFolder(remotePath: remotePath, localURL: url, outOfImages: outOfImages)
        }
    }

    private var statusColor: Color {
        switch sync.overallStatus {
        case "Connected": return .green
        case "Disconnected", "Connecting…": return .yellow
        case "Missing domain/password", "Wrong password": return .red
        default: return sync.overallStatus.hasPrefix("Too many attempts") ? .red : .gray
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
                    Text("Backup · this Mac → device, upload only")
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }
                Spacer()
                FolderStateView(state: folder.state, watchingLabel: "Backed up")
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
                FolderStateView(state: folder.state, watchingLabel: "Synced")
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
enum OutOfImagesText {
    static let toggle = "Keep out of Images"
    static let keep = "Keep Out of Images"
    static let show = "Show in Images"
    static let state = "Kept out of Images"
    /// The add flow's: the folder may be on the device already (one synced
    /// from it, or added again), and keeping it out deletes the tags and
    /// faces found there (otc-sync's engine.OutOfImagesAddCaption).
    static let addCaption = "Photos and videos in it aren't tagged, searched for faces or shown in Images. Files still has them. Any tags and faces already found in them are deleted."
    static let addInfo = "Check it before choosing one of the options below: it applies to the folder you add. You can change it later with the eye button on the folder's row. A folder renamed later is a new folder on the device: keep it out of Images again."
    static let keepMessage = "Its photos and videos won't be tagged, searched for faces or shown in Images, and the tags and faces already found in them are deleted. Files still shows them."
    static let showMessage = "Its photos and videos go back to Images, and are tagged - and searched for faces, if face recognition is on - in the background."
    static let needsUpdate = "Your device needs an update to keep folders out of Images."
    static func keepTitle(_ name: String) -> String { "Keep “\(name)” out of Images?" }
    static func showTitle(_ name: String) -> String { "Show “\(name)” in Images?" }
    static func byParent(_ parent: String) -> String { "Inside \(parent), which is kept out of Images" }
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
        case .keeping: return "Keeping out of Images…"
        case .showing: return "Showing in Images…"
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
                Button("Cancel", action: onCancel)
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

    /// "165/6420 · IMG_4513.jpg" as "165 of 6,420" in full and the file
    /// name shortened in the middle: shortening the whole string cut the
    /// count and the name alike ("165/6…513.jpg").
    @ViewBuilder
    private func fileLabel(_ text: String, font: Font) -> some View {
        let parts = text.components(separatedBy: " · ")
        HStack(spacing: 4) {
            if parts.count == 2 {
                Text(Self.countText(parts[0]))
                    .monospacedDigit()
                    .fixedSize()
            }
            Text(parts.count == 2 ? parts[1] : text)
                .lineLimit(1)
                .truncationMode(.middle)
        }
        .font(font)
        .foregroundStyle(.secondary)
    }

    /// "Checking 12/340" -> "Checking 12 of 340"; "165/6420" -> "165 of 6,420".
    private static func countText(_ raw: String) -> String {
        var prefix = ""
        var nums = raw
        if let sp = raw.lastIndex(of: " ") {
            prefix = String(raw[...sp])
            nums = String(raw[raw.index(after: sp)...])
        }
        let bits = nums.split(separator: "/")
        guard bits.count == 2, let n = Int(bits[0]), let total = Int(bits[1]) else { return raw }
        let f = NumberFormatter()
        f.numberStyle = .decimal
        return prefix + "\(f.string(from: n as NSNumber) ?? "\(n)") of \(f.string(from: total as NSNumber) ?? "\(total)")"
    }

    var body: some View {
        switch state {
        case .scanning(let progress, let currentFile) where progress == 0:
            // A flat 0% bar right as a folder starts (or on the very
            // first pass over it) reads as stalled/broken, not "working" —
            // an indeterminate spinner says "something's happening" without
            // implying a number that hasn't actually moved yet. Once real
            // progress exists (bytesDone > 0) the case below takes over.
            HStack(spacing: 6) {
                ProgressView()
                    .controlSize(.small)
                fileLabel(currentFile ?? "Checking…", font: .caption)
                    .frame(maxWidth: 170, alignment: .trailing)
            }
        case .scanning(let progress, let currentFile):
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
                if let currentFile {
                    HStack(spacing: 4) {
                        ProgressView()
                            .controlSize(.mini)
                        fileLabel(currentFile, font: .caption2)
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
        case .error(let message):
            Image(systemName: "exclamationmark.triangle.fill")
                .foregroundStyle(.red)
            Text(message)
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
/// always, "Start at login" (StartAtLoginToggle) with "Set Up a New Device…" (issue #184) at its
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
                Text("Settings").font(.subheadline.bold())
                Spacer()
            }
            if settings.ready && confirmDisconnect {
                disconnectConfirmation
            } else if settings.ready {
                connected
            } else {
                connectForm
            }
            HStack(spacing: 8) {
                // The real state; a change records the owner's choice.
                StartAtLoginToggle(login: login)
                Spacer()
                Button("Set Up a New Device…", action: onSetUpDevice)
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
            Label("Disconnect from \(deviceLabel)?", systemImage: "exclamationmark.triangle.fill")
                .font(.callout.bold())
                .foregroundStyle(.orange)
            Text("All your synced folders are removed from this app, so none of them starts syncing with a different device by mistake. The files themselves stay on this Mac and on the device. You can add the folders again after connecting.")
                .font(.footnote)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            HStack {
                Spacer()
                Button("Cancel") { confirmDisconnect = false }
                    .keyboardShortcut(.cancelAction)
                Button("Disconnect", role: .destructive) {
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
            if sync.overallStatus != "Connected" {
                // After "Wrong password" nothing else would try again.
                Button("Retry") { sync.reconnectNow() }
                    .controlSize(.small)
            }
            Button("Disconnect…") { confirmDisconnect = true }
                .controlSize(.small)
        }
    }

    private var connectForm: some View {
        VStack(spacing: 8) {
            // Issue #121: the device's name is all the bridge needs; a
            // custom address is behind the toggle.
            DeviceAddressFields(domain: $domain)
            SecureField("Password", text: $password)
                .textFieldStyle(.roundedBorder)
                .onSubmit { if canConnect { connect() } }
            HStack {
                Text("Enter your device and its password.")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                Spacer()
                Button("Connect") { connect() }
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
                TextField("Address (wss://host/ws or host)", text: $domain)
                    .textFieldStyle(.roundedBorder)
                    .disableAutocorrection(true)
            } else {
                HStack(spacing: 4) {
                    TextField("Device name", text: $deviceName)
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
            Toggle("Custom address", isOn: $customAddress)
                .toggleStyle(.checkbox)
                .font(.footnote)
                .onChange(of: customAddress) { _, custom in
                    if !custom {
                        deviceName = SettingsStore.bridgeName(fromDomain: domain) ?? ""
                        domain = deviceName.isEmpty ? "" : SettingsStore.bridgeDomain(forName: deviceName)
                    }
                }
            if customAddress {
                Text("For a device not on the Off The Cloud bridge - your own bridge, Tailscale Funnel, or the local network (ws://192.168.…:8080/ws).")
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

/// "Add Folder": the three kinds of folder, each with an (i) that says in
/// plain words what it does - the old menu's two entries didn't, and
/// every folder being two-way came as a surprise.
struct AddFolderChooser: View {
    /// Issue #192: whether the device can keep folders out of Images (nil
    /// while not known: offered, and sent once it is connected).
    let outOfImagesSupported: Bool?
    // Each is told whether to keep the folder out of Images.
    let onBackup: (Bool) -> Void
    let onSyncLocal: (Bool) -> Void
    let onSyncDevice: (Bool) -> Void
    let onCancel: () -> Void
    @State private var open: Int?
    @State private var keepOut = false

    private var canKeepOut: Bool { outOfImagesSupported != false }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                Text("Add a folder").font(.headline)
                Spacer()
                Button("Cancel", action: onCancel).buttonStyle(.borderless)
            }
            // First: each option below acts on the first click, so a
            // checkbox under them would be read too late.
            outOfImagesOption
            option(0, icon: "arrow.up.circle.fill", tint: .orange,
                   title: "Back up a folder from this Mac",
                   subtitle: "One way: this Mac → device (upload only, no deletes)",
                   info: "New and changed files are copied to the device. Nothing is ever deleted there: files you delete on this Mac stay on the device, and when a file changes the device keeps its older version too. Nothing done on the device - from a phone, another computer or the web - ever changes or deletes anything in this folder on the Mac. Good for photo archives and backups.",
                   action: { onBackup(keepOut && canKeepOut) })
            option(1, icon: "arrow.triangle.2.circlepath.circle.fill", tint: .blue,
                   title: "Sync a folder from this Mac",
                   subtitle: "Two ways: starts from this Mac",
                   info: "The folder is copied to the device, and from then on it is kept the same in both places: files added, changed or deleted on the device (from a phone, another computer or the web) change this folder too, and the other way round. Deleted files go to the Trash on the Mac. The first sync only adds - it never deletes.",
                   action: { onSyncLocal(keepOut && canKeepOut) })
            option(2, icon: "arrow.down.circle.fill", tint: .green,
                   title: "Sync a folder from the device",
                   subtitle: "Two ways: starts from the device",
                   info: "Pick a folder that is already on the device and a place on this Mac: it is downloaded there and kept the same in both places from then on, changes and deletions included, like the option above. Handy for getting a folder onto a second computer.",
                   action: { onSyncDevice(keepOut && canKeepOut) })
        }
    }

    /// Issue #192: for whichever kind is picked below.
    private var outOfImagesOption: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .top, spacing: 10) {
                VStack(alignment: .leading, spacing: 1) {
                    Toggle(OutOfImagesText.toggle, isOn: $keepOut)
                        .toggleStyle(.checkbox)
                        .disabled(!canKeepOut)
                    // Outside the toggle, so a device that needs an update
                    // says so in full contrast.
                    Text(canKeepOut ? OutOfImagesText.addCaption : OutOfImagesText.needsUpdate)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                        .padding(.leading, 20)
                }
                Spacer(minLength: 0)
                Button {
                    open = open == 3 ? nil : 3
                } label: {
                    Image(systemName: open == 3 ? "info.circle.fill" : "info.circle")
                        .foregroundStyle(.secondary)
                }
                .buttonStyle(.borderless)
                .help("What this does")
            }
            if open == 3 {
                Text(OutOfImagesText.addInfo)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(.leading, 20)
            }
        }
        .padding(8)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 10))
    }

    private func option(_ i: Int, icon: String, tint: Color, title: String, subtitle: String, info: String, action: @escaping () -> Void) -> some View {
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
                    }
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                Button {
                    open = open == i ? nil : i
                } label: {
                    Image(systemName: open == i ? "info.circle.fill" : "info.circle")
                        .foregroundStyle(.secondary)
                }
                .buttonStyle(.borderless)
                .help("What this does")
            }
            if open == i {
                Text(info)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(.leading, 34)
            }
        }
        .padding(8)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 10))
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
                Text(" · \(Int((frac * 100).rounded()))% used")
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
                Label("Critical device update \(alert.version) - install it from the device's Settings as soon as possible",
                      systemImage: "exclamationmark.triangle.fill")
                    .foregroundStyle(Color.red)
            } else {
                Text("Device update \(alert.version) available")
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
/// ("474"), TB with one decimal from 1000 GB ("3.6").
private func sizeText(_ units: Int32) -> (number: String, unit: String) {
    let gb = Double(units) * 1.024 / 1000
    if gb < 99.95 { return (String(format: "%.1f", gb), "GB") }
    if gb < 999.5 { return (String(format: "%.0f", gb), "GB") }
    return (String(format: "%.1f", gb / 1000), "TB")
}

/// "39.6 of 474 GB", "1.2 of 3.6 TB", or "39.6 GB of 3.6 TB" when the two
/// need different units - otc-sync's `usedOfTotal`.
private func usedOfTotal(_ used: Int32, _ size: Int32) -> String {
    let u = sizeText(used), s = sizeText(size)
    return u.unit == s.unit
        ? "\(u.number) of \(s.number) \(s.unit)"
        : "\(u.number) \(u.unit) of \(s.number) \(s.unit)"
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
                    Text("Storage").font(.caption)
                    ProgressView(value: s.fraction).frame(width: 90)
                        .tint(fullnessColor(s.fraction))
                    Text(usedOfTotal(s.used, s.size))
                        .font(.caption).monospacedDigit()
                }
            }
            GridRow {
                Text("CPU").font(.caption)
                ProgressView(value: min(max(cpu, 0), 1)).frame(width: 90)
                Text("\(Int((cpu * 100).rounded()))%")
                    .font(.caption).monospacedDigit()
            }
            GridRow {
                Text("Memory").font(.caption)
                ProgressView(value: min(max(mem, 0), 1)).frame(width: 90)
                Text(usedOfTotal(status.memUsage, status.memSize))
                    .font(.caption).monospacedDigit()
            }
        }
        .padding(12)
    }
}
