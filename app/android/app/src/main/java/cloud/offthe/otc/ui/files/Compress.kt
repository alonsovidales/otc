// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.files

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appFilesCompressFailed
import cloud.offthe.otc.i18n.appFilesCompressOptionsFailed
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.proto.CompressChoice
import cloud.offthe.otc.proto.CompressFiles
import cloud.offthe.otc.proto.CompressItem
import cloud.offthe.otc.proto.CompressJob
import cloud.offthe.otc.proto.CompressOption
import cloud.offthe.otc.proto.CompressResult
import cloud.offthe.otc.proto.CompressTask
import cloud.offthe.otc.proto.GetCompressJob
import cloud.offthe.otc.proto.GetCompressOptions
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.ui.common.DEVICE_UNREACHABLE
import cloud.offthe.otc.ui.gallery.GalleryRequest
import cloud.offthe.otc.ui.gallery.isUnknownPayload
import cloud.offthe.otc.ui.settings.deviceOr
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import cloud.offthe.otc.proto.File as PbFile

// Compress (owner, 2026-11-08): photos and videos picked in Images or in
// Settings › Largest files made smaller on the device - a 4K video
// re-encoded at 1080p or 720p, an H.264 one as HEVC at its own size, a
// photo over 13 MP shrunk to about 12, a big JPEG saved again at a lower
// quality, a PNG with no transparency saved as JPEG - and the originals'
// bytes then deleted there to free the space (File.original_removed: no
// Revert to Original). The device first says what it can do for each item
// and what each would weigh, from the files' headers (GetCompressOptions);
// the dialog (CompressDialog.kt) offers the choices, sums them up and sends
// the tasks (CompressFiles), and the device works through them in the
// background (GetCompressJob, Settings' Background work). The rules here
// are pure, for the JVM tests (CompressTest, cases C1-C13 and J1-J6 as the
// web's compress-test.mjs and iOS's CompressTests.swift).

/**
 * The Compress dialog's rules: what is asked, which choices are offered
 * and which one each item gets, what they come to, and what the device's
 * answers mean.
 */
object CompressRules {
    /** The most items one GetCompressOptions takes: past it the device answers "too_many". */
    const val MAX_ITEMS = 500

    const val VIDEO_1080P = "video_1080p"
    const val VIDEO_720P = "video_720p"
    const val VIDEO_HEVC = "video_hevc"
    /** Not a preset: the videos' "Leave as they are". */
    const val VIDEO_NONE = "video_none"
    const val PHOTO_12MP = "photo_12mp"
    const val JPEG_RESAVE = "jpeg_resave"
    const val PNG_TO_JPEG = "png_to_jpeg"

    /** The videos' presets, in the order the dialog lists them ("Leave as they are" after them). */
    val VIDEOS = listOf(VIDEO_1080P, VIDEO_720P, VIDEO_HEVC)

    /** The photos' presets, in the order their switches are listed - and the order an item takes them in. */
    val PHOTOS = listOf(PHOTO_12MP, JPEG_RESAVE, PNG_TO_JPEG)

    // A CompressResult's states (the device's words for them come in its message).
    const val DONE = "done"
    const val SKIPPED = "skipped"
    const val FAILED = "failed"

    /** How often the job is asked about while the dialog shows it (J2)... */
    const val POLL_SHOWN_MS = 2_000L

    /** ...and while only the screen behind it follows it (Images, the list). */
    const val POLL_FOLLOWED_MS = 10_000L

    /** GetCompressJob's error_code when the device has no such job. */
    const val NOT_FOUND = "not_found"

    /**
     * What is asked about for [picked] (path and the content shown): each
     * path once, in the order picked, the first [MAX_ITEMS] of them - the
     * dialog's title counts these.
     */
    fun items(picked: List<CompressItem>): List<CompressItem> = picked.distinctBy { it.path }.take(MAX_ITEMS)

    /** C13: GetCompressOptions for [items]: each one's path and the content shown. */
    fun request(items: List<CompressItem>): GetCompressOptions = GetCompressOptions.newBuilder().addAllItems(items).build()

