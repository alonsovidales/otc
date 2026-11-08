// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  VideoTrimmerView.swift
//  OffTheCloud
//
//  Issue #108: pick the piece of a video that actually goes into a post.
//
//  The cut itself happens on the device (ffmpeg, at publish time — see
//  social.NewPublication); this screen only ever produces a start/end pair.
//  That matters more here than on the web: exporting a trimmed copy with
//  AVAssetExportSession would mean re-encoding on the phone and then
//  uploading the result, when the device is already re-encoding social
//  videos anyway. Sending two numbers keeps the upload identical to what it
//  was and puts both clients on exactly the same cut.
//
//  Mirrors web/src/components/VideoTrimmer.tsx — same two-slider layout,
//  same "Play cut" preview, same rules about what counts as a real trim.
//
//  The viewer's Edit on a video (2026-11-08, PhotoEditor.swift's
//  VideoEditorView) is this screen too, with TrimmerCut: the device cuts
//  the video itself (EditVideo), so there the two numbers are the whole
//  edit as well.

import SwiftUI
import AVKit

struct TrimRange: Equatable {
    var start: Double
    var end: Double

    var length: Double { max(0, end - start) }

    /// How near a clip's end an end still counts as the end: the device's
    /// own slack (a post's shouldTrimForSocial, a cut's cutRange).
    static let endSlack: Double = 0.05

    /// Ends at the end of a clip `duration` long.
    func reachesEnd(of duration: Double) -> Bool { end >= duration - Self.endSlack }

    /// Keeps the whole of a clip `duration` long: the same as no cut at
    /// all, and sending it anyway would make the device re-encode a post's
    /// video to change nothing about it (shouldTrimForSocial on the Go
    /// side) - or copy a whole video into the same video.
    func isWhole(of duration: Double) -> Bool { start <= 0 && reachesEnd(of: duration) }

    /// What Apply (or the viewer's Save) takes: a real cut of a clip
    /// `duration` long, of at least cMinTrimSecs.
    func isCut(of duration: Double) -> Bool { !isWhole(of: duration) && length >= cMinTrimSecs }
}

/// Below this there's nothing left to watch, and ffmpeg's own output for a
/// sub-frame cut isn't something worth publishing.
private let cMinTrimSecs: Double = 0.5

/// The viewer's Edit on a video (VideoEditorView) in place of the post
/// composer's trim: the same screen, with Save in Apply's place, the cut's
/// small print under the slider, no Remove Trim, the discard question for
/// Cancel once a cut is chosen (the photo editor's), and Saving… while the
/// device cuts.
struct TrimmerCut {
    /// The device is saving the cut: the screen takes nothing meanwhile.
    var saving = false
    /// The viewer couldn't get the video (VideoEditorView's source): the
    /// screen says it can't open it rather than reading it for good.
    var unavailable = false
    /// Save, on a real cut: the part chosen and the video's length.
    var save: (TrimRange, Double) -> Void
}

func formatTimecode(_ secs: Double) -> String {
    let s = secs.isFinite && secs > 0 ? secs : 0
    let m = Int(s) / 60
    let rem = Int(s) % 60
    let tenths = Int(s * 10) % 10
    return String(format: "%d:%02d.%d", m, rem, tenths)
}

struct VideoTrimmerView: View {
    /// The clip: a file on the phone (the composer's), or what the viewer
    /// plays a video from (its stream URL or file, for a cut); nil until
    /// the viewer has it.
    let url: URL?
    let existing: TrimRange?
    /// The viewer's cut in place of the composer's trim; nil in the composer.
    var cut: TrimmerCut? = nil
    let onApply: (TrimRange?) -> Void
    let onCancel: () -> Void

    @State private var player: AVPlayer?
    @State private var duration: Double?
    @State private var start: Double = 0
    @State private var end: Double = 0
    /// Set while "Play cut" is running, so the time observer below knows to
    /// stop at `end` instead of letting the rest of the clip play on.
    @State private var playingCut = false
    @State private var timeObserver: Any?
    /// AVFoundation can fail to read a clip's length (a format it can't
    /// demux, an iCloud fetch that didn't complete), and without this the
    /// sheet sits on "Reading the clip…" with no explanation - the same
    /// dead end the web trimmer had before it grew this. Kept as the
    /// reason and worded when shown.
    @State private var loadError: LoadError?
    /// A cut's Cancel with a part chosen asks first (the photo editor's
    /// discard question).
    @State private var askDiscard = false

    private enum LoadError {
        case cantOpen, noLength

        var text: String {
            switch self {
            case .cantOpen: return S.appSocialTrimCantOpen
            case .noLength: return S.appSocialTrimNoLength
            }
        }
    }

    private var effectiveEnd: Double { end > 0 ? end : (duration ?? 0) }
    private var cutLength: Double { max(0, effectiveEnd - start) }
    private var chosen: TrimRange { TrimRange(start: start, end: effectiveEnd) }
    /// Apply and Save take a real cut only (TrimRange.isCut).
    private var canApply: Bool { duration.map { chosen.isCut(of: $0) } ?? false }
    /// Something other than the whole clip is chosen.
    private var changed: Bool { duration.map { !chosen.isWhole(of: $0) } ?? false }
    private var saving: Bool { cut?.saving == true }
    /// Why nothing can be shown: the clip didn't open, or the viewer never
    /// had it for a cut.
    private var shownError: LoadError? {
        loadError ?? (url == nil && cut?.unavailable == true ? .cantOpen : nil)
    }

