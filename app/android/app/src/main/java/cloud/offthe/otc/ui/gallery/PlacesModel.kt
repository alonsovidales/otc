// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import cloud.offthe.otc.data.PlacesSupport
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.net.retryWaitMs
import cloud.offthe.otc.net.sleepOrWake
import cloud.offthe.otc.proto.GetPhotoPlaces
import cloud.offthe.otc.proto.GetThumbnails
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.ui.common.GridThumbFetcher
import cloud.offthe.otc.ui.common.GridThumbStore
import cloud.offthe.otc.ui.common.TILES_KIND
import cloud.offthe.otc.ui.common.ThumbStore
import cloud.offthe.otc.ui.common.thumbTileKey
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

/**
 * The map's places for what Images shows (Places.kt, PlacesMap.kt): asks
 * the device for the tiles the map shows, keeps the answers and works out
 * the pins. One for the session (PhotoGalleryViewModel.places), so the map
 * comes back where it was. iOS's PlacesModel:
 *
 *  - Only while the map shows ([appeared] to [disappeared]) is anything
 *    asked for: first the search's extent (zoom 0, the world's tile),
 *    then - once the camera has rested [settleMs] - the screen's tiles
 *    and one around them, 64 a request, then after [prefetchMs] more the
 *    next zoom's, to have them before a zoom in.
 *  - Answers are kept per search and tile for five minutes, or until the
 *    library changes (PlacesCache); while the device still reads where the
 *    photos were taken (PhotoPlaces.reading) only 15 s, and what the map
 *    shows is asked again as they age.
 *  - A failed ask says so ([State.problem], the map's banner) and is asked
 *    again, 1 s doubling to 10 s, sooner on a Wake. "places_off" (turned
 *    off on another app) or "unknown_payload" (a device before Places)
 *    hides the map at once (PlacesSupport).
 *  - The pins' thumbnails come from the phone's cache, else GetThumbnails
 *    (GridThumbFetcher), the pins on the screen first.
 *
 * Confined to [scope]'s thread (the main one): call it from there.
 */
