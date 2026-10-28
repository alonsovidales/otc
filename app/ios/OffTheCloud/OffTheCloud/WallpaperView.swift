// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  WallpaperView.swift
//  OffTheCloud
//
//  The screen opened by "Use as wallpaper…" (or "Wallpaper settings…") on
//  the collection open in Images (Wallpaper.swift has the rules and the
//  work). The owner's one hard requirement comes first and can't be
//  missed: before anything is stored, a box at the top says in bold that
//  these photos will be stored on this phone, how many, from which
//  collection and until when, and that they download on Wi-Fi only when
//  that is on. Then the two ways - the lock screen, through an album in
//  the Photos app, and a home screen widget - each with its own button
//  saying that it stores copies; nothing starts by itself.
//

import SwiftUI
import UIKit

/// The collection the screen is for.
struct WallpaperTarget: Identifiable, Equatable {
    let id: String
    let name: String
}

struct WallpaperView: View {
    let target: WallpaperTarget
    let onClose: () -> Void
    @ObservedObject var model: WallpaperModel = .shared

    /// The collection's photos, once listed (the notice's count); nil while
    /// they are being listed.
    @State private var photos: [WallpaperPhoto]?
    @State private var listFailed = false

    private var albumName: String { WallpaperRules.albumTitle(target.name) }
    private var albumHere: Bool { model.album?.collectionID == target.id }
    private var widgetHere: Bool { model.widget?.id == target.id }
    private var canStart: Bool { (photos?.isEmpty == false) && !model.working }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 24) {
                    Text(S.iosWallpaperTitle(name: target.name))
                        .font(.title2.weight(.bold))
                        .fixedSize(horizontal: false, vertical: true)
                        .accessibilityAddTraits(.isHeader)
                    notice
                    lockSection
                    Divider()
                    widgetSection
                }
                .padding()
                .frame(maxWidth: 640, alignment: .leading)
                .frame(maxWidth: .infinity)
            }
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button(S.commonDone, action: onClose)
                }
            }
        }
        .task {
            model.forgetNews()
            await list()
        }
    }

    private func list() async {
        listFailed = false
        do {
            photos = try await model.photos(of: target.id)
        } catch {
            listFailed = true
        }
    }

    // MARK: The notice, first

    /// What is stored, where and for how long: first on the screen, in a
    /// box of its own, its first line in bold - before any button.
    private var notice: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Image(systemName: "exclamationmark.triangle.fill")
                    .foregroundStyle(.orange)
                    .accessibilityHidden(true)
                Text(S.appPhotosWallpaperStoredTitle)
                    .font(.headline.weight(.bold))
                    .fixedSize(horizontal: false, vertical: true)
            }
            if let photos {
                if photos.isEmpty {
                    Text(S.appPhotosWallpaperEmpty)
                } else {
                    Text(S.appPhotosWallpaperStoredText(count: WallpaperRules.copyCount(photos.count), name: target.name))
                        .fixedSize(horizontal: false, vertical: true)
                }
            } else if listFailed {
                Text(S.appPhotosWallpaperFailed)
                Button(LoadProblem.tryAgain) { Task { await list() } }
                    .buttonStyle(.bordered)
            } else {
                ProgressView()
            }
            if SecretsStore.wifiOnlyNow {
                Text(S.appPhotosWallpaperWifiOnly)
                    .font(.subheadline)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .padding(16)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color.orange.opacity(0.14), in: RoundedRectangle(cornerRadius: 14, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 14, style: .continuous).strokeBorder(Color.orange, lineWidth: 1.5))
        // One stop for VoiceOver, read first.
        .accessibilityElement(children: .contain)
        .accessibilitySortPriority(1)
    }

    // MARK: A. The lock screen

    private var lockSection: some View {
        VStack(alignment: .leading, spacing: 12) {
            Label(S.iosWallpaperLockTitle, systemImage: "lock.iphone")
                .font(.title3.weight(.semibold))
                .accessibilityAddTraits(.isHeader)
            Text(S.iosWallpaperLockText(album: albumName))
                .fixedSize(horizontal: false, vertical: true)
            Label(S.appPhotosWallpaperLockNote, systemImage: "eye")
                .font(.subheadline.weight(.medium))
                .fixedSize(horizontal: false, vertical: true)
            if albumHere {
                phaseView(model.albumPhase, ready: { count in
                    VStack(alignment: .leading, spacing: 8) {
                        Label(S.iosWallpaperAlbumReady(count: count), systemImage: "checkmark.circle.fill")
                            .foregroundStyle(.green)
                        Text(S.iosWallpaperStepsTitle).font(.headline).padding(.top, 4)
                        step(1, S.iosWallpaperStep1)
                        step(2, S.iosWallpaperStep2(album: albumName))
                        step(3, S.iosWallpaperStep3)
                    }
                })
                if !isStoring(model.albumPhase) {
                    Button(role: .destructive) {
                        Task { await model.stopAlbum() }
                    } label: {
                        Text(S.iosWallpaperStopAlbum).frame(maxWidth: .infinity)
                    }
                    .buttonStyle(.bordered)
                    .disabled(model.working)
                    Text(S.iosWallpaperStopAlbumNote)
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
            } else {
                if model.needsFullAccess {
                    Text(S.iosWallpaperFullAccess)
                        .font(.subheadline)
                        .fixedSize(horizontal: false, vertical: true)
                    Button(S.iosOpenSettings) {
                        if let url = URL(string: UIApplication.openSettingsURLString) { UIApplication.shared.open(url) }
                    }
                    .buttonStyle(.bordered)
                }
                if photos?.isEmpty != true {
                    Button {
                        guard let photos else { return }
                        Task { await model.startAlbum(collection: target.id, name: target.name, photos: photos) }
                    } label: {
                        Text(S.iosWallpaperLockButton).frame(maxWidth: .infinity)
                    }
                    .buttonStyle(.borderedProminent)
                    .disabled(!canStart)
                }
                news(model.albumPhase)
            }
        }
    }

    // MARK: B. The home screen widget

    private var widgetSection: some View {
        VStack(alignment: .leading, spacing: 12) {
            Label(S.iosWallpaperWidgetTitle, systemImage: "square.grid.2x2")
                .font(.title3.weight(.semibold))
                .accessibilityAddTraits(.isHeader)
            Text(S.iosWallpaperWidgetText)
                .fixedSize(horizontal: false, vertical: true)
            if widgetHere {
                phaseView(model.widgetPhase, ready: { _ in
                    VStack(alignment: .leading, spacing: 8) {
                        Label(S.iosWallpaperWidgetReady, systemImage: "checkmark.circle.fill")
                            .foregroundStyle(.green)
                        Text(S.iosWallpaperWidgetSteps)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                })
                if !isStoring(model.widgetPhase) {
                    Button(role: .destructive) {
                        model.stopWidget()
                    } label: {
                        Text(S.iosWallpaperStopWidget).frame(maxWidth: .infinity)
                    }
                    .buttonStyle(.bordered)
                    .disabled(model.working)
                }
            } else {
                if photos?.isEmpty != true {
                    Button {
                        guard let photos else { return }
                        Task { await model.startWidget(collection: target.id, name: target.name, photos: photos) }
                    } label: {
                        Text(S.iosWallpaperWidgetButton).frame(maxWidth: .infinity)
                    }
                    .buttonStyle(.borderedProminent)
                    .disabled(!canStart)
                }
                news(model.widgetPhase)
            }
        }
    }

    // MARK: Pieces

    private func isStoring(_ p: WallpaperModel.Phase) -> Bool {
        if case .storing = p { return true }
        return false
    }

    /// Progress, the ready message, or what went wrong.
    @ViewBuilder
    private func phaseView<Ready: View>(_ phase: WallpaperModel.Phase, @ViewBuilder ready: (Int) -> Ready) -> some View {
        switch phase {
        case .storing(let done, let total, let waiting):
            VStack(alignment: .leading, spacing: 6) {
                ProgressView(value: Double(done), total: Double(max(total, 1)))
                Text(S.appPhotosWallpaperProgress(done: done, total: total))
                    .font(.subheadline)
                    .monospacedDigit()
                if waiting {
                    Text(S.appSettingsUploadsWaitingWifi)
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                }
            }
            .accessibilityElement(children: .combine)
        case .ready(let count):
            ready(count)
        case .failed:
            Text(S.appPhotosWallpaperFailed).foregroundStyle(.red)
        case .idle, .stopped:
            EmptyView()
        }
    }

    /// After a way stopped or failed, under its button.
    @ViewBuilder
    private func news(_ phase: WallpaperModel.Phase) -> some View {
        switch phase {
        case .stopped:
            Label(S.appPhotosWallpaperStopped, systemImage: "checkmark.circle")
                .font(.subheadline)
                .foregroundStyle(.secondary)
        case .failed:
            Text(S.appPhotosWallpaperFailed).foregroundStyle(.red)
        case .storing:
            phaseView(phase, ready: { _ in EmptyView() })
        default:
            EmptyView()
        }
    }

    private func step(_ n: Int, _ text: String) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            Image(systemName: "\(n).circle.fill")
                .foregroundStyle(Color.accentColor)
                .accessibilityHidden(true)
            Text(text).fixedSize(horizontal: false, vertical: true)
        }
    }
}
