// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  TextInPhotos.swift
//  OffTheCloud
//
//  Text in photos (device release 180): the device reads the text in each
//  photo - signs, documents, screenshots - with a model of its own and keeps
//  it encrypted. Android follows the same rules:
//    - Only on a device that has it (Settings.text_in_photos_enabled, on
//      unless the user turns it off; absent = a device before it: no
//      section, no text, the search as before).
//    - Settings: a "Text in photos" section after Smart search, its switch
//      named as the section; turning it off asks first (the device deletes
//      the text it read).
//    - The search: while it is on, the free-text row ("Search photos for
//      “…”") is offered as Smart search's is, with Smart search off or its
//      model not there yet too (SmartSearchSupport.searchable): the device
//      answers with the photos whose text holds the words first.
//    - Info: the text read in the photo (FileExifInfo.text, a line of it a
//      line there), under "Text in this photo" after the details and before
//      People, as it came and selectable, with Copy beside the heading -
//      "Copied" for a moment once copied.
//  Pets (the same release) is a switch in Settings' People section, under
//  Face Recognition (SettingsView): the pets it finds are people in People.
//

import SwiftUI
import UIKit

/// What Info shows of the text the device read in a photo.
enum PhotoText {
    /// The text as it came - nil when there is none to show: no answer, a
    /// device before Text in photos (it never sets it), a photo it found no
    /// text in or hasn't read yet, or Text in photos off (all "").
    static func shown(_ info: Msg_FileExifInfo?) -> String? {
        guard let text = info?.text,
              !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return nil }
        return text
    }

    /// How long Copy says "Copied" after the last copy.
    static let copiedFor: Duration = .milliseconds(2500)
}

/// Info's "Text in this photo": the text as the device read it, line by
/// line, selectable (the system's Copy on a long press), and Copy beside
/// the heading for all of it.
struct PhotoTextSection: View {
    let text: String
    /// Copy was tapped: it says "Copied" until `PhotoText.copiedFor` after
    /// the last tap (`copies` tells the last from the ones before).
    @State private var copied = false
    @State private var copies = 0

    var body: some View {
        Section {
            Text(text)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
                .fixedSize(horizontal: false, vertical: true)
        } header: {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Text(S.appPhotosViewerTextTitle)
                    .accessibilityAddTraits(.isHeader)
                Spacer(minLength: 0)
                Button(action: copy) {
                    Label(copied ? S.appPhotosViewerTextCopied : S.appPhotosViewerTextCopy,
                          systemImage: copied ? "checkmark" : "doc.on.doc")
                }
                .buttonStyle(.borderless)
                .font(.subheadline)
                .textCase(nil)
                .fixedSize()
            }
        }
        // Another photo's text: nothing copied of it yet.
        .onChange(of: text) { _, _ in copied = false }
    }

    private func copy() {
        UIPasteboard.general.string = text
        copies += 1
        let mine = copies
        copied = true
        Announce.polite(S.appPhotosViewerTextCopied)
        Task {
            try? await Task.sleep(for: PhotoText.copiedFor)
            if copies == mine { copied = false }
        }
    }
}

/// Settings > Text in photos: its switch, named as the section. Turning it
/// off asks first: the device deletes the text it read in the photos.
struct TextInPhotosSettingsSection: View {
    @ObservedObject var device: DeviceSettingsViewModel
    let on: Bool
    @State private var confirmOff = false

    var body: some View {
        Section(
            header: Text(S.appSettingsTextPhotosTitle),
            footer: Text(S.appSettingsTextPhotosFooter)
        ) {
            Toggle(
                S.appSettingsTextPhotosTitle,
                isOn: Binding(
                    get: { on },
                    set: { newValue in
                        if newValue {
                            Task { await device.toggleTextInPhotos(true) }
                        } else {
                            confirmOff = true
                        }
                    }
                )
            )
            .disabled(device.savingTextInPhotos)
        }
        .alert(S.appSettingsTextPhotosOffTitle, isPresented: $confirmOff) {
            Button(S.appSettingsTextPhotosOffConfirm, role: .destructive) {
                Task { await device.toggleTextInPhotos(false) }
            }
            Button(S.commonCancel, role: .cancel) {}
        } message: {
            Text(S.appSettingsTextPhotosOffText)
        }
    }
}
