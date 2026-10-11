// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui

import androidx.activity.compose.BackHandler
import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.tween
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.slideInHorizontally
import androidx.compose.animation.slideOutHorizontally
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.indication
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.ScrollState
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.WindowInsetsSides
import androidx.compose.foundation.layout.only
import androidx.compose.foundation.layout.safeDrawing
import androidx.compose.foundation.layout.windowInsetsPadding
import androidx.compose.foundation.relocation.BringIntoViewRequester
import androidx.compose.foundation.relocation.bringIntoViewRequester
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.LocalTextStyle
import androidx.compose.material3.Text
import androidx.compose.material3.ripple
import androidx.compose.runtime.Composable
import androidx.compose.runtime.Immutable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.withFrameNanos
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.rotate
import androidx.compose.ui.draw.shadow
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.graphics.luminance
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.em
import androidx.compose.ui.unit.sp
import cloud.offthe.otc.i18n.LanguageSettings
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.androidMenuSections
import cloud.offthe.otc.i18n.androidStorageGauge
import cloud.offthe.otc.i18n.androidStorageGaugeAttention
import cloud.offthe.otc.i18n.androidStorageGaugeUsed
import cloud.offthe.otc.i18n.androidStorageGaugeUsedAttention
import cloud.offthe.otc.i18n.androidStorageHideDetails
import cloud.offthe.otc.i18n.androidStorageHideDetailsAttention
import cloud.offthe.otc.i18n.androidStorageShowDetails
import cloud.offthe.otc.i18n.androidStorageShowDetailsAttention
import cloud.offthe.otc.i18n.androidStorageUsed
import cloud.offthe.otc.i18n.appShellMenuDevice
import cloud.offthe.otc.i18n.appShellMenuHide
import cloud.offthe.otc.i18n.appShellMenuSharing
import cloud.offthe.otc.i18n.appShellMenuShow
import cloud.offthe.otc.i18n.appShellMirrorDegraded
import cloud.offthe.otc.i18n.appShellMirrorInSync
import cloud.offthe.otc.i18n.appShellMirrorNone
import cloud.offthe.otc.i18n.appShellMirrorSyncing
import cloud.offthe.otc.i18n.appShellMirrorUnknown
import cloud.offthe.otc.i18n.appShellPercent
import cloud.offthe.otc.i18n.appShellPrivacy
import cloud.offthe.otc.i18n.appShellSectionAlerts
import cloud.offthe.otc.i18n.appShellSectionCollections
import cloud.offthe.otc.i18n.appShellSectionFiles
import cloud.offthe.otc.i18n.appShellSectionFriends
import cloud.offthe.otc.i18n.appShellSectionImages
import cloud.offthe.otc.i18n.appShellSectionPeople
import cloud.offthe.otc.i18n.appShellSectionSocial
import cloud.offthe.otc.i18n.appShellSectionUnread
import cloud.offthe.otc.i18n.appShellStorage
import cloud.offthe.otc.i18n.appShellStorageCpu
import cloud.offthe.otc.i18n.appShellStorageDisks
import cloud.offthe.otc.i18n.appShellStorageFree
import cloud.offthe.otc.i18n.appShellStorageMemory
import cloud.offthe.otc.i18n.appShellStorageOf
import cloud.offthe.otc.i18n.appShellStorageReading
import cloud.offthe.otc.i18n.appShellStorageSystemCard
import cloud.offthe.otc.i18n.appShellStorageUnavailable
import cloud.offthe.otc.i18n.appShellStorageUsed
import cloud.offthe.otc.i18n.appShellTerms
import cloud.offthe.otc.i18n.appShellSectionSettings
import cloud.offthe.otc.proto.RaidState
import cloud.offthe.otc.proto.Status
import cloud.offthe.otc.ui.common.FitText
import cloud.offthe.otc.ui.common.NavIcons
import cloud.offthe.otc.ui.common.Share
import cloud.offthe.otc.ui.settings.sizeText
import kotlin.math.max
import kotlin.math.min
import kotlin.math.roundToInt
import java.text.NumberFormat
import java.util.Locale

