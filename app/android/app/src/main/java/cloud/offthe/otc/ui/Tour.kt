// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui

import android.content.Context
import android.content.pm.ApplicationInfo
import android.provider.Settings
import androidx.activity.compose.BackHandler
import androidx.compose.animation.core.animateDpAsState
import androidx.compose.animation.core.tween
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawing
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Rect
import androidx.compose.ui.geometry.RoundRect
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.PathFillType
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.node.GlobalPositionAwareModifierNode
import androidx.compose.ui.layout.LayoutCoordinates
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.layout.positionInWindow
import androidx.compose.ui.node.ModifierNodeElement
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalLayoutDirection
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.paneTitle
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.toSize
import androidx.compose.ui.Modifier.Node
import cloud.offthe.otc.OTCApp
import cloud.offthe.otc.data.FaceRecognition
import cloud.offthe.otc.i18n.RichTag
import cloud.offthe.otc.i18n.RichText
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appFilesSortBy
import cloud.offthe.otc.i18n.appFilesViewGrid
import cloud.offthe.otc.i18n.appFilesViewList
import cloud.offthe.otc.i18n.appFilesViewStorage
import cloud.offthe.otc.i18n.appPhotosCollectionsNewItem
import cloud.offthe.otc.i18n.appPhotosPeopleAddName
import cloud.offthe.otc.i18n.appPhotosPeopleMerge
import cloud.offthe.otc.i18n.appPhotosViewerMoreInfo
import cloud.offthe.otc.i18n.appSettingsSyncTitle
import cloud.offthe.otc.i18n.appSettingsSyncWifiOnly
import cloud.offthe.otc.i18n.appSettingsUsersRequestBridge
import cloud.offthe.otc.i18n.appSettingsUsersTitle
import cloud.offthe.otc.i18n.appShellAddToCollection
import cloud.offthe.otc.i18n.appShellSectionAlerts
import cloud.offthe.otc.i18n.appShellSectionSettings
import cloud.offthe.otc.i18n.appSocialComposePublish
import cloud.offthe.otc.i18n.appSocialComposeSourcePhone
import cloud.offthe.otc.i18n.appSocialComposeSourceSynced
import cloud.offthe.otc.i18n.appSocialFriendsAddHeading
import cloud.offthe.otc.i18n.appSocialFriendsWaitingAnswer
import cloud.offthe.otc.i18n.appTourAlertsText
import cloud.offthe.otc.i18n.appTourAlertsTitle
import cloud.offthe.otc.i18n.appTourBackupText
import cloud.offthe.otc.i18n.appTourBackupTitle
import cloud.offthe.otc.i18n.appTourCollectionsText
import cloud.offthe.otc.i18n.appTourCollectionsTextWide
import cloud.offthe.otc.i18n.appTourCollectionsTitle
import cloud.offthe.otc.i18n.appTourDatesText
import cloud.offthe.otc.i18n.appTourDatesTitle
import cloud.offthe.otc.i18n.appTourFilesText
import cloud.offthe.otc.i18n.appTourFilesTitle
import cloud.offthe.otc.i18n.appTourFriendsText
import cloud.offthe.otc.i18n.appTourFriendsTextWide
import cloud.offthe.otc.i18n.appTourFriendsTitle
import cloud.offthe.otc.i18n.appTourPeopleText
import cloud.offthe.otc.i18n.appTourPeopleTextWide
import cloud.offthe.otc.i18n.appTourPeopleTitle
import cloud.offthe.otc.i18n.appTourPostText
import cloud.offthe.otc.i18n.appTourPostTitle
import cloud.offthe.otc.i18n.appTourProgress
import cloud.offthe.otc.i18n.appTourSearchText
import cloud.offthe.otc.i18n.appTourSearchTextFaces
import cloud.offthe.otc.i18n.appTourSearchTitle
import cloud.offthe.otc.i18n.appTourSelectText
import cloud.offthe.otc.i18n.appTourSelectTitle
import cloud.offthe.otc.i18n.appTourShow
import cloud.offthe.otc.i18n.appTourSkip
import cloud.offthe.otc.i18n.appTourSocialText
import cloud.offthe.otc.i18n.appTourSocialTitle
import cloud.offthe.otc.i18n.appTourUploadText
import cloud.offthe.otc.i18n.appTourUploadTitle
import cloud.offthe.otc.i18n.appTourUsersBridge
import cloud.offthe.otc.i18n.appTourUsersText
import cloud.offthe.otc.i18n.appTourUsersTitle
import cloud.offthe.otc.i18n.appTourViewerText
import cloud.offthe.otc.i18n.appTourViewerTitle
import cloud.offthe.otc.i18n.appTourWelcomeText
import cloud.offthe.otc.i18n.appTourWelcomeTextWide
import cloud.offthe.otc.i18n.appTourWelcomeTitle
import cloud.offthe.otc.i18n.commonAdd
import cloud.offthe.otc.i18n.commonAccept
import cloud.offthe.otc.i18n.commonBack
import cloud.offthe.otc.i18n.commonDelete
import cloud.offthe.otc.i18n.commonDone
import cloud.offthe.otc.i18n.commonDownload
import cloud.offthe.otc.i18n.commonNext
import cloud.offthe.otc.i18n.commonSend
import cloud.offthe.otc.i18n.commonShare
import cloud.offthe.otc.i18n.commonUpload
import cloud.offthe.otc.ui.settings.askIsPrimary
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow

// Port of Tour.swift: the first-run tour, tour 2 (the owner's request of
// 2026-10-27: show how things work, in each section, rather than say what
// each section is). A card RootView lays over MainView, in the same window
// rather than a Dialog, one step at a time: each step opens the section it
// talks about (MainView follows TourModel.step through TourRules.section),
// dims the app around the real control it teaches - a hole with an ember
// ring, where the control is on screen (TourTargets, marked with
// Modifier.tourTarget where each control is drawn) - and shows a small
// moving drawing of that control in use (TourArt.kt), so it teaches an empty
// library as well. Ending it - Skip, Done, Back - goes back to the section it
// opened over. It never opens Alerts, which marks every alert read: its last
// step points at Alerts and shows Settings. Nothing it does sends a request
// that changes anything (the sections it opens make their usual reads).
//
// It opens by itself once: when the phone is signed in, MainView is up (not
// the connection screen, the Bluetooth setup or a card saying the device
// can't be reached), the sync question is answered and this phone hasn't
// seen this version of it. "Seen" is marked the moment it opens by itself,
// so Skip, Done, Back or leaving the app midway all leave it seen; prefs
// that can't be read or written mean it doesn't open by itself. Settings'
// Getting started shows it again from its first step, the mark untouched.
//
// The mark is this phone's alone - no protocol change, nothing told to the
// device (the demo is one shared account) - in the otc_tour prefs: not
// wiped at Log Out (SecretsStore.logOut clears otc_secrets, otc_settings and
// otc_sync), and not backed up (backup_rules.xml and
// data_extraction_rules.xml list only otc_settings). Raise
// TourRules.VERSION only to show a rewritten tour again (2: a phone that saw
// the first tour sees this one once).
//
// Steps: welcome, search, dates, select, viewer, people (only while face
// recognition is on), collections, files, upload, social, post, friends,
// backup, users (only for the device's owner, who alone can add users),
// alerts. Every label a step quotes comes from that label's own key. The
// same steps, keys and rules as iOS (TourTest.kt <-> TourTests.swift, the
// same cases).

/** The tour's steps, in their fixed order. */
enum class TourStep { WELCOME, SEARCH, DATES, SELECT, VIEWER, PEOPLE, COLLECTIONS, FILES, UPLOAD, SOCIAL, POST, FRIENDS, BACKUP, USERS, ALERTS }

/** The real controls the steps ring, marked where they are drawn (Modifier.tourTarget). */
enum class TourTarget {
    /** Where the sections are: the bottom bar, or the menu on a wide window. */
    SECTIONS,
    /** The search field: Images' own, or the wide top bar's. */
    SEARCH,
    /** The date scrubber's strip along the photos' right edge. */
    SCRUBBER,
    /** The grid's first photo. */
    FIRST_PHOTO,
    /** Beside Images' search on a narrow window. */
    PEOPLE_BUTTON, COLLECTIONS_BUTTON,
    /** The People and Collections pages' first face and card (wide). */
    FIRST_FACE, FIRST_COLLECTION,
    /** Files' View options, its Upload and the first folder's lock. */
    VIEW_OPTIONS, UPLOAD, FOLDER_LOCK,
    /** The first post's heart and comment field. */
    LIKE, COMMENT,
    /** Social's + (narrow) or the top bar's + New post (wide). */
    NEW_POST,
    /** Social's two-people button (narrow). */
    FRIENDS_BUTTON,
    /** Friends' own address and the add-a-friend row. */
    ADDRESS, ADD_FRIEND,
    /** Settings: Sync Options' Wi-Fi only, Users' add row and bridge switch, Getting started's Show the tour. */
    WIFI_ONLY, ADD_USER, BRIDGE_ACCESS, SHOW_TOUR,
    /** Alerts and Settings: their tabs, or the menu's items. */
    ALERTS_SECTION, SETTINGS_SECTION,
}

/** The tour's rules, apart from any view (TourTest). Lengths in dp, as iOS's points. */
object TourRules {
    /** The tour this build shows (iOS `version`): raise it only for a rewritten tour. */
    const val VERSION = 2

    /** The steps shown: People only while face recognition is on, Users only for the device's owner (null, not known yet, leaves them out). */
    fun steps(faces: Boolean?, owner: Boolean?): List<TourStep> = TourStep.entries.filter {
        when (it) {
            TourStep.PEOPLE -> faces == true
            TourStep.USERS -> owner == true
            else -> true
        }
    }

