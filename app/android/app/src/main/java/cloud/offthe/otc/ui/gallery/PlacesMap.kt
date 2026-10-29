// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import android.content.Context
import androidx.compose.animation.core.Animatable
import androidx.compose.animation.core.tween
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxScope
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.defaultMinSize
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.State
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.draw.shadow
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalLifecycleOwner
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.onClick
import androidx.compose.ui.semantics.role
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.LinkAnnotation
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextLinkStyles
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.withLink
import androidx.compose.ui.unit.IntOffset
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.viewmodel.compose.viewModel
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appPhotosPlacesFailed
import cloud.offthe.otc.i18n.appPhotosPlacesNone
import cloud.offthe.otc.i18n.appPhotosPlacesNoneFiltered
import cloud.offthe.otc.i18n.appPhotosPlacesReading
import cloud.offthe.otc.i18n.appShellSectionPlaces
import cloud.offthe.otc.i18n.commonLoading
import cloud.offthe.otc.net.byteSize
import cloud.offthe.otc.ui.animationsOff
import cloud.offthe.otc.ui.common.TRY_AGAIN
import cloud.offthe.otc.ui.common.ThumbStore
import cloud.offthe.otc.ui.common.rememberTileThumb
import cloud.offthe.otc.ui.theme.Ember
import org.maplibre.android.MapLibre
import org.maplibre.android.camera.CameraPosition
import org.maplibre.android.camera.CameraUpdateFactory
import org.maplibre.android.geometry.LatLng
import org.maplibre.android.maps.MapLibreMap
import org.maplibre.android.maps.MapLibreMapOptions
import org.maplibre.android.maps.MapView
import org.maplibre.android.maps.Style
import java.text.NumberFormat
import java.time.ZoneId
import kotlin.math.abs
import kotlin.math.floor
import kotlin.math.hypot
import kotlin.math.ln
import kotlin.math.max
import kotlin.math.min
import kotlin.math.pow
import kotlin.math.roundToInt
import cloud.offthe.otc.proto.File as PbFile

// Places' map (Places.kt, PlacesModel.kt): MapLibre with OpenFreeMap's
// tiles - the web's map (PlacesView.tsx), no key and no Google services -
// in the app's colours (assets/places, scripts/places-style.mjs), names in
// the app's language. A narrow window's Images shows it in the grid's place
// (the switch in its search row), a wide one's menu has it as the Places
// page (PhotoGalleryView). OpenFreeMap sees the area on the screen, never
// the photos; nothing here asks for the phone's own position.
//
// The pins are Compose, over the map: each is the newest photo's small
// thumbnail in a white frame with its count, as iOS's and the web's. They
// are laid out (not composed again) as the camera moves, from the camera's
// own numbers (MapCamera), and take no touches themselves - the map gets
// every gesture, a drag starting on a pin included - so a tap is found from
// the map's click (pinAt). TalkBack reaches each one by its label.

/** MapLibre's zoom counts 512-point tiles. */
private const val MAP_TILE = 512.0
/** The closest the map goes. */
private const val MAX_MAP_ZOOM = 18.0
/** A pin's side, its frame included. */
private val PIN = 48.dp
private val PIN_CORNER = RoundedCornerShape(11.dp)

/** The map's look: the style for the theme, place names in [lang] (the app's language, "pt" for Português). */
internal object MapStyles {
    private const val LANG_TOKEN = "__OTC_LANG__"
    private val loaded = HashMap<Boolean, String>()

    fun json(context: Context, dark: Boolean, lang: String): String {
        val raw = synchronized(loaded) {
            loaded.getOrPut(dark) {
                context.assets.open(if (dark) "places/style-dark.json" else "places/style-light.json").bufferedReader().use { it.readText() }
            }
        }
        return raw.replace(LANG_TOKEN, lang.filter { it.isLetter() }.ifEmpty { "en" })
    }

