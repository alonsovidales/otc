// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import cloud.offthe.otc.net.retryWaitMs
import cloud.offthe.otc.proto.File
import cloud.offthe.otc.proto.GetThumbnails
import cloud.offthe.otc.proto.ListOfFiles
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.ui.gallery.isUnknownPayload
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch

/*
 * The photo grids' tiles (Images, the composer's Synced photos, the profile
 * photo picker) over the phone's thumbnail cache (ThumbStore). Every page
 * is asked without its thumbnails (SearchPhotos.omit_thumbnails, release
 * 113, with small_thumbnails): each entry's content hash is looked up in the
 * cache, and what it lacks is fetched with GetThumbnails, a batch at a time,
 * the tiles on screen first. A device before 113 ignores the flag and sends
 * the thumbnails in the page: they are shown and kept. See tileAction for
 * what each entry gets, and GridThumbFetcher for the fetching. The same
 * rules as the iOS app's GridThumbLoader.swift.
 */

/** A page's first request: 60 entries without thumbnails (from a device known to leave them out), else 12 with them, as before. */
const val FIRST_PAGE_LIMIT_WITHOUT_THUMBS = 60
/** Pages continuing a search (a device before release 113 answers at most its default, 30, whatever is asked). */
const val NEXT_PAGE_LIMIT_WITHOUT_THUMBS = 120
/** Paths per GetThumbnails: 24 small thumbnails are ~1 MB, well under the device's 48 paths and 8 MB. */
const val THUMB_BATCH = 24
/** The Patience kind (OTCConnection.ask) GetThumbnails batches wait as. */
const val TILES_KIND = "tiles"

/** One thumbnail to keep: under [hash] (the answer's), of [kind]. */
class ThumbPut(val hash: String, val bytes: ByteArray, val kind: ThumbKind)

/** Where the grids keep their tiles' thumbnails: ThumbStore (tests: a ThumbStoreCore in a temporary folder). */
interface GridThumbStore {
    /** The scope (device and account) a page or fetch is asked under: its writes carry it, and are dropped once another is bound. */
    suspend fun scope(): Long

    /** The kinds kept for these hashes (no bytes read); absent: not kept. */
    suspend fun kinds(hashes: Collection<String>): Map<String, ThumbKind>

    /**
     * Keeps them: the kind each hash holds afterwards (a small one already
     * kept stays); a hash left out had bytes that don't decode - none to
     * show. Null: asked under another [scope] - nothing kept.
     */
    suspend fun put(entries: List<ThumbPut>, scope: Long): Map<String, ThumbKind>?

    /** A tile's bytes by its key ([thumbTileKey]); null when not kept (any more, or they don't decode). */
    suspend fun load(key: String): ByteArray?

    /** A tile's bytes didn't decode: dropped. */
    suspend fun discard(key: String)

    /** Whether the device of this scope is known to leave thumbnails out of pages (release 113). */
    suspend fun omitsThumbnails(): Boolean

    /** A page came: [omitted] when none of its rows had content (a device that leaves them out), false when one had. */
    suspend fun notePage(omitted: Boolean, scope: Long)

    /**
     * Of these hashes - kept big or unknown, on rows that say the device has
     * the small one (thumbnail_small on a row without content means "a small
     * one is stored") - those to ask for again: each once a launch, and not
     * one whose small one was asked for and came big within a week
     * ([noteStillBig]) - the answer's marker is the one that counts.
     */
    suspend fun claimReasks(hashes: Collection<String>): Set<String>

    /** [hash]'s small one was asked for again and the big one came: not asked for again for a week, launches included. */
    suspend fun noteStillBig(hash: String, scope: Long)

    /** Made ready ahead (a grid's first page is on its way). */
    fun warmUp() {}

    /** Bumped by Settings' Clear thumbnail cache: grids drop what they hold besides (decoded tiles, big thumbnails, "none" marks). */
    val clears: StateFlow<Int>
}

/** A tile's key: its content hash and the kind kept - it changes when a small one replaces a big one, so the tile shows it. */
fun thumbTileKey(hash: String, kind: ThumbKind) = "$hash#${kind.code}"

/** The content hash a tile key is for. */
fun thumbKeyHash(key: String) = key.substringBefore('#')

/** File.thumbnail_small: null when unset (a device before release 113). */
fun File.saysSmall(): Boolean? = if (hasThumbnailSmall()) thumbnailSmall else null

