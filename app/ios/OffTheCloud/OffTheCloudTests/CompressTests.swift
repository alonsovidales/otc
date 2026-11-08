// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import SwiftProtobuf
import Testing
@testable import OffTheCloud

/// Compress and Settings › Largest files (Compress.swift, LargestFiles.swift):
/// the dialog's rules, the job's answers, the list's paging and states, and
/// the viewer's compressed files - then the dialog, Images and the list
/// against a device answering in its place. The case ids are the web's
/// (web/scripts/compress-test.mjs) and Android's (CompressTest.kt), with the
/// same files: the same rules, tested alike - change the three together.
///
///   C1  the videos' radio: the video presets offered, 1080p, 720p, HEVC in
///       that order, then "Leave as they are"; nothing without a video preset
///   C2  the radio's default: 1080p if any item offers it, else HEVC, else 720p
///   C3  a switch per photo preset offered by any item, in order, each on
///   C4  per item: a video the chosen preset, else HEVC, else nothing ("Leave
///       as they are": nothing); a photo 12 MP, else JPEG resave, else PNG to
///       JPEG, each only with its switch on; a preset the app doesn't know is
///       left out
///   C5  the tasks, in the order asked, with the path and hash the device
///       answered for
///   C6  a video choice's row: everything it would do if chosen (HEVC
///       fallbacks included), count, sizes now and estimates after
///   C7  a photo switch's row: the photos that would get its preset were it
///       on, the other switches as they are
///   C8  the foot's total: each task's size less its estimate, never below 0
///   C9  "videos take a while" only with a video among the tasks
///   C10 the files not offered (no known preset), with the device's reason;
///       "nothing can be made smaller" when that is every file
///   C11 GetCompressOptions' answer: options, an older device, a refusal in
///       the device's words (more than 500), or nothing usable
///   C12 CompressFiles' answer: the job, an older device, a refusal, nothing
///   C13 the requests' payloads
///   J1  GetCompressJob's answer: the job, gone (not_found, another id), or none
///   J2  the job is asked every 2 s while shown, every 10 s while only a page
///       follows it, never once over or unwatched
///   J3  the job's progress: tasks over plus a running video's percent
///   J4  what it came to: compressed, freed, and the files left as they were
///       (failed and skipped; gone, the tasks not over too)
///   J5  each done result is put in place once
///   J6  the file shown after: the device's over the old one, its copies kept
///   L1  ListLargestFiles' payload: pages of 50 after a token
///   L2  its answer: a page and the next token, an older device, a failure
///   L3  a page added under the rows: no path or content shown twice
///   L4  what the screen shows: loading, an older device, failed, empty, rows
///       with a spinner, a failure or nothing at their foot
///   L5  the next page is asked only while there is one and none is on its way
///   L6  "in N places" only for content at several paths
///   L7  photos and videos by their type, or a HEIC/HEIF by its name
///   L8  Compress is on while a photo or video is picked
///   L9  the list is read again after a job that compressed anything
///   L10 a row's folder
///   R1  no Revert to original for a compressed file (original_removed)
///   R2  Info's note for a compressed file

private let GB = Double(1 << 30)
private let MB = Double(1 << 20)
private func b(_ x: Double) -> Int64 { Int64(x) }

/// An option as the device answers it.
private func opt(_ path: String, _ size: Double, _ choices: [(String, Double)] = [],
                 _ reason: String = "", _ code: String = "") -> Msg_CompressOption {
    var o = Msg_CompressOption()
    o.path = path
    o.hash = "h:\(path)"
    o.size = b(size)
    o.copies = 1
    o.choices = choices.map { p, est in
        var c = Msg_CompressChoice()
        c.preset = p
        c.estimatedSize = b(est)
        return c
    }
    o.reason = reason
    o.reasonCode = code
    return o
}

private let fourKHevc = opt("/v/4k-hevc.mov", 4 * GB, [("video_1080p", 1.2 * GB), ("video_720p", 0.6 * GB)])
private let fourKH264 = opt("/v/4k-h264.mp4", 6 * GB, [("video_1080p", 1.5 * GB), ("video_720p", 0.8 * GB), ("video_hevc", 3 * GB)])
private let hdH264 = opt("/v/1080p.mp4", 2 * GB, [("video_720p", 0.5 * GB), ("video_hevc", 1 * GB)])
private let big = opt("/p/48mp.heic", 12 * MB, [("photo_12mp", 4 * MB)])
private let bigJpeg = opt("/p/48mp.jpg", 20 * MB, [("photo_12mp", 4 * MB), ("jpeg_resave", 10 * MB)])
private let fatJpeg = opt("/p/12mp.jpg", 8 * MB, [("jpeg_resave", 3 * MB)])
private let png = opt("/p/shot.png", 6 * MB, [("png_to_jpeg", 1 * MB)])
private let pdf = opt("/docs/scan.pdf", 30 * MB, [], "only photos and videos can be compressed", "not_media")
private let small = opt("/v/720p.mov", 300 * MB, [], "the device can't make this file noticeably smaller", "nothing_to_gain")
private let all = [fourKHevc, fourKH264, hdH264, big, bigJpeg, fatJpeg, png, pdf, small]

/// The photo switches on: all but `off`.
private func on(_ off: [String] = []) -> Set<String> { Set(CompressRules.photoPresets).subtracting(off) }

private func sum(_ count: Int, _ now: Double, _ after: [Double]) -> CompressRules.Sum {
    CompressRules.Sum(count: count, now: b(now), after: after.map(b).reduce(0, +))
}

private func result(_ path: String, _ state: String, percent: Int32 = 0, message: String = "", file: Msg_File? = nil) -> Msg_CompressResult {
    var r = Msg_CompressResult()
    r.path = path
    r.state = state
    r.percent = percent
    r.message = message
    if let file { r.file = file }
    return r
}

private func file(_ path: String, _ hash: String, mime: String = "image/jpeg", size: Int64 = 0) -> Msg_File {
    var f = Msg_File()
    f.path = path
    f.hash = hash
    f.mime = mime
    f.size64 = size
    return f
}

private func job(_ id: String = "j1", total: Int32 = 0, done: Int32 = 0, saved: Int64 = 0, finished: Bool = false,
                 _ results: [Msg_CompressResult] = []) -> Msg_CompressJob {
    var j = Msg_CompressJob()
    j.id = id
    j.total = total
    j.done = done
    j.saved = saved
    j.finished = finished
    j.results = results
    return j
}