    /** The credits OpenFreeMap and OpenStreetMap ask for, as the web writes them (names only: nothing to translate). */
    val credits = listOf(
        "OpenFreeMap" to "https://openfreemap.org",
        "© OpenMapTiles" to "https://www.openmaptiles.org/",
        "© OpenStreetMap" to "https://www.openstreetmap.org/copyright",
    )
}

/** A point on the screen, in pixels. */
internal data class ScreenPoint(val x: Float, val y: Float)

/**
 * Where the map is looking: the world's point at its middle ([cx], [cy],
 * WorldPoint units), MapLibre's zoom, and the map's size in pixels - what
 * places a pin on the screen, with no call into the map (it doesn't turn
 * or tilt: the projection is a scale and a shift).
 */
internal data class MapCamera(val cx: Double, val cy: Double, val zoom: Double, val widthPx: Float, val heightPx: Float, val density: Float) {
    /** Pixels per world (its width at this zoom). */
    val worldPx: Double get() = MAP_TILE * 2.0.pow(zoom) * density
    val widthDp: Double get() = widthPx / density.toDouble()

    /** The world on the screen. */
    val rect: WorldRect get() {
        val w = widthPx / worldPx
        val h = heightPx / worldPx
        return WorldRect(cx - w / 2, cy - h / 2, w, h)
    }

    /** Where a point is on the screen, in pixels: on the copy of the world nearest the middle. */
    fun screen(p: WorldPoint): ScreenPoint {
        val x = p.x + Math.round(cx - p.x)
        return ScreenPoint(((x - cx) * worldPx + widthPx / 2).toFloat(), ((p.y - cy) * worldPx + heightPx / 2).toFloat())
    }

    companion object {
        fun of(map: MapLibreMap, density: Float): MapCamera? {
            val pos = map.cameraPosition
            val t = pos.target ?: return null
            if (map.width <= 0f || map.height <= 0f) return null
            val p = PlaceTiles.point(t.latitude, t.longitude)
            return MapCamera(p.x, p.y, pos.zoom, map.width, map.height, density)
        }
    }
}

/** The least zoom for a map [widthDp] wide: never more than one world across. */
internal fun minMapZoom(widthDp: Double) = max(0.0, ln(widthDp / MAP_TILE) / ln(2.0))

/** Where the camera goes: its middle and MapLibre's zoom. */
internal data class MapTarget(val latitude: Double, val longitude: Double, val zoom: Double) {
    val position: CameraPosition get() = CameraPosition.Builder().target(LatLng(latitude, longitude)).zoom(zoom).build()
}

/** The camera that shows [rect] whole on a map [widthDp] by [heightDp], within [minZoom] and the map's last. */
internal fun cameraFor(rect: WorldRect, widthDp: Double, heightDp: Double, minZoom: Double): MapTarget {
    val zx = if (rect.width > 0) ln(widthDp / (rect.width * MAP_TILE)) / ln(2.0) else MAX_MAP_ZOOM
    val zy = if (rect.height > 0) ln(heightDp / (rect.height * MAP_TILE)) / ln(2.0) else MAX_MAP_ZOOM
    val zoom = min(zx, zy).coerceIn(minZoom, MAX_MAP_ZOOM)
    var mx = rect.midX
    mx -= floor(mx)
    val (lat, lon) = PlaceTiles.coordinate(WorldPoint(mx, rect.midY.coerceIn(0.0, 1.0)))
    return MapTarget(lat, lon, zoom)
}

/**
 * A MapLibre map view for the composition, its lifecycle the screen's:
 * [options] once, when it is made. Text on the map comes from glyphs the
 * style names; no position of the phone is ever asked for.
 */
