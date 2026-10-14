// SPDX-License-Identifier: AGPL-3.0-or-later

import SwiftUI

// Issue #195: Files' Storage view, the third view beside List and Grid -
// what takes space in the open folder: each file and folder in it with a
// bar, its share of the folder and its size, largest first, as the device
// orders them (GetFolderUsage, asked only while this view shows). The same
// view as Android's and the web's.

/// One file or folder of the open folder, as the Storage view shows it.
struct FolderUsageRow: Identifiable {
    /// (path, is_dir): a file and a folder of one name are two rows.
    let id: String
    /// Its full path, as the listing names it.
    let path: String
    let name: String
    let isDir: Bool
    /// Its files and their older versions.
    let size: Int64
    /// Its share of the folder's whole size (0...1); nil when the folder
    /// takes no space at all - then no bar.
    let share: Double?
    /// The listing's row of the same path and kind, for its icon, its
    /// marks and what a tap opens; nil for a folder holding only older
    /// versions, which the listing leaves out.
    let listed: FileRow?
}

enum FolderUsageModel {
    /// Whether an answer (FolderUsage.path, always with its trailing
    /// slash) is for `folder` (the one shown): requests overlap, and the
    /// answer for a folder left is dropped.
    static func answers(_ usagePath: String, folder: String) -> Bool {
        usagePath == withSlash(folder)
    }

    /// Whether the answer to ask number `ask` is shown: it is for the
    /// folder shown (answers), and no ask went out after it (`latest`, the
    /// last one's number) - two asks for one folder overlap, and the older
    /// answer can land last.
    static func takes(_ usagePath: String, folder: String, ask: Int, latest: Int) -> Bool {
        ask == latest && answers(usagePath, folder: folder)
    }

    static func withSlash(_ folder: String) -> String {
        folder.hasSuffix("/") ? folder : folder + "/"
    }

    /// The folder's whole size: its files and their older versions - the
    /// base of the rows' shares, and what the summary says.
    static func total(_ u: Msg_FolderUsage) -> Int64 { u.filesSize + u.versionsSize }

    /// The rows, in the device's order (largest first; never sorted
    /// again), each joined with the listing (`listing`, of `folder`) by
    /// its path and kind.
    static func rows(_ u: Msg_FolderUsage, listing: [FileRow], folder: String) -> [FolderUsageRow] {
        let whole = total(u)
        var listed: [String: FileRow] = [:]
        for r in listing where r.path != ".." {
            listed[key(full(r.path, in: folder), dir: r.isDir)] = r
        }
        return u.entries.map { e in
            let path = full(e.path, in: folder)
            let size = e.filesSize + e.versionsSize
            let k = key(path, dir: e.isDir)
            return FolderUsageRow(
                id: k,
                path: path,
                name: (path as NSString).lastPathComponent,
                isDir: e.isDir,
                size: size,
                share: whole > 0 ? Double(size) / Double(whole) : nil,
                listed: listed[k]
            )
        }
    }

    /// A share as VoiceOver says it: a whole percentage in the app's
    /// language ("45 %" in German), "under 1%" for one that takes some
    /// space but less, and 0% when the folder takes none.
    static func shareText(_ share: Double?, locale: Locale) -> String {
        let s = share ?? 0
        if s > 0 && s < 0.01 { return S.appFilesUsageShareSmall }
        return s.formatted(.percent.precision(.fractionLength(0)).locale(locale))
    }

    /// The line under a row's name: its size and its share - only its
    /// size when the folder takes no space (no share shows, as no bar
    /// does; the web's empty share cell).
    static func rowText(size: String, share: Double?, locale: Locale) -> String {
        guard let share else { return size }
        return S.appFilesUsageRow(size: size, share: shareText(share, locale: locale))
    }

    /// What the view shows for an answer, top to bottom: the folder's
    /// summary, its rows (or that nothing in it takes space), and the
    /// footnote - always the three, as on the web and Android.
    enum Part: Equatable { case summary, rows, empty, note }

    static func parts(_ u: Msg_FolderUsage) -> [Part] {
        [.summary, u.entries.isEmpty ? .empty : .rows, .note]
    }

    private static func key(_ path: String, dir: Bool) -> String { (dir ? "d:" : "f:") + path }

    /// A path as the listing gives it (a full path; a bare name from an
    /// older device) made full.
    private static func full(_ path: String, in folder: String) -> String {
        path.hasPrefix("/") ? path : withSlash(folder) + path
    }
}

/// What the Storage view shows for the open folder.
enum FolderUsageState: Equatable {
    /// Asked for; nothing of this folder's yet.
    case loading
    case loaded(Msg_FolderUsage)
    /// The device is too old to say (unknown_payload, issue #195): not
    /// asked again until the next sign-in.
    case needsUpdate
    /// No answer (no connection, none in time): ours, with Try again; or
    /// the device's own words.
    case failed(String?)
}

/// The Storage view's list: the summary, a row per file and folder with
/// its bar, and the footnote. No selection here (List has it).
struct FolderUsageList: View {
    @ObservedObject var vm: FilesExplorerViewModel
    /// A file's or folder's row was tapped (".." included).
    let open: (FileRow) -> Void