private func answer(_ payload: Msg_RespEnvelope.OneOf_Payload) -> Msg_RespEnvelope {
    var r = Msg_RespEnvelope()
    r.payload = payload
    return r
}

private func refusal(_ code: String, _ message: String) -> Msg_RespEnvelope {
    var r = Msg_RespEnvelope()
    r.error = true
    r.errorCode = code
    r.errorMessage = message
    return r
}

@MainActor
private func waitUntil(_ seconds: Double = 3, _ done: () -> Bool) async {
    let end = Date() + seconds
    while !done() && Date() < end {
        try? await Task.sleep(nanoseconds: 10_000_000)
    }
}

// MARK: - The dialog's rules

@MainActor
struct CompressRulesTests {
    @Test func c1TheVideosRadio() {
        #expect(CompressRules.videoChoices(all) == ["video_1080p", "video_720p", "video_hevc", "video_none"])
        #expect(CompressRules.videoChoices([hdH264]) == ["video_720p", "video_hevc", "video_none"])
        #expect(CompressRules.videoChoices([fourKHevc]) == ["video_1080p", "video_720p", "video_none"])
        // No video preset: no radio at all.
        #expect(CompressRules.videoChoices([big, png, pdf]).isEmpty)
        #expect(CompressRules.videoChoices([]).isEmpty)
    }

    @Test func c2TheRadiosDefault() {
        #expect(CompressRules.defaultVideo([hdH264, fourKHevc]) == "video_1080p")
        #expect(CompressRules.defaultVideo([hdH264]) == "video_hevc")
        #expect(CompressRules.defaultVideo([opt("/v/x.mov", GB, [("video_720p", 0.3 * GB)])]) == "video_720p")
        // No video: none, and the dialog's choice is Leave as they are.
        #expect(CompressRules.defaultVideo([big]) == nil)
    }

    @Test func c3ThePhotoSwitches() {
        #expect(CompressRules.photoSwitches(all) == ["photo_12mp", "jpeg_resave", "png_to_jpeg"])
        #expect(CompressRules.photoSwitches([png, fatJpeg]) == ["jpeg_resave", "png_to_jpeg"])
        #expect(CompressRules.photoSwitches([fourKH264, pdf]).isEmpty)
    }

    @Test func c4WhatEachItemGets() {
        let p = "video_1080p"
        // A video: the preset chosen when it offers it, else HEVC, else nothing.
        #expect(CompressRules.preset(for: fourKHevc, video: p, photos: on()) == "video_1080p")
        #expect(CompressRules.preset(for: fourKH264, video: "video_720p", photos: on()) == "video_720p")
        #expect(CompressRules.preset(for: hdH264, video: p, photos: on()) == "video_hevc")
        #expect(CompressRules.preset(for: fourKHevc, video: "video_hevc", photos: on()) == nil)
        // Leave as they are: nothing for any video.
        for o in [fourKHevc, fourKH264, hdH264] {
            #expect(CompressRules.preset(for: o, video: "video_none", photos: on()) == nil)
        }
        // A photo: 12 MP, then the JPEG resave, then PNG to JPEG, each with
        // its switch on.
        #expect(CompressRules.preset(for: bigJpeg, video: p, photos: on()) == "photo_12mp")
        #expect(CompressRules.preset(for: bigJpeg, video: p, photos: on(["photo_12mp"])) == "jpeg_resave")
        #expect(CompressRules.preset(for: bigJpeg, video: p, photos: on(["photo_12mp", "jpeg_resave"])) == nil)
        #expect(CompressRules.preset(for: png, video: p, photos: on()) == "png_to_jpeg")
        #expect(CompressRules.preset(for: png, video: p, photos: on(["png_to_jpeg"])) == nil)
        // The videos' choice leaves photos alone.
        #expect(CompressRules.preset(for: big, video: "video_none", photos: on()) == "photo_12mp")
        // A preset this app doesn't know is left out.
        let newer = opt("/v/new.mov", GB, [("video_av1", 0.2 * GB), ("video_hevc", 0.5 * GB)])
        #expect(CompressRules.preset(for: newer, video: p, photos: on()) == "video_hevc")
        #expect(CompressRules.videoChoices([newer]) == ["video_hevc", "video_none"])
        let onlyNew = opt("/p/new.avif", 9 * MB, [("photo_avif", 2 * MB)])
        #expect(CompressRules.preset(for: onlyNew, video: p, photos: on()) == nil)
        #expect(CompressRules.notOffered([onlyNew]).map { CompressRules.name($0.path) } == ["new.avif"])
        // Nothing offered: nothing.
        #expect(CompressRules.preset(for: pdf, video: p, photos: on()) == nil)
    }