    /** C13: CompressFiles with [tasks]. */
    fun compressRequest(tasks: List<CompressTask>): CompressFiles = CompressFiles.newBuilder().addAllTasks(tasks).build()

    /** C13: GetCompressJob for the job [id] ("" for the one running, or the last). */
    fun jobRequest(id: String): GetCompressJob = GetCompressJob.newBuilder().setId(id).build()

    /** [o]'s choices this app knows, strongest first as the device sent them: a preset added later is left out (C4). */
    fun choices(o: CompressOption): List<CompressChoice> = o.choicesList.filter { it.preset in VIDEOS || it.preset in PHOTOS }

    /** [o]'s choice for [preset], null when it isn't offered. */
    fun offered(o: CompressOption, preset: String): CompressChoice? = o.choicesList.firstOrNull { it.preset == preset }

    /** Whether [o] is a video (it is offered a video's preset this app knows), else a photo. */
    fun isVideo(o: CompressOption): Boolean = o.choicesList.any { it.preset in VIDEOS }

    /**
     * C1: the videos' radio - the video presets any item is offered, in
     * the dialog's order, then Leave as they are; nothing (no Videos
     * section) when no item is offered one.
     */
    fun videoChoices(options: List<CompressOption>): List<String> {
        val offered = VIDEOS.filter { p -> options.any { offered(it, p) != null } }
        return if (offered.isEmpty()) emptyList() else offered + VIDEO_NONE
    }

    /**
     * C2: the radio's choice to begin with - Lower to 1080p if any item is
     * offered it (a 4K video), else Same size, HEVC if any is, else Lower
     * to 720p; null with no video preset (the choice is Leave as they are).
     */
    fun defaultVideo(options: List<CompressOption>): String? {
        val offered = videoChoices(options)
        return listOf(VIDEO_1080P, VIDEO_HEVC, VIDEO_720P).firstOrNull { it in offered }
    }

    /** C3: a switch per photos' preset any item is offered, strongest first - each on as the dialog opens. */
    fun photoSwitches(options: List<CompressOption>): List<String> = PHOTOS.filter { p -> options.any { offered(it, p) != null } }

    /**
     * C4: what a video gets with [video] chosen - that preset if it is
     * offered it, else Same size, HEVC if it is offered that (a 1080p
     * video when 1080p is chosen), else nothing; nothing with Leave as
     * they are.
     */
    fun videoFor(o: CompressOption, video: String): CompressChoice? =
        if (video == VIDEO_NONE || !isVideo(o)) null else offered(o, video) ?: offered(o, VIDEO_HEVC)

    /** C4: what a photo gets with the switches [on] - the first of 12 MP, the JPEG's quality, PNG as JPEG it is offered whose switch is on. */
    fun photoFor(o: CompressOption, on: Set<String>): CompressChoice? =
        if (isVideo(o)) null else PHOTOS.firstNotNullOfOrNull { p -> if (p in on) offered(o, p) else null }

    /** C4: what [o] gets with [video] chosen and the photos' switches [on]; null: it is left as it is. */
    fun choiceFor(o: CompressOption, video: String, on: Set<String>): CompressChoice? =
        if (isVideo(o)) videoFor(o, video) else photoFor(o, on)

    /** What a choice comes to: [count] files weighing [size] now, about [estimate] after. */
    data class Sum(val count: Int, val size: Long, val estimate: Long) {
        operator fun plus(c: Pair<CompressOption, CompressChoice>): Sum =
            Sum(count + 1, size + c.first.size, estimate + c.second.estimatedSize)

        companion object {
            val NONE = Sum(0, 0, 0)
        }
    }

    /**
     * C6: the line under a video's choice - everything it would do were it
     * chosen: the videos offered it, and those that would fall back to
     * Same size, HEVC, each at what it would get. The same whichever is
     * chosen now; photos never count.
     */
    fun videoRow(options: List<CompressOption>, preset: String): Sum =
        options.fold(Sum.NONE) { s, o -> videoFor(o, preset)?.let { s + (o to it) } ?: s }