// Port of the web's Sidebar.tsx and Sidebar.css: the left menu of the wide
// layout (MainView, a window 600dp or more across - a phone turned
// sideways, an unfolded Fold, a tablet). Grouped like a photo library -
// what is stored (Images, People while face recognition is on,
// Collections, Files), what is shared (Social, Friends), the device
// (Alerts, Settings) - with the storage at its foot. Beside the page it is
// the whole menu (FULL: pill rows under headings, the storage bar) or a
// rail (RAIL: an icon in a pill with its label under it, a hairline
// between the groups, the storage as a ring gauge); the drawer lays the
// whole menu over the page. The icons are NavIcons, the web's own.

/**
 * The app's sections. [id] is the one the bottom bar's tabs always had
 * (kept in the saved state); People, Collections and Friends are sections
 * of their own in the wide menu, and on a narrow window are shown by the
 * tab in [tab] (Images' People page and Collections sheet, Social's
 * Friends sheet).
 */
enum class Section(val id: Int) {
    Alerts(0),
    Social(1),
    Files(3),
    Images(4),
    Settings(5),
    People(6),
    Collections(7),
    Friends(8);

    /** Its name in the menu and the bottom bar, resolved where it is shown. */
    val label: UiText get() = when (this) {
        Alerts -> S.appShellSectionAlerts()
        Social -> S.appShellSectionSocial()
        Files -> S.appShellSectionFiles()
        Images -> S.appShellSectionImages()
        Settings -> S.appShellSectionSettings()
        People -> S.appShellSectionPeople()
        Collections -> S.appShellSectionCollections()
        Friends -> S.appShellSectionFriends()
    }

    /** The bottom bar's tab that shows this section on a narrow window. */
    val tab: Section get() = when (this) {
        People, Collections -> Images
        Friends -> Social
        else -> this
    }

    val icon: ImageVector get() = when (this) {
        Alerts -> NavIcons.Alerts
        Social -> NavIcons.Social
        Files -> NavIcons.Files
        Images -> NavIcons.Images
        Settings -> NavIcons.Settings
        People -> NavIcons.People
        Collections -> NavIcons.Collections
        Friends -> NavIcons.Friends
    }

    companion object {
        fun of(id: Int): Section = entries.firstOrNull { it.id == id } ?: Social
    }
}

/** Beside the page: the whole menu, the rail, or nothing (a narrow window's bottom bar). */
enum class MenuLayout { FULL, RAIL, NONE }

/** The web's breakpoints (App.tsx): the rail from 600dp, the whole menu (or the rail, by choice) from 1024dp. */
const val MENU_RAIL_MIN_DP = 600
const val MENU_FULL_MIN_DP = 1024
val SIDEBAR_WIDTH = 256.dp
val RAIL_WIDTH = 80.dp

fun menuLayout(widthDp: Float, menuOpen: Boolean): MenuLayout = when {
    widthDp >= MENU_FULL_MIN_DP -> if (menuOpen) MenuLayout.FULL else MenuLayout.RAIL
    widthDp >= MENU_RAIL_MIN_DP -> MenuLayout.RAIL
    else -> MenuLayout.NONE
}

/** A group of the menu: a heading (none for the first) over its sections. */
data class MenuGroup(val heading: UiText?, val items: List<Section>)

/** The menu's groups; People only while face recognition is on. */
fun menuGroups(faces: Boolean): List<MenuGroup> = listOf(
    MenuGroup(null, listOfNotNull(Section.Images, Section.People.takeIf { faces }, Section.Collections, Section.Files)),
    MenuGroup(S.appShellMenuSharing(), listOf(Section.Social, Section.Friends)),
    MenuGroup(S.appShellMenuDevice(), listOf(Section.Alerts, Section.Settings)),
)

/** The open section, as the menu marks it: a collection open in Images is still Collections. */
fun menuCurrent(section: Section, collectionOpen: Boolean): Section =
    if (section == Section.Images && collectionOpen) Section.Collections else section

// ---- storage ----

/** Fuller than comfortable: the meter turns ember, then red. */
enum class StorageLevel { OK, WARN, CRIT }

/** What the menu says about the storage (the web's summarize); its text is resolved where it is shown. */
data class StorageSummary(
    val status: Status?,
    val usedPct: Double,
    val level: StorageLevel,
    val degraded: Boolean,
    val attention: Boolean,
    val usedText: UiText,
    val mirrorText: UiText,
)

/** Rounded to two decimals, kept within 0-100 (the web's round). */
internal fun pctRound(x: Double): Double = (Math.round(x * 100) / 100.0).coerceIn(0.0, 100.0)

