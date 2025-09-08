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
        print("UPLOADING: \(file)")
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

struct UploadBar: View {
    @EnvironmentObject var upload: UploadModel

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
        .padding(12)
        .background(.ultraThinMaterial)
        .cornerRadius(12)
        .shadow(radius: 6)
    }
}
