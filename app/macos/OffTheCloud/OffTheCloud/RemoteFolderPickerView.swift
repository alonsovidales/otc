import SwiftUI

// Issue #47: lets the user browse the device's remote file tree and pick a
// directory to mirror down locally. There's no native macOS control for
// browsing a *remote* filesystem (NSOpenPanel only knows about local
// disks), so this is a small purpose-built list view, one directory level
// at a time — mirrors the drill-down navigation FilesExplorer.tsx already
// does on the web.
struct RemoteFolderPickerView: View {
    let onChoose: (String) -> Void
    let onCancel: () -> Void

    @State private var currentPath: String = "/"
    @State private var entries: [SyncModel.RemoteEntry] = []
    @State private var loading = false
    @State private var errorMessage: String?

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Button {
                    onCancel()
                } label: {
                    Text("Cancel")
                }
                .buttonStyle(.plain)
                Spacer()
                Text("Choose a Remote Folder")
                    .font(.headline)
                Spacer()
                // Balances the Cancel button so the title stays centered.
                Text("Cancel").hidden()
            }
            .padding(12)

            HStack(spacing: 8) {
                Button {
                    goUp()
                } label: {
                    Image(systemName: "chevron.up")
                }
                .buttonStyle(.plain)
                .disabled(currentPath == "/")

                Text(currentPath)
                    .font(.system(.body, design: .monospaced))
                    .lineLimit(1)
                    .truncationMode(.head)
                    .foregroundStyle(.secondary)
                Spacer()
            }
            .padding(.horizontal, 12)
            .padding(.bottom, 8)

            Divider()

            if loading {
                ProgressView()
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if let errorMessage {
                VStack(spacing: 8) {
                    Image(systemName: "exclamationmark.triangle.fill")
                        .foregroundStyle(.red)
                    Text(errorMessage)
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                        .multilineTextAlignment(.center)
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .padding()
            } else {
                List {
                    ForEach(entries.filter(\.isDir)) { entry in
                        Button {
                            currentPath = entry.path
                        } label: {
                            HStack {
                                Image(systemName: "folder.fill")
                                    .foregroundStyle(Color.accentColor)
                                Text(entry.name)
                                Spacer()
                                Image(systemName: "chevron.right")
                                    .font(.caption)
                                    .foregroundStyle(.tertiary)
                            }
                        }
                        .buttonStyle(.plain)
                    }
                    if entries.filter(\.isDir).isEmpty {
                        Text("No subfolders here.")
                            .font(.footnote)
                            .foregroundStyle(.secondary)
                    }
                }
                .listStyle(.plain)
            }

            Divider()

            HStack {
                Spacer()
                Button {
                    onChoose(currentPath)
                } label: {
                    Text("Choose “\((currentPath as NSString).lastPathComponent.isEmpty ? "/" : (currentPath as NSString).lastPathComponent)”")
                }
                .buttonStyle(.borderedProminent)
            }
            .padding(12)
        }
        // Matches PopoverView's own width (360) so swapping between the two
        // inline — see PopoverView.body's comment for why it's inline
        // rather than a .sheet() — doesn't visibly resize the popover.
        .frame(width: 360, height: 360)
        .task(id: currentPath) {
            await load()
        }
    }

    private func goUp() {
        guard currentPath != "/" else { return }
        let trimmed = currentPath.hasSuffix("/") ? String(currentPath.dropLast()) : currentPath
        let parent = (trimmed as NSString).deletingLastPathComponent
        currentPath = parent.isEmpty ? "/" : parent
    }

    private func load() async {
        loading = true
        errorMessage = nil
        do {
            entries = try await SyncModel.shared.listRemoteDirectory(currentPath)
        } catch {
            errorMessage = error.localizedDescription
        }
        loading = false
    }
}