    /** Whether the tour opens by itself now. [seenVersion] null: the mark can't be read, so it doesn't. */
    fun shouldShow(seenVersion: Int?, syncChoicePending: Boolean, signedIn: Boolean, mainVisible: Boolean): Boolean {
        val seen = seenVersion ?: return false
        return seen < VERSION && signedIn && mainVisible && !syncChoicePending
    }

    /** The step after [step] among [steps], by the fixed order (null after the last); [step] itself may be gone. */
    fun after(step: TourStep, steps: List<TourStep>): TourStep? = steps.firstOrNull { it.ordinal > step.ordinal }

    /** The step before [step] among [steps] (null before the first). */
    fun before(step: TourStep, steps: List<TourStep>): TourStep? = steps.lastOrNull { it.ordinal < step.ordinal }

    /** Where [step] is among [steps]: itself, or - gone, as People when face recognition is turned off - the next step. */
    fun place(step: TourStep, steps: List<TourStep>): TourStep =
        if (step in steps) step else after(step, steps) ?: before(step, steps) ?: TourStep.WELCOME

    /**
     * The section a step shows (null: the one the tour opened over). On a
     * narrow window People and Collections stay Images, whose buttons beside
     * the search the step rings, and Friends stays Social. Never Alerts:
     * opening it marks every alert read.
     */
    fun section(step: TourStep, wide: Boolean): Section? = when (step) {
        TourStep.WELCOME -> null
        TourStep.SEARCH, TourStep.DATES, TourStep.SELECT, TourStep.VIEWER -> Section.Images
        TourStep.PEOPLE -> if (wide) Section.People else Section.Images
        TourStep.COLLECTIONS -> if (wide) Section.Collections else Section.Images
        TourStep.FILES, TourStep.UPLOAD -> Section.Files
        TourStep.SOCIAL, TourStep.POST -> Section.Social
        TourStep.FRIENDS -> if (wide) Section.Friends else Section.Social
        TourStep.BACKUP, TourStep.USERS, TourStep.ALERTS -> Section.Settings
    }

    /** What a step rings, those of them on screen. */
    fun targets(step: TourStep, wide: Boolean): List<TourTarget> = when (step) {
        TourStep.WELCOME -> listOf(TourTarget.SECTIONS)
        TourStep.SEARCH -> listOf(TourTarget.SEARCH)
        TourStep.DATES -> listOf(TourTarget.SCRUBBER)
        TourStep.SELECT, TourStep.VIEWER -> listOf(TourTarget.FIRST_PHOTO)
        TourStep.PEOPLE -> listOf(if (wide) TourTarget.FIRST_FACE else TourTarget.PEOPLE_BUTTON)
        TourStep.COLLECTIONS -> listOf(if (wide) TourTarget.FIRST_COLLECTION else TourTarget.COLLECTIONS_BUTTON)
        TourStep.FILES -> listOf(TourTarget.VIEW_OPTIONS)
        TourStep.UPLOAD -> listOf(TourTarget.UPLOAD, TourTarget.FOLDER_LOCK)
        TourStep.SOCIAL -> listOf(TourTarget.LIKE, TourTarget.COMMENT)
        TourStep.POST -> listOf(TourTarget.NEW_POST)
        TourStep.FRIENDS -> if (wide) listOf(TourTarget.ADDRESS, TourTarget.ADD_FRIEND) else listOf(TourTarget.FRIENDS_BUTTON)
        TourStep.BACKUP -> listOf(TourTarget.WIFI_ONLY)
        TourStep.USERS -> listOf(TourTarget.ADD_USER, TourTarget.BRIDGE_ACCESS)
        // Show the tour sits too near the top of Settings to scroll clear of
        // a narrow window's card, which the bottom bar puts at the top.
        TourStep.ALERTS ->
            if (wide) listOf(TourTarget.ALERTS_SECTION, TourTarget.SETTINGS_SECTION, TourTarget.SHOW_TOUR)
            else listOf(TourTarget.ALERTS_SECTION, TourTarget.SETTINGS_SECTION)
    }

    /**
     * Where Settings scrolls for a step: the control, at this share of its
     * height from the top - clear of the card, which goes at the foot of a
     * narrow window. The last step brings Show the tour back in sight (rung
     * only on a wide window), where the tour ends.
     */
    fun settingsScroll(step: TourStep, wide: Boolean): Pair<TourTarget, Float>? = when (step) {
        TourStep.BACKUP -> TourTarget.WIFI_ONLY to (if (wide) 0.4f else 0.15f)
        TourStep.USERS -> TourTarget.ADD_USER to (if (wide) 0.4f else 0.15f)
        TourStep.ALERTS -> TourTarget.SHOW_TOUR to (if (wide) 0.4f else 0.5f)
        else -> null
    }

