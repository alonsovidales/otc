import SwiftUI

struct PopoverView: View {
    @EnvironmentObject var settings: SettingsStore
    @EnvironmentObject var sync: SyncModel

    @State private var showSettings = false

    var body: some View {
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

            // Folders list
            ScrollView {
                VStack(spacing: 8) {
                    ForEach(sync.folders) { f in
                        FolderRow(folder: f, remove: { sync.removeFolder(f) })
                    }
                }
                .padding(.vertical, 4)
            }.frame(maxHeight: 280)

            HStack {
                Button {
                    sync.addFolder()
                } label: {
                    Label("Add Folder", systemImage: "plus.circle.fill")
                }
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
            ProgressView(value: folder.progress)
                .progressViewStyle(.linear)
                .frame(width: 120)
                .tint(progressColor)
            Text("\(Int(folder.progress * 100))%")
                .monospacedDigit()
                .foregroundStyle(.secondary)
            Button(role: .destructive) {
                remove()
            } label: {
                Image(systemName: "minus.circle")
            }.buttonStyle(.plain)
        }
        .padding(8)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 10))
    }

    private var progressColor: Color {
        let p = folder.progress
        if p < 0.20 { return .red }
        if p < 0.90 { return .yellow }
        return .green
    }
}

struct SettingsInlineView: View {
    @EnvironmentObject var settings: SettingsStore
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