/** What a grid does with one entry of a page. */
enum class TileAction {
    /** The page carried it (a device before release 113, which ignores omit_thumbnails): shown and kept. */
    SHOW_CONTENT,
    /** Kept already, and good enough. */
    USE_CACHED,
    /** Kept as the big one (or unknown), and the device has the small one now: shown, and fetched again (once: claimReasks). */
    USE_CACHED_AND_REFETCH,
    /** Not kept: fetched (GetThumbnails). */
    FETCH,
}

/**
 * [hasContent]: the entry came with its thumbnail. [saysSmall]: its
 * thumbnail_small (null: unset). [cached]: the kind kept for its hash.
 */
fun tileAction(hasContent: Boolean, saysSmall: Boolean?, cached: ThumbKind?): TileAction = when {
    hasContent -> TileAction.SHOW_CONTENT
    cached == null -> TileAction.FETCH
    cached == ThumbKind.SMALL -> TileAction.USE_CACHED
    // The device has its small one now (it says a small one is stored).
    saysSmall == true -> TileAction.USE_CACHED_AND_REFETCH
    // The device has no small one yet either (false), or doesn't say.
    else -> TileAction.USE_CACHED
}

/**
 * A page's tiles: each path's key (null: none yet), the paths to fetch in
 * the page's order, and of those the ones asked for again for their small
 * one ([refetch]: GridThumbFetcher.enqueue's reasks).
 */
class LandedPage(val keys: Map<String, String?>, val fetch: List<String>, val refetch: Set<String> = emptySet())

/**
 * [files] (a page asked under [scope]) against the cache: what each tile
 * shows now and what to fetch; thumbnails the page carried are kept (one
 * that doesn't decode isn't: its tile asks GetThumbnails, whose answer then
 * says none). The page says whether the device leaves thumbnails out: none
 * of its rows had content.
 */
suspend fun GridThumbStore.land(files: List<File>, scope: Long): LandedPage {
    if (files.isNotEmpty()) notePage(files.none { it.hasContent() }, scope)
    val withContent = files.filter { it.hasContent() && it.hash.isNotEmpty() }
    val kept = if (withContent.isEmpty()) emptyMap() else
        put(withContent.map { ThumbPut(it.hash, it.content.toByteArray(), ThumbKind.of(it.saysSmall())) }, scope) ?: emptyMap()
    val without = files.filter { !it.hasContent() && it.hash.isNotEmpty() }
    val cached = if (without.isEmpty()) emptyMap() else kinds(without.map { it.hash })
    val stale = without.filter { tileAction(false, it.saysSmall(), cached[it.hash]) == TileAction.USE_CACHED_AND_REFETCH }.map { it.hash }
    val reask = if (stale.isEmpty()) emptySet() else claimReasks(stale)
    val keys = LinkedHashMap<String, String?>()
    val fetch = ArrayList<String>()
    val refetch = HashSet<String>()
    for (f in files) {
        if (f.hash.isEmpty()) { keys[f.path] = null; continue }
        when (tileAction(f.hasContent(), f.saysSmall(), cached[f.hash])) {
            TileAction.SHOW_CONTENT -> keys[f.path] = kept[f.hash]?.let { thumbTileKey(f.hash, it) }
            TileAction.USE_CACHED -> keys[f.path] = thumbTileKey(f.hash, cached.getValue(f.hash))
            TileAction.USE_CACHED_AND_REFETCH -> {
                keys[f.path] = thumbTileKey(f.hash, cached.getValue(f.hash))
                if (f.hash in reask) { fetch += f.path; refetch += f.path }
            }
            TileAction.FETCH -> { keys[f.path] = null; fetch += f.path }
        }
    }
    return LandedPage(keys, fetch, refetch)
}

/**
 * What a GetThumbnails answer to [asked] means: [landed] - each path the
 * answer had a thumbnail for, with its tile key (kept under the ANSWER's
 * hash: the path may hold other content than when it was listed), and each
 * path that has none now, with null; [again] - the paths to ask again.
 * [dropped]: asked under a scope no longer bound - nothing kept or landed.
 *
 * From release 113 the answer says where it stopped short
 * (ask_again_from: those from there on weren't looked at), and a path
 * before that left out has no thumbnail now (not processed, unreadable,
 * gone): a placeholder, nothing kept. A device before 113 cuts an answer
 * at 8 MB unsaid, so a path it left out is never taken as having none:
 * those after the last one answered may only have been cut and are asked
 * again (a cut answer always holds at least one, so this ends), the rest
 * stay without a tile until it shows again. Content that doesn't decode is
 * none, from any device. A path asked for again for its small one
 * ([reasked]) whose answer is the big one again is noted still big.
 */