    // ---- holes and the card ----

    /** The hole's margin around its control. */
    const val HOLE_PAD = 6f
    /** Kept this far inside the window, so the ring shows whole. */
    const val HOLE_INSET = 2f
    /** Between the card and the window's edges (its safe area). */
    const val CARD_MARGIN = 12f
    /** Between the card and a hole beside it. */
    const val CARD_GAP = 16f
    /** The card beside the rings on a wide window. */
    const val CARD_WIDTH = 380f
    /** The card at the foot (or top) of a narrow window, at most. */
    const val SHEET_MAX_WIDTH = 560f

    /**
     * The holes for the controls in [rects] (window coordinates, in dp), one
     * each, padded and kept inside [bounds]: those not on screen left out,
     * those that would touch made one (the dimmed layer is drawn even-odd,
     * where two over each other would dim again).
     */
    fun holes(rects: List<Rect>, bounds: Rect): List<Rect> {
        val inner = bounds.deflate(HOLE_INSET)
        if (inner.width <= 0f || inner.height <= 0f) return emptyList()
        val holes = rects
            .filter { it.width > 0f && it.height > 0f && it.overlaps(bounds) }
            .map { it.inflate(HOLE_PAD).intersect(inner) }
            .filter { it.width > 0f && it.height > 0f }
            .toMutableList()
        var merged = true
        while (merged) {
            merged = false
            outer@ for (i in holes.indices) {
                for (j in i + 1 until holes.size) {
                    val a = holes[i]
                    val b = holes[j]
                    if (a.left <= b.right && b.left <= a.right && a.top <= b.bottom && b.top <= a.bottom) {
                        holes[i] = Rect(minOf(a.left, b.left), minOf(a.top, b.top), maxOf(a.right, b.right), maxOf(a.bottom, b.bottom))
                        holes.removeAt(j)
                        merged = true
                        break@outer
                    }
                }
            }
        }
        return holes
    }

    /** How much of the holes a card at [frame] covers. */
    fun covered(frame: Rect, holes: List<Rect>): Float = holes.sumOf { h ->
        val w = minOf(frame.right, h.right) - maxOf(frame.left, h.left)
        val hh = minOf(frame.bottom, h.bottom) - maxOf(frame.top, h.top)
        (if (w > 0f && hh > 0f) w * hh else 0f).toDouble()
    }.toFloat()

    /**
     * Where the card goes (iOS cardFrame). [area] is the window's safe area,
     * [card] the card's size. A narrow window ([sheet]) has it across the
     * foot, else across the top or in the middle, whichever covers less of
     * the holes. A wide one
     * has it beside them - to the right, below, to the left or above, the
     * roomiest first - and where no side has room, the corner that covers
     * the least; in the middle when nothing is ringed.
     */
    fun cardFrame(holes: List<Rect>, card: Size, area: Rect, sheet: Boolean): Rect {
        val m = CARD_MARGIN
        val height = minOf(card.height, maxOf(0f, area.height - 2 * m))
        if (sheet) {
            val width = minOf(area.width - 2 * m, SHEET_MAX_WIDTH)
            val x = area.center.x - width / 2
            val bottom = Rect(x, area.bottom - m - height, x + width, area.bottom - m)
            val top = Rect(x, area.top + m, x + width, area.top + m + height)
            val middle = Rect(x, area.center.y - height / 2, x + width, area.center.y + height / 2)
            return listOf(bottom, top, middle).minByOrNull { covered(it, holes) } ?: bottom
        }
        val width = minOf(card.width, area.width - 2 * m)
        fun clampX(x: Float) = maxOf(area.left + m, minOf(x, area.right - m - width))
        fun clampY(y: Float) = maxOf(area.top + m, minOf(y, area.bottom - m - height))
        fun at(x: Float, y: Float) = Rect(x, y, x + width, y + height)
        val middle = at(clampX(area.center.x - width / 2), clampY(area.center.y - height / 2))
        if (holes.isEmpty()) return middle
        val u = holes.reduce { a, b -> Rect(minOf(a.left, b.left), minOf(a.top, b.top), maxOf(a.right, b.right), maxOf(a.bottom, b.bottom)) }
        fun fits(r: Rect) = r.left >= area.left + m - 0.5f && r.top >= area.top + m - 0.5f &&
            r.right <= area.right - m + 0.5f && r.bottom <= area.bottom - m + 0.5f
        val sides = listOf(
            (area.right - u.right - CARD_GAP - m) / width to at(u.right + CARD_GAP, clampY(u.center.y - height / 2)),
            (area.bottom - u.bottom - CARD_GAP - m) / height to at(clampX(u.center.x - width / 2), u.bottom + CARD_GAP),
            (u.left - CARD_GAP - m - area.left) / width to at(u.left - CARD_GAP - width, clampY(u.center.y - height / 2)),
            (u.top - CARD_GAP - m - area.top) / height to at(clampX(u.center.x - width / 2), u.top - CARD_GAP - height),
        ).sortedByDescending { it.first }
        sides.firstOrNull { fits(it.second) && covered(it.second, holes) == 0f }?.let { return it.second }
        val spots = listOf(
            at(clampX(area.right), clampY(area.bottom)),
            at(clampX(area.left), clampY(area.bottom)),
            at(clampX(area.right), clampY(area.top)),
            at(clampX(area.left), clampY(area.top)),
            middle,
        )
        return spots.minByOrNull { covered(it, holes) } ?: middle
    }
}

