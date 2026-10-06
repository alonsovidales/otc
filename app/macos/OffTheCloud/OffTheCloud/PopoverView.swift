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

    @State private var showSettings = false
    // Issue #47: remote → local sync — browse the device's tree, then pick
    // a local destination for it.
    @State private var showRemotePicker = false
    // The three ways to add a folder, each with its explanation - inline,
    // for the same reason as the remote picker below.
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
                onBackup: { showAddChooser = false; sync.addBackupFolder() },
                onSyncLocal: { showAddChooser = false; sync.addFolder() },
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
                Text("Off The Cloud — Sync")
                    .font(.headline)
                Spacer()
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
                        FolderRow(folder: f, remove: { sync.removeFolder(f) })
                    }
                    // Issue #47: remote → local mirrors, shown alongside
                    // the (local → remote) upload folders above — same row
                    // style, a down-arrow instead of a plain folder icon is
                    // the only thing distinguishing direction.
                    ForEach(sync.remoteFolders) { f in
                        RemoteFolderRow(folder: f, remove: { sync.removeRemoteFolder(f) })
                    }
                }
            }
            .padding(.vertical, 4)

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
        panel.prompt = "Choose"
        panel.message = "Choose where to download “\(remotePath)” and keep it in sync."
        if runFolderPanel(panel) == .OK, let url = panel.url {
            sync.addRemoteFolder(remotePath: remotePath, localURL: url)
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
    let remove: () -> Void

    var body: some View {
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
            Button(role: .destructive) {
                remove()
            } label: {
                Image(systemName: "minus.circle")
            }.buttonStyle(.plain)
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
    let remove: () -> Void

    var body: some View {
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
            }
            Spacer()
            FolderStateView(state: folder.state, watchingLabel: "Synced")
            Button(role: .destructive) {
                remove()
            } label: {
                Image(systemName: "minus.circle")
            }.buttonStyle(.plain)
        }
        .padding(8)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 10))
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
/// always, the big "Set Up a New Device…" (issue #184).
struct SettingsInlineView: View {
    var onSetUpDevice: () -> Void

    @ObservedObject private var settings = SettingsStore.shared
    @ObservedObject private var sync = SyncModel.shared
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
            Toggle("Start at login", isOn: $settings.startAtLogin)
                .toggleStyle(.checkbox)
                .font(.footnote)
                .frame(maxWidth: .infinity, alignment: .leading)
            Divider()
            Button(action: onSetUpDevice) {
                Label("Set Up a New Device…", systemImage: "externaldrive.badge.plus")
                    .frame(maxWidth: .infinity)
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.large)
        }
        .padding(8)
        .background(.thinMaterial, in: RoundedRectangle(cornerRadius: 10))
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
    let onBackup: () -> Void
    let onSyncLocal: () -> Void
    let onSyncDevice: () -> Void
    let onCancel: () -> Void
    @State private var open: Int?

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                Text("Add a folder").font(.headline)
                Spacer()
                Button("Cancel", action: onCancel).buttonStyle(.borderless)
            }
            option(0, icon: "arrow.up.circle.fill", tint: .orange,
                   title: "Back up a folder from this Mac",
                   subtitle: "One way: this Mac → device (upload only, no deletes)",
                   info: "New and changed files are copied to the device. Nothing is ever deleted there: files you delete on this Mac stay on the device, and when a file changes the device keeps its older version too. Nothing done on the device - from a phone, another computer or the web - ever changes or deletes anything in this folder on the Mac. Good for photo archives and backups.",
                   action: onBackup)
            option(1, icon: "arrow.triangle.2.circlepath.circle.fill", tint: .blue,
                   title: "Sync a folder from this Mac",
                   subtitle: "Two ways: starts from this Mac",
                   info: "The folder is copied to the device, and from then on it is kept the same in both places: files added, changed or deleted on the device (from a phone, another computer or the web) change this folder too, and the other way round. Deleted files go to the Trash on the Mac. The first sync only adds - it never deletes.",
                   action: onSyncLocal)
            option(2, icon: "arrow.down.circle.fill", tint: .green,
                   title: "Sync a folder from the device",
                   subtitle: "Two ways: starts from the device",
                   info: "Pick a folder that is already on the device and a place on this Mac: it is downloaded there and kept the same in both places from then on, changes and deletions included, like the option above. Handy for getting a folder onto a second computer.",
                   action: onSyncDevice)
        }
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
/// with the pointer over it - the device's CPU and memory in a pop-up.
/// otc-sync's tray shows the same (a text bar, and a submenu).
private struct StorageStatusView: View {
    let health: RaidHealth
    let status: Msg_Status?
    @State private var showLoad = false

    /// Used and total, in the status's units (1.024 MB): the storage
    /// path's disk, or the OS disk on a device without one.
    private var storage: (used: Double, size: Double)? {
        guard let s = status else { return nil }
        if s.raidSize > 0 { return (Double(s.raidUsage), Double(s.raidSize)) }
        if s.diskSize > 0 { return (Double(s.diskUsage), Double(s.diskSize)) }
        return nil
    }

    var body: some View {
        HStack(spacing: 0) {
            Text(health.summary)
                .foregroundStyle(health == .ok ? Color.secondary : Color.red)
            if let s = storage {
                let frac = min(max(s.used / s.size, 0), 1)
                Text(" · \(Int((frac * 100).rounded()))% used")
                    .foregroundStyle(frac > 0.9 ? Color.red : frac > 0.75 ? Color.orange : Color.secondary)
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

private struct DeviceLoadView: View {
    let status: Msg_Status

    private func gb(_ v: Int32) -> String {
        String(format: "%.1f", Double(v) * 1.024 / 1000)
    }

    var body: some View {
        let cpu = Double(status.cpuUsagePrc) / 100
        let mem = status.memSize > 0 ? Double(status.memUsage) / Double(status.memSize) : 0
        Grid(alignment: .leading, horizontalSpacing: 10, verticalSpacing: 8) {
            GridRow {
                Text("CPU").font(.caption)
                ProgressView(value: min(max(cpu, 0), 1)).frame(width: 90)
                Text("\(Int((cpu * 100).rounded()))%")
                    .font(.caption).monospacedDigit()
            }
            GridRow {
                Text("Memory").font(.caption)
                ProgressView(value: min(max(mem, 0), 1)).frame(width: 90)
                Text("\(gb(status.memUsage)) of \(gb(status.memSize)) GB")
                    .font(.caption).monospacedDigit()
            }
        }
        .padding(12)
    }
}