class ThumbAnswer(val landed: Map<String, String?>, val again: List<String>, val device113: Boolean, val dropped: Boolean = false)

suspend fun GridThumbStore.takeAnswer(
    asked: List<String>, lof: ListOfFiles, knownDevice113: Boolean, scope: Long, reasked: Set<String> = emptySet(),
): ThumbAnswer {
    val askedSet = asked.toHashSet()
    val files = lof.filesList.filter { it.path in askedSet }
    val device113 = knownDevice113 || lof.filesList.any { it.hasThumbnailSmall() }
    val withContent = files.filter { it.hasContent() && it.hash.isNotEmpty() }
    val kept = if (withContent.isEmpty()) emptyMap() else
        put(withContent.map { ThumbPut(it.hash, it.content.toByteArray(), ThumbKind.of(it.saysSmall())) }, scope)
            ?: return ThumbAnswer(emptyMap(), emptyList(), device113, dropped = true)
    val landed = LinkedHashMap<String, String?>()
    for (f in withContent) {
        val k = kept[f.hash]
        landed[f.path] = k?.let { thumbTileKey(f.hash, it) }
        if (k != null && f.path in reasked && f.saysSmall() == false) noteStillBig(f.hash, scope)
    }
    val answered = files.mapTo(HashSet()) { it.path }
    val cut = lof.askAgainFrom.takeIf { it in 1 until asked.size } ?: 0
    val again = ArrayList<String>()
    if (cut > 0) again += asked.subList(cut, asked.size)
    val looked = if (cut > 0) asked.subList(0, cut) else asked
    if (device113) {
        for (p in looked) if (p !in answered) landed[p] = null
    } else if (answered.isNotEmpty()) {
        val last = looked.indexOfLast { it in answered }
        for (p in looked.subList(last + 1, looked.size)) if (p !in answered) again += p
    }
    return ThumbAnswer(landed, again, device113)
}

/**
 * Fetches a grid's missing thumbnails with GetThumbnails (small ones) - only
 * those needed now: a page's misses are [note]d, and asked for once their
 * tiles show ([shown]) or come within reach of the scroll ([near]: about 12
 * tiles before the ones on screen and 30 after). At most [batch] paths a
 * request and one request at a time; asks are gathered for [gatherMs] first,
 * and while a tile on screen is waiting a batch holds only tiles on screen.
 * A request that fails is sent again after 1 s doubling to 10 s ([pause]:
 * the app's sleepOrWake, cut short by a Wake), as the grids' pages are. Each
 * answer goes to [onLanded] (path -> tile key; null: no thumbnail now) - not
 * one for a search the grid has left ([reset]). Confined to [scope]'s
 * thread (the main one): call it from there. As iOS's GridThumbLoader.
 */