/** Where "seen" is kept: on this phone only. */
interface TourStore {
    /** The version seen (0 for none); null when it can't be read. */
    fun seenVersion(): Int?

    /** Marks [version] seen; false when it couldn't be kept. */
    fun markSeen(version: Int): Boolean
}

/** The otc_tour prefs: kept through Log Out, never backed up. */
object PrefsTourStore : TourStore {
    const val PREFS = "otc_tour"
    const val KEY = "seen_version"

    private fun prefs() = OTCApp.instance.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    override fun seenVersion(): Int? = try { prefs().getInt(KEY, 0) } catch (_: Exception) { null }

    // commit, not apply: a write that fails is known, and the tour doesn't open.
    override fun markSeen(version: Int): Boolean = try { prefs().edit().putInt(KEY, version).commit() } catch (_: Exception) { false }
}

/** Whether the tour is up and on which step (iOS TourModel.shared). */
object TourModel {
    /** Tests put a store of their own here. */
    internal var store: TourStore = PrefsTourStore
    /** Whether the account signed in is the device's owner; tests put their own answer here. */
    internal var role: suspend () -> Boolean? = { askIsPrimary() }

    private val _shown = MutableStateFlow(false)
    /** Up: RootView draws TourView over MainView. */
    val shown: StateFlow<Boolean> = _shown

    private val _step = MutableStateFlow(TourStep.WELCOME)
    /** The step on screen, by its id: People and Users can come or go meanwhile (TourRules.place). */
    val step: StateFlow<TourStep> = _step

    private val _owner = MutableStateFlow<Boolean?>(null)
    /** Whether this account is the device's owner (Settings' Users shows for it alone): asked as the tour opens; null until known. */
    val owner: StateFlow<Boolean?> = _owner

    /** Opens the tour by itself when TourRules.shouldShow says so, marking it seen first: one that can't be marked doesn't open. */
    fun evaluate(syncChoicePending: Boolean, signedIn: Boolean, mainVisible: Boolean) {
        if (_shown.value) return
        if (!TourRules.shouldShow(store.seenVersion(), syncChoicePending, signedIn, mainVisible)) return
        if (!store.markSeen(TourRules.VERSION)) return
        open()
    }

    /** Settings' Show the tour: from the first step, the mark untouched. */
    fun replay() = open()

    /** Next: the step after, or Done on the last. */
    fun next(steps: List<TourStep>) {
        val after = TourRules.after(TourRules.place(_step.value, steps), steps)
        if (after != null) _step.value = after else close()
    }

    fun back(steps: List<TourStep>) {
        TourRules.before(TourRules.place(_step.value, steps), steps)?.let { _step.value = it }
    }

    /** Skip, Done, Back: MainView goes back to where it opened. */
    fun close() {
        _shown.value = false
    }

    /** Log Out (logOut): closed, back at the first step. The mark stays. */
    fun reset() {
        _shown.value = false
        _step.value = TourStep.WELCOME
        _owner.value = null
    }

    /** Who is signed in, for the Users step: asked once a tour shows (TourView). */
    suspend fun learnOwner() {
        role()?.let { _owner.value = it }
    }

    private fun open() {
        _step.value = TourStep.WELCOME
        _shown.value = true
    }
}

// ---- the controls on screen ----

/**
 * Where the controls the tour rings are, in the window's pixels: each mark
 * (Modifier.tourTarget) keeps its own frame here while it is laid out.
 * Kept quietly while the tour is down; while it is up (`live`) every change
 * bumps [version], which TourView reads.
 */
object TourTargets {
    private val frames = mutableMapOf<TourTarget, MutableMap<Any, Rect>>()
    private val _version = MutableStateFlow(0)
    val version: StateFlow<Int> = _version

    var live = false
        set(value) {
            if (value && !field) _version.value++
            field = value
        }

    fun put(target: TourTarget, key: Any, rect: Rect) {
        if (rect.width <= 0f || rect.height <= 0f) { remove(target, key); return }
        val m = frames.getOrPut(target) { mutableMapOf() }
        if (m[key] == rect) return
        m[key] = rect
        if (live) _version.value++
    }

    fun remove(target: TourTarget, key: Any) {
        if (frames[target]?.remove(key) != null && live) _version.value++
    }