/**
 * A percentage as the web writes it: "45", "45.5", "45.67" - with the
 * decimal mark of the app's language ("45,5" in Spanish), as iOS's
 * StorageSummary and the Settings status line (decimalText) write it.
 */
internal fun pctText(x: Double, locale: Locale = LanguageSettings.numberLocale): String {
    val r = pctRound(x)
    if (r == Math.floor(r)) return r.toLong().toString()
    return NumberFormat.getNumberInstance(locale).apply {
        isGroupingUsed = false
        maximumFractionDigits = 2
    }.format(r)
}

internal fun usedPct(used: Int, total: Int): Double = if (used <= 0 || total <= 0) 0.0 else pctRound(used.toDouble() / total * 100)

/** Issue #65: the mirror's own health (the web's raidStateLabel). */
internal fun mirrorLabel(s: Status): UiText = when (s.raidState) {
    RaidState.RaidNone -> S.appShellMirrorNone()
    RaidState.RaidInSync -> S.appShellMirrorInSync()
    RaidState.RaidSyncing -> S.appShellMirrorSyncing(pctText(s.raidSyncPercent.toDouble()))
    RaidState.RaidDegraded -> S.appShellMirrorDegraded()
    else -> S.appShellMirrorUnknown()
}

fun summarizeStorage(status: Status?, error: String?): StorageSummary {
    val pct = if (status == null) 0.0 else usedPct(status.raidUsage, status.raidSize)
    val degraded = status?.raidState == RaidState.RaidDegraded
    return StorageSummary(
        status = status,
        usedPct = pct,
        level = if (pct >= 90) StorageLevel.CRIT else if (pct >= 70) StorageLevel.WARN else StorageLevel.OK,
        degraded = degraded,
        attention = degraded || (status?.errorsCount ?: 0) > 0,
        usedText = when {
            status != null -> S.appShellStorageUsed(sizeText(status.raidUsage.toDouble()), sizeText(status.raidSize.toDouble()))
            error != null -> S.appShellStorageUnavailable()
            else -> S.appShellStorageReading()
        },
        mirrorText = status?.let(::mirrorLabel) ?: UiText.Raw(""),
    )
}

// ---- colours ----

/**
 * The web's palette (index.css) over this app's light or dark theme: the
 * page's surface and text, the gold of the open section, the ember of the
 * unread count. Gold on white is unreadable, so a light theme takes a
 * deeper amber for it.
 */
@Immutable
internal data class MenuColors(
    val surface: Color,
    val drawer: Color,
    val text: Color,
    val dim: Color,
    val line: Color,
    val accent: Color,
    val accentFill: Color,
    val railFill: Color,
    val badge: Color,
    val badgeText: Color,
    val warn: Color,
    val danger: Color,
    val track: Color,
)

private val Gold = Color(0xFFFFC857)
private val EmberColor = Color(0xFFFF6B4A)

@Composable
internal fun menuColors(): MenuColors {
    val c = MaterialTheme.colorScheme
    val dark = c.surface.luminance() < 0.5f
    return remember(c, dark) {
        MenuColors(
            surface = c.surface,
            drawer = c.surfaceContainerLow,
            text = c.onSurface,
            dim = c.onSurfaceVariant,
            line = c.outlineVariant,
            accent = if (dark) Gold else Color(0xFF7A5300),
            accentFill = if (dark) Gold.copy(alpha = 0.14f) else Gold.copy(alpha = 0.32f),
            railFill = if (dark) Gold.copy(alpha = 0.16f) else Gold.copy(alpha = 0.36f),
            badge = EmberColor,
            badgeText = Color(0xFF26100A),
            warn = EmberColor,
            danger = if (dark) Color(0xFFFF8A75) else Color(0xFFD32F2F),
            track = c.surfaceContainerHighest,
        )
    }
}

// ---- the menu ----

/**
 * The menu beside the page: [layout] FULL or RAIL ([compact]: a short
 * window, where the rail tightens so all of it still fits). [onStorage]:
 * the rail's gauge, which asks for the whole menu at the storage details.
 */
