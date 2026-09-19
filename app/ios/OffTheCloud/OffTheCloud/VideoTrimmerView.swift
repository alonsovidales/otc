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

import SwiftUI
import AVKit

struct TrimRange: Equatable {
    var start: Double
    var end: Double

    var length: Double { max(0, end - start) }
}

/// Below this there's nothing left to watch, and ffmpeg's own output for a
/// sub-frame cut isn't something worth publishing.
private let cMinTrimSecs: Double = 0.5

func formatTimecode(_ secs: Double) -> String {
    let s = secs.isFinite && secs > 0 ? secs : 0
    let m = Int(s) / 60
    let rem = Int(s) % 60
    let tenths = Int(s * 10) % 10
    return String(format: "%d:%02d.%d", m, rem, tenths)
}

struct VideoTrimmerView: View {
    let url: URL
    let existing: TrimRange?
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
    /// dead end the web trimmer had before it grew this.
    @State private var loadError: String?

    private var effectiveEnd: Double { end > 0 ? end : (duration ?? 0) }
    private var cutLength: Double { max(0, effectiveEnd - start) }
    /// A cut that keeps the whole clip is the same as no cut at all, and
    /// sending it anyway would make the device re-encode the video to
    /// change nothing about it (see shouldTrimForSocial on the Go side).
    private var isWholeClip: Bool {
        guard let d = duration else { return true }
        return start <= 0 && effectiveEnd >= d - 0.05
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

                    Text("Keeping \(formatTimecode(cutLength)) of \(formatTimecode(d))")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .frame(maxWidth: .infinity, alignment: .leading)

                    Button {
                        playCut()
                    } label: {
                        Label("Play cut", systemImage: "play.fill")
                    }
                    .buttonStyle(.bordered)

                    if existing != nil {
                        Button("Remove trim", role: .destructive) { onApply(nil) }
                            .buttonStyle(.bordered)
                    }
                } else if let loadError {
                    ContentUnavailableView("Can't trim this video", systemImage: "exclamationmark.triangle", description: Text(loadError))
                } else {
                    ProgressView("Reading the clip…")
                }

                Spacer(minLength: 0)
            }
            .padding()
            .navigationTitle("Trim video")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { onCancel() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Apply") { onApply(TrimRange(start: start, end: effectiveEnd)) }
                        .disabled(isWholeClip || cutLength < cMinTrimSecs)
                }
            }
        }
        .task { await load() }
        .onDisappear { teardown() }
    }

    private func load() async {
        let asset = AVURLAsset(url: url)
        let p = AVPlayer(url: url)
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
            loadError = "This video could not be opened."
            return
        }
        let secs = d.seconds
        guard secs.isFinite, secs > 0 else {
            loadError = "This video's length could not be read."
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
private struct DualRangeSlider: View {
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

                handle
                    .offset(x: startX)
                    .gesture(drag(usable: usable) { start = $0 })
                    .accessibilityLabel("Start of the cut")
                    .accessibilityValue(formatTimecode(start))
                handle
                    .offset(x: endX)
                    .gesture(drag(usable: usable) { end = $0 })
                    .accessibilityLabel("End of the cut")
                    .accessibilityValue(formatTimecode(end))
            }
            .frame(height: handleSize)
            .frame(maxHeight: .infinity)
        }
        .frame(height: 28)
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