    @Test func c5TheTasks() {
        let tasks = CompressRules.tasks(all, video: CompressRules.defaultVideo(all) ?? "video_none", photos: on())
        #expect(tasks.map { [$0.path, $0.preset] } == [
            ["/v/4k-hevc.mov", "video_1080p"], ["/v/4k-h264.mp4", "video_1080p"], ["/v/1080p.mp4", "video_hevc"],
            ["/p/48mp.heic", "photo_12mp"], ["/p/48mp.jpg", "photo_12mp"], ["/p/12mp.jpg", "jpeg_resave"], ["/p/shot.png", "png_to_jpeg"],
        ])
        #expect(tasks[0].hash == "h:/v/4k-hevc.mov")
        #expect(tasks[0].size == b(4 * GB))
        #expect(tasks[0].estimate == b(1.2 * GB))
        // Nothing chosen: no task.
        #expect(CompressRules.tasks(all, video: "video_none", photos: []).isEmpty)
        #expect(CompressRules.tasks([pdf, small], video: "video_none", photos: on()).isEmpty)
    }

    @Test func c6AVideoChoicesRow() {
        // 1080p: the two 4K videos at 1080p and the 1080p one's fallback to HEVC.
        #expect(CompressRules.videoRow(all, "video_1080p") == sum(3, 12 * GB, [1.2 * GB, 1.5 * GB, 1 * GB]))
        // 720p: every video offers it.
        #expect(CompressRules.videoRow(all, "video_720p") == sum(3, 12 * GB, [0.6 * GB, 0.8 * GB, 0.5 * GB]))
        // HEVC: only those offering it (a 4K HEVC video gets nothing).
        #expect(CompressRules.videoRow(all, "video_hevc") == sum(2, 8 * GB, [3 * GB, 1 * GB]))
        // Photos never count, whatever their switches.
        #expect(CompressRules.videoRow([big, png], "video_1080p") == CompressRules.Sum())
    }

    @Test func c7APhotoSwitchsRow() {
        // 12 MP: the photos over 13 MP.
        #expect(CompressRules.photoRow(all, "photo_12mp", photos: on()) == sum(2, 32 * MB, [4 * MB, 4 * MB]))
        // The JPEG resave: only the JPEGs 12 MP leaves, while 12 MP is on.
        #expect(CompressRules.photoRow(all, "jpeg_resave", photos: on()) == sum(1, 8 * MB, [3 * MB]))
        // With 12 MP off, the JPEG it would have shrunk is saved again instead.
        #expect(CompressRules.photoRow(all, "jpeg_resave", photos: on(["photo_12mp"])) == sum(2, 28 * MB, [10 * MB, 3 * MB]))
        // A switch off still shows what turning it on would do - and makes
        // no task.
        let off = on(["png_to_jpeg"])
        #expect(CompressRules.photoRow(all, "png_to_jpeg", photos: off) == sum(1, 6 * MB, [1 * MB]))
        #expect(!CompressRules.tasks(all, video: "video_none", photos: off).contains { $0.preset == "png_to_jpeg" })
    }

    @Test func c8TheTotal() {
        let tasks = CompressRules.tasks([fourKHevc, png], video: "video_1080p", photos: on())
        #expect(CompressRules.saved(tasks) == b(4 * GB) - b(1.2 * GB) + b(5 * MB))
        // An estimate above the file's size frees nothing, never less.
        let over = CompressRules.Planned(path: "/a", hash: "h", preset: "jpeg_resave", size: 10, estimate: 12)
        #expect(CompressRules.saved([over]) == 0)
        #expect(CompressRules.saved([]) == 0)
    }

    @Test func c9VideosTakeAWhile() {
        #expect(CompressRules.slow(CompressRules.tasks(all, video: "video_1080p", photos: on())))
        #expect(CompressRules.slow(CompressRules.tasks([hdH264], video: "video_720p", photos: on())))
        // Not with photos alone, nor with the videos left as they are.
        #expect(!CompressRules.slow(CompressRules.tasks([big, png], video: "video_none", photos: on())))
        #expect(!CompressRules.slow(CompressRules.tasks(all, video: "video_none", photos: on())))
    }

    @Test func c10TheFilesNotOffered() {
        let left = CompressRules.notOffered(all)
        #expect(left.map { CompressRules.name($0.path) } == ["scan.pdf", "720p.mov"])
        #expect(left.map(\.reason) == ["only photos and videos can be compressed", "the device can't make this file noticeably smaller"])
        // A file that changed meanwhile is one, in the device's words.
        let changed = opt("/p/x.jpg", 5 * MB, [], "this file has changed - refresh and try again", "changed")
        #expect(CompressRules.notOffered([changed]).map(\.reason) == ["this file has changed - refresh and try again"])
        // Nothing at all can be made smaller - an empty answer included.
        #expect(CompressRules.nothing([pdf, small]))
        #expect(!CompressRules.nothing(all))
        #expect(CompressRules.nothing([]))
    }

    @Test func c11GetCompressOptionsAnswer() {
        var o = Msg_CompressOptions()
        o.items = [pdf]
        #expect(CompressRules.optionsAnswer(answer(.respCompressOptions(o))) == .options([pdf]))
        // An older device: it needs an update.
        #expect(CompressRules.optionsAnswer(refusal("unknown_payload", "unknown payload")) == .old)
        // A refusal in the device's words (more than 500 files).
        #expect(CompressRules.optionsAnswer(refusal("too_many", "pick at most 500 files at a time")) == .refused("pick at most 500 files at a time"))
        #expect(CompressRules.maxItems == 500)
        // Nothing usable: no answer, the bridge's (its words gone), an
        // answer of another kind.
        #expect(CompressRules.optionsAnswer(nil) == .failed)
        var ack = Msg_Ack()
        ack.code = "device_unreachable"
        var bridge = answer(.respAck(ack))
        bridge.error = true
        #expect(CompressRules.optionsAnswer(bridge) == .failed)
        var ok = Msg_Ack()
        ok.ok = true
        #expect(CompressRules.optionsAnswer(answer(.respAck(ok))) == .failed)
    }

    @Test func c12CompressFilesAnswer() {
        let j = job(total: 3)
        #expect(CompressRules.startAnswer(answer(.respCompressJob(j))) == .job(j))
        #expect(CompressRules.startAnswer(refusal("unknown_payload", "x")) == .old)
        #expect(CompressRules.startAnswer(refusal("demo", "changes are turned off")) == .refused("changes are turned off"))
        #expect(CompressRules.startAnswer(nil) == .failed)
    }

    @Test func c13TheRequests() {
        // GetCompressOptions with each file's path and the content shown,
        // once each, at most 500.
        let items = CompressRules.items([("/a.jpg", "h1"), ("/b.mov", ""), ("/a.jpg", "h1")])
        #expect(CompressRules.optionsRequest(items).items.map { [$0.path, $0.hash] } == [["/a.jpg", "h1"], ["/b.mov", ""]])
        let many = (0..<600).map { (path: "/p/\($0).jpg", hash: "h\($0)") }
        let first = CompressRules.items(many)
        #expect(first.count == 500 && first.first?.path == "/p/0.jpg" && first.last?.path == "/p/499.jpg")
        // CompressFiles with the tasks, GetCompressJob with the job's id.
        let tasks = CompressRules.tasks([png], video: "video_none", photos: on())
        #expect(CompressRules.compressRequest(tasks).tasks.map { [$0.path, $0.hash, $0.preset] } == [["/p/shot.png", "h:/p/shot.png", "png_to_jpeg"]])
        #expect(CompressRules.jobRequest("j1").id == "j1")
    }
}

// MARK: - The job's rules

@MainActor
struct CompressJobRulesTests {
    @Test func j1GetCompressJobsAnswer() {
        let j = job(total: 1, done: 1, saved: 5, finished: true)
        #expect(CompressRules.pollAnswer(answer(.respCompressJob(j)), id: "j1") == .job(j))
        // not_found, or another job's: gone.
        #expect(CompressRules.pollAnswer(refusal("not_found", "there is no compress job"), id: "j1") == .gone)
        #expect(CompressRules.pollAnswer(answer(.respCompressJob(job("j2"))), id: "j1") == .gone)
        // Whichever there is, asked with no id.
        #expect(CompressRules.pollAnswer(answer(.respCompressJob(job("j2"))), id: "") == .job(job("j2")))
        // No answer: asked again later.
        #expect(CompressRules.pollAnswer(nil, id: "j1") == .failed)
        #expect(CompressRules.pollAnswer(refusal("", ""), id: "j1") == .failed)
    }