    fun rects(target: TourTarget): List<Rect> = frames[target]?.values?.toList() ?: emptyList()
}

private class TourTargetNode(var target: TourTarget) : Node(), GlobalPositionAwareModifierNode {
    // Unclipped (not boundsInWindow): Settings scrolls to a row out of sight.
    override fun onGloballyPositioned(coordinates: LayoutCoordinates) {
        TourTargets.put(target, this, if (coordinates.isAttached) Rect(coordinates.positionInWindow(), coordinates.size.toSize()) else Rect.Zero)
    }

    override fun onDetach() {
        TourTargets.remove(target, this)
    }
}

private data class TourTargetElement(val target: TourTarget) : ModifierNodeElement<TourTargetNode>() {
    override fun create() = TourTargetNode(target)

    override fun update(node: TourTargetNode) {
        if (node.target != target) {
            TourTargets.remove(node.target, node)
            node.target = target
        }
    }
}

/** Marks the control a tour step rings (null: none). */
fun Modifier.tourTarget(target: TourTarget?): Modifier = if (target == null) this else this then TourTargetElement(target)

// ---- over MainView ----

private val Bold = mapOf("b" to RichTag(SpanStyle(fontWeight = FontWeight.Bold)))

/** Animations off in the system's developer or accessibility settings: the drawings stand still. */
internal fun animationsOff(context: Context): Boolean =
    try { Settings.Global.getFloat(context.contentResolver, Settings.Global.ANIMATOR_DURATION_SCALE, 1f) == 0f } catch (_: Exception) { false }

/** The UI checks of an empty library (a debuggable build only): no control rung, the drawing alone. */
internal var tourNoTargets = false
    set(value) {
        field = value && (OTCApp.instance.applicationInfo.flags and ApplicationInfo.FLAG_DEBUGGABLE) != 0
    }

/**
 * The tour itself: the app dimmed but for the holes, each ringed in ember,
 * and the card. Every touch on the dimmed app stops there; Back is Skip.
 */
@Composable
fun TourView() {
    val faces by FaceRecognition.enabled.collectAsState()
    val owner by TourModel.owner.collectAsState()
    val step by TourModel.step.collectAsState()
    val version by TourTargets.version.collectAsState()
    val steps = remember(faces, owner) { TourRules.steps(faces, owner) }
    val current = TourRules.place(step, steps)
    val context = LocalContext.current
    val reduceMotion = remember { animationsOff(context) }
    val density = LocalDensity.current
    val direction = LocalLayoutDirection.current
    var origin by remember { mutableStateOf(Offset.Zero) }
    var cardHeight by remember { mutableFloatStateOf(420f) }
    LaunchedEffect(Unit) { TourModel.learnOwner() }
    // Back is Skip, as the button at the top.
    BackHandler { TourModel.close() }
    val ember = menuColors().badge
    BoxWithConstraints(Modifier.fillMaxSize().onGloballyPositioned { origin = it.positionInWindow() }) {
        val w = maxWidth.value
        val h = maxHeight.value
        // MainView's rule: the menu beside the page from 600dp.
        val wide = menuLayout(w, true) != MenuLayout.NONE
        val insets = WindowInsets.safeDrawing
        val area = with(density) {
            Rect(
                insets.getLeft(this, direction) / this.density, insets.getTop(this) / this.density,
                w - insets.getRight(this, direction) / this.density, h - insets.getBottom(this) / this.density,
            )
        }
        val bounds = Rect(0f, 0f, w, h)
        // Read again whenever a mark moves (version) or the step changes.
        val rects = remember(version, current, wide, origin, density) {
            if (tourNoTargets) emptyList() else TourRules.targets(current, wide).flatMap { TourTargets.rects(it) }.map {
                Rect((it.left - origin.x) / density.density, (it.top - origin.y) / density.density, (it.right - origin.x) / density.density, (it.bottom - origin.y) / density.density)
            }
        }
        val holes = TourRules.holes(rects, bounds)
        val frame = TourRules.cardFrame(holes, Size(TourRules.CARD_WIDTH, cardHeight), area, sheet = !wide)
        val tallest = maxOf(0f, area.height - 2 * TourRules.CARD_MARGIN)
        val dark = MaterialTheme.colorScheme.surface.let { (it.red + it.green + it.blue) / 3f < 0.5f }
        Canvas(
            Modifier.fillMaxSize().clearAndSetSemantics {}
                .pointerInput(Unit) { awaitEachGesture { while (true) awaitPointerEvent().changes.forEach { it.consume() } } },
        ) {
            val px = density.density
            val path = Path().apply {
                fillType = PathFillType.EvenOdd
                addRect(Rect(Offset.Zero, size))
                holes.forEach { hole ->
                    val r = minOf(14f, minOf(hole.width, hole.height) / 2) * px
                    addRoundRect(RoundRect(Rect(hole.left * px, hole.top * px, hole.right * px, hole.bottom * px), CornerRadius(r, r)))
                }
            }
            drawPath(path, Color.Black.copy(alpha = if (dark) 0.62f else 0.5f))
            holes.forEach { hole ->
                val r = minOf(14f, minOf(hole.width, hole.height) / 2) * px
                drawRoundRect(
                    ember, topLeft = Offset(hole.left * px, hole.top * px), size = Size(hole.width * px, hole.height * px),
                    cornerRadius = CornerRadius(r, r), style = Stroke(width = 2 * px),
                )
            }
        }
        val x by animateDpAsState(frame.left.dp, if (reduceMotion) tween(0) else tween(300), label = "tourCardX")
        val y by animateDpAsState(frame.top.dp, if (reduceMotion) tween(0) else tween(300), label = "tourCardY")
        TourCard(
            steps, current, wide, compact = h < 560f,
            modifier = Modifier.offset(x, y).width(frame.width.dp).heightIn(max = tallest.dp)
                .onSizeChanged { cardHeight = it.height / density.density },
        )
    }
}