    /**
     * C7: the line under a photo's switch - the photos that would get
     * [preset] were its switch on, the others as they are ([on]): what it
     * does now when on, what it would do when off.
     */
    fun photoRow(options: List<CompressOption>, preset: String, on: Set<String>): Sum =
        options.fold(Sum.NONE) { s, o -> photoFor(o, on + preset)?.takeIf { it.preset == preset }?.let { s + (o to it) } ?: s }

    /**
     * C5: the tasks CompressFiles sends for the choices made - each item
     * that gets a preset, in the order asked, with the path and content the
     * device answered for (the content shown, else it said "changed").
     */
    fun tasks(options: List<CompressOption>, video: String, on: Set<String>): List<CompressTask> = options.mapNotNull { o ->
        choiceFor(o, video, on)?.let { c -> CompressTask.newBuilder().setPath(o.path).setHash(o.hash).setPreset(c.preset).build() }
    }

    /** C8: what the choices would free, about: each task's size less its estimate, never below nothing. */
    fun frees(options: List<CompressOption>, video: String, on: Set<String>): Long =
        options.sumOf { o -> choiceFor(o, video, on)?.let { c -> maxOf(0L, o.size - c.estimatedSize) } ?: 0L }

    /** C9: a video is among [tasks]: the dialog says they take a while. */
    fun slow(tasks: List<CompressTask>): Boolean = tasks.any { it.preset in VIDEOS }

    /** C10: the items nothing is offered for (no preset this app knows), with the device's reason each, in the order asked. */
    fun notOffered(options: List<CompressOption>): List<CompressOption> = options.filter { choices(it).isEmpty() }

    /** C10: nothing can be done for any item (nor for none): the dialog says so, and Compress stays off. */
    fun nothing(options: List<CompressOption>): Boolean = options.none { choices(it).isNotEmpty() }

    /** A file's name, as the not-offered list and the failed results name it. */
    fun name(path: String): String = path.trimEnd('/').substringAfterLast('/')

    /** What a GetCompressOptions brought. */
    sealed interface OptionsAnswer {
        data class Shown(val options: List<CompressOption>) : OptionsAnswer
        /** A device before Compress (unknown_payload). */
        data object NeedsUpdate : OptionsAnswer
        /** No answer, the bridge's "device unreachable", or the device's own refusal ([text]): Try again. */
        data class Failed(val text: UiText) : OptionsAnswer
    }

    /**
     * C11: what a GetCompressOptions answer ([resp]; null: none came)
     * means - the options, an older device, a refusal in the device's words
     * (more than 500 files, its storage not ready...), else options_failed
     * (none, the bridge's, an answer of another kind).
     */
    fun optionsAnswer(resp: RespEnvelope?): OptionsAnswer = when {
        resp == null -> OptionsAnswer.Failed(S.appFilesCompressOptionsFailed())
        resp.payloadCase == RespEnvelope.PayloadCase.RESP_COMPRESS_OPTIONS -> OptionsAnswer.Shown(resp.respCompressOptions.itemsList)
        resp.isUnknownPayload() -> OptionsAnswer.NeedsUpdate
        resp.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && resp.respAck.code == DEVICE_UNREACHABLE -> OptionsAnswer.Failed(S.appFilesCompressOptionsFailed())
        resp.error -> OptionsAnswer.Failed(deviceOr(resp.errorMessage, S.appFilesCompressOptionsFailed()))
        else -> OptionsAnswer.Failed(S.appFilesCompressOptionsFailed())
    }

    /** What a CompressFiles brought. */
    sealed interface JobAnswer {
        /** Taken: the job, at once (tasks sent while one runs join it). */
        data class Started(val job: CompressJob) : JobAnswer
        /** A device before Compress (unknown_payload). */
        data object NeedsUpdate : JobAnswer
        /** Refused - the demo's words, "too_many"... - or no answer ([failed]). */
        data class Refused(val code: String, val text: UiText) : JobAnswer
    }