    var body: some View {
        List {
            if vm.path != "/" {
                Button {
                    open(Self.up)
                } label: {
                    HStack {
                        Image(systemName: "folder.fill")
                            .foregroundColor(.accentColor)
                            .frame(width: 20)
                        Text(verbatim: "..")
                        Spacer()
                    }
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
            }
            content
        }
        .listStyle(.plain)
        // Read again, the listing and the sizes (FilesExplorerViewModel.load).
        .refreshable { await vm.load() }
    }

    /// ".." as the listing's row: up a folder.
    private static let up: FileRow = {
        var f = Msg_File()
        f.mime = "inode/directory"
        f.path = ".."
        return FileRow(file: f)
    }()

    @ViewBuilder
    private var content: some View {
        switch vm.usage ?? .loading {
        case .loading:
            HStack(spacing: 8) {
                ProgressView()
                Text(S.commonLoading).foregroundStyle(.secondary)
            }
            .listRowSeparator(.hidden)
        case .needsUpdate:
            Text(S.appFilesUsageNeedsUpdate)
                .foregroundStyle(.secondary)
                .listRowSeparator(.hidden)
        case .failed(let words):
            VStack(alignment: .leading, spacing: 10) {
                Text(words ?? S.appFilesUsageFailed).foregroundStyle(.secondary)
                Button(LoadProblem.tryAgain) { Task { await vm.loadUsage() } }
                    .buttonStyle(.bordered)
            }
            .listRowSeparator(.hidden)
            .onAppear { Announce.polite(words ?? S.appFilesUsageFailed) }
        case .loaded(let u):
            ForEach(FolderUsageModel.parts(u), id: \.self) { part in
                switch part {
                case .summary:
                    summary(u)
                case .rows:
                    ForEach(FolderUsageModel.rows(u, listing: vm.rows, folder: vm.path)) { row in
                        usageRow(row)
                    }
                case .empty:
                    Text(S.appFilesUsageEmpty)
                        .foregroundStyle(.secondary)
                        .listRowSeparator(.hidden)
                case .note:
                    Text(S.appFilesUsageNote)
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                        .listRowSeparator(.hidden)
                        .padding(.top, 4)
                }
            }
        }
    }

    /// "This folder: 1.2 TB in 45,210 files", and how much of it older
    /// versions take when they take any.
    private func summary(_ u: Msg_FolderUsage) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(S.appFilesUsageTotal(size: formatByteCount(FolderUsageModel.total(u)), count: Int(u.fileCount)))
                .font(.subheadline.weight(.semibold))
            if u.versionsSize > 0 {
                Text(S.appFilesUsageVersions(size: formatByteCount(u.versionsSize)))
                    .font(.footnote)
                    .foregroundStyle(.secondary)
            }
        }
        .fixedSize(horizontal: false, vertical: true)
        .accessibilityElement(children: .combine)
        .listRowSeparator(.hidden)
    }

    private func usageRow(_ row: FolderUsageRow) -> some View {
        let size = formatByteCount(row.size)
        let share = FolderUsageModel.shareText(row.share, locale: L10n.shared.locale)
        let listed = row.listed
        // The listing's marks, as List shows them, by the name: the bars
        // all start and end at the same place.
        let keptOut = listed.map { row.isDir && $0.outOfImages && vm.outOfImagesSupported } ?? false
        let uploadOnly = listed?.uploadOnly ?? false
        let marks = (keptOut ? [OutOfImagesText.state] : [])
            + (uploadOnly ? [row.isDir ? S.appFilesUploadOnly : S.appFilesInUploadOnly] : [])
        return Button {
            open(listed ?? Self.fileRow(row))
        } label: {
            HStack(alignment: .center, spacing: 10) {
                if let listed, vm.openingPath == listed.path {
                    ProgressView().frame(width: 20)
                } else {
                    Image(systemName: row.isDir ? "folder.fill" : (listed.map { $0.raw.mime.hasPrefix("image/") } == true ? "photo" : "doc"))
                        .foregroundColor(row.isDir ? .accentColor : .secondary)
                        .frame(width: 20)
                }
                VStack(alignment: .leading, spacing: 4) {
                    HStack(spacing: 6) {
                        Text(row.name).lineLimit(1).truncationMode(.middle)
                        if keptOut {
                            Image(systemName: "eye.slash.fill")
                                .font(.caption)
                                .foregroundColor(.accentColor)
                        }
                        if uploadOnly {
                            Image(systemName: "lock.fill")
                                .font(.caption)
                                .foregroundColor(row.isDir ? .accentColor : .secondary)
                        }
                    }
                    if let s = row.share {
                        UsageBar(share: s)
                    }
                    Text(FolderUsageModel.rowText(size: size, share: row.share, locale: L10n.shared.locale))
                        .font(.caption2)
                        .monospacedDigit()
                        .foregroundColor(.secondary)
                }
                Spacer(minLength: 0)
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(S.appFilesUsageRowLabel(name: row.name, size: size, share: share))
        // The marks' meaning, after the name and the sizes.
        .accessibilityValue(marks.formatted(.list(type: .and).locale(L10n.shared.locale)))
        .accessibilityAddTraits(.isButton)
    }

    /// A row the listing doesn't have (a folder of older versions only),
    /// as one, for its tap.
    private static func fileRow(_ row: FolderUsageRow) -> FileRow {
        var f = Msg_File()
        f.path = row.path
        if row.isDir { f.mime = "inode/directory" }
        return FileRow(file: f)
    }
}

/// A row's share of the folder: a thin bar, filled from the leading side.
private struct UsageBar: View {
    let share: Double

    var body: some View {
        Capsule()
            .fill(Color.secondary.opacity(0.18))
            .frame(height: 4)
            .overlay(alignment: .leading) {
                GeometryReader { g in
                    Capsule()
                        .fill(Color.accentColor)
                        // Something shows for a share under a pixel.
                        .frame(width: share > 0 ? max(2, g.size.width * min(1, share)) : 0)
                }
            }
            .accessibilityHidden(true)
    }
}
