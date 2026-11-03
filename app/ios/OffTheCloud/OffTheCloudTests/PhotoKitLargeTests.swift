// SPDX-License-Identifier: AGPL-3.0-or-later

import CryptoKit
import Foundation
import Photos
import Testing
@testable import OffTheCloud

/// Issue #210 against the simulator's real Photos library: a video over 1 GB
/// is copied out (exportAsset), hashed and sent in chunks from that copy -
/// with a cut in the middle, resumed - while the app's memory stays flat;
/// and a copy without room on the phone isn't started, or stops on the way,
/// leaving no file behind. Skipped unless OTC_PHOTOKIT_LARGE is the path of
/// a video over 1 GB on the Mac, which the test adds to the simulator's
/// library when no video there has its name (`simctl addmedia` crashed on
/// Xcode 27 with a file this big). To run it:
///
///     ffmpeg -f lavfi -i "testsrc2=size=3840x2160:rate=30,noise=alls=30:allf=t" -t 60 \
///         -c:v hevc_videotoolbox -b:v 260M -tag:v hvc1 /tmp/otc-large-210.mov
///     xcrun simctl boot <sim>
///     xcrun simctl privacy <sim> grant photos cloud.off-the.OffTheCloud
///     TEST_RUNNER_OTC_PHOTOKIT_LARGE=/tmp/otc-large-210.mov \
///     TEST_RUNNER_OTC_PHOTOKIT_LARGE_SHA=<shasum -a 256 of it> \
///         xcodebuild test ... -only-testing:OffTheCloudTests/PhotoKitLargeTests
@Suite(.enabled(if: ProcessInfo.processInfo.environment["OTC_PHOTOKIT_LARGE"] != nil), .serialized)
struct PhotoKitLargeTests {
    private let path = ProcessInfo.processInfo.environment["OTC_PHOTOKIT_LARGE"] ?? ""
    private var name: String { (path as NSString).lastPathComponent }
    private let expectedSHA = ProcessInfo.processInfo.environment["OTC_PHOTOKIT_LARGE_SHA"] ?? ""

    private func find() -> PHAsset? {
        let opts = PHFetchOptions()
        opts.predicate = NSPredicate(format: "mediaType == %d", PHAssetMediaType.video.rawValue)
        var found: PHAsset?
        PHAsset.fetchAssets(with: opts).enumerateObjects { a, _, stop in
            if PhotoSync.sentResource(of: a)?.originalFilename == name { found = a; stop.pointee = true }
        }
        return found
    }

    /// The video in the library, added from `path` the first time.
    private func asset() async throws -> PHAsset {
        #expect(PHPhotoLibrary.authorizationStatus(for: .readWrite) == .authorized)
        if let found = find() { return found }
        // From the app's own temporary folder: Photos reads the file there.
        let local = FileManager.default.temporaryDirectory.appendingPathComponent("add-\(name)")
        try? FileManager.default.removeItem(at: local)
        try FileManager.default.copyItem(at: URL(fileURLWithPath: path), to: local)
        defer { try? FileManager.default.removeItem(at: local) }
        try await PHPhotoLibrary.shared().performChanges {
            let options = PHAssetResourceCreationOptions()
            options.originalFilename = name
            PHAssetCreationRequest.forAsset().addResource(with: .video, fileURL: local, options: options)
        }
        return try #require(find(), "\(name) wasn't added to the library")
    }

    /// The app's memory as iOS counts it (phys_footprint).
    private static func footprint() -> Int64 {
        var info = task_vm_info_data_t()
        var count = mach_msg_type_number_t(MemoryLayout<task_vm_info_data_t>.size / MemoryLayout<natural_t>.size)
        let kr = withUnsafeMutablePointer(to: &info) {
            $0.withMemoryRebound(to: integer_t.self, capacity: Int(count)) { task_info(mach_task_self_, task_flavor_t(TASK_VM_INFO), $0, &count) }
        }
        return kr == KERN_SUCCESS ? Int64(info.phys_footprint) : 0
    }

    private static func tmpFiles() -> Set<String> {
        Set((try? FileManager.default.contentsOfDirectory(atPath: NSTemporaryDirectory())) ?? [])
    }

    /// A device that keeps only the running hash of what it was sent, cuts
    /// the connection once at `cutAt` bytes and resumes from what it holds.
    private final class Device: @unchecked Sendable {
        private let lock = NSLock()
        private var hasher = SHA256()
        private(set) var received: Int64 = 0
        private var cutAt: Int64
        private(set) var begins = 0
        private(set) var peak: Int64 = 0

        init(cutAt: Int64) { self.cutAt = cutAt }