    /**
     * C12: what a CompressFiles answer ([resp]) means - the job, an older
     * device, or the refusal in the device's words (the demo's among them),
     * else [failed] (no answer, or none in words).
     */
    fun jobAnswer(resp: RespEnvelope?, failed: UiText): JobAnswer = when {
        resp == null -> JobAnswer.Refused("", failed)
        resp.payloadCase == RespEnvelope.PayloadCase.RESP_COMPRESS_JOB -> JobAnswer.Started(resp.respCompressJob)
        resp.isUnknownPayload() -> JobAnswer.NeedsUpdate
        resp.error -> JobAnswer.Refused(resp.errorCode, deviceOr(resp.errorMessage, failed))
        else -> JobAnswer.Refused("", failed)
    }

    /** What a GetCompressJob brought. */
    sealed interface PollAnswer {
        data class Shown(val job: CompressJob) : PollAnswer
        /** The device has no such job (not_found: it restarted), or another job runs now: this one is over. */
        data object Gone : PollAnswer
        /** No answer, or one with no say: asked again at the next round. */
        data object Failed : PollAnswer
    }

    /** J1: what a GetCompressJob answer ([resp]) for the job [id] means. */
    fun pollAnswer(resp: RespEnvelope?, id: String): PollAnswer = when {
        resp == null -> PollAnswer.Failed
        resp.payloadCase == RespEnvelope.PayloadCase.RESP_COMPRESS_JOB ->
            if (id.isNotEmpty() && resp.respCompressJob.id != id) PollAnswer.Gone else PollAnswer.Shown(resp.respCompressJob)
        resp.error && resp.errorCode == NOT_FOUND -> PollAnswer.Gone
        else -> PollAnswer.Failed
    }

    /**
     * J2: how long before the job is asked about again - every 2 s while
     * the dialog shows it ([shown]), every 10 s while only the screen
     * behind follows it ([followed]: Images or the list, on screen), never
     * once it is [over] or nothing looks (null).
     */
    fun pollDelay(shown: Boolean, followed: Boolean, over: Boolean): Long? = when {
        over -> null
        shown -> POLL_SHOWN_MS
        followed -> POLL_FOLLOWED_MS
        else -> null
    }

    /**
     * J3: how far [job] is, 0 to 1 - its tasks over, and the share a
     * running video has done of its own (CompressResult.percent); a total
     * that grew (tasks sent since joined it) is the device's.
     */
    fun progress(job: CompressJob): Float {
        if (job.total <= 0) return 0f
        val running = job.resultsList.firstOrNull { it.state == "running" }?.percent?.coerceIn(0, 100) ?: 0
        return ((job.done + running / 100.0) / job.total).toFloat().coerceIn(0f, 1f)
    }

    /** J4: whether [job] is over - it says so, or it is [gone]. */
    fun over(job: CompressJob, gone: Boolean = false): Boolean = job.finished || gone

    /** J4: the files [job] compressed: their results, each with the file now at its path (CompressResult.file). */
    fun done(job: CompressJob): List<CompressResult> = job.resultsList.filter { it.state == DONE }

    /**
     * J4: the files [job] left as they were, each with the device's why:
     * those that failed, and those whose result wasn't smaller (skipped);
     * once it is [gone] (the device restarted mid-job), the tasks it hadn't
     * got through either.
     */
    fun notDone(job: CompressJob, gone: Boolean = false): List<CompressResult> =
        job.resultsList.filter { it.state == FAILED || it.state == SKIPPED || (gone && it.state != DONE) }

    /** J5: what tells one done result from another: its path and the content it left there. */
    fun resultKey(r: CompressResult): String = "${r.path}\u0000${r.file.hash}"

    /** J5: [job]'s done results with their file that aren't in [seen] (resultKey): each is put in place once. */
    fun newlyDone(job: CompressJob, seen: Set<String>): List<CompressResult> =
        done(job).filter { it.hasFile() && resultKey(it) !in seen }