/** One step: where the tour is and Skip, the drawing, the title and the text (scrolling when the text is large), Back and Next. */
@Composable
private fun TourCard(steps: List<TourStep>, current: TourStep, wide: Boolean, compact: Boolean, modifier: Modifier) {
    val faces by FaceRecognition.enabled.collectAsState()
    val index = steps.indexOf(current).coerceAtLeast(0)
    val first = index == 0
    val last = index == steps.lastIndex
    val colors = menuColors()
    val progress = S.appTourProgress(index + 1, steps.size).resolve()
    val title = tourTitle(current)
    Surface(
        modifier.semantics { paneTitle = progress },
        shape = RoundedCornerShape(22.dp), color = colors.drawer, tonalElevation = 2.dp, shadowElevation = 12.dp,
        border = BorderStroke(0.5.dp, colors.line),
    ) {
        Column(Modifier.padding(start = 18.dp, end = 10.dp, top = 4.dp, bottom = 14.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(progress, Modifier.weight(1f), style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
                TextButton(onClick = { TourModel.close() }) { Text(S.appTourSkip().resolve()) }
            }
            Column(Modifier.weight(1f, fill = false).verticalScroll(rememberScrollState()).padding(end = 8.dp)) {
                TourArt(current, faces == true, wide, compact)
                Spacer(Modifier.padding(top = 10.dp))
                Text(
                    title, style = MaterialTheme.typography.titleLarge, fontWeight = FontWeight.Bold,
                    // Read out as the step changes, as a heading.
                    modifier = Modifier.semantics { heading(); liveRegion = LiveRegionMode.Polite },
                )
                Spacer(Modifier.padding(top = 6.dp))
                Text(tourText(current, wide, faces == true), style = MaterialTheme.typography.bodyLarge)
            }
            Spacer(Modifier.padding(top = 10.dp))
            TourButtons(first, last, steps, Modifier.fillMaxWidth().padding(end = 8.dp))
        }
    }
}

/** Back and Next at the card's foot, side by side while they fit, else Next over Back. */
@Composable
private fun TourButtons(first: Boolean, last: Boolean, steps: List<TourStep>, modifier: Modifier) {
    val colors = menuColors()
    BoxWithConstraints(modifier) {
        val next = @Composable { m: Modifier ->
            Button(
                onClick = { TourModel.next(steps) }, modifier = m,
                // The web's --ember-ink on --ember: white on ember is too faint to read.
                colors = ButtonDefaults.buttonColors(containerColor = colors.badge, contentColor = colors.badgeText),
            ) { Text((if (last) S.commonDone() else S.commonNext()).resolve(), fontWeight = FontWeight.SemiBold) }
        }
        val back = @Composable { m: Modifier ->
            OutlinedButton(onClick = { TourModel.back(steps) }, modifier = m) { Text(S.commonBack().resolve()) }
        }
        // Side by side from about two buttons' width of text.
        if (maxWidth >= 260.dp && LocalDensity.current.fontScale <= 1.6f) {
            Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(10.dp, Alignment.End)) {
                if (!first) back(Modifier)
                next(Modifier)
            }
        } else {
            Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                next(Modifier.fillMaxWidth())
                if (!first) back(Modifier.fillMaxWidth())
            }
        }
    }
}