class GridThumbFetcher(
    private val scope: CoroutineScope,
    private val store: GridThumbStore,
    private val send: suspend (GetThumbnails.Builder) -> RespEnvelope,
    private val pause: suspend (Long) -> Unit,
    private val batch: Int = THUMB_BATCH,
    private val gatherMs: Long = GATHER_MS,
    private val onLanded: (Map<String, String?>) -> Unit,
) {
    // A page's misses not asked for yet; the paths to ask for, in order; the
    // tiles on screen; the batch on its way; the paths the device said have
    // none (release 113, or content that doesn't decode), not asked again
    // until the grid starts over or the cache is cleared; the paths asked
    // for again for their small one.
    private val noted = HashSet<String>()
    private val queue = LinkedHashSet<String>()
    private val shown = LinkedHashSet<String>()
    private val inFlight = HashSet<String>()
    private val none = HashSet<String>()
    private val reasking = HashSet<String>()
    private var job: Job? = null
    private var failures = 0
    // Bumped by reset: a batch of an older search lands nowhere and isn't asked again.
    private var generation = 0
    // The device says what a GetThumbnails left out means (release 113): seeded
    // from the scope's marker (a page without content), then from answers.
    var device113 = false
    // A device before release 80 has no GetThumbnails (its pages carry the thumbnails).
    private var unsupported = false

    /** A page's misses ([reasks]: those asked for again for their small one): asked for when their tiles show or come near. */
    fun note(paths: Collection<String>, reasks: Collection<String> = emptyList()) {
        reasking += reasks
        var added = false
        for (p in paths) {
            if (p in none || p in inFlight || p in queue) continue
            // Already on screen (its tile came before the page's answer was taken).
            if (p in shown) { queue += p; added = true } else noted += p
        }
        if (added) pump()
    }

    /** Tiles within reach of the scroll: those noted are asked for, after the ones on screen. */
    fun near(paths: Collection<String>) {
        var added = false
        for (p in paths) if (noted.remove(p) && p !in inFlight && queue.add(p)) added = true
        if (added) pump()
    }

    /** A tile is on screen; [needsThumb]: it has none (or lost it) - fetched first, as is a noted one. */
    fun shown(path: String, needsThumb: Boolean) {
        shown += path
        // Out of the noted ones either way: asked for now (or not at all), never twice.
        val wasNoted = noted.remove(path)
        val wanted = needsThumb || wasNoted
        if (wanted && path !in none && path !in inFlight && queue.add(path)) pump() else if (path in queue) pump()
    }

    /** The tile left the screen: no longer first (still fetched in its turn). */
    fun gone(path: String) { shown -= path }

    /** No thumbnail to have for [path] (its bytes kept failing to decode): not asked for again until the grid starts over. */
    fun markNone(path: String) {
        none += path
        queue -= path
        noted -= path
    }

    /** The cache was cleared (Settings): what the device said had none may be asked again. */
    fun forgetNone() { none.clear() }

    /** The grid starts over (a new search): what waits goes; the batch on its way is kept, not landed. */
    fun reset() {
        generation++
        noted.clear()
        queue.clear()
        none.clear()
        reasking.clear()
        failures = 0
    }

    /** Waiting or on its way (tests). */
    val pending: Int get() = queue.size + inFlight.size

    /** Noted, not asked for yet (tests). */
    val waitingToShow: Int get() = noted.size

    // The tiles on screen alone while any of them waits; else the rest in order.
    private fun nextBatch(): List<String> {
        val out = ArrayList<String>(batch)
        for (p in shown) { if (out.size == batch) break; if (p in queue) out += p }
        if (out.isEmpty()) for (p in queue) { if (out.size == batch) break; out += p }
        queue.removeAll(out.toSet())
        return out
    }

    private fun pump() {
        if (job?.isActive == true || unsupported) return
        job = scope.launch {
            if (!device113 && store.omitsThumbnails()) device113 = true
            while (true) {
                // A scroll shows tiles over a few frames: gathered into one request.
                if (gatherMs > 0) delay(gatherMs)
                val asked = nextBatch()
                if (asked.isEmpty()) break
                val gen = generation
                val sc = store.scope()
                inFlight += asked
                val resp = try {
                    send(GetThumbnails.newBuilder().addAllPaths(asked).setSmallThumbnails(true))
                } catch (e: CancellationException) {
                    inFlight -= asked.toSet()
                    throw e
                } catch (_: Exception) { null }
                if (resp != null && resp.isUnknownPayload()) {
                    inFlight -= asked.toSet()
                    unsupported = true
                    queue.clear()
                    break
                }
                if (resp == null || resp.payloadCase != RespEnvelope.PayloadCase.RESP_LIST_OF_FILES) {
                    inFlight -= asked.toSet()
                    // Asked again first, unless the grid started over meanwhile.
                    if (gen == generation) {
                        val rest = queue.toList()
                        queue.clear()
                        queue += asked
                        queue += rest
                    }
                    pause(retryWaitMs(++failures))
                    continue
                }
                failures = 0
                val answer = try {
                    store.takeAnswer(asked, resp.respListOfFiles, device113, sc, asked.filterTo(HashSet()) { it in reasking })
                } finally {
                    inFlight -= asked.toSet()
                    reasking -= asked.toSet()
                }
                if (answer.device113) device113 = true
                // A search left meanwhile: kept in the cache, not landed nor asked again.
                if (gen != generation || answer.dropped) continue
                for ((p, k) in answer.landed) if (k == null) none += p
                if (answer.again.isNotEmpty()) {
                    val rest = queue.toList()
                    queue.clear()
                    queue += answer.again
                    queue += rest
                }
                if (answer.landed.isNotEmpty()) onLanded(answer.landed)
            }
        }
    }

    companion object {
        /** How long asks are gathered into one request. */
        const val GATHER_MS = 30L
        /** Tiles within reach: this many before the first on screen... */
        const val REACH_BEFORE = 12
        /** ...and this many after the last. */
        const val REACH_AFTER = 30
    }
}
