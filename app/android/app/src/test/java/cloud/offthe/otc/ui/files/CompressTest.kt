// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.files

import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appFilesCompressFailed
import cloud.offthe.otc.i18n.appFilesCompressOptionsFailed
import cloud.offthe.otc.i18n.appFilesLargestFailed
import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.CompressChoice
import cloud.offthe.otc.proto.CompressItem
import cloud.offthe.otc.proto.CompressJob
import cloud.offthe.otc.proto.CompressOption
import cloud.offthe.otc.proto.CompressOptions
import cloud.offthe.otc.proto.CompressResult
import cloud.offthe.otc.proto.CompressTask
import cloud.offthe.otc.proto.File
import cloud.offthe.otc.proto.ListOfFiles
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.RespPhotoDateBuckets
import cloud.offthe.otc.ui.common.LoadProblem
import cloud.offthe.otc.ui.common.TestThumbStore
import cloud.offthe.otc.ui.gallery.GalleryRequest
import cloud.offthe.otc.ui.gallery.PhotoEdit
import cloud.offthe.otc.ui.gallery.PhotoGalleryViewModel
import com.google.protobuf.Timestamp
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.cancel
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import java.io.IOException
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.Executors

// Compress and Settings › Largest files (Compress.kt, LargestFiles.kt): the
// Compress dialog's rules, the job's answers, the list's paging and states,
// and the viewer's compressed photos - then the models that run them, on a
// pretend device. The case ids are the web's (web/scripts/compress-test.mjs)
// and iOS's (CompressTests.swift): the same rules, tested alike.
//   C1  the videos' radio: the video presets offered, 1080p, 720p, HEVC in
//       that order, then "Leave as they are"; nothing without a video preset
//   C2  the radio's default: 1080p if any item offers it, else HEVC, else 720p
//   C3  a switch per photo preset offered by any item, in order, each on
//   C4  per item: a video the chosen preset, else HEVC, else nothing ("Leave
//       as they are": nothing); a photo 12 MP, else JPEG resave, else PNG to
//       JPEG, each only with its switch on; a preset the app doesn't know is
//       left out
//   C5  the tasks, in the order asked, with the path and hash the device
//       answered for
//   C6  a video choice's row: everything it would do if chosen (HEVC
//       fallbacks included), count, sizes now and estimates after
//   C7  a photo switch's row: the photos that would get its preset were it
//       on, the other switches as they are
//   C8  the foot's total: each task's size less its estimate, never below 0
//   C9  "videos take a while" only with a video among the tasks
//   C10 the files not offered (no known preset), with the device's reason;
//       "nothing can be made smaller" when that is every file
//   C11 GetCompressOptions' answer: options, an older device, a refusal in
//       the device's words (more than 500), or nothing usable
//   C12 CompressFiles' answer: the job, an older device, a refusal, nothing
//   C13 the requests' payloads
//   J1  GetCompressJob's answer: the job, gone (not_found, another id), or none
//   J2  the job is asked every 2 s while shown, every 10 s while only a page
//       follows it, never once over or unwatched
//   J3  the job's progress: tasks over plus a running video's percent
//   J4  what it came to: compressed, freed, and the files left as they were
//       (failed and skipped; gone, the tasks not over too)
//   J5  each done result is put in place once
//   J6  the file shown after: the device's over the old one, its copies kept
//   L1  ListLargestFiles' payload: pages of 50 after a token
//   L2  its answer: a page and the next token, an older device, a failure
//   L3  a page added under the rows: no path or content shown twice
//   L4  what the screen shows: loading, an older device, failed, empty, rows
//       with a spinner, a failure or nothing at their foot
//   L5  the next page is asked only while there is one and none is on its way
//   L6  "in N places" only for content at several paths
//   L7  photos and videos by their type, or a HEIC/HEIF by its name
//   L8  Compress is on while a photo or video is picked
//   L9  the list is read again after a job that compressed anything
//   L10 a row's folder
//   R1  no Revert to original for a compressed file (original_removed)
//   R2  Info's note for a compressed file
class CompressTest {
    private val gbBytes = 1L shl 30
    private val mb = 1L shl 20
    // Whole bytes, as the device sends them.
    private fun gb(x: Double): Long = Math.round(x * gbBytes)

    // An option as the device answers it.
    private fun opt(path: String, size: Long, choices: List<Pair<String, Long>> = emptyList(), reason: String = "", reasonCode: String = ""): CompressOption =
        CompressOption.newBuilder().setPath(path).setHash("h:$path").setSize(size).setCopies(1)
            .addAllChoices(choices.map { (p, est) -> CompressChoice.newBuilder().setPreset(p).setEstimatedSize(est).build() })
            .setReason(reason).setReasonCode(reasonCode).build()

    private val fourKHevc = opt("/v/4k-hevc.mov", gb(4.0), listOf(CompressRules.VIDEO_1080P to gb(1.2), CompressRules.VIDEO_720P to gb(0.6)))
    private val fourKH264 = opt("/v/4k-h264.mp4", gb(6.0), listOf(CompressRules.VIDEO_1080P to gb(1.5), CompressRules.VIDEO_720P to gb(0.8), CompressRules.VIDEO_HEVC to gb(3.0)))
    private val hdH264 = opt("/v/1080p.mp4", gb(2.0), listOf(CompressRules.VIDEO_720P to gb(0.5), CompressRules.VIDEO_HEVC to gb(1.0)))
    private val big = opt("/p/48mp.heic", 12 * mb, listOf(CompressRules.PHOTO_12MP to 4 * mb))
    private val bigJpeg = opt("/p/48mp.jpg", 20 * mb, listOf(CompressRules.PHOTO_12MP to 4 * mb, CompressRules.JPEG_RESAVE to 10 * mb))
    private val fatJpeg = opt("/p/12mp.jpg", 8 * mb, listOf(CompressRules.JPEG_RESAVE to 3 * mb))
    private val png = opt("/p/shot.png", 6 * mb, listOf(CompressRules.PNG_TO_JPEG to 1 * mb))
    private val pdf = opt("/docs/scan.pdf", 30 * mb, reason = "only photos and videos can be compressed", reasonCode = "not_media")
    private val small = opt("/v/720p.mov", 300 * mb, reason = "the device can't make this file noticeably smaller", reasonCode = "nothing_to_gain")
    private val all = listOf(fourKHevc, fourKH264, hdH264, big, bigJpeg, fatJpeg, png, pdf, small)

    private val allOn = CompressRules.PHOTOS.toSet()
    private fun on(vararg off: String) = allOn - off.toSet()
    private fun presetOf(o: CompressOption, video: String, photos: Set<String> = allOn) = CompressRules.choiceFor(o, video, photos)?.preset