@Composable
fun Sidebar(
    layout: MenuLayout, current: Section, faces: Boolean, unread: Int, storage: StorageSummary,
    details: Boolean, onToggleDetails: () -> Unit, onPick: (Section) -> Unit, onStorage: () -> Unit,
    compact: Boolean, revealStorage: Boolean = false, onStorageRevealed: () -> Unit = {}, modifier: Modifier = Modifier,
) {
    if (layout == MenuLayout.NONE) return
    val colors = menuColors()
    val groups = remember(faces) { menuGroups(faces) }
    val sections = S.androidMenuSections().resolve()
    Box(
        modifier.fillMaxHeight().width(if (layout == MenuLayout.FULL) SIDEBAR_WIDTH else RAIL_WIDTH).background(colors.surface)
            .semantics { contentDescription = sections },
    ) {
        if (layout == MenuLayout.RAIL) RailMenu(groups, current, unread, storage, compact, colors, onPick, onStorage)
        else FullMenu(groups, current, unread, storage, details, onToggleDetails, colors, revealStorage, onStorageRevealed, onPick)
    }
}

/**
 * The whole menu over the page, from the top bar's menu button on a window
 * too narrow for it beside the page (600-1023dp, or the gauge there):
 * over a dimmed page whose tap closes it, as does a pick and the back.
 */
@Composable
fun MenuDrawer(
    open: Boolean, onClose: () -> Unit, current: Section, faces: Boolean, unread: Int, storage: StorageSummary,
    details: Boolean, onToggleDetails: () -> Unit, onPick: (Section) -> Unit,
    revealStorage: Boolean = false, onStorageRevealed: () -> Unit = {},
) {
    val colors = menuColors()
    val groups = remember(faces) { menuGroups(faces) }
    val sections = S.androidMenuSections().resolve()
    // Registered as it opens, so it comes before the page under it
    // (People's, Collections', Friends' own back, registered later than an
    // always-present handler here would be).
    if (open) BackHandler { onClose() }
    BoxWithConstraints(Modifier.fillMaxSize()) {
        AnimatedVisibility(open, enter = fadeIn(tween(250)), exit = fadeOut(tween(200))) {
            Box(
                Modifier.fillMaxSize().background(Color.Black.copy(alpha = 0.5f))
                    .clickable(interactionSource = remember { MutableInteractionSource() }, indication = null, onClick = onClose),
            )
        }
        AnimatedVisibility(
            open,
            enter = slideInHorizontally(tween(250)) { -it },
            exit = slideOutHorizontally(tween(200)) { -it },
        ) {
            // From the window's edge, under a cutout too, its content clear of it.
            Box(
                Modifier.fillMaxHeight()
                    .shadow(16.dp, RoundedCornerShape(topEnd = 16.dp, bottomEnd = 16.dp))
                    .clip(RoundedCornerShape(topEnd = 16.dp, bottomEnd = 16.dp))
                    .background(colors.drawer)
                    .windowInsetsPadding(WindowInsets.safeDrawing.only(WindowInsetsSides.Start))
                    .width(min(SIDEBAR_WIDTH.value, (maxWidth - 56.dp).value).dp)
                    .semantics { contentDescription = sections },
            ) {
                FullMenu(
                    groups, current, unread, storage, details, onToggleDetails, colors.copy(surface = colors.drawer),
                    revealStorage, onStorageRevealed,
                ) { s ->
                    onPick(s)
                    onClose()
                }
            }
        }
    }
}

/**
 * A column that scrolls when its content is taller than it, and otherwise
 * stretches it to its height, so what follows a weighted spacer sits at
 * its foot (the web's min-height: 100% with a flex filler).
 */
@Composable
private fun FillingScrollColumn(
    padding: androidx.compose.foundation.layout.PaddingValues, scroll: ScrollState = rememberScrollState(),
    content: @Composable ColumnScope.() -> Unit,
) {
    BoxWithConstraints(Modifier.fillMaxSize()) {
        Column(
            Modifier.fillMaxWidth().verticalScroll(scroll).heightIn(min = maxHeight).padding(padding),
            content = content,
        )
    }
}

