// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import cloud.offthe.otc.data.MemoriesSupport
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appPhotosMemoriesHideFailed
import cloud.offthe.otc.net.retryWaitMs
import cloud.offthe.otc.net.sleepOrWake
import cloud.offthe.otc.proto.GetMemories
import cloud.offthe.otc.proto.GetThumbnails
import cloud.offthe.otc.proto.HideMemory
import cloud.offthe.otc.proto.Memories
import cloud.offthe.otc.proto.Memory
import cloud.offthe.otc.proto.MemoryHideKind
import cloud.offthe.otc.proto.MemoryPeriod
import cloud.offthe.otc.proto.Person
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.ui.common.GridThumbFetcher
import cloud.offthe.otc.ui.common.GridThumbStore
import cloud.offthe.otc.ui.common.ThumbStore
import cloud.offthe.otc.ui.common.thumbTileKey
import cloud.offthe.otc.ui.settings.deviceOr
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.withTimeoutOrNull
import java.time.Duration
import java.time.Instant
import java.time.ZoneId
import java.time.ZonedDateTime
import cloud.offthe.otc.proto.File as PbFile

/** On this day's three choices: the same day, the week around it, the calendar month - in earlier years. */
enum class MemoryPeriodChoice(val proto: MemoryPeriod) {
    DAY(MemoryPeriod.MemoryDay), WEEK(MemoryPeriod.MemoryWeek), MONTH(MemoryPeriod.MemoryMonth),
}

/**
 * The phone's today, as GetMemories takes it: the date, the IANA zone and
 * the UTC offset (which the device uses for a zone it doesn't know).
 */
data class MemoriesDay(val date: String, val zone: String, val offsetMinutes: Int) {
    fun request(p: MemoryPeriodChoice): GetMemories = GetMemories.newBuilder()
        .setDate(date).setPeriod(p.proto).setTimeZone(zone).setUtcOffsetMinutes(offsetMinutes).build()

    companion object {
        fun of(now: ZonedDateTime) = MemoriesDay(now.toLocalDate().toString(), now.zone.id, now.offset.totalSeconds / 60)

        /** How long until the phone's next day starts, and a second more: then the memories are asked again. */
        fun msToNextDay(now: ZonedDateTime): Long {
            val next = now.toLocalDate().plusDays(1).atStartOfDay(now.zone)
            return Duration.between(now, next).toMillis().coerceAtLeast(0) + 1_000
        }
    }
}

/**
 * Something hidden from the memories, said at the foot of the screen
 * (app.photos.memories.hidden) with Undo - [undo] shows it again - or a
 * hide that didn't happen, in the device's words or ours (no [undo]).
 */
data class MemoriesNotice(val id: Int, val text: UiText, val undo: HideMemory?)

/**
 * The rules of the strip, the same as iOS's (MemoriesRules there): which
 * choice shows, what a hide takes and who can be hidden.
 */
object MemoriesRules {
    val ORDER = listOf(MemoryPeriodChoice.DAY, MemoryPeriodChoice.WEEK, MemoryPeriodChoice.MONTH)

    fun has(answers: Map<MemoryPeriodChoice, Memories>, p: MemoryPeriodChoice) = (answers[p]?.memoriesCount ?: 0) > 0

    /**
     * The choice showing: the one picked this session while it has
     * memories, else the first that has any, day before week before month -
     * decided only once the earlier ones have answered (null until then, and
     * when none has any).
     */
    fun period(chosen: MemoryPeriodChoice?, answers: Map<MemoryPeriodChoice, Memories>): MemoryPeriodChoice? {
        if (chosen != null && has(answers, chosen)) return chosen
        for (p in ORDER) {
            val a = answers[p] ?: return null
            if (a.memoriesCount > 0) return p
        }
        return null
    }

    /** How many things are hidden, as the latest answers say. */
    fun hiddenCount(answers: Map<MemoryPeriodChoice, Memories>): Int = answers.values.maxOfOrNull { it.hiddenCount } ?: 0

    fun measuring(answers: Map<MemoryPeriodChoice, Memories>) = answers.values.any { it.measuring }