    @Test func j2HowOftenTheJobIsAskedAbout() {
        #expect(CompressRules.pollDelay(shown: true, watched: true, over: false) == .seconds(2))
        #expect(CompressRules.pollDelay(shown: false, watched: true, over: false) == .seconds(10))
        // Never once it is over, or nothing looks.
        #expect(CompressRules.pollDelay(shown: true, watched: true, over: true) == nil)
        #expect(CompressRules.pollDelay(shown: false, watched: false, over: false) == nil)
    }

    @Test func j3TheJobsProgress() {
        let p = CompressRules.progress(job(total: 4, done: 1, [
            result("/a", "done"), result("/b", "running", percent: 50), result("/c", "waiting"), result("/d", "waiting"),
        ]))
        #expect(p.done == 1 && p.total == 4 && p.fraction == 1.5 / 4)
        // A total that grew (tasks joined) is the device's.
        #expect(CompressRules.progress(job(total: 9, done: 2)).total == 9)
        // Never past the whole, nor a percent outside 0-100.
        #expect(CompressRules.progress(job(total: 3, done: 3, [result("/x", "running", percent: 400)])).fraction == 1)
        #expect(CompressRules.progress(job()).fraction == 0)
    }

    @Test func j4WhatTheJobCameTo() {
        let j = job(saved: 3000, [
            result("/p/a.jpg", "done", file: file("/p/a.jpg", "n1")),
            result("/p/b.png", "done", file: file("/p/b.jpg", "n2")),
            result("/p/c.jpg", "skipped", message: "compressed, it came out no smaller, so the file was left as it was"),
            result("/v/d.mov", "failed", message: "the device could not compress this file"),
            result("/v/e.mov", "waiting"),
        ])
        let o = CompressRules.outcome(j)
        #expect(o.compressed == 2 && o.saved == 3000)
        #expect(o.failed == [
            CompressRules.LeftAsItWas(path: "/p/c.jpg", message: "compressed, it came out no smaller, so the file was left as it was"),
            CompressRules.LeftAsItWas(path: "/v/d.mov", message: "the device could not compress this file"),
        ])
        #expect(o.failed.map(\.name) == ["c.jpg", "d.mov"])
        // Gone mid-job: the tasks not over weren't compressed either.
        #expect(CompressRules.outcome(j, gone: true).failed.map(\.name) == ["c.jpg", "d.mov", "e.mov"])
        #expect(CompressRules.over(job(finished: false), gone: true))
        #expect(!CompressRules.over(job(finished: false), gone: false))
        #expect(CompressRules.over(job(finished: true), gone: false))
    }

    @Test func j5EachDoneResultPutInPlaceOnce() {
        let j = job("j1", [result("/p/a.jpg", "done", file: file("/p/a.jpg", "n1")), result("/p/b.jpg", "running"), result("/p/c.jpg", "done")])
        let first = CompressRules.newlyDone(j, seen: [])
        #expect(first.map(\.path) == ["/p/a.jpg"])
        #expect(CompressRules.newlyDone(j, seen: Set(first.map(CompressRules.resultKey))).isEmpty)
    }

    @Test func j6TheFileShownAfter() {
        var shown = file("/p/b.png", "old", mime: "image/png", size: 6000)
        shown.copies = 3
        shown.created = Google_Protobuf_Timestamp(date: Date(timeIntervalSince1970: 0))
        var a = file("/p/b.jpg", "new", size: 1000)
        a.originalHash = "old"
        a.originalRemoved = true
        let now = CompressRules.compressedFile(shown: shown, a)
        #expect(now.path == "/p/b.jpg" && now.hash == "new" && now.size64 == 1000)
        #expect(now.originalHash == "old" && now.originalRemoved)
        #expect(now.copies == 3)
        #expect(now.created.date == Date(timeIntervalSince1970: 0))
        #expect(!now.hasContent)
    }
}

// MARK: - The list's rules

@MainActor
struct LargestFilesRulesTests {
    @Test func l1ListLargestFilesPayload() {
        #expect(LargestFiles.request(after: "").after == "" && LargestFiles.request(after: "").limit == 50)
        #expect(LargestFiles.request(after: "123:abc").after == "123:abc")
    }

    @Test func l2ItsAnswer() {
        var lof = Msg_ListOfFiles()
        lof.files = [file("/a", "h")]
        lof.token = "9:h"
        #expect(LargestFiles.answer(answer(.respListOfFiles(lof))) == .page([file("/a", "h")], token: "9:h"))
        #expect(LargestFiles.answer(refusal("unknown_payload", "x")) == .old)
        // A failure, in the device's words when it said why.
        #expect(LargestFiles.answer(refusal("", "database is down")) == .failed("database is down"))
        #expect(LargestFiles.answer(nil) == .failed(""))
    }

    @Test func l3APageAddedUnderTheRows() {
        #expect(LargestFiles.addPage([file("/a", "1")], [file("/b", "2"), file("/c", "3")]).map(\.path) == ["/a", "/b", "/c"])
        // No path shown twice, nor content shown at another path.
        let out = LargestFiles.addPage([file("/a", "1"), file("/b", "2")], [file("/b", "2"), file("/z", "1"), file("/c", "3"), file("/c", "4")])
        #expect(out.map { "\($0.path)#\($0.hash)" } == ["/a#1", "/b#2", "/c#3"])
    }

    @Test func l4WhatTheScreenShows() {
        #expect(LargestFiles.shown(rows: 0, load: .loading) == .loading)
        // An older device, whatever was shown.
        #expect(LargestFiles.shown(rows: 0, load: .old) == .old)
        #expect(LargestFiles.shown(rows: 3, load: .old) == .old)
        // The first page failed: the device's words or ours, with Try again.
        #expect(LargestFiles.shown(rows: 0, load: .failed("")) == .failed(""))
        // No files at all.
        #expect(LargestFiles.shown(rows: 0, load: .end) == .empty)
        // Rows, with at their foot a spinner, a failure or nothing.
        #expect(LargestFiles.shown(rows: 50, load: .loading) == .rows(.loading))
        #expect(LargestFiles.shown(rows: 50, load: .failed("")) == .rows(.failed))
        #expect(LargestFiles.shown(rows: 50, load: .more) == .rows(.none))
        #expect(LargestFiles.shown(rows: 12, load: .end) == .rows(.none))
    }

