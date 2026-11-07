// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  BackgroundWork.swift
//  OffTheCloud
//
//  Settings › In progress (owner, 2026-11-07: the uploads and the background
//  work "should be in the same section"): this phone's uploads first, then
//  Background work (device release 152) - what the device works
//  through by itself for this library - new uploads' analysis, thumbnails,
//  places, On this day's measures, Smart search's descriptions, photo
//  dates, and kinds still to come - from Status.background_tasks, the
//  Status answer Settings already asks for every 5 s while it shows
//  (StatusViewModel). The section shows only while the list isn't empty:
//  one row per task with the device's words, a progress bar (a spinner
//  while the total isn't known) and "done of total". Older devices send no
//  list, and the section never shows. Android's BackgroundWork has the same
//  rules.
//

import SwiftUI

/// The rules: which rows the section shows, and what each says.
enum BackgroundWork {
    /// Whether In progress shows this phone's uploads: a run sending or
    /// with photos and videos waiting, or one held up (Wi-Fi only off
    /// Wi-Fi, the device full). Android's BackgroundWork.showsUploads.
    static func showsUploads(isUploading: Bool, pending: Int, held: Bool) -> Bool {
        isUploading || pending > 0 || held
    }

    /// One piece of work, as a row shows it.
    struct Item: Equatable, Identifiable {
        /// The task's kind ("smart_search"), a key never shown; "#<index>"
        /// when the device gave none.
        let id: String
        /// What it is doing, in the device's words.
        let text: String
        /// Done of total, done kept within 0...total.
        let done: Int
        /// 0: not known yet.
        let total: Int

        /// The bar's share (0-1); nil while the total isn't known (a spinner).
        var progress: Double? { total > 0 ? Double(done) / Double(total) : nil }

        /// "4,210 of 12,800", in the app's language; nil while the total
        /// isn't known.
        var count: String? { total > 0 ? S.appSettingsBackgroundCount(done: done, total: total) : nil }
    }

    /// The rows for a Status answer, in the device's order: none from an
    /// older device, or while nothing is going on (the section then doesn't
    /// show). A kind is listed once (the first), as Android's.
    static func items(_ status: Msg_Status?) -> [Item] {
        guard let status else { return [] }
        var seen = Set<String>()
        return status.backgroundTasks.enumerated().compactMap { i, t in
            let id = t.kind.isEmpty ? "#\(i)" : t.kind
            guard seen.insert(id).inserted else { return nil }
            let total = max(Int(t.total), 0)
            let done = total > 0 ? min(max(Int(t.done), 0), total) : max(Int(t.done), 0)
            return Item(id: id, text: t.text.trimmingCharacters(in: .whitespacesAndNewlines),
                        done: done, total: total)
        }
    }
}

/// Settings › In progress: this phone's uploads (UploadDetail: what is
/// being sent, a bar, how many wait, pause - or why the run waits), then
/// the device's background work with its footer. Shown only while either
/// has something; Android's InProgressSection.
struct InProgressSection: View {
    @ObservedObject var upload: UploadModel
    let items: [BackgroundWork.Item]

    var body: some View {
        let uploads = BackgroundWork.showsUploads(
            isUploading: upload.isUploading, pending: upload.totalPending, held: upload.halt != nil)
        if uploads || !items.isEmpty {
            Section {
                if uploads { UploadDetail(upload: upload) }
                ForEach(items) { BackgroundWorkRow(item: $0) }
            } header: {
                Text(S.appSettingsProgressTitle)
            } footer: {
                if !items.isEmpty { Text(S.appSettingsBackgroundFooter) }
            }
        }
    }
}

/// One piece of work: the device's words with "done of total" beside them
/// and a bar under them - or a spinner while the total isn't known. One
/// VoiceOver element: the words, and the count as its value.
struct BackgroundWorkRow: View {
    let item: BackgroundWork.Item

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            // The count on the words' baseline; a spinner beside their middle.
            HStack(alignment: item.count == nil ? .center : .firstTextBaseline, spacing: 8) {
                Text(item.text)
                Spacer(minLength: 8)
                if let count = item.count {
                    Text(count)
                        .font(.footnote)
                        .monospacedDigit()
                        .foregroundStyle(.secondary)
                } else {
                    ProgressView()
                        .controlSize(.small)
                }
            }
            if let progress = item.progress {
                ProgressView(value: progress)
                    .progressViewStyle(.linear)
            }
        }
        .padding(.vertical, 2)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(item.text)
        .accessibilityValue(item.count ?? "")
        .accessibilityAddTraits(.updatesFrequently)
    }
}
