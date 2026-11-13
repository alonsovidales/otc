// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  SaveToPhone.swift
//  OffTheCloud
//
//  Images' selection bar's Download (the owner, 2026-11-13: "it should
//  offer the option to download all the photos as photos to the device")
//  asks first: Save to Photos, or Download as ZIP (what it did before).
//  Save to Photos saves the picked photos and videos into this phone's
//  library one at a time, each as the viewer's Save saves one - its
//  original, with its own name and date - with a pill over the grid
//  meanwhile ("Saving 2 of 5…", Cancel) and a note for a few seconds once
//  it is done. Not saved again: the same content picked twice, what this
//  phone's library already holds (what its photo sync sent from here, or
//  Free Up Space read here: AssetSyncCache), and what was saved from this
//  screen already. The viewer's Save shows a check instead of its arrow
//  once its photo or video is saved, and saves no second copy; an edit
//  (new content) can be saved again. Android's SaveToPhone.kt has the
//  same rules.
//

import Photos
import SwiftUI

/// The pure rules, tested as Android's SaveToPhoneTest.
enum SaveToPhone {
    /// What a photo or video saved from here is remembered by: its
    /// content's hash, so its check follows the content (an edited photo,
    /// a new hash, can be saved again), else its path.
    static func key(hash: String, path: String) -> String {
        hash.isEmpty ? path : hash
    }

    /// What Save to Photos does with the picked items.
    struct Plan: Equatable {
        /// To save, in the picked order.
        var save: [PhotoGalleryVM.Item]
        /// How many were in the library already, so not saved again.
        var already: Int
    }

    /// The picked items, in the grid's order: the same content picked
    /// twice is saved once; content this phone's library holds (`onPhone`:
    /// hashes) or that was saved from here already (`saved`: keys) is left
    /// out and counted as already there. Without a hash nothing can be
    /// matched: always saved.
    static func plan(_ items: [PhotoGalleryVM.Item], onPhone: Set<String>, saved: Set<String>) -> Plan {
        var plan = Plan(save: [], already: 0)
        var seen = Set<String>()
        for it in items {
            if it.hash.isEmpty {
                plan.save.append(it)
            } else if seen.insert(it.hash).inserted {
                if onPhone.contains(it.hash) || saved.contains(key(hash: it.hash, path: it.path)) {
                    plan.already += 1
                } else {
                    plan.save.append(it)
                }
            }
        }
        return plan
    }

    /// The note once it is done with nothing failed: how many were saved,
    /// then how many were there already; nil for neither.
    static func note(saved: Int, already: Int) -> String? {
        let there = already > 0 ? S.appPhotosDownloadAlready(count: already) : nil
        guard saved > 0 else { return there }
        let text = S.iosPhotosSavedToPhotos(count: saved)
        return there.map { text + " " + $0 } ?? text
    }

    /// Photos refused the file's format (PHPhotosError.invalidResource):
    /// the alert names the file rather than quoting Photos' error number.
    static func refusedFormat(_ error: Error) -> Bool {
        let e = error as NSError
        return e.domain == PHPhotosErrorDomain && e.code == PHPhotosError.Code.invalidResource.rawValue
    }

    /// Which one is being saved (from 1) of how many: the pill's line.
    struct Progress: Equatable {
        let done: Int
        let total: Int
    }

    /// What came of it: the note's counts, worded when it shows.
    struct Done: Equatable {
        let saved: Int
        let already: Int
    }
}

// MARK: - Images' side (PhotoGalleryVM)

extension PhotoGalleryVM {
    /// Saved to the library from here (savedToPhotos).
    func isSaved(_ it: Item) -> Bool {
        savedToPhotos.contains(SaveToPhone.key(hash: it.hash, path: it.path))
    }

    /// The open item was saved from here: the viewer's Save is a check.
    var openSaved: Bool {
        openItem.map { isSaved($0) } ?? false
    }

    /// The choice's Save to Photos: the picked photos and videos, as the
    /// bar shows them now, saved one at a time. It runs here, not in the
    /// bar: closing the selection doesn't stop it, and the selection stays
    /// as it is. Never two at once.
    func saveSelectionToPhotos() {
        guard saveToPhotosTask == nil, preparing == nil else { return }
        let picked = pickedInOrder()
        guard !picked.isEmpty else { return }
        // The bar waits, its Download a spinner, as during Delete.
        preparing = .download
        hideSaveToPhotosNote()
        saveToPhotosTask = Task { await saveToPhotos(picked) }
    }

    /// The pill's Cancel: the one being saved stops (its download at its
    /// next piece, its temporary file removed), what was saved stays
    /// saved, and nothing more is said. The pill goes at once; the bar
    /// waits until that one has stopped.
    func cancelSaveToPhotos() {
        saveToPhotosTask?.cancel()
        savingToPhotos = nil
    }

    /// The note goes: tapped, or another Save to Photos starting.
    func hideSaveToPhotosNote() {
        saveToPhotosNoteTask?.cancel()
        saveToPhotosNote = nil
    }