    private fun refused(code: String, message: String) = RespEnvelope.newBuilder().setError(true).setErrorCode(code).setErrorMessage(message).build()
    private fun jobOf(id: String = "j1", total: Int = 0, done: Int = 0, saved: Long = 0, finished: Boolean = false, results: List<CompressResult> = emptyList()) =
        CompressJob.newBuilder().setId(id).setTotal(total).setDone(done).setSaved(saved).setFinished(finished).addAllResults(results).build()
    private fun result(path: String, state: String, percent: Int = 0, message: String = "", file: File? = null): CompressResult =
        CompressResult.newBuilder().setPath(path).setState(state).setPercent(percent).setMessage(message).also { b -> file?.let { b.file = it } }.build()
    private fun file(path: String, hash: String, mime: String = "image/jpeg", copies: Int? = null, size: Long = 0, created: Long? = null,
                     originalHash: String = "", removed: Boolean = false): File =
        File.newBuilder().setPath(path).setHash(hash).setMime(mime).setSize64(size).setOriginalHash(originalHash).setOriginalRemoved(removed)
            .also { b -> copies?.let { b.copies = it }; created?.let { b.created = Timestamp.newBuilder().setSeconds(it).build() } }.build()
    private fun jobAnswer(j: CompressJob) = RespEnvelope.newBuilder().setRespCompressJob(j).build()
    private fun page(files: List<File>, token: String) = RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.newBuilder().addAllFiles(files).setToken(token)).build()

    // ---- The dialog's rules ----

    /** C1 */
    @Test fun theVideosRadio() {
        val none = CompressRules.VIDEO_NONE
        assertEquals(listOf(CompressRules.VIDEO_1080P, CompressRules.VIDEO_720P, CompressRules.VIDEO_HEVC, none), CompressRules.videoChoices(all))
        assertEquals(listOf(CompressRules.VIDEO_720P, CompressRules.VIDEO_HEVC, none), CompressRules.videoChoices(listOf(hdH264)))
        assertEquals(listOf(CompressRules.VIDEO_1080P, CompressRules.VIDEO_720P, none), CompressRules.videoChoices(listOf(fourKHevc)))
        // No video preset: no radio at all.
        assertTrue(CompressRules.videoChoices(listOf(big, png, pdf)).isEmpty())
        assertTrue(CompressRules.videoChoices(emptyList()).isEmpty())
    }

    /** C2 */
    @Test fun theRadiosDefault() {
        assertEquals(CompressRules.VIDEO_1080P, CompressRules.defaultVideo(listOf(hdH264, fourKHevc)))
        assertEquals(CompressRules.VIDEO_HEVC, CompressRules.defaultVideo(listOf(hdH264)))
        assertEquals(CompressRules.VIDEO_720P, CompressRules.defaultVideo(listOf(opt("/v/x.mov", gbBytes, listOf(CompressRules.VIDEO_720P to gb(0.3))))))
        // No video: none (the dialog's choice is Leave as they are, checked on the model below).
        assertNull(CompressRules.defaultVideo(listOf(big)))
    }

    /** C3 */
    @Test fun thePhotoSwitches() {
        assertEquals(listOf(CompressRules.PHOTO_12MP, CompressRules.JPEG_RESAVE, CompressRules.PNG_TO_JPEG), CompressRules.photoSwitches(all))
        assertEquals(listOf(CompressRules.JPEG_RESAVE, CompressRules.PNG_TO_JPEG), CompressRules.photoSwitches(listOf(png, fatJpeg)))
        assertTrue(CompressRules.photoSwitches(listOf(fourKH264, pdf)).isEmpty())
    }

    /** C4 */
    @Test fun whatEachItemGets() {
        // A video: the preset chosen when it is offered it...
        assertEquals(CompressRules.VIDEO_1080P, presetOf(fourKHevc, CompressRules.VIDEO_1080P))
        assertEquals(CompressRules.VIDEO_720P, presetOf(fourKH264, CompressRules.VIDEO_720P))
        // ...else HEVC when it is offered that, else nothing.
        assertEquals(CompressRules.VIDEO_HEVC, presetOf(hdH264, CompressRules.VIDEO_1080P))
        assertNull(presetOf(fourKHevc, CompressRules.VIDEO_HEVC))
        // Leave as they are: nothing for any video.
        for (o in listOf(fourKHevc, fourKH264, hdH264)) assertNull(presetOf(o, CompressRules.VIDEO_NONE))
        // A photo: 12 MP, then the JPEG resave, then PNG to JPEG, each with its switch on.
        assertEquals(CompressRules.PHOTO_12MP, presetOf(bigJpeg, CompressRules.VIDEO_1080P))
        assertEquals(CompressRules.JPEG_RESAVE, presetOf(bigJpeg, CompressRules.VIDEO_1080P, on(CompressRules.PHOTO_12MP)))
        assertNull(presetOf(bigJpeg, CompressRules.VIDEO_1080P, on(CompressRules.PHOTO_12MP, CompressRules.JPEG_RESAVE)))
        assertEquals(CompressRules.PNG_TO_JPEG, presetOf(png, CompressRules.VIDEO_1080P))
        assertNull(presetOf(png, CompressRules.VIDEO_1080P, on(CompressRules.PNG_TO_JPEG)))
        // The videos' choice leaves photos alone.
        assertEquals(CompressRules.PHOTO_12MP, presetOf(big, CompressRules.VIDEO_NONE))
        // A preset this app doesn't know is left out.
        val newer = opt("/v/new.mov", gbBytes, listOf("video_av1" to gb(0.2), CompressRules.VIDEO_HEVC to gb(0.5)))
        assertEquals(CompressRules.VIDEO_HEVC, presetOf(newer, CompressRules.VIDEO_1080P))
        assertEquals(listOf(CompressRules.VIDEO_HEVC, CompressRules.VIDEO_NONE), CompressRules.videoChoices(listOf(newer)))
        val onlyNew = opt("/p/new.avif", 9 * mb, listOf("photo_avif" to 2 * mb))
        assertNull(presetOf(onlyNew, CompressRules.VIDEO_1080P))
        assertEquals(listOf("new.avif"), CompressRules.notOffered(listOf(onlyNew)).map { CompressRules.name(it.path) })
        // Nothing offered: nothing.
        assertNull(presetOf(pdf, CompressRules.VIDEO_1080P))
    }

    /** C5 */
    @Test fun theTasks() {
        val tasks = CompressRules.tasks(all, CompressRules.defaultVideo(all)!!, allOn)
        assertEquals(
            listOf(
                "/v/4k-hevc.mov" to CompressRules.VIDEO_1080P, "/v/4k-h264.mp4" to CompressRules.VIDEO_1080P, "/v/1080p.mp4" to CompressRules.VIDEO_HEVC,
                "/p/48mp.heic" to CompressRules.PHOTO_12MP, "/p/48mp.jpg" to CompressRules.PHOTO_12MP, "/p/12mp.jpg" to CompressRules.JPEG_RESAVE,
                "/p/shot.png" to CompressRules.PNG_TO_JPEG,
            ),
            tasks.map { it.path to it.preset },
        )
        assertEquals("h:/v/4k-hevc.mov", tasks[0].hash)
        // Nothing chosen: no task.
        assertTrue(CompressRules.tasks(all, CompressRules.VIDEO_NONE, emptySet()).isEmpty())
        assertTrue(CompressRules.tasks(listOf(pdf, small), CompressRules.defaultVideo(listOf(pdf, small)) ?: CompressRules.VIDEO_NONE, allOn).isEmpty())
    }

    /** C6 */
    @Test fun aVideoChoicesRow() {
        // 1080p: the two 4K videos at 1080p and the 1080p one's fallback to HEVC.
        assertEquals(CompressRules.Sum(3, gb(12.0), gb(1.2) + gb(1.5) + gb(1.0)), CompressRules.videoRow(all, CompressRules.VIDEO_1080P))
        // 720p: every video is offered it.
        assertEquals(CompressRules.Sum(3, gb(12.0), gb(0.6) + gb(0.8) + gb(0.5)), CompressRules.videoRow(all, CompressRules.VIDEO_720P))
        // HEVC: only those offered it (a 4K HEVC video gets nothing).
        assertEquals(CompressRules.Sum(2, gb(8.0), gb(4.0)), CompressRules.videoRow(all, CompressRules.VIDEO_HEVC))
        // No row under Leave as they are.
        assertEquals(CompressRules.Sum.NONE, CompressRules.videoRow(all, CompressRules.VIDEO_NONE))
        assertNull(compressRowText(CompressRules.videoRow(all, CompressRules.VIDEO_NONE)))
    }

    /** C7 */
    @Test fun aPhotoSwitchsRow() {
        // 12 MP: the photos over 13 MP.
        assertEquals(CompressRules.Sum(2, 32 * mb, 8 * mb), CompressRules.photoRow(all, CompressRules.PHOTO_12MP, allOn))
        // The JPEG resave: only the JPEGs 12 MP leaves, while 12 MP is on...
        assertEquals(CompressRules.Sum(1, 8 * mb, 3 * mb), CompressRules.photoRow(all, CompressRules.JPEG_RESAVE, allOn))
        // ...with 12 MP off, the JPEG it would have shrunk is saved again instead.
        assertEquals(CompressRules.Sum(2, 28 * mb, 13 * mb), CompressRules.photoRow(all, CompressRules.JPEG_RESAVE, on(CompressRules.PHOTO_12MP)))
        // A switch off still shows what turning it on would do, and makes no task.
        val off = on(CompressRules.PNG_TO_JPEG)
        assertEquals(CompressRules.Sum(1, 6 * mb, 1 * mb), CompressRules.photoRow(all, CompressRules.PNG_TO_JPEG, off))
        assertEquals(listOf(2, 1, 1), CompressRules.photoSwitches(all).map { CompressRules.photoRow(all, it, off).count })
        assertFalse(CompressRules.tasks(all, CompressRules.VIDEO_NONE, off).any { it.preset == CompressRules.PNG_TO_JPEG })
    }

    /** C8 */
    @Test fun theTotal() {
        assertEquals(gb(4.0) - gb(1.2) + 5 * mb, CompressRules.frees(listOf(fourKHevc, png), CompressRules.VIDEO_1080P, allOn))
        // An estimate above the file's size frees nothing, never less.
        assertEquals(0L, CompressRules.frees(listOf(opt("/a", 10, listOf(CompressRules.JPEG_RESAVE to 12L))), CompressRules.VIDEO_NONE, allOn))
        // No task: nothing.
        assertEquals(0L, CompressRules.frees(all, CompressRules.VIDEO_NONE, emptySet()))
    }

    /** C9 */
    @Test fun videosTakeAWhile() {
        assertTrue(CompressRules.slow(CompressRules.tasks(all, CompressRules.VIDEO_1080P, allOn)))
        assertTrue(CompressRules.slow(CompressRules.tasks(listOf(hdH264), CompressRules.VIDEO_720P, allOn)))
        // Not with photos alone, nor with the videos left as they are.
        assertFalse(CompressRules.slow(CompressRules.tasks(listOf(big, png), CompressRules.VIDEO_NONE, allOn)))
        assertFalse(CompressRules.slow(CompressRules.tasks(all, CompressRules.VIDEO_NONE, allOn)))
    }

    /** C10 */
    @Test fun theFilesNotOffered() {
        val no = CompressRules.notOffered(all)
        assertEquals(listOf("scan.pdf", "720p.mov"), no.map { CompressRules.name(it.path) })
        assertEquals(listOf("only photos and videos can be compressed", "the device can't make this file noticeably smaller"), no.map { it.reason })
        // A file that changed meanwhile is one, in the device's words.
        val changed = opt("/p/x.jpg", 5 * mb, reason = "this file has changed - refresh and try again", reasonCode = "changed")
        assertEquals(listOf("this file has changed - refresh and try again"), CompressRules.notOffered(listOf(changed)).map { it.reason })
        // Nothing at all can be made smaller (an empty answer: nothing either).
        assertTrue(CompressRules.nothing(listOf(pdf, small)))
        assertFalse(CompressRules.nothing(all))
        assertTrue(CompressRules.nothing(emptyList()))
    }

    /** C11 */
    @Test fun getCompressOptionsAnswer() {
        val shown = CompressRules.optionsAnswer(RespEnvelope.newBuilder().setRespCompressOptions(CompressOptions.newBuilder().addItems(pdf)).build())
        assertEquals(1, (shown as CompressRules.OptionsAnswer.Shown).options.size)
        // An older device: it needs an update.
        assertEquals(CompressRules.OptionsAnswer.NeedsUpdate, CompressRules.optionsAnswer(refused("unknown_payload", "unknown payload")))
        // A refusal in the device's words (more than 500 files).
        assertEquals(
            CompressRules.OptionsAnswer.Failed(UiText.Raw("pick at most 500 files at a time")),
            CompressRules.optionsAnswer(refused("too_many", "pick at most 500 files at a time")),
        )
        assertEquals(500, CompressRules.MAX_ITEMS)
        val picked = (1..600).map { CompressItem.newBuilder().setPath("/p/$it.jpg").setHash("h$it").build() }
        assertEquals(500, CompressRules.items(picked).size)
        assertEquals("/p/1.jpg", CompressRules.items(picked + picked).first().path)
        // Nothing usable: no answer, the bridge's (its words gone), an answer of another kind.
        val ours = CompressRules.OptionsAnswer.Failed(S.appFilesCompressOptionsFailed())
        assertEquals(ours, CompressRules.optionsAnswer(null))
        assertEquals(ours, CompressRules.optionsAnswer(RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setCode("device_unreachable")).build()))
        assertEquals(ours, CompressRules.optionsAnswer(RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(true)).build()))
    }

    /** C12 */
    @Test fun compressFilesAnswer() {
        val failed = UiText.Raw("ours")
        val job = jobOf(total = 3)
        assertEquals(CompressRules.JobAnswer.Started(job), CompressRules.jobAnswer(jobAnswer(job), failed))
        assertEquals(CompressRules.JobAnswer.NeedsUpdate, CompressRules.jobAnswer(refused("unknown_payload", "x"), failed))
        // The demo's refusal, in its words.
        assertEquals(CompressRules.JobAnswer.Refused("demo", UiText.Raw("changes are turned off")), CompressRules.jobAnswer(refused("demo", "changes are turned off"), failed))
        assertEquals(CompressRules.JobAnswer.Refused("", failed), CompressRules.jobAnswer(null, failed))
    }

    /** C13 */
    @Test fun theRequests() {
        val items = listOf(CompressItem.newBuilder().setPath("/a.jpg").setHash("h1").build(), CompressItem.newBuilder().setPath("/b.mov").build())
        val g = CompressRules.request(items)
        assertEquals(listOf("/a.jpg" to "h1", "/b.mov" to ""), g.itemsList.map { it.path to it.hash })
        val tasks = CompressRules.tasks(listOf(png), CompressRules.VIDEO_NONE, allOn)
        val c = CompressRules.compressRequest(tasks)
        assertEquals(listOf(Triple("/p/shot.png", "h:/p/shot.png", CompressRules.PNG_TO_JPEG)), c.tasksList.map { Triple(it.path, it.hash, it.preset) })
        assertEquals("j1", CompressRules.jobRequest("j1").id)
    }

    // ---- The job ----

    /** J1 */
    @Test fun getCompressJobsAnswer() {
        val job = jobOf(total = 1, done = 1, saved = 5, finished = true)
        assertEquals(CompressRules.PollAnswer.Shown(job), CompressRules.pollAnswer(jobAnswer(job), "j1"))
        // not_found, or another job's: gone.
        assertEquals(CompressRules.PollAnswer.Gone, CompressRules.pollAnswer(refused("not_found", "there is no compress job"), "j1"))
        assertEquals(CompressRules.PollAnswer.Gone, CompressRules.pollAnswer(jobAnswer(job.toBuilder().setId("j2").build()), "j1"))
        // No answer: asked again later.
        assertEquals(CompressRules.PollAnswer.Failed, CompressRules.pollAnswer(null, "j1"))
        assertEquals(CompressRules.PollAnswer.Failed, CompressRules.pollAnswer(refused("", ""), "j1"))
    }

    /** J2 (the rule; the model follows it below) */
    @Test fun howOftenTheJobIsAskedAbout() {
        assertEquals(2_000L, CompressRules.pollDelay(shown = true, followed = true, over = false))
        assertEquals(10_000L, CompressRules.pollDelay(shown = false, followed = true, over = false))
        assertNull(CompressRules.pollDelay(shown = true, followed = true, over = true))
        assertNull(CompressRules.pollDelay(shown = false, followed = false, over = false))
    }

    /** J3 */
    @Test fun theJobsProgress() {
        val running = jobOf(total = 4, done = 1, results = listOf(
            result("/a", "done"), result("/b", "running", percent = 50), result("/c", "waiting"), result("/d", "waiting"),
        ))
        assertEquals(1.5f / 4, CompressRules.progress(running), 1e-6f)
        // Never past the whole, nor a percent outside 0-100.
        assertEquals(1f, CompressRules.progress(jobOf(total = 3, done = 3, results = listOf(result("/x", "running", percent = 400)))), 1e-6f)
        assertEquals(0f, CompressRules.progress(jobOf(total = 0)), 1e-6f)
    }

    /** J4 */
    @Test fun whatTheJobCameTo() {
        val job = jobOf(saved = 3000, results = listOf(
            result("/p/a.jpg", "done", file = file("/p/a.jpg", "n1")),
            result("/p/b.png", "done", file = file("/p/b.jpg", "n2")),
            result("/p/c.jpg", "skipped", message = "compressed, it came out no smaller, so the file was left as it was"),
            result("/v/d.mov", "failed", message = "the device could not compress this file"),
            result("/v/e.mov", "waiting"),
        ))
        assertEquals(2, CompressRules.done(job).size)
        assertEquals(3000L, job.saved)
        assertEquals(listOf("c.jpg", "d.mov"), CompressRules.notDone(job).map { CompressRules.name(it.path) })
        assertEquals(
            listOf("compressed, it came out no smaller, so the file was left as it was", "the device could not compress this file"),
            CompressRules.notDone(job).map { it.message },
        )
        // Gone mid-job: the tasks not over weren't compressed either.
        assertEquals(listOf("c.jpg", "d.mov", "e.mov"), CompressRules.notDone(job, gone = true).map { CompressRules.name(it.path) })
        assertTrue(CompressRules.over(jobOf(), gone = true))
        assertFalse(CompressRules.over(jobOf(), gone = false))
        assertEquals("3 files couldn't be compressed", S.appFilesCompressFailed(3).english())
    }

    /** J5 */
    @Test fun eachDoneResultPutInPlaceOnce() {
        val job = jobOf(results = listOf(result("/p/a.jpg", "done", file = file("/p/a.jpg", "n1")), result("/p/b.jpg", "running"), result("/p/c.jpg", "done")))
        val first = CompressRules.newlyDone(job, emptySet())
        assertEquals(listOf("/p/a.jpg"), first.map { it.path })
        assertTrue(CompressRules.newlyDone(job, first.map { CompressRules.resultKey(it) }.toSet()).isEmpty())
    }

    /** J6 */
    @Test fun theFileShownAfter() {
        val shown = file("/p/b.png", "old", mime = "image/png", copies = 3, created = 0, size = 6000)
        val now = CompressRules.fileAfter(shown, result("/p/b.png", "done", file = file("/p/b.jpg", "new", size = 1000, originalHash = "old", removed = true)))
        assertEquals("/p/b.jpg", now.path)
        assertEquals("new", now.hash)
        assertEquals(1000L, now.size64)
        assertEquals("old", now.originalHash)
        assertTrue(now.originalRemoved)
        assertEquals(3, now.copies)
        assertTrue(now.hasCreated())
        // The list's row the same.
        val rows = LargestRules.compressed(listOf(file("/a.jpg", "a"), shown), listOf(result("/p/b.png", "done", file = file("/p/b.jpg", "new"))))
        assertEquals(listOf("/a.jpg" to "a", "/p/b.jpg" to "new"), rows.map { it.path to it.hash })
        assertEquals(3, rows[1].copies)
        // A result that isn't done changes nothing.
        assertEquals(rows, LargestRules.compressed(rows, listOf(result("/a.jpg", "failed", file = file("/a.jpg", "x")))))
    }

    // ---- Largest files ----

    /** L1 */
    @Test fun listLargestFilesPayload() {
        assertEquals("", LargestRules.request("").after)
        assertEquals(50, LargestRules.request("").limit)
        assertEquals("123:abc", LargestRules.request("123:abc").after)
    }

    /** L2 */
    @Test fun itsAnswer() {
        val a = LargestRules.answer(page(listOf(file("/a", "h")), "9:h")) as LargestRules.Answer.Page
        assertEquals("9:h", a.token)
        assertEquals(listOf("/a"), a.files.map { it.path })
        assertEquals(LargestRules.Answer.NeedsUpdate, LargestRules.answer(refused("unknown_payload", "x")))
        // A failure, in the device's words when it said why.
        assertEquals(LargestRules.Answer.Failed(UiText.Raw("database is down")), LargestRules.answer(refused("", "database is down")))
        assertEquals(LargestRules.Answer.Failed(S.appFilesLargestFailed()), LargestRules.answer(null))
    }

    /** L3 */
    @Test fun aPageAddedUnderTheRows() {
        assertEquals(listOf("/a", "/b", "/c"), LargestRules.merged(listOf(file("/a", "1")), listOf(file("/b", "2"), file("/c", "3"))).map { it.path })
        // No path shown twice, nor content shown at another path - nor twice in the page itself.
        val out = LargestRules.merged(listOf(file("/a", "1"), file("/b", "2")), listOf(file("/b", "2"), file("/z", "1"), file("/c", "3"), file("/c", "4")))
        assertEquals(listOf("/a#1", "/b#2", "/c#3"), out.map { "${it.path}#${it.hash}" })
        // Folders never come.
        assertEquals(listOf("/a"), LargestRules.merged(emptyList(), listOf(file("/a", "1"), file("/d", "", mime = "inode/directory"))).map { it.path })
    }

    /** L4 */
    @Test fun whatTheScreenShows() {
        val st = LargestFilesModel.State()
        val rows = listOf(file("/a", "1"))
        val failed = UiText.Raw("x")
        assertEquals(LargestBody.LOADING, st.copy(loading = true).body)
        assertEquals(LargestBody.NEEDS_UPDATE, st.copy(needsUpdate = true).body)
        assertEquals(LargestBody.NEEDS_UPDATE, st.copy(needsUpdate = true, rows = rows, loaded = true).body)
        assertEquals(LargestBody.FAILED, st.copy(problem = failed).body)
        assertEquals(LargestBody.EMPTY, st.copy(loaded = true, end = true).body)
        // Rows, with at their foot a spinner, a failure or nothing.
        val shown = st.copy(rows = rows, loaded = true)
        assertEquals(LargestBody.ROWS, shown.body)
        assertEquals(LargestFoot.LOADING, shown.copy(loading = true).foot)
        assertEquals(LargestFoot.FAILED, shown.copy(problem = failed).foot)
        assertEquals(LargestFoot.NONE, shown.foot)
        assertEquals(LargestFoot.NONE, shown.copy(end = true).foot)
        // Trying again: "Trying…".
        assertTrue(shown.copy(problem = failed, loading = true).retrying)
    }

    /** L5 */
    @Test fun theNextPage() {
        val more = LargestFilesModel.State(rows = listOf(file("/a", "1")), loaded = true)
        assertTrue(more.wantsMore)
        assertFalse(more.copy(loading = true).wantsMore)
        assertFalse(more.copy(end = true).wantsMore)
        assertFalse(more.copy(needsUpdate = true).wantsMore)
        assertFalse(more.copy(problem = UiText.Raw("x")).wantsMore)
        assertFalse(LargestFilesModel.State().wantsMore)
    }

    /** L6 */
    @Test fun inNPlaces() {
        assertEquals("in 3 places", LargestRules.places(file("/a", "1", copies = 3))?.english())
        assertNull(LargestRules.places(file("/a", "1", copies = 1)))
        assertNull(LargestRules.places(file("/a", "1")))
    }

    /** L7 */
    @Test fun photosAndVideos() {
        assertTrue(LargestRules.isMedia(file("/a", "1", mime = "image/png")))
        assertTrue(LargestRules.isMedia(file("/a", "1", mime = "video/quicktime")))
        assertFalse(LargestRules.isMedia(file("/a.pdf", "1", mime = "application/pdf")))
        // A HEIC or HEIF by its name, whatever its type.
        assertTrue(LargestRules.isMedia(file("/IMG_1.HEIC", "1", mime = "application/octet-stream")))
        assertTrue(LargestRules.isMedia(file("/x.heif", "1", mime = "")))
    }

    /** L8 */
    @Test fun compressInTheSelectionBar() {
        val rows = listOf(file("/a.pdf", "1", mime = "application/pdf"), file("/b.mov", "2", mime = "video/quicktime"), file("/c.jpg", "3"))
        assertTrue(LargestRules.canCompress(rows, setOf("/a.pdf", "/b.mov")))
        assertFalse(LargestRules.canCompress(rows, setOf("/a.pdf")))
        assertFalse(LargestRules.canCompress(rows, emptySet()))
        // What it asks about: the photos and videos picked, in the list's order, with their content.
        assertEquals(listOf("/b.mov" to "2", "/c.jpg" to "3"), LargestRules.compressItems(rows, setOf("/c.jpg", "/a.pdf", "/b.mov")).map { it.path to it.hash })
    }

    /** L9 */
    @Test fun theListAfterAJob() {
        assertTrue(LargestRules.rereadsAfter(jobOf(finished = true, results = listOf(result("/a", "done", file = file("/a", "n"))))))
        assertFalse(LargestRules.rereadsAfter(jobOf(finished = true, results = listOf(result("/a", "failed")))))
    }

    /** L10 */
    @Test fun aRowsFolder() {
        assertEquals("/Photos/2024", LargestRules.folder("/Photos/2024/IMG_1.jpg"))
        assertEquals("/", LargestRules.folder("/notes.txt"))
        assertEquals("IMG_1.jpg", CompressRules.name("/Photos/2024/IMG_1.jpg"))
    }

    // ---- The viewer ----

    private fun item(originalHash: String = "", removed: Boolean = false) =
        PhotoGalleryViewModel.Item("i", "/p/a.jpg", "image/jpeg", 1, null, hash = "h", originalHash = originalHash, originalRemoved = removed)

    /** R1 */
    @Test fun noRevertForACompressedFile() {
        assertTrue(PhotoEdit.revertable(item(originalHash = "abc")))
        assertFalse(PhotoEdit.revertable(item(originalHash = "abc", removed = true)))
        assertFalse(PhotoEdit.revertable(item(removed = true)))
        // Edit stays: the device keeps the flag through a later edit.
        assertTrue(PhotoEdit.editable(item(originalHash = "abc", removed = true)))
    }

    /** R2 */
    @Test fun infosNote() {
        assertTrue(PhotoEdit.compressedNote(item(removed = true)))
        assertFalse(PhotoEdit.compressedNote(item()))
        assertFalse(PhotoEdit.compressedNote(null))
    }

    // ---- The models, on a pretend device ----

    private val executor = Executors.newSingleThreadExecutor()
    private val dispatcher = executor.asCoroutineDispatcher()
    private val scope = CoroutineScope(SupervisorJob() + dispatcher)
    @get:Rule val tmp = TemporaryFolder()
    private val thumbs by lazy { TestThumbStore(tmp.newFolder()) }

    @After fun tearDown() {
        scope.cancel()
        executor.shutdownNow()
    }

    private fun <T> on(block: suspend () -> T): T = runBlocking(dispatcher) { block() }

    private fun await(what: String, cond: () -> Boolean) {
        val until = System.nanoTime() + 5_000_000_000L
        while (!cond()) {
            if (System.nanoTime() > until) throw AssertionError("timed out waiting for $what")
            Thread.sleep(2)
        }
    }

    /** A pretend device: [answer] per request (null: no answer), every request kept; ListLargestFiles waits for [hold]. */
    private class Device(@Volatile var answer: (ReqEnvelope) -> RespEnvelope?) {
        val sent = CopyOnWriteArrayList<ReqEnvelope>()
        @Volatile var hold: CompletableDeferred<Unit>? = null
        val send: GalleryRequest = { _, _, build ->
            val req = ReqEnvelope.newBuilder().also(build).build()
            sent += req
            if (req.payloadCase == ReqEnvelope.PayloadCase.REQ_LIST_LARGEST_FILES) hold?.await()
            answer(req) ?: throw IOException("no answer")
        }
        fun of(case: ReqEnvelope.PayloadCase) = sent.filter { it.payloadCase == case }
    }

    private val optionsAnswer = RespEnvelope.newBuilder().setRespCompressOptions(CompressOptions.newBuilder().addAllItems(listOf(fourKHevc, png, pdf))).build()
    private val picked = listOf(fourKHevc, png, pdf).map { CompressItem.newBuilder().setPath(it.path).setHash(it.hash).build() }

    /** C2, C3, C12, J2, J5: the dialog's model - its choices to begin with, Compress, the job followed and its files handed on once. */
    @Test fun theDialogsModel() {
        val running = jobOf(total = 2, results = listOf(result(fourKHevc.path, "running", percent = 10), result(png.path, "waiting")))
        val half = jobOf(total = 2, done = 1, saved = 5 * mb, results = listOf(result(fourKHevc.path, "running", percent = 60), result(png.path, "done", file = file("/p/shot.jpg", "n"))))
        val over = half.toBuilder().setDone(2).setFinished(true).setResults(0, result(fourKHevc.path, "failed", message = "no")).build()
        val polls = Channel<CompressJob>(Channel.UNLIMITED)
        val dev = Device { req ->
            when (req.payloadCase) {
                ReqEnvelope.PayloadCase.REQ_GET_COMPRESS_OPTIONS -> optionsAnswer
                ReqEnvelope.PayloadCase.REQ_COMPRESS_FILES -> jobAnswer(running)
                ReqEnvelope.PayloadCase.REQ_GET_COMPRESS_JOB -> polls.tryReceive().getOrNull()?.let { jobAnswer(it) }
                else -> null
            }
        }
        val waits = CopyOnWriteArrayList<Long>()
        val ticks = Channel<Unit>(Channel.UNLIMITED)
        val m = CompressModel(dev.send, tick = { waits += it; ticks.receive() }, scope = scope)
        on { m.follow(); m.open(picked) }
        await("the options") { m.state.value.phase == CompressModel.Phase.OPTIONS }
        val st = m.state.value
        // C2, C3: 1080p for the 4K video, every photo switch on; the PDF not offered.
        assertEquals(CompressRules.VIDEO_1080P, st.video)
        assertEquals(setOf(CompressRules.PNG_TO_JPEG), st.photosOn)
        assertEquals(listOf(fourKHevc.path to CompressRules.VIDEO_1080P, png.path to CompressRules.PNG_TO_JPEG), st.tasks.map { it.path to it.preset })
        assertEquals(3, st.asked.size)
        var sent = 0
        on { m.compress { sent++ } }
        await("the job") { m.state.value.phase == CompressModel.Phase.JOB }
        assertEquals(1, sent)
        assertTrue(m.state.value.videoSent)
        assertEquals(st.tasks, dev.of(ReqEnvelope.PayloadCase.REQ_COMPRESS_FILES).single().reqCompressFiles.tasksList)
        // J2: every 2 s while the dialog shows it.
        await("the first wait") { waits.size == 1 }
        assertEquals(2_000L, waits[0])
        polls.trySend(half)
        ticks.trySend(Unit)
        await("the half") { m.state.value.job?.done == 1 }
        // J5: the PNG, now a JPEG, handed to the screen once.
        assertEquals(listOf("/p/shot.jpg"), m.state.value.landed.map { it.file.path })
        on { m.taken(m.state.value.landed) }
        // Closed: the job goes on, followed every 10 s by the screen in front.
        on { m.close() }
        assertFalse(m.state.value.shown)
        assertEquals("j1", m.state.value.job?.id)
        await("a slower wait") { waits.lastOrNull() == 10_000L }
        polls.trySend(half)
        ticks.trySend(Unit)
        await("asked again") { dev.of(ReqEnvelope.PayloadCase.REQ_GET_COMPRESS_JOB).size == 2 }
        // The same file again: not handed on twice.
        await("the next wait") { waits.size == 4 }
        assertEquals(listOf(2_000L, 2_000L, 10_000L, 10_000L), waits.toList())
        assertTrue(m.state.value.landed.isEmpty())
        polls.trySend(over)
        ticks.trySend(Unit)
        await("over") { m.state.value.over }
        // Over: nothing more is asked.
        Thread.sleep(50)
        assertEquals(3, dev.of(ReqEnvelope.PayloadCase.REQ_GET_COMPRESS_JOB).size)
        assertEquals(4, waits.size)
    }

    /** C12, J2: a refusal keeps the choices; nothing is asked about a job with the screen gone. */
    @Test fun aRefusalKeepsTheChoicesAndNothingLooksNothingIsAsked() {
        var refuse = true
        val dev = Device { req ->
            when (req.payloadCase) {
                ReqEnvelope.PayloadCase.REQ_GET_COMPRESS_OPTIONS -> optionsAnswer
                ReqEnvelope.PayloadCase.REQ_COMPRESS_FILES -> if (refuse) refused("demo", "This is a demo device: changes aren't kept.") else jobAnswer(jobOf(total = 2))
                else -> null
            }
        }
        val waits = CopyOnWriteArrayList<Long>()
        val m = CompressModel(dev.send, tick = { waits += it; CompletableDeferred<Unit>().await() }, scope = scope)
        on { m.open(picked) }
        await("the options") { m.state.value.phase == CompressModel.Phase.OPTIONS }
        var sent = 0
        on { m.compress { sent++ } }
        await("the refusal") { m.state.value.refusal != null }
        assertEquals(UiText.Raw("This is a demo device: changes aren't kept."), m.state.value.refusal)
        assertEquals(CompressModel.Phase.OPTIONS, m.state.value.phase)
        assertTrue(m.state.value.canCompress)
        assertEquals(0, sent)
        // Taken, with no screen following it: no wait, no ask.
        refuse = false
        on { m.compress { sent++ } }
        await("the job") { m.state.value.phase == CompressModel.Phase.JOB }
        Thread.sleep(50)
        assertTrue(waits.isEmpty())
        assertTrue(dev.of(ReqEnvelope.PayloadCase.REQ_GET_COMPRESS_JOB).isEmpty())
    }

    /** C11: an older device, and options that didn't come (Try again). */
    @Test fun anOlderDeviceAndTryAgain() {
        var answer: RespEnvelope? = refused("unknown_payload", "unknown payload")
        val dev = Device { req -> if (req.payloadCase == ReqEnvelope.PayloadCase.REQ_GET_COMPRESS_OPTIONS) answer else null }
        val m = CompressModel(dev.send, scope = scope)
        on { m.open(picked) }
        await("needs an update") { m.state.value.phase == CompressModel.Phase.NEEDS_UPDATE }
        on { m.close(); answer = null; m.open(picked) }
        await("failed") { m.state.value.phase == CompressModel.Phase.FAILED }
        assertEquals(S.appFilesCompressOptionsFailed(), m.state.value.failure)
        answer = optionsAnswer
        on { m.retry() }
        await("the options") { m.state.value.phase == CompressModel.Phase.OPTIONS }
    }

    /** C12: no answer to CompressFiles - the job is looked for, and taken if it holds the tasks. */
    @Test fun noAnswerLooksForTheJob() {
        val job = jobOf(total = 2, results = listOf(result(fourKHevc.path, "running"), result(png.path, "waiting")))
        var holds = false
        val dev = Device { req ->
            when (req.payloadCase) {
                ReqEnvelope.PayloadCase.REQ_GET_COMPRESS_OPTIONS -> optionsAnswer
                ReqEnvelope.PayloadCase.REQ_GET_COMPRESS_JOB -> jobAnswer(if (holds) job else jobOf(id = "old", results = listOf(result("/else", "done"))))
                else -> null
            }
        }
        val m = CompressModel(dev.send, tick = { CompletableDeferred<Unit>().await() }, scope = scope)
        on { m.open(picked) }
        await("the options") { m.state.value.phase == CompressModel.Phase.OPTIONS }
        on { m.compress() }
        await("not compressed") { m.state.value.refusal != null }
        assertEquals("2 files couldn't be compressed", m.state.value.refusal?.english())
        holds = true
        on { m.compress() }
        await("the job after all") { m.state.value.phase == CompressModel.Phase.JOB }
        assertEquals("j1", m.state.value.job?.id)
    }

    private fun largest(dev: Device) = LargestFilesModel(dev.send, pause = { }, thumbs = thumbs, scope = scope)

    // The device's library, largest first, in pages as ListLargestFiles cuts them.
    private fun library(n: Int) = (1..n).map { file("/f/$it.jpg", "h$it", size = (1000L - it) * mb) }
    private fun pages(lib: List<File>): (ReqEnvelope) -> RespEnvelope? = { req ->
        when (req.payloadCase) {
            ReqEnvelope.PayloadCase.REQ_LIST_LARGEST_FILES -> {
                val r = req.reqListLargestFiles
                val from = if (r.after.isEmpty()) 0 else lib.indexOfFirst { it.hash == r.after } + 1
                val to = minOf(lib.size, from + (if (r.limit == 0) 50 else r.limit))
                page(lib.subList(from, to), if (to < lib.size) lib[to - 1].hash else "")
            }
            ReqEnvelope.PayloadCase.REQ_GET_THUMBNAILS -> page(emptyList(), "")
            else -> null
        }
    }

    /** L1, L4, L5: the pages - the first as the screen shows, the next as its end nears, none past the last. */
    @Test fun thePagesAsTheListScrolls() {
        val lib = library(120)
        val dev = Device(pages(lib))
        val m = largest(dev)
        assertEquals(LargestBody.LOADING, m.state.value.body)
        on { m.appeared() }
        await("the first page") { m.state.value.rows.size == 50 }
        assertEquals(LargestBody.ROWS, m.state.value.body)
        assertEquals(listOf("" to 50), dev.of(ReqEnvelope.PayloadCase.REQ_LIST_LARGEST_FILES).map { it.reqListLargestFiles.after to it.reqListLargestFiles.limit })
        // Shown again (a rotation): not asked again.
        on { m.appeared() }
        on { m.nearEnd() }
        await("the second page") { m.state.value.rows.size == 100 }
        on { m.nearEnd() }
        await("the last page") { m.state.value.end }
        assertEquals(lib.map { it.path }, m.state.value.rows.map { it.path })
        on { m.nearEnd() }
        Thread.sleep(30)
        assertEquals(listOf("", "h50", "h100"), dev.of(ReqEnvelope.PayloadCase.REQ_LIST_LARGEST_FILES).map { it.reqListLargestFiles.after })
        // Left: forgotten, and the next opening starts from the top.
        on { m.left() }
        assertTrue(m.state.value.rows.isEmpty())
    }

    /** L2, L4: a page that fails is asked again by itself; Try again at once; an older device; nothing at all. */
    @Test fun aFailedPageAndTheOtherStates() {
        var fail = true
        val lib = library(60)
        val ok = pages(lib)
        val waits = Channel<Unit>(Channel.UNLIMITED)
        val dev = Device { req -> if (fail && req.payloadCase == ReqEnvelope.PayloadCase.REQ_LIST_LARGEST_FILES) refused("", "database is down") else ok(req) }
        val m = LargestFilesModel(dev.send, pause = { waits.receive() }, thumbs = thumbs, scope = scope)
        on { m.appeared() }
        await("the failure") { m.state.value.body == LargestBody.FAILED }
        assertEquals(UiText.Raw("database is down"), m.state.value.problem)
        fail = false
        // Its wait over: asked again by itself.
        waits.trySend(Unit)
        await("the page") { m.state.value.rows.size == 50 }
        assertNull(m.state.value.problem)
        // A later page fails: the rows stay, the foot says so; Try again asks at once.
        fail = true
        on { m.nearEnd() }
        await("the foot") { m.state.value.foot == LargestFoot.FAILED }
        assertEquals(50, m.state.value.rows.size)
        fail = false
        on { m.retry() }
        await("the rest") { m.state.value.end }
        assertEquals(60, m.state.value.rows.size)
        // An older device; a device with no files.
        val old = largest(Device { refused("unknown_payload", "x") })
        on { old.appeared() }
        await("needs an update") { old.state.value.body == LargestBody.NEEDS_UPDATE }
        val none = largest(Device(pages(emptyList())))
        on { none.appeared() }
        await("empty") { none.state.value.body == LargestBody.EMPTY }
    }

    /** J6, L9, R1: a compress shows in the rows at once, and the list is read again in place for its order. */
    @Test fun aCompressShowsInTheRowsAndTheListIsReadAgain() {
        val lib = library(70).toMutableList()
        val dev = Device { pages(lib.toList())(it) }
        val m = largest(dev)
        on { m.appeared() }
        await("the first page") { m.state.value.rows.size == 50 }
        on { m.nearEnd() }
        await("the rest") { m.state.value.end }
        on { m.pick("/f/2.jpg"); m.pick("/f/5.jpg") }
        assertEquals(listOf("/f/2.jpg", "/f/5.jpg"), m.compressItems().map { it.path })
        // The second file compressed: smaller, and later in the list.
        val small = file("/f/2.jpg", "n2", size = 10 * mb, originalHash = "h2", removed = true)
        lib.removeAt(1)
        lib.add(small)
        on { m.compressed(listOf(result("/f/2.jpg", "done", file = small))) }
        assertEquals("n2", m.state.value.rows[1].hash)
        assertTrue(m.state.value.rows[1].originalRemoved)
        // R1: the viewer opened from the list offers no Revert for it.
        assertFalse(PhotoEdit.revertable(m.viewerItems()[1]))
        // L9: over, the list is read again as far as it was shown, in the device's biggest pages.
        val before = dev.of(ReqEnvelope.PayloadCase.REQ_LIST_LARGEST_FILES).size
        on { m.compressed(emptyList(), over = true) }
        await("read again") { m.state.value.rows.lastOrNull()?.hash == "n2" }
        val again = dev.of(ReqEnvelope.PayloadCase.REQ_LIST_LARGEST_FILES).drop(before).map { it.reqListLargestFiles }
        assertEquals(listOf("" to 70), again.map { it.after to it.limit })
        assertEquals(70, m.state.value.rows.size)
        assertEquals(setOf("/f/2.jpg", "/f/5.jpg"), m.state.value.selected)
    }

    /** The viewer's side: what it deleted leaves the list at once, and the list is read again. */
    @Test fun whatTheViewerDeletedLeavesTheList() {
        val lib = library(5).toMutableList()
        val dev = Device { pages(lib.toList())(it) }
        val m = largest(dev)
        on { m.appeared() }
        await("the page") { m.state.value.end }
        val items = m.viewerItems()
        assertEquals(5, items.size)
        // The viewer deleted the third one.
        lib.removeAt(2)
        val held = CompletableDeferred<Unit>()
        dev.hold = held
        on { m.viewerChanged(items.filterIndexed { i, _ -> i != 2 }) }
        // At once, before the list is read again.
        assertEquals(listOf("/f/1.jpg", "/f/2.jpg", "/f/4.jpg", "/f/5.jpg"), m.state.value.rows.map { it.path })
        held.complete(Unit)
        await("read again") { !m.state.value.loading }
        assertEquals(listOf("/f/1.jpg", "/f/2.jpg", "/f/4.jpg", "/f/5.jpg"), m.state.value.rows.map { it.path })
    }

    /** J5, J6 in Images: each compressed file takes its tile's place - a PNG now a JPEG - once. */
    @Test fun imagesPutsWhatWasCompressedInPlace() {
        val t = Timestamp.newBuilder().setSeconds(1_760_000_000).build()
        val a = file("/Camera/a.png", "ha", mime = "image/png", copies = 2).toBuilder().setCreated(t).build()
        val b = file("/Camera/b.jpg", "hb").toBuilder().setCreated(t).build()
        val dev = Device { req ->
            when (req.payloadCase) {
                ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS -> page(listOf(a, b), "")
                ReqEnvelope.PayloadCase.REQ_PHOTO_DATE_BUCKETS -> RespEnvelope.newBuilder().setRespPhotoDateBuckets(RespPhotoDateBuckets.getDefaultInstance()).build()
                ReqEnvelope.PayloadCase.REQ_GET_THUMBNAILS -> page(emptyList(), "")
                else -> null
            }
        }
        val vm = PhotoGalleryViewModel(
            "test", send = dev.send, pause = {}, online = { true }, problemOf = { _, _ -> LoadProblem.FAILED },
            thumbs = thumbs, fullPhotos = thumbs, scope = scope, signIns = { 1 },
        )
        scope.launch { vm.resetAndLoadFirstPage() }
        await("the photos") { vm.state.value.items.size == 2 && !vm.state.value.loading }
        val now = file("/Camera/a.jpg", "na", originalHash = "ha", removed = true)
        val r = result("/Camera/a.png", "done", file = now)
        on { vm.compressed(listOf(r)) }
        val shown = vm.state.value.items[0]
        assertEquals("/Camera/a.jpg", shown.path)
        assertEquals("na", shown.hash)
        assertTrue(shown.originalRemoved)
        assertFalse(PhotoEdit.revertable(shown))
        // As many copies as before, its month kept.
        assertEquals(2, shown.copies)
        assertEquals(vm.state.value.items[1].month, shown.month)
        // Again: nothing changes.
        on { vm.compressed(listOf(r)) }
        assertEquals(listOf("/Camera/a.jpg", "/Camera/b.jpg"), vm.state.value.items.map { it.path })
    }
}