    @Test func l5TheNextPage() {
        #expect(LargestFiles.wantsMore(.more))
        for l: LargestFiles.Load in [.loading, .end, .old, .failed("")] {
            #expect(!LargestFiles.wantsMore(l))
        }
    }

    @Test func l6InNPlaces() {
        var f = file("/a", "h")
        #expect(LargestFiles.places(f) == 0)
        f.copies = 1
        #expect(LargestFiles.places(f) == 0)
        f.copies = 3
        #expect(LargestFiles.places(f) == 3)
    }

    @Test func l7PhotosAndVideos() {
        #expect(LargestFiles.isMedia(file("/a", "", mime: "image/png")))
        #expect(LargestFiles.isMedia(file("/a", "", mime: "video/quicktime")))
        #expect(LargestFiles.isMedia(file("/a", "", mime: "Video/MP4; codecs=hvc1")))
        #expect(!LargestFiles.isMedia(file("/a.pdf", "", mime: "application/pdf")))
        // A HEIC or HEIF by its name, whatever its type.
        #expect(LargestFiles.isMedia(file("/IMG_1.HEIC", "", mime: "application/octet-stream")))
        #expect(LargestFiles.isMedia(file("/x.heif", "", mime: "")))
    }

    @Test func l8CompressInTheSelectionBar() {
        let doc = file("/a.pdf", "1", mime: "application/pdf")
        let video = file("/b.mov", "2", mime: "video/quicktime")
        #expect(LargestFiles.canCompress([doc, video]))
        #expect(!LargestFiles.canCompress([doc]))
        #expect(!LargestFiles.canCompress([]))
        // What Compress asks about: the photos and videos picked, in the
        // list's order.
        #expect(LargestFiles.compressPicks([doc, video], ["/a.pdf", "/b.mov"]).map { $0.path } == ["/b.mov"])
    }

    @Test func l9TheListAfterAJob() {
        #expect(LargestFiles.reloadsAfter(compressed: 1))
        #expect(!LargestFiles.reloadsAfter(compressed: 0))
    }

    @Test func l10ARowsFolder() {
        #expect(LargestFiles.folder(of: "/Photos/2024/IMG_1.jpg") == "/Photos/2024")
        #expect(LargestFiles.folder(of: "/notes.txt") == "/")
        #expect(LargestFiles.name(of: "/Photos/2024/IMG_1.jpg") == "IMG_1.jpg")
    }
}

// MARK: - The viewer's rules

@MainActor
struct CompressViewerRulesTests {
    private func item(original: String, removed: Bool = false) -> PhotoGalleryVM.Item {
        PhotoGalleryVM.Item(id: "/a.jpg", path: "/a.jpg", mime: "image/jpeg", size: 1, thumbData: nil, localURL: nil,
                            isLocalOnly: false, hash: "h", originalHash: original, originalRemoved: removed)
    }

    @Test func r1NoRevertToOriginalForACompressedFile() {
        #expect(PhotoEditRules.offersRevert(item(original: "abc")))
        #expect(!PhotoEditRules.offersRevert(item(original: "abc", removed: true)))
        #expect(!PhotoEditRules.offersRevert(item(original: "", removed: true)))
        // Edit stays.
        #expect(PhotoEditRules.offersEdit(item(original: "abc", removed: true)))
    }

    @Test func r2InfosNote() {
        #expect(CompressRules.compressedNote(item(original: "abc", removed: true)))
        #expect(!CompressRules.compressedNote(item(original: "abc")))
        #expect(!CompressRules.compressedNote(item(original: "")))
    }
}

// MARK: - The dialog, against a device

/// The device, for the dialog's requests: its options, its job as it goes
/// (one answer each GetCompressJob, the last one kept), and how Compress is
/// answered.
@MainActor
final class CompressDevice {
    var options: [Msg_CompressOption] = []
    var optionsRefusal: Msg_RespEnvelope?
    /// CompressFiles' answer: nil for none at all (the connection dropped).
    var start: Msg_RespEnvelope?
    /// The job's answers to GetCompressJob, in turn; the last stays.
    var jobs: [Msg_RespEnvelope] = []
    private(set) var asked: [Msg_ReqEnvelope.OneOf_Payload] = []

    var jobAsks: [String] {
        asked.compactMap { if case .reqGetCompressJob(let g) = $0 { return g.id } else { return nil } }
    }

    var sent: Msg_CompressFiles? {
        for p in asked { if case .reqCompressFiles(let c) = p { return c } }
        return nil
    }

    func attach(_ m: CompressModel) {
        m.pollDelay = { shown, watched, over in
            CompressRules.pollDelay(shown: shown, watched: watched, over: over).map { $0 == .seconds(2) ? .milliseconds(20) : .milliseconds(60) }
        }
        m.request = { [weak self] payload in
            guard let self else { throw CancellationError() }
            self.asked.append(payload)
            switch payload {
            case .reqGetCompressOptions:
                if let no = self.optionsRefusal { return no }
                var o = Msg_CompressOptions()
                o.items = self.options
                return answer(.respCompressOptions(o))
            case .reqCompressFiles:
                guard let start = self.start else { throw WSClient.TimedOut() }
                return start
            case .reqGetCompressJob:
                guard let next = self.jobs.first else { throw WSClient.TimedOut() }
                if self.jobs.count > 1 { self.jobs.removeFirst() }
                return next
            default:
                throw CancellationError()
            }
        }
    }
}

@MainActor
@Suite(.serialized)
struct CompressModelTests {
    private func items(_ options: [Msg_CompressOption]) -> [Msg_CompressItem] {
        CompressRules.items(options.map { ($0.path, $0.hash) })
    }

    @Test func theDialogOpensOnItsDefaultsAndSendsWhatItShows() async {
        let device = CompressDevice()
        device.options = all
        device.start = answer(.respCompressJob(job(total: 7)))
        device.jobs = [answer(.respCompressJob(job(total: 7, done: 7, finished: true)))]
        let m = CompressModel(items: items(all))
        device.attach(m)
        var started = 0
        m.onStarted = { started += 1 }
        await m.load()
        #expect(m.phase == .choosing)
        #expect(m.video == "video_1080p" && m.photos == on())
        #expect(m.tasks.count == 7)
        await m.start()
        #expect(started == 1)
        #expect(device.sent?.tasks.map(\.preset) == ["video_1080p", "video_1080p", "video_hevc", "photo_12mp", "photo_12mp", "jpeg_resave", "png_to_jpeg"])
        #expect(m.jobID == "j1" && m.sentVideo)
        // Leave as they are, and photos only: nothing slow.
        m.video = "video_none"
        #expect(!CompressRules.slow(m.tasks))
    }