    var body: some View {
        NavigationStack {
            VStack(spacing: 14) {
                if let player {
                    VideoPlayer(player: player)
                        .frame(maxHeight: 260)
                        .clipShape(RoundedRectangle(cornerRadius: 10))
                } else {
                    Color.black.opacity(0.1)
                        .frame(maxHeight: 260)
                        .clipShape(RoundedRectangle(cornerRadius: 10))
                }

                if let d = duration {
                    // One track, a handle at each end - the two values
                    // read as a single span, which is what a trim
                    // actually is. Matches the web trimmer's own layout.
                    HStack(spacing: 10) {
                        Text(formatTimecode(start))
                            .font(.caption).monospacedDigit()
                            .frame(width: 52, alignment: .leading)
                        DualRangeSlider(
                            duration: d,
                            start: Binding(get: { start }, set: { changeStart($0) }),
                            end: Binding(get: { effectiveEnd }, set: { changeEnd($0) })
                        )
                        Text(formatTimecode(effectiveEnd))
                            .font(.caption).monospacedDigit()
                            .frame(width: 52, alignment: .trailing)
                    }

                    Text(S.appSocialTrimKeeping(kept: formatTimecode(cutLength), total: formatTimecode(d)))
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .frame(maxWidth: .infinity, alignment: .leading)

                    if cut != nil {
                        // The device copies the video as it is (EditVideo),
                        // and a copy starts at a keyframe: a moment before
                        // the start chosen, which this says beforehand.
                        Text(S.appPhotosEditVideoCutNote)
                            .font(.caption2)
                            .foregroundStyle(.secondary)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .fixedSize(horizontal: false, vertical: true)
                    }

                    Button {
                        playCut()
                    } label: {
                        Label(S.iosTrimPlayCut, systemImage: "play.fill")
                    }
                    .buttonStyle(.bordered)

                    // A cut is saved on the device or not at all: there is
                    // no trim to take back.
                    if existing != nil && cut == nil {
                        Button(S.appSocialTrimRemove, role: .destructive) { onApply(nil) }
                            .buttonStyle(.bordered)
                    }
                } else if let shownError {
                    ContentUnavailableView(S.iosTrimCantTitle, systemImage: "exclamationmark.triangle", description: Text(shownError.text))
                } else {
                    ProgressView(S.appSocialTrimReading)
                }

                Spacer(minLength: 0)
            }
            // While the device saves a cut, nothing here changes it.
            .disabled(saving)
            .padding()
            .navigationTitle(S.appSocialTrimTitle)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button(S.commonCancel) { cancel() }
                        .disabled(saving)
                }
                ToolbarItem(placement: .confirmationAction) {
                    if let cut {
                        if cut.saving {
                            HStack(spacing: 6) {
                                ProgressView()
                                Text(S.appPhotosEditSaving)
                            }
                            .accessibilityElement(children: .combine)
                        } else {
                            Button(S.commonSave) { save(cut) }
                                .disabled(!canApply)
                        }
                    } else {
                        Button(S.commonApply) { onApply(chosen) }
                            .disabled(!canApply)
                    }
                }
            }
        }
        .confirmationDialog(S.appPhotosEditDiscardTitle, isPresented: $askDiscard, titleVisibility: .visible) {
            Button(S.appPhotosEditDiscard, role: .destructive) { onCancel() }
            Button(S.commonCancel, role: .cancel) {}
        }
        // Once there is a clip: the viewer's for a cut may come after the
        // screen opened.
        .task(id: url) { await load() }
        .onDisappear { teardown() }
    }

    /// Cancel: a cut with a part chosen asks before it is dropped, as the
    /// photo editor does; the composer's trim never did.
    private func cancel() {
        if cut != nil && changed {
            askDiscard = true
        } else {
            onCancel()
        }
    }

    /// The cut's Save: the preview stops, and the viewer asks how to save it.
    private func save(_ cut: TrimmerCut) {
        guard let duration, canApply else { return }
        playingCut = false
        player?.pause()
        cut.save(chosen, duration)
    }

    private func load() async {
        guard let url else { return }
        if player != nil { teardown() }
        // A stream URL on the home network plays only through its loader
        // (MediaStream.asset); a file or a bridge URL is a plain asset, as
        // the composer's always was.
        let asset = MediaStream.asset(for: url)
        let p = AVPlayer(playerItem: AVPlayerItem(asset: asset))
        player = p
        // Stopping the preview at the out point is what makes the chosen
        // cut legible — watching it run past the end tells you nothing
        // about what was actually selected.
        timeObserver = p.addPeriodicTimeObserver(
            forInterval: CMTime(seconds: 0.05, preferredTimescale: 600),
            queue: .main
        ) { time in
            guard playingCut else { return }
            let stopAt = end > 0 ? end : (duration ?? .greatestFiniteMagnitude)
            if time.seconds >= stopAt {
                p.pause()
                playingCut = false
            }
        }

        guard let d = try? await asset.load(.duration) else {
            loadError = .cantOpen
            return
        }
        let secs = d.seconds
        guard secs.isFinite, secs > 0 else {
            loadError = .noLength
            return
        }
        duration = secs
        // An end of 0 means "to the end" all the way through this feature
        // (see the proto's VideoTrim), so that's what an untouched end
        // stays; the slider shows the real duration in the meantime.
        if let existing {
            start = existing.start
            end = min(existing.end > 0 ? existing.end : secs, secs)
        } else {
            end = secs
        }
    }

    private func teardown() {
        if let timeObserver { player?.removeTimeObserver(timeObserver) }
        timeObserver = nil
        player?.pause()
        player = nil
    }

    private func seek(_ t: Double) {
        playingCut = false
        player?.pause()
        player?.seek(to: CMTime(seconds: t, preferredTimescale: 600), toleranceBefore: .zero, toleranceAfter: .zero)
    }

    private func changeStart(_ t: Double) {
        let upper = (end > 0 ? end : (duration ?? 0)) - cMinTrimSecs
        let next = min(max(0, t), max(0, upper))
        start = next
        seek(next)
    }

    private func changeEnd(_ t: Double) {
        let next = min(duration ?? t, max(t, start + cMinTrimSecs))
        end = next
        seek(next)
    }

    private func playCut() {
        guard let player else { return }
        player.seek(to: CMTime(seconds: start, preferredTimescale: 600), toleranceBefore: .zero, toleranceAfter: .zero) { _ in
            playingCut = true
            player.play()
        }
    }
}