@Composable
internal fun rememberMapView(options: (Context) -> MapLibreMapOptions): MapView {
    val context = LocalContext.current
    val view = remember {
        MapLibre.getInstance(context.applicationContext)
        MapView(context, options(context)).apply { onCreate(null) }
    }
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    DisposableEffect(lifecycle, view) {
        var started = false
        var resumed = false
        val observer = LifecycleEventObserver { _, e ->
            when (e) {
                Lifecycle.Event.ON_START -> { view.onStart(); started = true }
                Lifecycle.Event.ON_RESUME -> { view.onResume(); resumed = true }
                Lifecycle.Event.ON_PAUSE -> { view.onPause(); resumed = false }
                Lifecycle.Event.ON_STOP -> { view.onStop(); started = false }
                else -> {}
            }
        }
        lifecycle.addObserver(observer)
        onDispose {
            lifecycle.removeObserver(observer)
            if (resumed) view.onPause()
            if (started) view.onStop()
            view.onDestroy()
        }
    }
    return view
}

/** The map's options: no turning or tilting, no logo or compass (the credits are written out), a texture so the pins and the page move with it. */
internal fun mapOptions(context: Context, dark: Boolean): MapLibreMapOptions =
    MapLibreMapOptions.createFromAttributes(context)
        .textureMode(true)
        .logoEnabled(false)
        .attributionEnabled(false)
        .compassEnabled(false)
        .rotateGesturesEnabled(false)
        .tiltGesturesEnabled(false)
        .maxZoomPreference(MAX_MAP_ZOOM)
        .foregroundLoadColor(if (dark) 0xFF1C1E1D.toInt() else 0xFFF2F3F0.toInt())

/**
 * The map of Images' photos (a narrow window's Images switched to the map;
 * a wide one's Places page). [onShowInImages]: a group too close to split
 * was opened in Images.
 */