    private func saveToPhotos(_ picked: [Item]) async {
        defer {
            savingToPhotos = nil
            preparing = nil
            saveToPhotosTask = nil
        }
        // Asked as the viewer's Save asks; refused, nothing is saved.
        guard await photosAccess() else { return }
        let hashes = Set(picked.map(\.hash).filter { !$0.isEmpty })
        let onPhone = await Task.detached(priority: .userInitiated) { Self.inLibrary(hashes) }.value
        let plan = SaveToPhone.plan(picked, onPhone: onPhone, saved: savedToPhotos)
        let total = plan.save.count
        var saved = 0
        var already = plan.already
        var failed = 0
        // The first one not saved, and its name: the alert's reason.
        var firstFailure: (error: Error, name: String)?
        if total > 0 {
            // The screen stays on while they go: a few hundred take a
            // while, and a locked phone would stop them. Off again after,
            // unless it was kept on already.
            let idle = UIApplication.shared.isIdleTimerDisabled
            UIApplication.shared.isIdleTimerDisabled = true
            defer { if !idle { UIApplication.shared.isIdleTimerDisabled = false } }
            for (i, it) in plan.save.enumerated() {
                guard !Task.isCancelled else { return }
                // The viewer's Save took it meanwhile: there already.
                if isSaved(it) {
                    already += 1
                    continue
                }
                savingToPhotos = SaveToPhone.Progress(done: i + 1, total: total)
                do {
                    // As the viewer's Save, but a format Photos refuses is
                    // one not saved: there is no picture on screen for it.
                    try await saveOriginal(it)
                    saved += 1
                } catch {
                    guard !Task.isCancelled else { return }
                    failed += 1
                    if firstFailure == nil { firstFailure = (error, (it.path as NSString).lastPathComponent) }
                }
            }
        }
        guard !Task.isCancelled else { return }
        if let first = firstFailure {
            notice = .someNotSaved(failed, total, first.error, first.name)
            showAlert = true
        } else {
            showSaveToPhotosNote(SaveToPhone.Done(saved: saved, already: already))
        }
    }

    /// What came of it, for 4 seconds (a tap hides it sooner), said to
    /// VoiceOver.
    private func showSaveToPhotosNote(_ done: SaveToPhone.Done) {
        saveToPhotosNote = done
        if let text = SaveToPhone.note(saved: done.saved, already: done.already) { Announce.polite(text) }
        saveToPhotosNoteTask?.cancel()
        saveToPhotosNoteTask = Task { [weak self] in
            try? await Task.sleep(nanoseconds: 4_000_000_000)
            guard !Task.isCancelled else { return }
            self?.saveToPhotosNote = nil
        }
    }

    /// Which of these hashes this phone's own library holds - the photo
    /// sync's and Free Up Space's records whose assets are still there
    /// (DeletePhoneCopyModel.phoneAssets). Only with access to read the
    /// library: with add-only access nothing is known, and all are saved.
    /// Off the main thread: the records and the library are read here.
    nonisolated private static func inLibrary(_ hashes: Set<String>) -> Set<String> {
        let access = PHPhotoLibrary.authorizationStatus(for: .readWrite)
        guard !hashes.isEmpty, access == .authorized || access == .limited else { return [] }
        return Set(DeletePhoneCopyModel.phoneAssets(withHashes: hashes, saved: []).map(\.hash))
    }
}

// MARK: - The pill

/// Save to Photos of the selection over the grid's bottom, where its other
/// pill shows (StillWaitingPill): which one is being saved, with Cancel;
/// then, for a few seconds, what came of it - hidden by a tap.
struct SaveToPhotosPill: View {
    @ObservedObject var vm: PhotoGalleryVM

    var body: some View {
        Group {
            if let progress = vm.savingToPhotos {
                HStack(spacing: 10) {
                    ProgressView().controlSize(.small)
                    Text(S.appPhotosDownloadSaving(done: progress.done, total: progress.total))
                        .font(.subheadline)
                    Button(S.commonCancel) { vm.cancelSaveToPhotos() }
                        .font(.subheadline.weight(.semibold))
                }
                .modifier(SaveToPhotosCapsule())
            } else if let done = vm.saveToPhotosNote,
                      let text = SaveToPhone.note(saved: done.saved, already: done.already) {
                HStack(spacing: 8) {
                    Image(systemName: "checkmark.circle.fill")
                        .foregroundStyle(.green)
                        .accessibilityHidden(true)
                    Text(text)
                        .font(.subheadline)
                }
                .modifier(SaveToPhotosCapsule())
                .accessibilityElement(children: .combine)
                .onTapGesture { vm.hideSaveToPhotosNote() }
            }
        }
        .animation(.easeInOut(duration: 0.2), value: vm.savingToPhotos)
        .animation(.easeInOut(duration: 0.2), value: vm.saveToPhotosNote)
    }
}

/// The grid's pills' capsule (StillWaitingPill's), kept off the screen's
/// edges when the line is long.
private struct SaveToPhotosCapsule: ViewModifier {
    func body(content: Content) -> some View {
        content
            .padding(.horizontal, 16)
            .padding(.vertical, 10)
            .background(.regularMaterial, in: Capsule())
            .shadow(color: .black.opacity(0.18), radius: 6, y: 2)
            .padding(.horizontal, 16)
            .transition(.opacity)
    }
}