/** The whole menu: pill rows with an icon and a label, under headings; the storage and the legal links at the foot. */
@Composable
private fun FullMenu(
    groups: List<MenuGroup>, current: Section, unread: Int, storage: StorageSummary,
    details: Boolean, onToggleDetails: () -> Unit, colors: MenuColors,
    revealStorage: Boolean, onStorageRevealed: () -> Unit, onPick: (Section) -> Unit,
) {
    // After the rail's gauge the whole menu shows the storage at its foot,
    // scrolled into view; otherwise the open section is in view.
    val scroll = rememberScrollState()
    LaunchedEffect(revealStorage) {
        if (!revealStorage) return@LaunchedEffect
        withFrameNanos { }
        scroll.animateScrollTo(scroll.maxValue)
        onStorageRevealed()
    }
    FillingScrollColumn(androidx.compose.foundation.layout.PaddingValues(horizontal = 12.dp, vertical = 8.dp), scroll) {
        for (g in groups) {
            Column(verticalArrangement = Arrangement.spacedBy(2.dp)) {
                g.heading?.let { h ->
                    Text(
                        h.resolve().uppercase(), fontSize = 12.sp, lineHeight = 16.sp, fontWeight = FontWeight.SemiBold, letterSpacing = 0.04.em,
                        color = colors.dim, modifier = Modifier.padding(start = 16.dp, top = 16.dp, bottom = 4.dp).semantics { heading() },
                    )
                }
                for (s in g.items) FullItem(s, s == current, if (s == Section.Alerts) unread else 0, colors, bringIntoView = !revealStorage) { onPick(s) }
            }
        }
        Spacer(Modifier.weight(1f).heightIn(min = 16.dp))
        StoragePanel(storage, details, onToggleDetails, colors)
        LegalLinks(colors)
    }
}