class PlacesModel(
    private val scope: CoroutineScope,
    private val request: suspend (GetPhotoPlaces) -> RespEnvelope = { r ->
        OTCConnection.ask("places", OTCConnection.LIST_TIMEOUT_MS) { it.setReqGetPhotoPlaces(r) }
    },
    private val thumbs: GridThumbStore = ThumbStore,
    sendThumbs: suspend (GetThumbnails.Builder) -> RespEnvelope = { b ->
        OTCConnection.ask(TILES_KIND, OTCConnection.PAGE_TIMEOUT_MS) { it.setReqGetThumbnails(b) }
    },
    private val pause: suspend (Long) -> Unit = { sleepOrWake(it) },
    private val now: () -> Long = { System.currentTimeMillis() },
    /** The device has no map now (places_off, unknown_payload). */
    private val noMap: () -> Unit = { PlacesSupport.set(false) },
) {
    /** How long the camera rests before the tiles are asked for, and before the next zoom's are (tests shorten them). */
    var settleMs = 200L
    var prefetchMs = 600L

    data class State(
        /** What is drawn. */
        val pins: List<PlacePin> = emptyList(),
        /** The search's photos with a position: null until the device said. */
        val extent: PlacesExtent? = null,
        /** The last ask failed: asked again meanwhile, and the map says so. */
        val problem: Boolean = false,
        /** The search shown. */
        val filter: PlacesFilter = PlacesFilter(),
        /** The covers' thumbnails by path: their tile key in the cache, "" when the device has none. */
        val thumbKeys: Map<String, String> = emptyMap(),
    )

    /** The rect on the screen and the map's width (dp), once the camera stopped. */
    data class View(val rect: WorldRect, val width: Double)

    private val _state = MutableStateFlow(State())
    val state: StateFlow<State> = _state

    /** Where the map was left, and for which search: it comes back there. */
    var savedRect: WorldRect? = null
        private set
    var savedFor: PlacesFilter? = null
        private set
    var view: View? = null
        private set

    private val cache = PlacesCache()
    private var asking = false
    private var settleJob: Job? = null
    private var prefetchJob: Job? = null
    private var readingJob: Job? = null
    private var retryJob: Job? = null
    private var failures = 0
    private var lastAnswer = Long.MIN_VALUE / 2
    // The map is on the screen: only then is anything asked for (a delete in
    // the grid only forgets what is kept).
    private var onMap = false
    // The covers (paths) whose pins were on the screen at the last count,
    // and those being looked up in the cache.
    private var onScreen = emptySet<String>()
    private val resolving = HashSet<String>()

    private val fetcher = GridThumbFetcher(scope, thumbs, send = sendThumbs, pause = pause) { landed ->
        _state.update { st -> st.copy(thumbKeys = st.thumbKeys + landed.mapValues { it.value ?: "" }) }
    }

    init {
        // Settings cleared the thumbnail cache: looked up and fetched again.
        scope.launch {
            var seen = thumbs.clears.value
            thumbs.clears.collect { n ->
                if (n == seen) return@collect
                seen = n
                fetcher.forgetNone()
                resolving.clear()
                _state.update { it.copy(thumbKeys = emptyMap()) }
                askThumbnails()
            }
        }
    }

    /** The zoom on the screen, exactly (the device's, 256-point tiles), and the tiles' zoom for it. */
    val exactZoom: Double get() = view?.let { PlaceTiles.exactZoom(it.width, PlaceTiles.lonSpan(it.rect)) } ?: 0.0
    val zoom: Int get() = view?.let { PlaceTiles.zoom(it.width, PlaceTiles.lonSpan(it.rect)) } ?: 0

    /** Where a map of the whole world has its middle: the longitude of the biggest of zoom 0's places for the search shown (null until it came). */
    fun worldMiddle(): Double? = cache.places(PlaceTileKey(0, 0, 0), _state.value.filter, now())?.let { PlaceTiles.middle(it) }

    /** The map came on screen, for this search. A map left a minute ago or more asks again for what it shows, keeping it drawn until then. */
    fun appeared(filter: PlacesFilter) {
        onMap = true
        if (now() - lastAnswer > 60_000L) cache.clearTiles()
        setFilter(filter)
        pump()
    }

    /** The map left the screen: nothing more is asked for until it comes back. */
    fun disappeared() {
        onMap = false
        settleJob?.cancel()
        prefetchJob?.cancel()
        readingJob?.cancel()
        for (gone in onScreen) fetcher.gone(gone)
        onScreen = emptySet()
    }

    /** Images' search changed: the map shows its photos, framed on them once their extent is known. */
    fun setFilter(f: PlacesFilter) {
        if (f == _state.value.filter) return
        // Another search's pins never stand in for this one's.
        _state.update { it.copy(filter = f, extent = cache.extent(f), pins = emptyList(), problem = false) }
        stopRetry()
        redraw()
        pump()
    }

    /**
     * The camera stopped at [rect], on a map [width] points wide: its tiles
     * asked for once it rests, and - [framed]: the map was on the search's
     * photos already, not the world it opens on - kept as where the map is.
     */
    fun cameraChanged(rect: WorldRect, width: Double, framed: Boolean = true) {
        if (width <= 0 || rect.width <= 0) return
        view = View(rect, width)
        if (framed) {
            savedRect = rect
            savedFor = _state.value.filter
        }
        prefetchJob?.cancel()
        redraw()
        settleJob?.cancel()
        settleJob = scope.launch {
            delay(settleMs)
            pump()
        }
    }

    /** The library changed (a delete, a folder kept out of Images): what is kept is asked for again, drawn as it was meanwhile. */
    fun libraryChanged() {
        cache.clearTiles()
        fetcher.reset()
        resolving.clear()
        _state.update { it.copy(thumbKeys = emptyMap()) }
        stopRetry()
        pump()
    }

    /** Signed out, or Places off: nothing of this library is kept. */
    fun clear() {
        cache.clear()
        fetcher.reset()
        resolving.clear()
        savedRect = null
        savedFor = null
        stopRetry()
        _state.value = State(filter = _state.value.filter)
    }

    /** The banner's Try again. */
    fun retryNow() {
        stopRetry()
        _state.update { it.copy(problem = false) }
        pump()
    }

    /** A pin's thumbnail didn't decode: fetched again. */
    fun thumbLost(path: String) {
        _state.update { it.copy(thumbKeys = it.thumbKeys - path) }
        fetcher.shown(path, needsThumb = true)
    }

    private fun stopRetry() {
        retryJob?.cancel()
        retryJob = null
        failures = 0
    }

    // ---- asking ----

    /** The tiles the screen shows, with one around them. */
    private val wanted: List<PlaceTileKey>
        get() = view?.let { PlaceTiles.tiles(it.rect, zoom, margin = 1) } ?: emptyList()

    /**
     * The next ask, unless one is on its way or waits for its retry: the
     * extent of a search not known yet (with the world's tile at zoom 0),
     * else the screen's tiles the cache lacks, else - once the camera has
     * rested a little more - the next zoom's.
     */
    private fun pump() {
        if (!onMap || asking || retryJob?.isActive == true) return
        val f = _state.value.filter
        if (cache.extent(f) == null) {
            ask(0, listOf(PlaceTileKey(0, 0, 0)), f)
            return
        }
        val v = view ?: return
        val z = zoom
        val missing = cache.missing(wanted, f, now())
        if (missing.isNotEmpty()) {
            ask(z, missing.take(PlaceTiles.BATCH_SIZE), f)
            return
        }
        if (z >= PlaceTiles.MAX_ZOOM) return
        val next = cache.missing(PlaceTiles.tiles(v.rect, z + 1, margin = 0), f, now())
        if (next.isEmpty()) return
        prefetchJob?.cancel()
        prefetchJob = scope.launch {
            delay(prefetchMs)
            if (!onMap || asking || _state.value.filter != f || zoom != z) return@launch
            ask(z + 1, next.take(PlaceTiles.BATCH_SIZE), f)
        }
    }

    private fun ask(zoom: Int, tiles: List<PlaceTileKey>, f: PlacesFilter) {
        asking = true
        scope.launch {
            val resp = try {
                request(f.request(zoom, tiles))
            } catch (e: CancellationException) {
                asking = false
                throw e
            } catch (_: Exception) { null }
            asking = false
            if (resp == null || resp.payloadCase != RespEnvelope.PayloadCase.RESP_PHOTO_PLACES) {
                if (resp != null && (resp.errorCode == "places_off" || resp.isUnknownPayload())) {
                    // Places turned off on another app, or a device before
                    // Places (signed in to another one meanwhile): no map.
                    noMap()
                    return@launch
                }
                _state.update { it.copy(problem = true) }
                retryJob = scope.launch {
                    pause(retryWaitMs(++failures))
                    retryJob = null
                    pump()
                }
                return@launch
            }
            failures = 0
            lastAnswer = now()
            val answer = resp.respPhotoPlaces
            cache.put(answer, tiles, f, now())
            _state.update { st ->
                if (st.filter != f) st.copy(problem = false)
                else st.copy(problem = false, extent = PlacesExtent(answer))
            }
            if (f == _state.value.filter) redraw()
            if (answer.reading) askAgainWhileReading()
            pump()
        }
    }

    /** An answer said the device is still reading positions: once it has aged, the screen's tiles (and the count with them) are asked again. */
    private fun askAgainWhileReading() {
        readingJob?.cancel()
        readingJob = scope.launch {
            delay(PlacesCache.READING_TTL_MS + 500)
            if (onMap) pump()
        }
    }

    // ---- drawing ----

    /**
     * The pins for the screen: each tile's kept places at the zoom shown,
     * or, for a tile still on its way, the pins drawn there before; then
     * what is too close together drawn as one. Their thumbnails are asked
     * for: those on the screen first.
     */
    private fun redraw() {
        if (view == null) {
            if (_state.value.pins.isNotEmpty()) _state.update { it.copy(pins = emptyList()) }
            return
        }
        val st = _state.value
        val z = zoom
        val before = st.pins.groupBy { PlaceTiles.tile(it.latitude, it.longitude, z) }
        val shown = ArrayList<PlacePin>()
        val t = now()
        for (tile in wanted) {
            val kept = cache.places(tile, st.filter, t)
            if (kept != null) kept.mapTo(shown) { PlacePin.of(it) } else shown += before[tile].orEmpty()
        }
        val merged = PinMerge.merge(shown, exactZoom)
        if (merged != st.pins) _state.update { it.copy(pins = merged) }
        askThumbnails()
    }

    private fun askThumbnails() {
        val v = view ?: return
        val pins = _state.value.pins
        val keys = _state.value.thumbKeys
        val now = HashSet<String>()
        val lookUp = ArrayList<cloud.offthe.otc.proto.File>()
        for (p in pins) {
            val c = p.cover ?: continue
            if (c.path.isEmpty() || c.hash.isEmpty()) continue
            if (onMap && v.rect.containsAcross(p.point)) {
                now += c.path
                fetcher.shown(c.path, needsThumb = false)
            }
            if (c.path !in keys && resolving.add(c.path)) lookUp += c
        }
        for (gone in onScreen - now) fetcher.gone(gone)
        onScreen = now
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
            // On the screen first (the fetcher knows which), the rest after.
            fetcher.note(missing)
            fetcher.near(missing)
        }
    }

    /** The point is in the rect, on either copy of the world beside it. */
    private fun WorldRect.containsAcross(p: WorldPoint) =
        contains(p) || contains(p.copy(x = p.x + 1)) || contains(p.copy(x = p.x - 1))
}