    /** The day a photo was taken, "YYYY-MM-DD" in [zone] (Don't show this day); null without a date. */
    fun dayOf(f: PbFile, zone: ZoneId): String? =
        if (f.hasCreated()) Instant.ofEpochSecond(f.created.seconds, f.created.nanos.toLong()).atZone(zone).toLocalDate().toString() else null

    /**
     * The named people of a memory that can be hidden: a memory carries
     * names, HideMemory takes a person id, so only a name exactly one
     * person has (People's list) - in the memory's order.
     */
    fun hideablePeople(names: List<String>, people: List<Person>): List<Person> = names.mapNotNull { n ->
        people.filter { it.name == n }.singleOrNull()
    }

    /**
     * The memory before or after [key] in the strip's order (the most recent
     * year first, iOS's MemoryStep): null at either end, or when it isn't
     * there any more - after the last one the viewer closes.
     */
    fun neighbour(list: List<Memory>, key: String, by: Int): Memory? =
        list.indexOfFirst { it.key == key }.takeIf { it >= 0 }?.let { list.getOrNull(it + by) }

    /** What shows once [kind]/[value] is hidden, before the device answers again: a photo or a memory goes at once. */
    fun withoutHidden(m: Memories, kind: MemoryHideKind, value: String): Memories {
        val kept = m.memoriesList.mapNotNull { mem ->
            when (kind) {
                MemoryHideKind.MemoryHideMemory -> mem.takeUnless { it.key == value }
                MemoryHideKind.MemoryHidePhoto -> {
                    val files = mem.filesList.filter { it.hash != value }
                    when {
                        files.size == mem.filesCount -> mem
                        files.isEmpty() -> null
                        else -> mem.toBuilder().clearFiles().addAllFiles(files)
                            .setCover(if (mem.cover.hash == value) files.first() else mem.cover).build()
                    }
                }
                else -> mem
            }
        }
        return if (kept.size == m.memoriesCount && kept.zip(m.memoriesList).all { (a, b) -> a === b }) m
        else m.toBuilder().clearMemories().addAllMemories(kept).build()
    }

    /** What a HideMemory answer means: null when it was done, else what to say. */
    fun refusal(resp: RespEnvelope?): UiText? = when {
        resp == null -> S.appPhotosMemoriesHideFailed()
        resp.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && resp.respAck.ok -> null
        resp.payloadCase == RespEnvelope.PayloadCase.RESP_ACK -> deviceOr(resp.respAck.errorMsg, S.appPhotosMemoriesHideFailed())
        resp.error -> deviceOr(resp.errorMessage, S.appPhotosMemoriesHideFailed())
        else -> S.appPhotosMemoriesHideFailed()
    }
}

/**
 * The strip's state: today's answers per choice (a new day starts them
 * afresh), the choice picked this session, [off] once the device said
 * memories_off or doesn't know GetMemories, the covers' thumbnails (path ->
 * tile key, "" when there is none) and the last hide's notice.
 */
data class MemoriesState(
    val day: MemoriesDay? = null,
    val answers: Map<MemoryPeriodChoice, Memories> = emptyMap(),
    val chosen: MemoryPeriodChoice? = null,
    val off: Boolean = false,
    val thumbKeys: Map<String, String> = emptyMap(),
    val notice: MemoriesNotice? = null,
) {
    val period: MemoryPeriodChoice? get() = MemoriesRules.period(chosen, answers)
    val shown: List<Memory> get() = period?.let { answers[it]?.memoriesList }.orEmpty()
    /** Hidden entirely when off, and when no choice has a memory. */
    val visible: Boolean get() = !off && shown.isNotEmpty()
    val hiddenCount: Int get() = MemoriesRules.hiddenCount(answers)
    fun has(p: MemoryPeriodChoice) = MemoriesRules.has(answers, p)
}

/**
 * On this day for Images (one for the session, PhotoGalleryViewModel.memories,
 * so the choice is kept): while the strip may show ([appeared]) it asks the
 * device for today's memories - day, week and month, one after the other -
 * again when the phone's date changes, every 2 minutes while the device is
 * still measuring the photos it held before, after a hide and when Settings
 * changed something; a failure is asked again 1 s doubling to 10 s, at once
 * on a Wake. memories_off or unknown_payload (another app turned it off, a
 * device before it) hides it for good (MemoriesSupport). The covers'
 * thumbnails come by hash from the cache, else GetThumbnails by path; the
 * slideshow's big thumbnails (GetThumbnails without small_thumbnails) are
 * kept for the last few photos.
 */