    /**
     * J6: the file shown after a compress in place of [shown]: the
     * device's ([r].file - its name, content, size and original), at
     * [shown]'s path if it names none; its copies and date kept where the
     * device left them out.
     */
    fun fileAfter(shown: PbFile, r: CompressResult): PbFile {
        val f = r.file
        val b = f.toBuilder()
        if (f.path.isEmpty()) b.path = shown.path
        if (!f.hasCopies() && shown.hasCopies()) b.copies = shown.copies
        if (!f.hasCreated() && shown.hasCreated()) b.created = shown.created
        return b.build()
    }

    /**
     * Whether [job] is the one [tasks] started: it holds a result for one
     * of them. A CompressFiles whose answer didn't come may have reached
     * the device and started it, or not: then the job there is an older
     * one, or none.
     */
    fun holds(job: CompressJob, tasks: List<CompressTask>): Boolean {
        val paths = tasks.mapTo(HashSet()) { it.path }
        return job.resultsList.any { it.path in paths }
    }
}

/**
 * The Compress dialog's model (CompressDialog.kt): one per screen that
 * offers Compress (Images, Settings › Largest files), so the dialog and
 * what it waits on outlive a rotation. [open] asks the device about the
 * picked items; the choices are kept here; [compress] sends the tasks.
 * The job is then asked about every 2 s while the dialog shows it and
 * every 10 s while only its screen follows it ([follow], J2): closed, the
 * job goes on on the device. The files each answer finds compressed are
 * handed to the screen once ([State.landed], [taken]): Images and the list
 * put them in place of what they showed.
 */