    @Test func anOlderDeviceOrNoAnswerToTheOptions() async {
        let device = CompressDevice()
        device.optionsRefusal = refusal("unknown_payload", "unknown payload")
        let m = CompressModel(items: items([png]))
        device.attach(m)
        await m.load()
        #expect(m.phase == .needsUpdate)
        device.optionsRefusal = refusal("too_many", "pick at most 500 files at a time")
        await m.load()
        #expect(m.phase == .optionsFailed("pick at most 500 files at a time"))
        // Nothing picked that the device holds: nothing asked, nothing to do.
        let none = CompressModel(items: [])
        device.attach(none)
        let before = device.asked.count
        await none.load()
        #expect(none.phase == .choosing && CompressRules.nothing(none.options))
        #expect(device.asked.count == before)
    }

    @Test func aRefusalSaysTheDevicesWordsAndKeepsTheChoices() async {
        let device = CompressDevice()
        device.options = [png, big]
        device.start = refusal("demo", "changes are turned off on the demo")
        let m = CompressModel(items: items([png, big]))
        device.attach(m)
        var started = false
        m.onStarted = { started = true }
        await m.load()
        await m.start()
        #expect(m.refusal == "changes are turned off on the demo")
        #expect(m.phase == .choosing && !started)
    }

    @Test func noAnswerAsksWhetherTheJobStartedEvenSo() async {
        let device = CompressDevice()
        device.options = [png]
        device.start = nil
        // The device's job holds the file sent: it got there.
        device.jobs = [answer(.respCompressJob(job(total: 1, [result("/p/shot.png", "running")])))]
        let m = CompressModel(items: items([png]))
        device.attach(m)
        await m.load()
        await m.start()
        #expect(m.jobID == "j1" && m.refusal == nil)
        #expect(device.jobAsks.first == "")
        // An older job, of other files: it didn't.
        let other = CompressDevice()
        other.options = [png]
        other.jobs = [answer(.respCompressJob(job("old", total: 1, finished: true, [result("/x.jpg", "done")])))]
        let n = CompressModel(items: items([png]))
        other.attach(n)
        await n.load()
        await n.start()
        #expect(n.jobID == nil && n.phase == .choosing)
        #expect(n.refusal == S.appFilesCompressFailed(count: 1))
    }

    @Test func theJobIsFollowedAndEachFileHandedOnce() async {
        let device = CompressDevice()
        device.options = [png, fatJpeg]
        let a = file("/p/shot.jpg", "n1", size: 1)
        device.start = answer(.respCompressJob(job(total: 2, [result("/p/shot.png", "running"), result("/p/12mp.jpg", "waiting")])))
        device.jobs = [
            answer(.respCompressJob(job(total: 2, done: 1, [result("/p/shot.png", "done", file: a), result("/p/12mp.jpg", "running")]))),
            answer(.respCompressJob(job(total: 2, done: 2, saved: 5, finished: true, [
                result("/p/shot.png", "done", file: a),
                result("/p/12mp.jpg", "skipped", message: "compressed, it came out no smaller, so the file was left as it was"),
            ]))),
        ]
        let m = CompressModel(items: items([png, fatJpeg]))
        device.attach(m)
        m.shown = true
        var handed: [[String]] = []
        var finished: [Int] = []
        m.onResults = { handed.append($0.map(\.path)) }
        m.onFinished = { finished.append($0) }
        await m.load()
        await m.start()
        await waitUntil { m.over }
        #expect(m.over)
        #expect(handed == [["/p/shot.png"]])
        #expect(finished == [1])
        let asks = device.jobAsks.count
        #expect(device.jobAsks.allSatisfy { $0 == "j1" })
        // Over: never asked again.
        try? await Task.sleep(nanoseconds: 200_000_000)
        #expect(device.jobAsks.count == asks)
        if case .job(let j) = m.phase {
            #expect(CompressRules.outcome(j).failed.map(\.name) == ["12mp.jpg"])
        }
    }

    @Test func aJobGoneIsOverAsLastHeardOf() async {
        let device = CompressDevice()
        device.options = [png]
        device.start = answer(.respCompressJob(job(total: 1, [result("/p/shot.png", "running")])))
        device.jobs = [refusal("not_found", "there is no compress job")]
        let m = CompressModel(items: items([png]))
        device.attach(m)
        var finished: [Int] = []
        m.onFinished = { finished.append($0) }
        await m.load()
        await m.start()
        await waitUntil { m.over }
        #expect(m.gone && m.over)
        #expect(finished == [0])
    }

    @Test func aModelNoOneHoldsAsksNoMore() async {
        let device = CompressDevice()
        device.options = [png]
        device.start = answer(.respCompressJob(job(total: 1, [result("/p/shot.png", "running")])))
        device.jobs = [answer(.respCompressJob(job(total: 1, [result("/p/shot.png", "running")])))]
        var m: CompressModel? = CompressModel(items: items([png]))
        device.attach(m!)
        await m!.load()
        await m!.start()
        await waitUntil { device.jobAsks.count >= 2 }
        weak var gone = m
        m = nil
        // Let go once an ask on its way is over.
        await waitUntil { gone == nil }
        #expect(gone == nil)
        let asks = device.jobAsks.count
        try? await Task.sleep(nanoseconds: 300_000_000)
        #expect(device.jobAsks.count == asks)
    }

    @Test func unwatchedTheJobWaitsAndBackOnScreenGoesOn() async {
        let device = CompressDevice()
        device.options = [png]
        device.start = answer(.respCompressJob(job(total: 1, [result("/p/shot.png", "running")])))
        device.jobs = [answer(.respCompressJob(job(total: 1, [result("/p/shot.png", "running")])))]
        let m = CompressModel(items: items([png]))
        device.attach(m)
        m.watched = false
        await m.load()
        await m.start()
        // Neither shown nor watched: not asked about.
        try? await Task.sleep(nanoseconds: 200_000_000)
        #expect(device.jobAsks.isEmpty)
        m.watched = true
        await waitUntil { !device.jobAsks.isEmpty }
        #expect(!device.jobAsks.isEmpty)
    }
}