@Composable
internal fun tourTitle(step: TourStep): String = when (step) {
    TourStep.WELCOME -> S.appTourWelcomeTitle()
    TourStep.SEARCH -> S.appTourSearchTitle()
    TourStep.DATES -> S.appTourDatesTitle()
    TourStep.SELECT -> S.appTourSelectTitle()
    TourStep.VIEWER -> S.appTourViewerTitle()
    TourStep.PEOPLE -> S.appTourPeopleTitle()
    TourStep.COLLECTIONS -> S.appTourCollectionsTitle()
    TourStep.FILES -> S.appTourFilesTitle()
    TourStep.UPLOAD -> S.appTourUploadTitle()
    TourStep.SOCIAL -> S.appTourSocialTitle()
    TourStep.POST -> S.appTourPostTitle()
    TourStep.FRIENDS -> S.appTourFriendsTitle()
    TourStep.BACKUP -> S.appTourBackupTitle()
    TourStep.USERS -> S.appTourUsersTitle()
    TourStep.ALERTS -> S.appTourAlertsTitle()
}.resolve()

/** A step's text: its key, or its key and the labels it quotes. */
internal sealed interface TourBody {
    data class Plain(val text: UiText) : TourBody
    data class Rich(val text: RichText, val more: RichText? = null) : TourBody
}

/** A step's text, every label it quotes from its own key, worded by [label] (iOS TourTexts.text). */
internal fun tourBody(step: TourStep, wide: Boolean, faces: Boolean, label: (UiText) -> String): TourBody = when (step) {
    TourStep.WELCOME -> TourBody.Plain(if (wide) S.appTourWelcomeTextWide() else S.appTourWelcomeText())
    TourStep.SEARCH -> TourBody.Plain(if (faces) S.appTourSearchTextFaces() else S.appTourSearchText())
    TourStep.DATES -> TourBody.Plain(S.appTourDatesText())
    TourStep.SELECT -> TourBody.Rich(S.appTourSelectText(label(S.commonShare()), label(S.commonDownload()), label(S.appShellAddToCollection()), label(S.commonDelete())))
    TourStep.VIEWER -> TourBody.Rich(S.appTourViewerText(label(S.appPhotosViewerMoreInfo())))
    TourStep.PEOPLE -> TourBody.Rich(
        if (wide) S.appTourPeopleTextWide(label(S.appPhotosPeopleAddName()), label(S.appPhotosPeopleMerge()))
        else S.appTourPeopleText(label(S.appPhotosPeopleAddName()), label(S.appPhotosPeopleMerge())),
    )
    TourStep.COLLECTIONS -> TourBody.Rich(
        if (wide) S.appTourCollectionsTextWide(label(S.appShellAddToCollection()), label(S.appPhotosCollectionsNewItem()))
        else S.appTourCollectionsText(label(S.appShellAddToCollection()), label(S.appPhotosCollectionsNewItem())),
    )
    TourStep.FILES -> TourBody.Rich(S.appTourFilesText(label(S.appFilesViewList()), label(S.appFilesViewGrid()), label(S.appFilesViewStorage()), label(S.appFilesSortBy())))
    TourStep.UPLOAD -> TourBody.Rich(S.appTourUploadText(label(S.commonUpload()), label(S.commonShare()), label(S.commonDownload())))
    TourStep.SOCIAL -> TourBody.Plain(S.appTourSocialText())
    TourStep.POST -> TourBody.Rich(S.appTourPostText(label(S.appSocialComposeSourcePhone()), label(S.appSocialComposeSourceSynced()), label(S.appSocialComposePublish())))
    TourStep.FRIENDS -> TourBody.Rich(
        if (wide) S.appTourFriendsTextWide(label(S.appSocialFriendsAddHeading()), label(S.commonSend()), label(S.appSocialFriendsWaitingAnswer()), label(S.commonAccept()))
        else S.appTourFriendsText(label(S.appSocialFriendsAddHeading()), label(S.commonSend()), label(S.appSocialFriendsWaitingAnswer()), label(S.commonAccept())),
    )
    TourStep.BACKUP -> TourBody.Rich(S.appTourBackupText(label(S.appSettingsSyncTitle()), label(S.appSettingsSyncWifiOnly())))
    // As the web's: the switch's sentence after the step's own.
    TourStep.USERS -> TourBody.Rich(
        S.appTourUsersText(label(S.appShellSectionSettings()), label(S.appSettingsUsersTitle()), label(S.commonAdd())),
        S.appTourUsersBridge(label(S.appSettingsUsersRequestBridge())),
    )
    TourStep.ALERTS -> TourBody.Rich(S.appTourAlertsText(label(S.appShellSectionAlerts()), label(S.appShellSectionSettings()), label(S.appTourShow())))
}

/** A step's text, in the composition's language, its labels in bold. */
@Composable
internal fun tourText(step: TourStep, wide: Boolean, faces: Boolean): AnnotatedString {
    val res = LocalResources.current
    return when (val body = tourBody(step, wide, faces) { it.resolve(res) }) {
        is TourBody.Plain -> AnnotatedString(body.text.resolve(res))
        is TourBody.Rich -> body.more?.let { more -> body.text.resolve(res, Bold) + AnnotatedString(" ") + more.resolve(res, Bold) } // i18n-ignore: the space between two sentences
            ?: body.text.resolve(res, Bold)
    }
}
