// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  UploadModel.swift
//  OffTheCloud
//
//  Created by Alonso Vidales on 8/9/25.
//


import Foundation
import SwiftUI

final class UploadModel: ObservableObject {
    static let shared = UploadModel()
    @Published var totalPending: Int = 0
    @Published var currentName: String = ""
    @Published var progress: Double = 0.0
    @Published var isUploading: Bool = false

    // Issue #30: a simple in-session pause flag, checked between uploads
    // (see PhotoSync.runForeground) — doesn't cancel a request already in
    // flight, just stops starting new ones until resumed.
    @Published var isPaused: Bool = false

    /// Log Out.
    func reset() {
        DispatchQueue.main.async {
            self.totalPending = 0
            self.currentName = ""
            self.progress = 0
            self.isUploading = false
            self.isPaused = false
        }
    }

    func togglePause() {
        DispatchQueue.main.async {
            self.isPaused.toggle()
        }
    }

    func begin(total: Int) {
        print("PENDING UPLOADS: \(total)")
        DispatchQueue.main.async {
            self.totalPending = total
            self.progress = 0
            self.isUploading = total > 0
            self.isPaused = false
        }
    }

    func step(file: String, index: Int, total: Int) {
        print("UPLOADING: \(file) index: \(index) total: \(total)")
        DispatchQueue.main.async {
            self.currentName = file
            self.totalPending = max(0, total - index)
            self.progress = total > 0 ? Double(index) / Double(total) : 0
            self.isUploading = (index < total)
        }
    }

    func complete() {
        DispatchQueue.main.async {
            self.totalPending = 0
            self.currentName = ""
            self.progress = 1.0
            self.isUploading = false
            self.isPaused = false
        }
    }
}

/// The full upload detail — used both by the expanded global indicator and
/// by the Uploads section in Settings (issue #14's fallback: if even a
/// tap-to-expand hairline is unwelcome, the info is always reachable there
/// without any global chrome at all).
struct UploadDetail: View {
    @ObservedObject var upload: UploadModel

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text(upload.isPaused ? "Paused" : (upload.isUploading ? "Uploading…" : "Upload queue"))
                    .font(.subheadline).bold()
                Spacer()
                if upload.totalPending > 0 {
                    Text("\(upload.totalPending) left").font(.caption)
                }
                // Issue #30: pause/resume the sync right from here.
                if upload.isUploading || upload.totalPending > 0 {
                    Button {
                        upload.togglePause()
                    } label: {
                        Image(systemName: upload.isPaused ? "play.fill" : "pause.fill")
                    }
                    .font(.caption)
                    .buttonStyle(.bordered)
                }
            }
            ProgressView(value: upload.progress)
                .progressViewStyle(.linear)
            if !upload.currentName.isEmpty {
                Text(upload.currentName).lineLimit(1).font(.caption2).foregroundColor(.secondary)
            }
        }
    }
}