class MemoriesModel(
    private val scope: CoroutineScope,
    private val getMemories: suspend (GetMemories) -> RespEnvelope,
    private val hideMemory: suspend (HideMemory) -> RespEnvelope,
    private val bigThumbAsk: suspend (String) -> RespEnvelope,
    private val thumbs: GridThumbStore = ThumbStore,
    sendThumbs: suspend (GetThumbnails.Builder) -> RespEnvelope,
    private val pause: suspend (Long) -> Unit = { sleepOrWake(it) },
    private val now: () -> ZonedDateTime = { ZonedDateTime.now() },
    private val clock: () -> Long = { System.currentTimeMillis() },
    private val turnedOff: () -> Unit = { MemoriesSupport.set(false) },
) {
    private val _state = MutableStateFlow(MemoriesState())
    val state: StateFlow<MemoriesState> = _state

    private var loop: Job? = null
    private val pokes = Channel<Unit>(Channel.CONFLATED)
    // Ask again before waiting: a hide, Settings, a new sign-in.
    private var dirty = false
    private var loadedAt = Long.MIN_VALUE / 2
    private var noticeIds = 0
    private val resolving = HashSet<String>()
    private val big = object : LinkedHashMap<String, ByteArray>(16, 0.75f, true) {
        override fun removeEldestEntry(eldest: MutableMap.MutableEntry<String, ByteArray>?) = size > BIG_KEPT
    }

    private val fetcher = GridThumbFetcher(scope, thumbs, send = sendThumbs, pause = pause) { landed ->
        _state.update { st -> st.copy(thumbKeys = st.thumbKeys + landed.mapValues { it.value ?: "" }) }
    }

    /** The strip may show (Images' grid on the whole library): asks, and keeps asking as the day changes. */
    fun appeared() {
        if (loop?.isActive == true) return
        loop = scope.launch { run() }
    }

    /** The strip can't show (a filter, another page): nothing more is asked meanwhile. */
    fun disappeared() {
        loop?.cancel()
        loop = null
    }

    /** Ask again now (Settings changed what is hidden, On this day turned on again). */
    fun refresh() {
        _state.update { it.copy(off = false) }
        dirty = true
        pokes.trySend(Unit)
    }

    /** On this day was turned off (here, on another app, or a device without it): nothing kept. */
    fun clear() {
        _state.update { MemoriesState(chosen = it.chosen, off = true) }
        big.clear()
    }

    fun choose(p: MemoryPeriodChoice) = _state.update { it.copy(chosen = p) }

    private suspend fun run() {
        var failures = 0
        while (true) {
            val t = now()
            val d = MemoriesDay.of(t)
            val st = _state.value
            val measuring = MemoriesRules.measuring(st.answers)
            val due = !st.off && (dirty || st.day != d || st.answers.size < MemoriesRules.ORDER.size ||
                (measuring && clock() - loadedAt >= MEASURING_MS))
            if (due) {
                dirty = false
                if (!load(d)) {
                    pause(retryWaitMs(++failures))
                    continue
                }
                failures = 0
                loadedAt = clock()
            }
            val wait = if (MemoriesRules.measuring(_state.value.answers)) minOf(MEASURING_MS, MemoriesDay.msToNextDay(t))
            else MemoriesDay.msToNextDay(t)
            withTimeoutOrNull(wait) { pokes.receive() }
        }
    }

    /** Today's three answers, one after the other: false when one didn't come (asked again later). */
    private suspend fun load(d: MemoriesDay): Boolean {
        for (p in MemoriesRules.ORDER) {
            val resp = try {
                getMemories(d.request(p))
            } catch (e: CancellationException) {
                throw e
            } catch (_: Exception) { null }
            when {
                resp?.payloadCase == RespEnvelope.PayloadCase.RESP_MEMORIES -> {
                    val m = resp.respMemories
                    _state.update { it.copy(day = d, answers = (if (it.day == d) it.answers else emptyMap()) + (p to m), off = false) }
                    noteThumbs(m.memoriesList.map { it.cover })
                }
                resp != null && (resp.errorCode == "memories_off" || resp.isUnknownPayload()) -> {
                    // Turned off on another app, or a device before it.
                    clear()
                    turnedOff()
                    return true
                }
                else -> return false
            }
        }
        return true
    }

    /** These files' thumbnails are wanted (the covers, a memory opened): from the cache, else asked for. */
    fun noteThumbs(files: List<PbFile>) {
        val keys = _state.value.thumbKeys
        val lookUp = files.filter { it.path.isNotEmpty() && it.hash.isNotEmpty() && it.path !in keys && resolving.add(it.path) }
        if (lookUp.isEmpty()) return
        scope.launch {
            val kinds = try { thumbs.kinds(lookUp.map { it.hash }) } catch (e: CancellationException) { throw e } catch (_: Exception) { emptyMap() }
            val found = HashMap<String, String>()
            val missing = ArrayList<String>()
            for (f in lookUp) {
                if (!resolving.remove(f.path)) continue
                val k = kinds[f.hash]
                if (k != null) found[f.path] = thumbTileKey(f.hash, k) else missing += f.path
            }
            if (found.isNotEmpty()) _state.update { it.copy(thumbKeys = it.thumbKeys + found) }
            if (missing.isEmpty()) return@launch
            fetcher.note(missing)
            fetcher.near(missing)
        }
    }

    /** A tile's bytes by its key (the covers', the slideshow's while its picture comes). */
    suspend fun tileBytes(key: String): ByteArray? = thumbs.load(key)

    /**
     * The slideshow's picture for [path]: its big thumbnail (the device's
     * own, at most 1000 px), kept for the last few; null when it can't be
     * had now (the tile stays).
     */
    suspend fun bigThumb(path: String): ByteArray? {
        synchronized(big) { big[path] }?.let { return it }
        val resp = try { bigThumbAsk(path) } catch (e: CancellationException) { throw e } catch (_: Exception) { return null }
        if (resp.payloadCase != RespEnvelope.PayloadCase.RESP_LIST_OF_FILES) return null
        val bytes = resp.respListOfFiles.filesList.firstOrNull { it.path == path && it.hasContent() }?.content?.toByteArray() ?: return null
        synchronized(big) { big[path] = bytes }
        return bytes
    }

    /**
     * Hides [kind]/[value] from the memories; [said] is the notice once it
     * is done (with Undo). A photo or a memory goes from the strip at once;
     * then everything is asked again. Refused (the demo device, an old one)
     * or not answered, the notice says why and nothing changes. True when it
     * was hidden.
     */
    suspend fun hide(kind: MemoryHideKind, value: String, said: UiText): Boolean {
        val req = HideMemory.newBuilder().setKind(kind).setValue(value).build()
        val resp = try { hideMemory(req) } catch (e: CancellationException) { throw e } catch (_: Exception) { null }
        val refused = MemoriesRules.refusal(resp)
        if (refused != null) {
            say(refused, null)
            return false
        }
        _state.update { st -> st.copy(answers = st.answers.mapValues { (_, m) -> MemoriesRules.withoutHidden(m, kind, value) }) }
        say(said, req.toBuilder().setShow(true).build())
        dirty = true
        pokes.trySend(Unit)
        return true
    }

    /** The notice's Undo: shows again what was hidden. */
    suspend fun undo(n: MemoriesNotice) {
        val req = n.undo ?: return
        dismiss(n.id)
        val resp = try { hideMemory(req) } catch (e: CancellationException) { throw e } catch (_: Exception) { null }
        MemoriesRules.refusal(resp)?.let { say(it, null) }
        dirty = true
        pokes.trySend(Unit)
    }

    fun dismiss(id: Int) = _state.update { if (it.notice?.id == id) it.copy(notice = null) else it }

    private fun say(text: UiText, undo: HideMemory?) {
        noticeIds += 1
        _state.update { it.copy(notice = MemoriesNotice(noticeIds, text, undo)) }
    }

    companion object {
        /** While the device measures the photos it held before, asked again this often. */
        const val MEASURING_MS = 120_000L
        /** The slideshow's big thumbnails kept (about 100 KB each). */
        const val BIG_KEPT = 8
    }
}