@Composable
private fun FullItem(s: Section, active: Boolean, count: Int, colors: MenuColors, bringIntoView: Boolean, onClick: () -> Unit) {
    val inView = rememberActiveInView(active, enabled = bringIntoView)
    val label = s.label.resolve()
    val described = if (count > 0) S.appShellSectionUnread(label, count).resolve() else label
    Row(
        Modifier.fillMaxWidth().then(inView).heightIn(min = 44.dp).clip(CircleShape)
            .background(if (active) colors.accentFill else Color.Transparent)
            .clickable(role = Role.Tab, onClick = onClick)
            .semantics(mergeDescendants = true) {
                selected = active
                contentDescription = described
            }
            .padding(horizontal = 16.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(16.dp),
    ) {
        Icon(s.icon, null, Modifier.size(22.dp), tint = if (active) colors.accent else colors.dim)
        Text(
            label, Modifier.weight(1f), fontSize = 15.sp, fontWeight = if (active) FontWeight.SemiBold else FontWeight.Normal,
            color = if (active) colors.accent else colors.text, maxLines = 1, overflow = TextOverflow.Ellipsis,
        )
        if (count > 0) CountBadge(count, colors, small = false)
    }
}

/**
 * The open section kept in view in a menu that scrolls (a short window):
 * when the menu appears (a turn of the phone, the drawer opening) and when
 * the section changes from elsewhere (the search, an alert).
 */
@Composable
private fun rememberActiveInView(active: Boolean, enabled: Boolean = true): Modifier {
    val requester = remember { BringIntoViewRequester() }
    // [enabled] as it is when the section changes: the storage revealed
    // and then let go must not pull the menu back up to the section.
    val allowed by rememberUpdatedState(enabled)
    LaunchedEffect(active) {
        if (!active || !allowed) return@LaunchedEffect
        withFrameNanos { }
        if (allowed) requester.bringIntoView()
    }
    return Modifier.bringIntoViewRequester(requester)
}

/** The unread count: an ember pill; [small] (the rail's) ringed in the menu's colour, to stand off the icon. */
@Composable
private fun CountBadge(count: Int, colors: MenuColors, small: Boolean, modifier: Modifier = Modifier) {
    val h = if (small) 16.dp else 20.dp
    Box(modifier.let { if (small) it.background(colors.surface, CircleShape).padding(2.dp) else it }) {
        Box(
            Modifier.height(h).widthIn(min = h).clip(CircleShape).background(colors.badge).padding(horizontal = if (small) 4.dp else 6.dp),
            contentAlignment = Alignment.Center,
        ) {
            Text(
                if (count > 99) "99+" else count.toString(), color = colors.badgeText, fontSize = if (small) 10.sp else 11.sp,
                lineHeight = if (small) 16.sp else 20.sp, fontWeight = FontWeight.Bold, maxLines = 1,
            )
        }
    }
}

/** The rail: the same items as columns (an icon in a pill, the label under it), a hairline between the groups, storage as a gauge. */
@Composable
private fun RailMenu(
    groups: List<MenuGroup>, current: Section, unread: Int, storage: StorageSummary, compact: Boolean,
    colors: MenuColors, onPick: (Section) -> Unit, onStorage: () -> Unit,
) {
    FillingScrollColumn(androidx.compose.foundation.layout.PaddingValues(vertical = if (compact) 4.dp else 8.dp)) {
        groups.forEachIndexed { i, g ->
            if (i > 0) Box(Modifier.padding(vertical = if (compact) 4.dp else 8.dp).align(Alignment.CenterHorizontally).width(40.dp).height(1.dp).background(colors.line))
            Column(verticalArrangement = Arrangement.spacedBy(if (compact) 2.dp else 4.dp)) {
                for (s in g.items) RailItem(s, s == current, if (s == Section.Alerts) unread else 0, compact, colors) { onPick(s) }
            }
        }
        Spacer(Modifier.weight(1f).heightIn(min = 8.dp))
        StorageGauge(storage, compact, colors, onStorage)
    }
}

@Composable
private fun RailItem(s: Section, active: Boolean, count: Int, compact: Boolean, colors: MenuColors, onClick: () -> Unit) {
    val press = remember { MutableInteractionSource() }
    // The open section's pill grows from the middle as it is picked.
    val shown by animateFloatAsState(if (active) 1f else 0f, tween(250), label = "pill")
    val tint = if (active) colors.accent else colors.dim
    val inView = rememberActiveInView(active)
    val label = s.label.resolve()
    val described = if (count > 0) S.appShellSectionUnread(label, count).resolve() else label
    Column(
        Modifier.fillMaxWidth().then(inView).heightIn(min = if (compact) 52.dp else 56.dp)
            .clickable(interactionSource = press, indication = null, role = Role.Tab, onClick = onClick)
            .semantics(mergeDescendants = true) {
                selected = active
                contentDescription = described
            }
            .padding(horizontal = 2.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.spacedBy(if (compact) 2.dp else 4.dp, Alignment.CenterVertically),
    ) {
        Box(Modifier.size(56.dp, 32.dp)) {
            Box(
                Modifier.fillMaxSize().graphicsLayer { scaleX = 0.4f + 0.6f * shown; alpha = shown }
                    .clip(RoundedCornerShape(16.dp)).background(colors.railFill),
            )
            Box(Modifier.fillMaxSize().clip(RoundedCornerShape(16.dp)).indication(press, ripple()), contentAlignment = Alignment.Center) {
                Icon(s.icon, null, Modifier.size(24.dp), tint = tint)
            }
            // On the pill's corner, ringed in the rail's colour so it stands off the icon.
            if (count > 0) CountBadge(count, colors, small = true, modifier = Modifier.offset(x = 30.dp, y = (-2).dp))
        }
        // Smaller rather than cut, down to 80% as iOS's rail (FitText).
        FitText(
            label,
            style = LocalTextStyle.current.copy(
                fontSize = 11.sp, lineHeight = 14.sp, fontWeight = if (active) FontWeight.Bold else FontWeight.Medium,
                textAlign = TextAlign.Center,
            ),
            color = tint, minScale = 0.8f,
        )
    }
}

/** The rail's storage: a ring filling up as the disks do, the percentage under it; a tap opens the whole menu at the details. */
@Composable
private fun StorageGauge(s: StorageSummary, compact: Boolean, colors: MenuColors, onClick: () -> Unit) {
    val press = remember { MutableInteractionSource() }
    val shown = if (s.status != null) S.appShellPercent(s.usedPct.roundToInt().toString()).resolve() else "–"
    val described = when {
        s.status != null && s.attention -> S.androidStorageGaugeUsedAttention(shown)
        s.status != null -> S.androidStorageGaugeUsed(shown)
        s.attention -> S.androidStorageGaugeAttention()
        else -> S.androidStorageGauge()
    }.resolve()
    val value = when (s.level) {
        StorageLevel.CRIT -> colors.danger
        StorageLevel.WARN -> colors.warn
        StorageLevel.OK -> colors.accent
    }
    Column(
        Modifier.fillMaxWidth().heightIn(min = if (compact) 52.dp else 56.dp)
            .clickable(interactionSource = press, indication = null, role = Role.Button, onClick = onClick)
            .semantics(mergeDescendants = true) {
                contentDescription = described
            },
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.spacedBy(if (compact) 2.dp else 4.dp, Alignment.CenterVertically),
    ) {
        Box(Modifier.size(56.dp, 32.dp), contentAlignment = Alignment.Center) {
            Box(Modifier.fillMaxSize().clip(RoundedCornerShape(16.dp)).indication(press, ripple()))
            val track = colors.line
            val pct = s.usedPct
            Canvas(Modifier.size(32.dp)) {
                // The web's 36-unit ring: radius 15.5, stroke 3.
                val unit = size.width / 36f
                val r = 15.5f * unit
                val stroke = 3f * unit
                drawCircle(track, radius = r, style = Stroke(stroke))
                if (pct > 0) drawArc(
                    value, startAngle = -90f, sweepAngle = (360.0 * pct / 100.0).toFloat(), useCenter = false,
                    topLeft = Offset(center.x - r, center.y - r), size = Size(2 * r, 2 * r), style = Stroke(stroke, cap = StrokeCap.Round),
                )
            }
            Icon(NavIcons.Storage, null, Modifier.size(14.dp), tint = colors.dim)
            // On the ring's top right, where it would be read first.
            if (s.attention) Box(
                Modifier.align(Alignment.TopStart).offset(x = 33.dp, y = (-1).dp).size(12.dp)
                    .background(colors.surface, CircleShape).padding(2.dp).background(colors.danger, CircleShape),
            )
        }
        Text(shown, fontSize = 11.sp, lineHeight = 14.sp, fontWeight = FontWeight.Medium, color = colors.dim, maxLines = 1)
    }
}

/** Storage at the foot of the whole menu: how full, the mirror's health; the rest of the device's status (disks, CPU, memory, errors) on a tap. */
@Composable
private fun StoragePanel(s: StorageSummary, details: Boolean, onToggle: () -> Unit, colors: MenuColors) {
    val turn by animateFloatAsState(if (details) 180f else 0f, tween(200), label = "chevron")
    val described = when {
        s.attention && details -> S.androidStorageHideDetailsAttention()
        s.attention -> S.androidStorageShowDetailsAttention()
        details -> S.androidStorageHideDetails()
        else -> S.androidStorageShowDetails()
    }.resolve()
    val meter = S.androidStorageUsed(pctText(s.usedPct)).resolve()
    Column(Modifier.fillMaxWidth()) {
        Box(Modifier.fillMaxWidth().height(1.dp).background(colors.line))
        Spacer(Modifier.height(8.dp))
        Row(
            Modifier.fillMaxWidth().heightIn(min = 44.dp).clip(CircleShape).clickable(onClick = onToggle)
                .semantics(mergeDescendants = true) {
                    contentDescription = described
                }
                .padding(horizontal = 16.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(16.dp),
        ) {
            Icon(NavIcons.Storage, null, Modifier.size(22.dp), tint = colors.dim)
            // Almacenamiento and Armazenamento are a little wider than the room: smaller rather than cut.
            FitText(S.appShellStorage().resolve(), Modifier.weight(1f), style = LocalTextStyle.current.copy(fontSize = 15.sp), color = colors.text, minScale = 0.8f)
            if (s.attention) Box(Modifier.size(8.dp).clip(CircleShape).background(colors.danger))
            Icon(NavIcons.Chevron, null, Modifier.size(18.dp).rotate(turn), tint = colors.dim)
        }
        val fill = when (s.level) {
            StorageLevel.CRIT -> colors.danger
            StorageLevel.WARN -> colors.warn
            StorageLevel.OK -> colors.accent
        }
        Box(
            Modifier.padding(start = 16.dp, end = 16.dp, top = 4.dp, bottom = 8.dp).fillMaxWidth().height(4.dp)
                .clip(CircleShape).background(colors.track)
                .semantics { contentDescription = meter },
        ) {
            Box(Modifier.fillMaxHeight().fillMaxWidth((s.usedPct / 100.0).toFloat().coerceIn(0f, 1f)).clip(CircleShape).background(fill))
        }
        StorageText(s.usedText.resolve(), colors.dim)
        // Its line is kept while the status loads, so the block doesn't grow when it comes.
        StorageText(s.mirrorText.resolve().ifEmpty { " " }, if (s.degraded) colors.danger else colors.dim)
        val st = s.status
        if (details && st != null) {
            Column(Modifier.padding(start = 16.dp, end = 16.dp, top = 8.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
                DetailRow(S.appShellStorageDisks().resolve(), if (st.disks > 0) st.disks.toString() else "—", colors)
                DetailRow(S.appShellStorageFree().resolve(), sizeText(max(0, st.raidSize - st.raidUsage).toDouble()), colors)
                DetailRow(S.appShellStorageSystemCard().resolve(), S.appShellStorageOf(sizeText(st.diskUsage.toDouble()), sizeText(st.diskSize.toDouble())).resolve(), colors)
                DetailRow(S.appShellStorageCpu().resolve(), S.appShellPercent(pctText(st.cpuUsagePrc.toDouble())).resolve(), colors)
                DetailRow(S.appShellStorageMemory().resolve(), S.appShellStorageOf(sizeText(st.memUsage.toDouble()), sizeText(st.memSize.toDouble())).resolve(), colors)
                for (e in st.errorsList) {
                    Text(e.message.ifEmpty { e.statusErrorCode.toString() }, fontSize = 12.sp, lineHeight = 16.sp, color = colors.danger)
                }
            }
        }
    }
}

@Composable
private fun StorageText(text: String, color: Color) {
    Text(text, Modifier.padding(horizontal = 16.dp), fontSize = 13.sp, lineHeight = 20.sp, color = color, maxLines = 2)
}

@Composable
private fun DetailRow(label: String, value: String, colors: MenuColors) {
    Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
        Text(label, Modifier.weight(1f), fontSize = 12.sp, lineHeight = 16.sp, color = colors.dim, maxLines = 1)
        Text(value, fontSize = 12.sp, lineHeight = 16.sp, color = colors.dim, maxLines = 1)
    }
}

/** Privacy · Terms, each a full 40dp to tap. */
@Composable
private fun LegalLinks(colors: MenuColors) {
    val context = LocalContext.current
    Row(Modifier.padding(start = 12.dp, end = 12.dp, top = 8.dp), verticalAlignment = Alignment.CenterVertically) {
        LegalLink(S.appShellPrivacy().resolve(), colors) { Share.openInBrowser(context, "https://off-the.cloud/privacy") }
        Text("·", fontSize = 12.sp, color = colors.dim, modifier = Modifier.padding(horizontal = 2.dp))
        LegalLink(S.appShellTerms().resolve(), colors) { Share.openInBrowser(context, "https://off-the.cloud/terms") }
    }
}

@Composable
private fun LegalLink(label: String, colors: MenuColors, onClick: () -> Unit) {
    Box(
        Modifier.heightIn(min = 40.dp).clip(RoundedCornerShape(4.dp)).clickable(role = Role.Button, onClick = onClick).padding(horizontal = 4.dp),
        contentAlignment = Alignment.Center,
    ) {
        // One line each: German Nutzungsbedingungen was broken in two.
        FitText(label, style = LocalTextStyle.current.copy(fontSize = 12.sp), color = colors.dim, minScale = 0.8f)
    }
}

/** The top bar's menu button (MenuIcon): 44dp round, lined up with the rail's icons below it. */
@Composable
fun MenuButton(expanded: Boolean, onClick: () -> Unit, modifier: Modifier = Modifier) {
    val colors = menuColors()
    val described = (if (expanded) S.appShellMenuHide() else S.appShellMenuShow()).resolve()
    Box(
        modifier.size(44.dp).clip(CircleShape).clickable(role = Role.Button, onClick = onClick)
            .semantics { contentDescription = described },
        contentAlignment = Alignment.Center,
    ) { Icon(NavIcons.Menu, null, Modifier.size(24.dp), tint = colors.text) }
}

/**
 * A page's name for TalkBack alone, where the wide layout's menu already
 * names the page on screen (the web keeps such a heading for screen
 * readers; iOS HiddenPageHeading). Put it in a Box over the page: it takes
 * no room.
 */
@Composable
fun HiddenPageHeading(title: String, modifier: Modifier = Modifier) {
    Box(modifier.size(1.dp).semantics { heading(); contentDescription = title })
}

/** A separate line the width of the bar, under it (the web's border-bottom: var(--line)). */
@Composable
fun Hairline(modifier: Modifier = Modifier) {
    val colors = menuColors()
    Box(modifier.fillMaxWidth().height(1.dp).background(colors.line))
}