// MARK: - Images and the list, against a device

extension ImagesNotificationTests {
@MainActor
@Suite(.serialized)
struct CompressGalleryTests {
    @Test func compressedPhotosTakeTheirPlacesInImages() async {
        let vm = PhotoGalleryVM(deviceID: "test", localPhotosFolder: nil, thumbCache: testThumbCache())
        let images = EditDevice()
        images.attach(vm)
        await vm.resetAndLoadFirstPage()
        #expect(vm.items.count == 3)
        vm.selected = ["/photos/0.HEIC", "/photos/2.jpg"]
        // In the grid's order, with the content shown.
        #expect(vm.compressPicks().map { [$0.path, $0.hash] } == [["/photos/0.HEIC", "h0"], ["/photos/2.jpg", "h2"]])
        let m = vm.compressModel()
        #expect(m.items.map(\.path) == ["/photos/0.HEIC", "/photos/2.jpg"])
        let device = CompressDevice()
        device.options = [opt("/photos/0.HEIC", 100, [("photo_12mp", 40)]), opt("/photos/2.jpg", 100, [("jpeg_resave", 50)])]
        device.options[0].hash = "h0"
        device.options[1].hash = "h2"
        var heic = file("/photos/0.jpg", "c0", size: 40)
        heic.originalHash = "h0"
        heic.originalRemoved = true
        device.start = answer(.respCompressJob(job(total: 2, [result("/photos/0.HEIC", "running"), result("/photos/2.jpg", "waiting")])))
        device.jobs = [answer(.respCompressJob(job(total: 2, done: 2, saved: 60, finished: true, [
            result("/photos/0.HEIC", "done", file: heic),
            result("/photos/2.jpg", "failed", message: "the device could not compress this file"),
        ])))]
        device.attach(m)
        await m.load()
        await m.start()
        #expect(device.sent?.tasks.map { [$0.path, $0.hash, $0.preset] } == [["/photos/0.HEIC", "h0", "photo_12mp"], ["/photos/2.jpg", "h2", "jpeg_resave"]])
        // Taken: the selection goes, and Images follows the job.
        #expect(vm.selected.isEmpty)
        #expect(vm.compressFollow === m)
        let shown = vm.items[0]
        await waitUntil { m.over }
        // J6: in its place, by its new name and content, its date kept.
        let now = vm.items[0]
        #expect(now.path == "/photos/0.jpg" && now.hash == "c0" && now.mime == "image/jpeg")
        #expect(now.originalHash == "h0" && now.originalRemoved)
        #expect(now.created == shown.created && now.month == shown.month)
        #expect(vm.items[2].hash == "h2")
        #expect(vm.items.count == 3)
        // Over: Images lets go of it.
        #expect(vm.compressFollow == nil)
        // R1, R2: opened, no Revert to Original; Info's note.
        vm.open(index: 0)
        #expect(!vm.revertOffered && vm.editOffered)
        #expect(vm.openItem.map(CompressRules.compressedNote) == true)
        vm.closeModal()
    }

    @Test func aPhotoOnlyOnThisPhoneIsNeverAskedAbout() async {
        let vm = PhotoGalleryVM(deviceID: "test", localPhotosFolder: nil, thumbCache: testThumbCache())
        let images = EditDevice()
        images.attach(vm)
        await vm.resetAndLoadFirstPage()
        vm.items[1].isLocalOnly = true
        vm.selected = ["/photos/1.jpg", "/photos/2.jpg"]
        #expect(vm.compressPicks().map { $0.path } == ["/photos/2.jpg"])
    }
}
}

/// The device, for the Largest files list: its pages in turn (an answer, or
/// nil for none), each ListLargestFiles noted.
@MainActor
final class LargestDevice {
    var pages: [Msg_RespEnvelope?] = []
    private(set) var asked: [Msg_ListLargestFiles] = []

    static func page(_ files: [Msg_File], token: String) -> Msg_RespEnvelope {
        var lof = Msg_ListOfFiles()
        lof.files = files
        lof.token = token
        return answer(.respListOfFiles(lof))
    }

    func attach(_ m: LargestFilesModel) {
        m.retry.delay = { _ in 0.05 }
        m.thumbs.request = { _ in throw CancellationError() }
        m.request = { [weak self] payload in
            guard let self, case .reqListLargestFiles(let l) = payload else { throw CancellationError() }
            self.asked.append(l)
            guard !self.pages.isEmpty, let next = self.pages.removeFirst() else { throw WSClient.TimedOut() }
            return next
        }
    }
}

@MainActor
@Suite(.serialized)
struct LargestFilesModelTests {
    private func video(_ name: String, _ size: Int64, copies: Int32 = 1) -> Msg_File {
        var f = file("/v/\(name)", "h-\(name)", mime: "video/quicktime", size: size)
        f.copies = copies
        return f
    }

    @Test func theListPagesByItsTokenAndEnds() async {
        let device = LargestDevice()
        device.pages = [
            LargestDevice.page([video("a.mov", 900), video("b.mov", 800, copies: 2)], token: "800:h-b.mov"),
            LargestDevice.page([file("/docs/c.pdf", "h-c", mime: "application/pdf", size: 700)], token: ""),
        ]
        let m = LargestFilesModel(thumbCache: testThumbCache())
        device.attach(m)
        #expect(m.view == .loading)
        await m.appeared()
        #expect(m.files.map(\.path) == ["/v/a.mov", "/v/b.mov"])
        #expect(m.load == .more && m.view == .rows(.none))
        #expect(LargestFiles.places(m.files[1]) == 2)
        // The end near: the next page, after the token.
        m.rowShown(1)
        await waitUntil { m.load == .end }
        #expect(m.files.map(\.path) == ["/v/a.mov", "/v/b.mov", "/docs/c.pdf"])
        #expect(device.asked.map(\.after) == ["", "800:h-b.mov"])
        #expect(device.asked.allSatisfy { $0.limit == 50 })
        // Over: nothing more asked.
        m.rowShown(2)
        try? await Task.sleep(nanoseconds: 100_000_000)
        #expect(device.asked.count == 2)
    }