        func answer(_ payload: Msg_ReqEnvelope.OneOf_Payload) throws -> Msg_RespEnvelope {
            try lock.withLock {
                peak = max(peak, PhotoKitLargeTests.footprint())
                var resp = Msg_RespEnvelope()
                switch payload {
                case .reqBeginUpload:
                    begins += 1
                    var st = Msg_UploadStarted()
                    st.uploadID = "u1"
                    st.received = received
                    resp.payload = .respUploadStarted(st)
                case .reqUploadChunk(let c):
                    if received >= cutAt { cutAt = .max; throw URLError(.networkConnectionLost) }
                    #expect(c.offset == received)
                    hasher.update(data: c.data)
                    received += Int64(c.data.count)
                    var p = Msg_UploadProgress()
                    p.received = received
                    resp.payload = .respUploadProgress(p)
                case .reqFinishUpload(let f):
                    let got = hasher.finalize().map { String(format: "%02x", $0) }.joined()
                    #expect(got == f.sha256)
                    var file = Msg_File()
                    file.hash = got
                    resp.payload = .respFile(file)
                default:
                    Issue.record("unexpected request")
                }
                return resp
            }
        }
    }

    @Test func aVideoOverOneGBIsCopiedHashedAndSentFromItsCopy() async throws {
        let video = try await asset()
        let before = Self.footprint()
        let copy = try await PhotoSync.shared.exportAsset(video, allowNetwork: false)
        defer { try? FileManager.default.removeItem(at: copy.url) }
        #expect(copy.size > PhotoSources.oldCeiling)
        #expect(copy.filename == name)
        let onDisk = try FileManager.default.attributesOfItem(atPath: copy.url.path)[.size] as? Int64
        #expect(onDisk == copy.size)

        let hash = try OTCConnection.sha256Hex(of: copy.url)
        if !expectedSHA.isEmpty { #expect(hash == expectedSHA) }
        let afterHash = Self.footprint()

        let device = Device(cutAt: copy.size / 2)
        let resp = try await OTCConnection.sendChunked(
            path: "/ios/test/\(name)", source: .file(copy.url), forceOverride: false,
            created: nil, modified: nil, cloudID: "", sha256: hash, progress: nil,
            request: { try device.answer($0) },
            pause: { _ in }, log: { print("ChunkedUpload:", $0) })
        guard case .respFile(let f) = resp.payload else { Issue.record("no File answer"); return }
        #expect(f.hash == hash)
        #expect(device.received == copy.size)
        // Cut once, begun again from what the device held.
        #expect(device.begins == 2)

        // Memory: a few chunks at most, never the file (1.6 GB here).
        let grew = max(afterHash, device.peak) - before
        print("PhotoKitLargeTests: \(copy.size) bytes; footprint before \(before), after hashing \(afterHash), peak while sending \(device.peak)")
        #expect(grew < 200 << 20)
    }

    @Test func noRoomForTheCopyStartsNothing() async throws {
        let video = try await asset()
        let files = Self.tmpFiles()
        // As much free as the video, not the floor besides.
        let room = ExportRoom(free: { 1 << 30 })
        do {
            _ = try await PhotoSync.shared.exportAsset(video, allowNetwork: false, room: room)
            Issue.record("copied without room")
        } catch let short as ExportNoRoom {
            #expect(short.filename == name)
            // iOS 27 says the size up front: the need is all of it plus the floor.
            if #available(iOS 27, *) { #expect(short.needed == short.size + ExportRoom.floorBytes) }
        }
        #expect(Self.tmpFiles() == files)
    }

    @Test func aCopyStopsWhenThePhoneRunsOutOfRoom() async throws {
        let video = try await asset()
        let files = Self.tmpFiles()
        // Room to start; then, at the copy's first look, down to the floor.
        let looks = Counter()
        let room = ExportRoom(free: { looks.next() == 0 ? 100 << 30 : ExportRoom.floorBytes - 1 })
        do {
            _ = try await PhotoSync.shared.exportAsset(video, allowNetwork: false, room: room)
            Issue.record("copied past the floor")
        } catch let short as ExportNoRoom {
            #expect(short.size >= ExportRoom.checkEvery)
            #expect(short.size < PhotoSources.oldCeiling)
        }
        #expect(Self.tmpFiles() == files)
        // What it learnt holds the next copy back before it starts.
        #expect((room.size(of: video.localIdentifier, given: nil) ?? 0) >= ExportRoom.checkEvery)
    }

    private final class Counter: @unchecked Sendable {
        private let lock = NSLock()
        private var n = 0
        func next() -> Int { lock.withLock { defer { n += 1 }; return n } }
    }
}