@Composable
internal fun PlacesMapView(vm: PhotoGalleryViewModel, st: PhotoGalleryViewModel.State, onShowInImages: () -> Unit, modifier: Modifier = Modifier) {
    val places = vm.places
    val ps by places.state.collectAsState()
    val context = LocalContext.current
    val density = LocalDensity.current.density
    val dark = isSystemInDarkTheme()
    val config = LocalConfiguration.current
    val lang = config.locales[0].language
    val reduceMotion = remember { animationsOff(context) }
    val filter = st.placesFilter
    val zone = remember { ZoneId.systemDefault() }

    val mapView = rememberMapView { mapOptions(it, dark) }
    var map by remember { mutableStateOf<MapLibreMap?>(null) }
    // Where the camera is, read only where the pins are laid out (never in
    // composition: a moving map would compose them again each frame).
    val camera = remember { mutableStateOf<MapCamera?>(null) }
    // The map is on the search's photos or where it was left: the extent
    // arriving later doesn't move it.
    var framedFor by remember { mutableStateOf<PlacesFilter?>(null) }
    var sized by remember { mutableStateOf(false) }

    // Opens a single photo, as Files opens one.
    val viewer: PhotoGalleryViewModel = viewModel(key = "places-viewer") { PhotoGalleryViewModel("") }
    val vst by viewer.state.collectAsState()

    DisposableEffect(places) {
        places.appeared(filter)
        onDispose { places.disappeared() }
    }

    fun move(target: WorldRect, animated: Boolean) {
        val m = map ?: return
        val c = camera.value ?: return
        val heightDp = c.heightPx / c.density.toDouble()
        val pos = cameraFor(target, c.widthDp, heightDp, minMapZoom(c.widthDp))
        val update = CameraUpdateFactory.newCameraPosition(pos.position)
        if (animated && !reduceMotion) m.easeCamera(update, 450) else m.moveCamera(update)
    }

    // The map on the search's photos: where it was left for this search,
    // else framed on them once the device said where they are (the world
    // when none has a position).
    fun frame(animated: Boolean) {
        val f = places.state.value.filter
        if (framedFor == f || map == null || camera.value == null) return
        val saved = places.savedRect
        val target = if (saved != null && places.savedFor == f) saved else {
            val e = places.state.value.extent ?: return
            e.area?.let { PlaceTiles.opening(it, places.worldMiddle(), PlaceTiles.townWidth) } ?: WorldRect.WORLD
        }
        framedFor = f
        move(target, animated)
    }

    fun open(f: PbFile) {
        viewer.showFiles(
            listOf(
                PhotoGalleryViewModel.Item(
                    "${f.path}#${f.hash}#${f.byteSize}", f.path, f.mime, f.byteSize, places.state.value.thumbKeys[f.path]?.ifEmpty { null },
                    created = if (f.hasCreated()) f.created.seconds * 1000 else null, hash = f.hash,
                ),
            ),
            0,
        ) { places.libraryChanged() }
    }

    fun tap(pin: PlacePin) {
        when (val t = PlaceTap.of(pin, places.exactZoom, places.view?.rect ?: WorldRect.WORLD)) {
            is PlaceTap.Open -> open(t.file)
            is PlaceTap.Zoom -> move(t.rect, animated = true)
            is PlaceTap.ShowInImages -> {
                vm.showArea(t.area)
                onShowInImages()
            }
        }
    }
    val onTap by rememberUpdatedState(::tap)
    val pins by rememberUpdatedState(ps.pins)

    // The map's own setup, once it is there.
    LaunchedEffect(mapView) {
        mapView.contentDescription = S.appShellSectionPlaces().resolve(context)
        mapView.getMapAsync { m ->
            m.uiSettings.apply {
                isRotateGesturesEnabled = false
                isTiltGesturesEnabled = false
                isCompassEnabled = false
                isLogoEnabled = false
                isAttributionEnabled = false
            }
            fun now() = MapCamera.of(m, density)?.also { c ->
                camera.value = c
                m.setMinZoomPreference(minMapZoom(c.widthDp))
            }
            m.addOnCameraMoveListener { now() }
            m.addOnCameraIdleListener {
                val c = now() ?: return@addOnCameraIdleListener
                if (!sized) sized = true
                places.cameraChanged(c.rect, c.widthDp, framed = framedFor == places.state.value.filter)
            }
            m.addOnMapClickListener { at ->
                val c = camera.value ?: return@addOnMapClickListener false
                val p = m.projection.toScreenLocation(at)
                val hit = pinAt(pins, c, ScreenPoint(p.x, p.y)) ?: return@addOnMapClickListener false
                onTap(hit)
                true
            }
            mapView.addOnLayoutChangeListener { _, _, _, _, _, _, _, _, _ -> now() }
            map = m
            now()?.let { sized = true }
        }
    }
    // Its look, again for another theme or language.
    LaunchedEffect(map, dark, lang) {
        map?.setStyle(Style.Builder().fromJson(MapStyles.json(context, dark, lang)))
    }
    // Images' search: the map shows its photos, framed afresh.
    LaunchedEffect(filter) { places.setFilter(filter) }
    LaunchedEffect(map, sized, ps.filter, ps.extent) { if (sized) frame(animated = framedFor != null) }

    Box(modifier.fillMaxSize().clipToBounds()) {
        AndroidView(factory = { mapView }, modifier = Modifier.fillMaxSize())
        Pins(ps.pins, ps.thumbKeys, camera, reduceMotion, zone, config.locales[0], onTap = { onTap(it) }, onLost = places::thumbLost)
        Notes(ps, onRetry = places::retryNow)
        Credits(Modifier.align(Alignment.BottomStart))
    }
    if (vst.openIndex != null) ImageModal(viewer, vst)
}

/** The pin under a tap at [at] (pixels): the nearest whose frame or count holds it. */
internal fun pinAt(pins: List<PlacePin>, c: MapCamera, at: ScreenPoint): PlacePin? {
    val half = 24f * c.density + 4f * c.density
    var best: PlacePin? = null
    var bestD = Float.MAX_VALUE
    for (p in pins) {
        val s = c.screen(p.point)
        val dx = at.x - s.x
        val dy = at.y - s.y
        // The count sticks out at the top right corner.
        val inBadge = p.count > 1 && dx > 0 && dy < 0 && dx < half + 14f * c.density && -dy < half + 14f * c.density
        if (!(abs(dx) <= half && abs(dy) <= half) && !inBadge) continue
        val d = hypot(dx, dy)
        if (d < bestD) {
            bestD = d
            best = p
        }
    }
    return best
}