    @Test func aPageThatFailsIsAskedAgainWithTheBackoff() async {
        let device = LargestDevice()
        device.pages = [nil, LargestDevice.page([video("a.mov", 900)], token: "")]
        let m = LargestFilesModel(thumbCache: testThumbCache())
        device.attach(m)
        await m.appeared()
        #expect(m.view == .failed(""))
        await waitUntil { m.load == .end }
        #expect(m.view == .rows(.none))
        #expect(device.asked.count == 2)
        // A later page that fails: the rows stay, the foot says so.
        let more = LargestDevice()
        more.pages = [LargestDevice.page([video("a.mov", 900)], token: "900:h-a.mov"), refusal("", "database is down")]
        let n = LargestFilesModel(thumbCache: testThumbCache())
        more.attach(n)
        n.retry.delay = { _ in 60 }
        await n.appeared()
        n.rowShown(0)
        await waitUntil { n.load == .failed("database is down") }
        #expect(n.view == .rows(.failed) && n.files.count == 1)
        // Try again: now.
        more.pages = [LargestDevice.page([video("b.mov", 800)], token: "")]
        n.tryAgain()
        await waitUntil { n.load == .end }
        #expect(n.files.map(\.path) == ["/v/a.mov", "/v/b.mov"])
    }

    @Test func anOlderDeviceOrNoFiles() async {
        let device = LargestDevice()
        device.pages = [refusal("unknown_payload", "unknown payload")]
        let m = LargestFilesModel(thumbCache: testThumbCache())
        device.attach(m)
        await m.appeared()
        #expect(m.view == .old)
        let empty = LargestDevice()
        empty.pages = [LargestDevice.page([], token: "")]
        let n = LargestFilesModel(thumbCache: testThumbCache())
        empty.attach(n)
        await n.appeared()
        #expect(n.view == .empty)
    }

    @Test func aFileDeletedInTheViewerLeavesTheList() async {
        let device = LargestDevice()
        device.pages = [LargestDevice.page([video("a.mov", 900), video("b.mov", 800)], token: "")]
        let m = LargestFilesModel(thumbCache: testThumbCache())
        device.attach(m)
        await m.appeared()
        m.selected = ["/v/a.mov"]
        // The viewer deleted it: gone from its items, gone from the list.
        let left = m.viewerItems().filter { $0.path != "/v/a.mov" }
        m.viewerChanged("/v/a.mov", shown: left)
        #expect(m.files.map(\.path) == ["/v/b.mov"])
        #expect(m.selected.isEmpty)
        // Nothing to read again as it closes.
        m.viewerClosed()
        try? await Task.sleep(nanoseconds: 100_000_000)
        #expect(device.asked.count == 1)
        // An edit there: the list is read again as the viewer closes.
        device.pages = [LargestDevice.page([video("b.mov", 700)], token: "")]
        m.viewerChanged("/v/b.mov", shown: m.viewerItems())
        m.viewerClosed()
        await waitUntil { device.asked.count == 2 && m.load == .end }
        #expect(device.asked.last?.after == "" && device.asked.last?.limit == 50)
        #expect(m.files.map(\.path) == ["/v/b.mov"] && m.files[0].size64 == 700)
    }

    @Test func readAgainInPlaceAsFarAsItWasShown() async {
        let device = LargestDevice()
        let shown = (0..<260).map { video("\($0).mov", Int64(10_000 - $0)) }
        device.pages = [LargestDevice.page(shown, token: "9741:h-259.mov")]
        let m = LargestFilesModel(thumbCache: testThumbCache())
        device.attach(m)
        await m.appeared()
        #expect(m.files.count == 260)
        m.selected = ["/v/0.mov", "/v/5.mov"]
        // The first one is gone since: the list is read again from the top,
        // in the device's biggest pages, as far as it was shown.
        device.pages = [
            LargestDevice.page(Array(shown[1...200]), token: "t200"),
            LargestDevice.page(Array(shown[201...259]) + [video("260.mov", 9_000)], token: "t260"),
        ]
        await m.reread()
        #expect(device.asked.dropFirst().map { [$0.after, "\($0.limit)"] } == [["", "200"], ["t200", "60"]])
        #expect(m.files.count == 260 && m.files.first?.path == "/v/1.mov" && m.files.last?.path == "/v/260.mov")
        #expect(m.load == .more)
        // What was picked and is still there stays picked.
        #expect(m.selected == ["/v/5.mov"])
        // Failing, the rows stay as they were and it is read again.
        device.pages = [nil, LargestDevice.page(Array(shown[2...10]), token: "")]
        await m.reread()
        #expect(m.files.count == 260 && m.view == .rows(.failed))
        await waitUntil { m.load == .end }
        #expect(m.files.count == 9)
    }

    @Test func compressedRowsChangeAndTheListIsReadAgain() async {
        let device = LargestDevice()
        var doc = file("/docs/c.pdf", "h-c", mime: "application/pdf", size: 700)
        doc.copies = 1
        device.pages = [LargestDevice.page([video("a.mov", 900, copies: 2), doc], token: "")]
        let m = LargestFilesModel(thumbCache: testThumbCache())
        device.attach(m)
        await m.appeared()
        m.selected = ["/v/a.mov", "/docs/c.pdf"]
        #expect(m.canCompress)
        let c = m.compressModel()
        // The document isn't asked about.
        #expect(c.items.map(\.path) == ["/v/a.mov"])
        let compress = CompressDevice()
        var o = opt("/v/a.mov", 900, [("video_hevc", 400)])
        o.hash = "h-a.mov"
        compress.options = [o]
        var smaller = file("/v/a.mov", "n-a", mime: "video/quicktime", size: 380)
        smaller.originalHash = "h-a.mov"
        smaller.originalRemoved = true
        compress.start = answer(.respCompressJob(job(total: 1, [result("/v/a.mov", "running", percent: 10)])))
        compress.jobs = [
            answer(.respCompressJob(job(total: 1, done: 1, [result("/v/a.mov", "done", file: smaller)]))),
            answer(.respCompressJob(job(total: 1, done: 1, saved: 520, finished: true, [result("/v/a.mov", "done", file: smaller)]))),
        ]
        compress.attach(c)
        c.shown = true
        device.pages = [LargestDevice.page([doc, smaller], token: "")]
        await c.load()
        await c.start()
        #expect(m.selected.isEmpty)
        // J6: in its row's place, its copies kept, until the list is read
        // again (L9) once the job is over.
        await waitUntil { m.files.first?.hash == "n-a" || device.asked.count == 2 }
        await waitUntil { device.asked.count == 2 && m.load == .end }
        #expect(device.asked.count == 2)
        #expect(m.files.map(\.path) == ["/docs/c.pdf", "/v/a.mov"])
    }
}