/// Issue #108: a two-handle range slider, which SwiftUI has no built-in
/// equivalent of. Deliberately small: a rail, the selected span, and two
/// draggable handles that can't cross each other - the bindings' own
/// setters (changeStart/changeEnd) clamp every value to the clip and keep
/// the two a minimum apart. Matches the web trimmer's single-line control.
struct DualRangeSlider: View {
    let duration: Double
    @Binding var start: Double
    @Binding var end: Double

    private let handleSize: CGFloat = 22

    var body: some View {
        GeometryReader { geo in
            // Positions are measured across the track minus one handle's
            // width, so a handle at either extreme sits inside the track
            // rather than half outside it.
            let usable = max(1, geo.size.width - handleSize)
            let startX = CGFloat(start / duration) * usable
            let endX = CGFloat(end / duration) * usable

            ZStack(alignment: .leading) {
                Capsule().fill(.quaternary).frame(height: 6)
                Capsule()
                    .fill(Color.accentColor)
                    .frame(width: max(2, endX - startX), height: 6)
                    .offset(x: startX + handleSize / 2)

                // VoiceOver adjusts each handle a step at a time (a swipe up
                // or down): the drag alone left a video untrimmable by ear.
                handle
                    .offset(x: startX)
                    .gesture(drag(usable: usable) { start = $0 })
                    .accessibilityElement()
                    .accessibilityLabel(S.iosTrimStartHandle)
                    .accessibilityValue(formatTimecode(start))
                    .accessibilityAdjustableAction { start = Self.stepped(start, $0 == .increment, duration: duration) }
                handle
                    .offset(x: endX)
                    .gesture(drag(usable: usable) { end = $0 })
                    .accessibilityElement()
                    .accessibilityLabel(S.iosTrimEndHandle)
                    .accessibilityValue(formatTimecode(end))
                    .accessibilityAdjustableAction { end = Self.stepped(end, $0 == .increment, duration: duration) }
            }
            .frame(height: handleSize)
            .frame(maxHeight: .infinity)
        }
        .frame(height: 28)
    }

    /// VoiceOver's step for a handle: a hundredth of the clip, in tenths of
    /// a second, at least one (a minute's clip moves 0.6 s a swipe).
    nonisolated static func step(for duration: Double) -> Double {
        max(0.1, (duration / 10).rounded() / 10)
    }

    /// A handle at `value` one step up or down; the bindings' setters keep it
    /// in the clip and the two a minimum apart.
    nonisolated static func stepped(_ value: Double, _ up: Bool, duration: Double) -> Double {
        value + (up ? 1 : -1) * step(for: duration)
    }

    private var handle: some View {
        Circle()
            .fill(Color.accentColor)
            .frame(width: handleSize, height: handleSize)
            .overlay(Circle().strokeBorder(Color(.systemBackground), lineWidth: 2))
            .contentShape(Circle())
    }

    private func drag(usable: CGFloat, apply: @escaping (Double) -> Void) -> some Gesture {
        // minimumDistance 0 so a tap-and-move starts adjusting straight
        // away; the binding's own setter is what enforces minGap and the
        // clip's bounds (see changeStart/changeEnd).
        DragGesture(minimumDistance: 0)
            .onChanged { value in
                let x = min(max(0, value.location.x - handleSize / 2), usable)
                apply(Double(x / usable) * duration)
            }
    }
}
