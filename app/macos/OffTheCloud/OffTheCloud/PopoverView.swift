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

    var body: some View {
        // Issue #47: swapped in *inline*, not as a .sheet() — a sheet
        // presented from a MenuBarExtra(.window) popover has a real quirk
        // where any state change inside it (here: tapping a directory row)
        // makes the popover's own window lose key status and auto-close,
        // since MenuBarExtra(.window) closes itself on exactly that
        // transition. Staying inside the one already-open, already-key
        // popover window (same trick showSettings already uses below)
        // sidesteps the whole problem.
        if showRemotePicker {
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
            }

            if showSettings {
                SettingsInlineView()
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
                Menu {
                    Button("Local Folder…") {
                        sync.addFolder()
                    }
                    Button("Remote Folder…") {
                        showRemotePicker = true
                    }
                } label: {
                    Label("Add Folder", systemImage: "plus.circle.fill")
                }
                .menuStyle(.borderlessButton)
                .fixedSize()
                Spacer()
                // status / version | optional
            }
        }
        .padding(12)
        .frame(width: 360) // similar to OneDrive panel
        .onAppear {
            // Start binding only once; safe if already bound.
            sync.bind(settings: settings)
        }
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
        if panel.runModal() == .OK, let url = panel.url {
            sync.addRemoteFolder(remotePath: remotePath, localURL: url)
        }
    }

    private var statusColor: Color {
        switch sync.overallStatus {
        case "Connected": return .green
        case "Disconnected": return .yellow
        case "Missing domain/password": return .red
        default: return .gray
        }
    }
}

struct FolderRow: View {
    let folder: SyncModel.TrackedFolder
    let remove: () -> Void

    var body: some View {
        HStack(spacing: 8) {
            Image(systemName: "folder.fill")
                .foregroundStyle(Color.accentColor)
            Text(folder.url.lastPathComponent)
                .lineLimit(1)
            Spacer()
            FolderStateView(state: folder.state, watchingLabel: "Watching")
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
            Image(systemName: "folder.fill.badge.minus")
                .foregroundStyle(Color.accentColor)
                .overlay(alignment: .bottomTrailing) {
                    Image(systemName: "arrow.down.circle.fill")
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
                Text(currentFile ?? "Checking…")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .truncationMode(.middle)
                    .frame(maxWidth: 140, alignment: .trailing)
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
                        Text(currentFile)
                            .font(.caption2)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                            .truncationMode(.middle)
                    }
                    .frame(maxWidth: 140, alignment: .trailing)
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

struct SettingsInlineView: View {
    @ObservedObject private var settings = SettingsStore.shared
    var body: some View {
        VStack(spacing: 8) {
            HStack {
                Text("Settings").font(.subheadline.bold())
                Spacer()
            }
            TextField("Domain (e.g. cala.off-the.cloud)", text: $settings.domain)
                .textFieldStyle(.roundedBorder)
                .disableAutocorrection(true)
            SecureField("Password", text: $settings.password)
                .textFieldStyle(.roundedBorder)
            HStack {
                Image(systemName: settings.ready ? "checkmark.circle" : "exclamationmark.triangle")
                    .foregroundStyle(settings.ready ? .green : .orange)
                Text(settings.ready ? "Syncing will start automatically." :
                     "Enter both domain and password to start syncing.")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                Spacer()
            }
        }
        .padding(8)
        .background(.thinMaterial, in: RoundedRectangle(cornerRadius: 10))
    }
}