class CompressModel(
    private val send: GalleryRequest = { kind, baseMs, build ->
        if (kind == null) OTCConnection.request(baseMs, build) else OTCConnection.ask(kind, baseMs, build)
    },
    // The wait between two GetCompressJob (tests: their own).
    private val tick: suspend (Long) -> Unit = { delay(it) },
    // Where its work runs: the view model's own scope, a test's in tests.
    scope: CoroutineScope? = null,
) : ViewModel() {
    private val scope: CoroutineScope = scope ?: viewModelScope

    /** What the dialog shows, step by step. */
    enum class Phase { ESTIMATING, FAILED, NEEDS_UPDATE, OPTIONS, JOB }

    /**
     * [shown]: the dialog is up. [asked]: the items asked about (the
     * title's count). [failure]: why the options didn't come (FAILED).
     * [video], [photosOn]: the choices made. [sending]: CompressFiles is on
     * its way; [refusal]: why it didn't go through (the dialog stays on
     * the choices). [job]: the job, as last answered - kept once the dialog
     * closes, while the device works on it; [gone]: the device no longer
     * has it (J1); [videoSent]: a video is among its tasks (it says they
     * take a while). [landed]: compressed files the screen hasn't put in
     * place yet.
     */
    data class State(
        val shown: Boolean = false,
        val asked: List<CompressItem> = emptyList(),
        val phase: Phase = Phase.ESTIMATING,
        val failure: UiText? = null,
        val options: List<CompressOption> = emptyList(),
        val video: String = CompressRules.VIDEO_NONE,
        val photosOn: Set<String> = emptySet(),
        val sending: Boolean = false,
        val refusal: UiText? = null,
        val job: CompressJob? = null,
        val gone: Boolean = false,
        val videoSent: Boolean = false,
        val landed: List<CompressResult> = emptyList(),
    ) {
        /** The tasks the choices make. */
        val tasks: List<CompressTask> get() = CompressRules.tasks(options, video, photosOn)
        /** Compress is on: the choices make a task, and none is on its way. */
        val canCompress: Boolean get() = phase == Phase.OPTIONS && !sending && tasks.isNotEmpty()
        /** The job is over (J4): it says so, or the device no longer has it. */
        val over: Boolean get() = job?.let { CompressRules.over(it, gone) } ?: true
    }

    private val _state = MutableStateFlow(State())
    val state: StateFlow<State> = _state

    // Bumped by each opening and closing: a late answer for the dialog
    // before lands nowhere (what a CompressFiles started still counts).
    private var generation = 0
    private var asking: Job? = null
    private var polling: Job? = null
    // The screens following the job (in front): it is asked about meanwhile.
    private var followers = 0
    // The done results handed to the screen already (J5).
    private val handed = HashSet<String>()

    /** Compress pressed with [picked] (path, content shown): the dialog opens and asks the device. */
    fun open(picked: List<CompressItem>) {
        val items = CompressRules.items(picked)
        if (items.isEmpty() || _state.value.shown) return
        generation++
        _state.update { State(shown = true, asked = items, job = it.job, gone = it.gone, videoSent = it.videoSent, landed = it.landed) }
        ask()
        repoll()
    }

    /** The options didn't come: asked again. */
    fun retry() {
        if (_state.value.phase != Phase.FAILED) return
        ask()
    }

    private fun ask() {
        val gen = generation
        val items = _state.value.asked
        _state.update { it.copy(phase = Phase.ESTIMATING, failure = null) }
        asking?.cancel()
        asking = scope.launch {
            val resp = try {
                // As a page: the device reads up to 500 files' headers.
                send("compress", OTCConnection.PAGE_TIMEOUT_MS) { it.setReqGetCompressOptions(CompressRules.request(items)) }
            } catch (e: CancellationException) {
                throw e
            } catch (_: Exception) { null }
            if (gen != generation) return@launch
            when (val a = CompressRules.optionsAnswer(resp)) {
                is CompressRules.OptionsAnswer.Shown -> _state.update {
                    it.copy(
                        phase = Phase.OPTIONS, options = a.options,
                        video = CompressRules.defaultVideo(a.options) ?: CompressRules.VIDEO_NONE,
                        photosOn = CompressRules.photoSwitches(a.options).toSet(),
                    )
                }
                CompressRules.OptionsAnswer.NeedsUpdate -> _state.update { it.copy(phase = Phase.NEEDS_UPDATE) }
                is CompressRules.OptionsAnswer.Failed -> _state.update { it.copy(phase = Phase.FAILED, failure = a.text) }
            }
        }
    }

    /** The videos' choice (a preset, or Leave as they are). */
    fun chooseVideo(preset: String) = _state.update { it.copy(video = preset, refusal = null) }

    /** A photo's switch turned [on] or off. */
    fun setPhoto(preset: String, on: Boolean) =
        _state.update { it.copy(photosOn = if (on) it.photosOn + preset else it.photosOn - preset, refusal = null) }

    /**
     * Compress: CompressFiles with the tasks the choices make. Taken, the
     * dialog shows the job, and [onSent] runs (the screen's selection
     * goes); refused, the device's words (the demo's among them) and the
     * choices stay. No answer: the device is asked whether the job started
     * even so (the request may have reached it); if not, that the files
     * weren't compressed. The model's own work: closing the dialog doesn't
     * cut it short.
     */
    fun compress(onSent: () -> Unit = {}): Job? {
        val st = _state.value
        if (!st.canCompress) return null
        val tasks = st.tasks
        val gen = generation
        _state.update { it.copy(sending = true, refusal = null) }
        return scope.launch {
            val failed = S.appFilesCompressFailed(tasks.size)
            val resp = try {
                send(null, OTCConnection.LIST_TIMEOUT_MS) { it.setReqCompressFiles(CompressRules.compressRequest(tasks)) }
            } catch (e: CancellationException) {
                throw e
            } catch (_: Exception) { null }
            val answer = if (resp != null) CompressRules.jobAnswer(resp, failed) else startedAnyway(tasks) ?: CompressRules.JobAnswer.Refused("", failed)
            val here = gen == generation
            when (answer) {
                is CompressRules.JobAnswer.Started -> {
                    // Closed meanwhile: the job is followed all the same.
                    val video = CompressRules.slow(tasks)
                    _state.update {
                        it.copy(sending = false, job = answer.job, gone = false, videoSent = video, phase = if (here) Phase.JOB else it.phase)
                    }
                    land(answer.job)
                    onSent()
                    repoll()
                }
                CompressRules.JobAnswer.NeedsUpdate -> if (here) _state.update { it.copy(sending = false, phase = Phase.NEEDS_UPDATE) }
                is CompressRules.JobAnswer.Refused -> if (here) _state.update { it.copy(sending = false, refusal = answer.text) }
            }
        }
    }

    // The job the device runs, if [tasks] started it after all.
    private suspend fun startedAnyway(tasks: List<CompressTask>): CompressRules.JobAnswer? {
        val resp = try {
            send("compress-job", OTCConnection.LIST_TIMEOUT_MS) { it.setReqGetCompressJob(CompressRules.jobRequest("")) }
        } catch (e: CancellationException) {
            throw e
        } catch (_: Exception) { return null }
        if (resp.payloadCase != RespEnvelope.PayloadCase.RESP_COMPRESS_JOB || !CompressRules.holds(resp.respCompressJob, tasks)) return null
        return CompressRules.JobAnswer.Started(resp.respCompressJob)
    }

    /**
     * The screen that offers Compress is in front (Images, the list) -
     * until [unfollow]: the job is asked about every 10 s meanwhile, so
     * what it compresses shows there with the dialog closed (J2).
     */
    fun follow() {
        followers++
        repoll()
    }

    /** The screen went (another one, the background). */
    fun unfollow() {
        followers = maxOf(0, followers - 1)
        repoll()
    }

    // GetCompressJob after each wait (J2) while the job isn't over and
    // something looks. No answer: asked again at the next round; gone (the
    // device restarted, another job began since): over as last answered.
    // Started again whenever what looks changes, so the dialog opening
    // asks every 2 s at once.
    private fun repoll() {
        polling?.cancel()
        polling = scope.launch {
            while (true) {
                val st = _state.value
                val job = st.job ?: return@launch
                val wait = CompressRules.pollDelay(st.shown && st.phase == Phase.JOB && followers > 0, followers > 0, st.over) ?: return@launch
                tick(wait)
                val resp = try {
                    send("compress-job", OTCConnection.LIST_TIMEOUT_MS) { it.setReqGetCompressJob(CompressRules.jobRequest(job.id)) }
                } catch (e: CancellationException) {
                    throw e
                } catch (_: Exception) { null }
                // Another job was started meanwhile: its own round follows it.
                if (_state.value.job?.id != job.id) continue
                when (val a = CompressRules.pollAnswer(resp, job.id)) {
                    is CompressRules.PollAnswer.Shown -> {
                        _state.update { it.copy(job = a.job) }
                        land(a.job)
                    }
                    CompressRules.PollAnswer.Gone -> _state.update { it.copy(gone = true) }
                    CompressRules.PollAnswer.Failed -> {}
                }
            }
        }
    }

    // The files [job] compressed that the screen hasn't had yet (J5).
    private fun land(job: CompressJob) {
        val fresh = CompressRules.newlyDone(job, handed)
        if (fresh.isEmpty()) return
        fresh.mapTo(handed) { CompressRules.resultKey(it) }
        _state.update { it.copy(landed = it.landed + fresh) }
    }

    /** The screen put [results] in place: not handed to it again. */
    fun taken(results: List<CompressResult>) {
        if (results.isEmpty()) return
        _state.update { s -> s.copy(landed = s.landed.filter { r -> results.none { it === r } }) }
    }

    /**
     * The dialog closes (Cancel, Close, Done, Back): a job under way goes
     * on on the device - followed every 10 s while the screen is in front
     * - and a finished one is let go.
     */
    fun close() {
        generation++
        asking?.cancel()
        _state.update { s ->
            val keep = s.job?.takeIf { !s.over }
            State(job = keep, gone = false, videoSent = keep != null && s.videoSent, landed = s.landed)
        }
        repoll()
    }
}
