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

    func begin(total: Int) {
        print("PENDING UPLOADS: \(total)")
        DispatchQueue.main.async {
            self.totalPending = total
            self.progress = 0
            self.isUploading = total > 0
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
                Text(upload.isUploading ? "Uploading…" : "Upload queue")
                    .font(.subheadline).bold()
                Spacer()
                if upload.totalPending > 0 {
                    Text("\(upload.totalPending) left").font(.caption)
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

/// Global upload indicator (issue #14). Used to be a floating card tall
/// enough to cover other screens' own bottom UI (e.g. the Files tab's
/// Edit-mode selection toolbar). Now it's just a hairline progress rule
/// sitting right above the tab bar — thin enough to not obscure anything —
/// that expands into the full detail card only when tapped, and collapses
/// again on a second tap.
struct UploadBar: View {
    @EnvironmentObject var upload: UploadModel
    @State private var expanded = false

    var body: some View {
        VStack(spacing: 0) {
            if expanded {
                UploadDetail(upload: upload)
                    .padding(12)
                    .background(.ultraThinMaterial)
                    .transition(.move(edge: .bottom).combined(with: .opacity))
            }

            // The visible rule stays a 3pt hairline, but a 3pt-tall region
            // is nearly impossible to actually land a finger on — pad the
            // *tappable* area out to something finger-sized while keeping
            // the drawn line just as thin.
            GeometryReader { geo in
                ZStack(alignment: .leading) {
                    Rectangle().fill(Color.secondary.opacity(0.2))
                    Rectangle().fill(Color.accentColor)
                        .frame(width: geo.size.width * CGFloat(min(max(upload.progress, 0), 1)))
                }
            }
            .frame(height: 3)
            .padding(.vertical, 9) // 3pt line + padding = 21pt tap target
            .contentShape(Rectangle())
            .onTapGesture {
                withAnimation(.easeInOut(duration: 0.2)) { expanded.toggle() }
            }
        }
        .background(expanded ? AnyShapeStyle(.ultraThinMaterial) : AnyShapeStyle(.clear))
    }
}