@Composable
private fun Pins(
    pins: List<PlacePin>, thumbKeys: Map<String, String>, camera: State<MapCamera?>, reduceMotion: Boolean, zone: ZoneId,
    locale: java.util.Locale, onTap: (PlacePin) -> Unit, onLost: (String) -> Unit,
) {
    val density = LocalDensity.current
    val sidePx = with(density) { PIN.roundToPx() }
    val halfPx = sidePx / 2
    val numbers = remember(locale) { NumberFormat.getIntegerInstance(locale) }
    for (pin in pins) key(pin.id) {
        val label = pin.label(zone).resolve()
        val path = pin.cover?.path
        Box(
            Modifier
                .offset {
                    val c = camera.value ?: return@offset IntOffset(-10_000, -10_000)
                    val s = c.screen(pin.point)
                    IntOffset(s.x.roundToInt() - halfPx, s.y.roundToInt() - halfPx)
                }
                .size(PIN)
                // Read by TalkBack, which taps it; a finger's tap is the map's (pinAt).
                .semantics {
                    contentDescription = label
                    role = Role.Button
                    onClick(label) { onTap(pin); true }
                },
        ) {
            PinFace(pin, path?.let { thumbKeys[it] }, sidePx, reduceMotion, numbers) { path?.let(onLost) }
        }
    }
}

/**
 * A pin: the newest photo's thumbnail, rounded, in a white frame with a
 * soft shadow, and the count at its corner when it holds more than one. A
 * neutral square until the thumbnail comes; it fades in as it is drawn (at
 * once with animations off).
 */
@Composable
private fun PinFace(pin: PlacePin, thumbKey: String?, sidePx: Int, reduceMotion: Boolean, numbers: NumberFormat, onLost: () -> Unit) {
    val key = thumbKey?.ifEmpty { null }
    val bmp = rememberTileThumb(key, sidePx, onUndecodable = onLost) { key?.let { ThumbStore.load(it) } }
    val shown = remember { Animatable(if (reduceMotion) 1f else 0f) }
    LaunchedEffect(Unit) { if (!reduceMotion) shown.animateTo(1f, tween(250)) }
    Box(Modifier.fillMaxSize().graphicsLayer { alpha = shown.value; val s = 0.85f + 0.15f * shown.value; scaleX = s; scaleY = s }) {
        Box(
            Modifier.fillMaxSize().shadow(5.dp, PIN_CORNER).clip(PIN_CORNER)
                .background(if (isSystemInDarkTheme()) Color(0xFF3B403E) else Color(0xFFDADDDB))
                .border(2.5.dp, Color.White, PIN_CORNER),
        ) {
            if (bmp != null) Image(bmp.asImageBitmap(), null, Modifier.fillMaxSize(), contentScale = ContentScale.Crop)
        }
        if (pin.count > 1) Box(
            Modifier.align(Alignment.TopEnd).offset(x = 9.dp, y = (-9).dp)
                .defaultMinSize(minWidth = 20.dp, minHeight = 20.dp)
                .background(Ember, CircleShape).border(1.5.dp, Color.White, CircleShape)
                .padding(horizontal = 6.dp),
            contentAlignment = Alignment.Center,
        ) {
            Text(
                numbers.format(pin.count), color = Color(0xFF26100A), fontSize = 11.sp, fontWeight = FontWeight.Bold,
                maxLines = 1, softWrap = false,
            )
        }
    }
}

/** Over the map's top: it couldn't load (with Try again), the device still reads positions, there are none, or the first answer is on its way. */
@Composable
private fun BoxScope.Notes(ps: PlacesModel.State, onRetry: () -> Unit) {
    val e = ps.extent
    val pill: @Composable (@Composable () -> Unit) -> Unit = { content ->
        Surface(Modifier.align(Alignment.TopCenter).padding(top = 10.dp, start = 16.dp, end = 16.dp), shape = RoundedCornerShape(20.dp), tonalElevation = 6.dp, shadowElevation = 3.dp) {
            Box(Modifier.padding(horizontal = 14.dp, vertical = 8.dp)) { content() }
        }
    }
    when {
        ps.problem -> pill {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(S.appPhotosPlacesFailed().resolve(), style = MaterialTheme.typography.bodyMedium)
                Spacer(Modifier.width(4.dp))
                TextButton(onClick = onRetry) { Text(TRY_AGAIN.resolve(), fontWeight = FontWeight.SemiBold) }
            }
        }
        e != null && e.reading -> pill { Text(S.appPhotosPlacesReading().resolve(), style = MaterialTheme.typography.bodyMedium) }
        e != null && e.total == 0 -> pill {
            Text((if (ps.filter.isEmpty) S.appPhotosPlacesNone() else S.appPhotosPlacesNoneFiltered()).resolve(), style = MaterialTheme.typography.bodyMedium)
        }
        e == null -> {
            val loading = S.commonLoading().resolve()
            Surface(Modifier.align(Alignment.TopCenter).padding(top = 10.dp).semantics { contentDescription = loading }, shape = CircleShape, tonalElevation = 6.dp, shadowElevation = 3.dp) {
                CircularProgressIndicator(Modifier.padding(8.dp).size(24.dp), strokeWidth = 2.dp)
            }
        }
    }
}

/** The map's credits, small, at its foot: each name opens its page. */
@Composable
internal fun Credits(modifier: Modifier = Modifier) {
    val colors = MaterialTheme.colorScheme
    val link = TextLinkStyles(SpanStyle(color = colors.onSurface))
    val text = remember(link) {
        buildAnnotatedString {
            MapStyles.credits.forEachIndexed { i, (name, url) ->
                if (i > 0) append("  ")
                withLink(LinkAnnotation.Url(url, link)) { append(name) }
            }
        }
    }
    Text(
        text, modifier.padding(6.dp).background(colors.surface.copy(alpha = 0.72f), RoundedCornerShape(6.dp)).padding(horizontal = 6.dp, vertical = 2.dp),
        fontSize = 10.sp, color = colors.onSurfaceVariant, maxLines = 1,
    )
}

/**
 * The viewer's info panel's map (issue #127): the photo's position on the
 * same map as Places, a pin there, about 0.05° of latitude across. Still -
 * a tap on the caption under it opens the phone's maps app.
 */
@Composable
internal fun LocationMap(latitude: Double, longitude: Double, modifier: Modifier = Modifier) {
    val context = LocalContext.current
    val dark = isSystemInDarkTheme()
    val lang = LocalConfiguration.current.locales[0].language
    val mapView = rememberMapView { mapOptions(it, dark) }
    var map by remember { mutableStateOf<MapLibreMap?>(null) }
    LaunchedEffect(mapView) {
        mapView.getMapAsync { m ->
            m.uiSettings.setAllGesturesEnabled(false)
            m.uiSettings.isCompassEnabled = false
            m.uiSettings.isLogoEnabled = false
            m.uiSettings.isAttributionEnabled = false
            // Zoom 12 (512-point tiles) spans about 0.05° of latitude on a phone-wide map.
            m.moveCamera(CameraUpdateFactory.newCameraPosition(CameraPosition.Builder().target(LatLng(latitude, longitude)).zoom(12.0).build()))
            map = m
        }
    }
    LaunchedEffect(map, dark, lang) { map?.setStyle(Style.Builder().fromJson(MapStyles.json(context, dark, lang))) }
    Box(modifier.clip(RoundedCornerShape(8.dp))) {
        AndroidView(factory = { mapView }, modifier = Modifier.fillMaxSize())
        // The photo's position, in the middle.
        Box(Modifier.align(Alignment.Center).size(18.dp).shadow(3.dp, CircleShape).background(Ember, CircleShape).border(2.5.dp, Color.White, CircleShape))
        Credits(Modifier.align(Alignment.BottomStart))
    }
}
