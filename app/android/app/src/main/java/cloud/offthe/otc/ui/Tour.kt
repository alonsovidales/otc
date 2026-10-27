// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui

import android.content.Context
import android.provider.Settings
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.pager.HorizontalPager
import androidx.compose.foundation.pager.rememberPagerState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.outlined.AddCircleOutline
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.snapshotFlow
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.zIndex
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.layout.Layout
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.Constraints
import androidx.compose.ui.unit.dp
import cloud.offthe.otc.OTCApp
import cloud.offthe.otc.R
import cloud.offthe.otc.data.FaceRecognition
import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.i18n.RichTag
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appPhotosPeopleAddName
import cloud.offthe.otc.i18n.appSettingsSyncTitle
import cloud.offthe.otc.i18n.appShellAddToCollection
import cloud.offthe.otc.i18n.appShellSectionAlerts
import cloud.offthe.otc.i18n.appShellSectionCollections
import cloud.offthe.otc.i18n.appShellSectionFiles
import cloud.offthe.otc.i18n.appShellSectionFriends
import cloud.offthe.otc.i18n.appShellSectionImages
import cloud.offthe.otc.i18n.appShellSectionPeople
import cloud.offthe.otc.i18n.appShellSectionSettings
import cloud.offthe.otc.i18n.appShellSectionSocial
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
import cloud.offthe.otc.i18n.appTourCollectionsWhere
import cloud.offthe.otc.i18n.appTourCollectionsWhereWide
import cloud.offthe.otc.i18n.appTourDoneText
import cloud.offthe.otc.i18n.appTourDoneTitle
import cloud.offthe.otc.i18n.appTourFilesText
import cloud.offthe.otc.i18n.appTourFriendsAccept
import cloud.offthe.otc.i18n.appTourFriendsAdd
import cloud.offthe.otc.i18n.appTourFriendsWhere
import cloud.offthe.otc.i18n.appTourFriendsWhereWide
import cloud.offthe.otc.i18n.appTourImagesText
import cloud.offthe.otc.i18n.appTourPeopleText
import cloud.offthe.otc.i18n.appTourPeopleWhere
import cloud.offthe.otc.i18n.appTourPeopleWhereWide
import cloud.offthe.otc.i18n.appTourPostStep1
import cloud.offthe.otc.i18n.appTourPostStep2
import cloud.offthe.otc.i18n.appTourPostStep3
import cloud.offthe.otc.i18n.appTourPostTitle
import cloud.offthe.otc.i18n.appTourProgress
import cloud.offthe.otc.i18n.appTourShow
import cloud.offthe.otc.i18n.appTourSkip
import cloud.offthe.otc.i18n.appTourSocialText
import cloud.offthe.otc.i18n.appTourWelcomeText
import cloud.offthe.otc.i18n.appTourWelcomeTitle
import cloud.offthe.otc.i18n.commonAccept
import cloud.offthe.otc.i18n.commonBack
import cloud.offthe.otc.i18n.commonDone
import cloud.offthe.otc.i18n.commonNext
import cloud.offthe.otc.i18n.commonSend
import cloud.offthe.otc.i18n.commonUpload
import cloud.offthe.otc.ui.common.NavIcons
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch

// Port of Tour.swift: the first-run tour (the owner's request of
// 2026-10-27: how the app works, how to publish on Social and the rest, and
// it can be skipped). A full-screen pager that RootView lays over MainView,
// in the same window rather than a Dialog, so it never meets SyncChoiceView
// or a screen's own dialogs, which open above it.
//
// It opens by itself once: when the phone is signed in, MainView is up (not
// the connection screen, the Bluetooth setup or a card saying the device
// can't be reached), the sync question is answered and this phone hasn't
// seen this version of it - an install that was signed in before the update
// that brought it included. "Seen" is marked the moment it opens by itself,
// so Skip, Done, Back or leaving the app midway all leave it seen; prefs
// that can't be read or written mean it doesn't open by itself. Settings'
// Getting started shows it again from its first page, the mark untouched.
//
// The mark is this phone's alone - no protocol change, nothing told to the
// device (the demo is one shared account) - in the otc_tour prefs: not
// wiped at Log Out (SecretsStore.logOut clears otc_secrets, otc_settings and
// otc_sync), so a Log Out and a new sign-in don't show it again, and not
// backed up (backup_rules.xml and data_extraction_rules.xml list only
// otc_settings). Raise TourRules.VERSION only to show a rewritten tour again.
//
// Pages: Welcome, Images, this phone's photo backup, People (only while
// face recognition is on, as in the menu), Collections, Files, Social,
// publishing a post, Friends, Alerts and Settings, the end. Each label a
// page names comes from that label's own key. Where People, Collections and
// Friends open follows MainView's own layout rule, live as the phone turns
// (menuLayout). The same pages, keys and rules as iOS (TourTest.kt <->
// TourTests.swift, the same cases).

/** The tour's pages, in their fixed order. */
enum class TourStep { WELCOME, IMAGES, BACKUP, PEOPLE, COLLECTIONS, FILES, SOCIAL, POST, FRIENDS, ALERTS, DONE }

/** The tour's rules, apart from any view (TourTest). */
object TourRules {
    /** The tour this build shows (iOS `version`): raise it only for a rewritten tour. */
    const val VERSION = 1

    /** The pages shown: People only while face recognition is on (null, not known yet, leaves it out as the menu does). */
    fun steps(faces: Boolean?): List<TourStep> = TourStep.entries.filter { it != TourStep.PEOPLE || faces == true }

    /** Whether the tour opens by itself now. [seenVersion] null: the mark can't be read, so it doesn't. */
    fun shouldShow(seenVersion: Int?, syncChoicePending: Boolean, signedIn: Boolean, mainVisible: Boolean): Boolean {
        val seen = seenVersion ?: return false
        return seen < VERSION && signedIn && mainVisible && !syncChoicePending
    }

    /** The page after [step] among [steps], by the fixed order (null after the last); [step] itself may be gone. */
    fun after(step: TourStep, steps: List<TourStep>): TourStep? = steps.firstOrNull { it.ordinal > step.ordinal }

    /** The page before [step] among [steps] (null before the first). */
    fun before(step: TourStep, steps: List<TourStep>): TourStep? = steps.lastOrNull { it.ordinal < step.ordinal }

    /** Where [step] is among [steps]: itself, or - gone, as People when face recognition is turned off - the next page. */
    fun place(step: TourStep, steps: List<TourStep>): TourStep =
        if (step in steps) step else after(step, steps) ?: before(step, steps) ?: TourStep.WELCOME
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

/** Whether the tour is up and on which page (iOS TourModel.shared). */
object TourModel {
    /** Tests put a store of their own here. */
    internal var store: TourStore = PrefsTourStore

    private val _shown = MutableStateFlow(false)
    /** Up: RootView draws TourView over MainView. */
    val shown: StateFlow<Boolean> = _shown

    private val _step = MutableStateFlow(TourStep.WELCOME)
    /** The page on screen, by its id: People can come or go meanwhile (TourRules.place). */
    val step: StateFlow<TourStep> = _step

    /** Opens the tour by itself when TourRules.shouldShow says so, marking it seen first: one that can't be marked doesn't open. */
    fun evaluate(syncChoicePending: Boolean, signedIn: Boolean, mainVisible: Boolean) {
        if (_shown.value) return
        if (!TourRules.shouldShow(store.seenVersion(), syncChoicePending, signedIn, mainVisible)) return
        if (!store.markSeen(TourRules.VERSION)) return
        open()
    }

    /** Settings' Show the tour: from the first page, the mark untouched. */
    fun replay() = open()

    /** The pager settled on [step] (a swipe, Back or Next). */
    fun show(step: TourStep) {
        _step.value = step
    }

    /** Next on the last page, Skip, Back. */
    fun close() {
        _shown.value = false
    }

    /** Log Out (logOut): closed, back at the first page. The mark stays. */
    fun reset() {
        _shown.value = false
        _step.value = TourStep.WELCOME
    }

    private fun open() {
        _step.value = TourStep.WELCOME
        _shown.value = true
    }
}

// ---- the pager ----

private val Bold = mapOf("b" to RichTag(SpanStyle(fontWeight = FontWeight.Bold)))

/** Animations off in the system's developer or accessibility settings: pages change at once. */
private fun animationsOff(context: Context): Boolean =
    try { Settings.Global.getFloat(context.contentResolver, Settings.Global.ANIMATOR_DURATION_SCALE, 1f) == 0f } catch (_: Exception) { false }

/**
 * The tour itself: where it is and Skip at the top, a page per step in the
 * middle (swiped, or Back and Next), the dots and the buttons below. A
 * full-size Surface, so its text takes the theme's colours in the dark too,
 * kept clear of the system bars and a cutout.
 */
@Composable
fun TourView() {
    val faces by FaceRecognition.enabled.collectAsState()
    val steps = remember(faces) { TourRules.steps(faces) }
    val stepsNow by rememberUpdatedState(steps)
    val context = LocalContext.current
    val reduceMotion = remember { animationsOff(context) }
    val pager = rememberPagerState(initialPage = steps.indexOf(TourRules.place(TourModel.step.value, steps)).coerceAtLeast(0)) { stepsNow.size }
    val scope = rememberCoroutineScope()
    // The model follows the page on screen (a swipe, Back, Next); a page
    // that goes (People) leaves the pager on the next one, by its key.
    LaunchedEffect(pager) {
        snapshotFlow { pager.currentPage }.collect { p -> stepsNow.getOrNull(p)?.let(TourModel::show) }
    }
    val index = pager.currentPage.coerceIn(0, steps.lastIndex)
    val last = index == steps.lastIndex
    fun turn(to: Int) {
        scope.launch { if (reduceMotion) pager.scrollToPage(to) else pager.animateScrollToPage(to) }
    }
    // Back is Skip, as the button at the top.
    BackHandler { TourModel.close() }
    val progress = S.appTourProgress(index + 1, steps.size).resolve()
    val colors = menuColors()
    Surface(Modifier.fillMaxSize()) {
        BoxWithConstraints(Modifier.fillMaxSize()) {
            // MainView's rule: the menu beside the page from 600dp.
            val wide = menuLayout(maxWidth.value, true) != MenuLayout.NONE
            Column(Modifier.fillMaxSize().safeDrawingPadding(), horizontalAlignment = Alignment.CenterHorizontally) {
                Row(
                    Modifier.widthIn(max = 520.dp).fillMaxWidth().padding(start = 20.dp, end = 8.dp, top = 4.dp),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    Text(progress, Modifier.weight(1f), style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    TextButton(onClick = { TourModel.close() }) { Text(S.appTourSkip().resolve()) }
                }
                HorizontalPager(
                    state = pager,
                    key = { stepsNow.getOrNull(it) ?: it },
                    modifier = Modifier.weight(1f).fillMaxWidth().semantics { stateDescription = progress },
                ) { page ->
                    stepsNow.getOrNull(page)?.let { TourPage(it, wide) }
                }
                TourFooter(Modifier.widthIn(max = 520.dp).fillMaxWidth().padding(start = 12.dp, end = 20.dp, top = 8.dp, bottom = 12.dp)) {
                    // Kept in its place from page 1, so the dots don't move.
                    val first = index == 0
                    TextButton(
                        onClick = { if (!first) turn(pager.targetPage - 1) }, enabled = !first,
                        modifier = if (first) Modifier.clearAndSetSemantics {} else Modifier,
                    ) { Text(S.commonBack().resolve(), color = if (first) Color.Transparent else MaterialTheme.colorScheme.primary) }
                    Row(Modifier.clearAndSetSemantics {}, horizontalArrangement = Arrangement.spacedBy(7.dp)) {
                        steps.forEachIndexed { i, _ ->
                            Box(Modifier.size(7.dp).clip(CircleShape).background(if (i == index) colors.badge else colors.dim.copy(alpha = 0.35f)))
                        }
                    }
                    Button(
                        onClick = { if (last) TourModel.close() else turn(pager.targetPage + 1) },
                        // The web's --ember-ink on --ember: white on ember is too faint to read.
                        colors = ButtonDefaults.buttonColors(containerColor = colors.badge, contentColor = colors.badgeText),
                    ) { Text((if (last) S.commonDone() else S.commonNext()).resolve(), fontWeight = FontWeight.SemiBold) }
                }
            }
        }
    }
}

/**
 * Back, the dots and Next on one line while they fit with the dots in the
 * middle; else the dots on a line of their own above the buttons, and at
 * the largest font sizes Next above Back (iOS TourFooterLayout).
 */
@Composable
private fun TourFooter(modifier: Modifier, content: @Composable () -> Unit) {
    Layout(content, modifier) { measurables, constraints ->
        val gap = 12.dp.roundToPx()
        // The shape from their natural widths: each child is measured once.
        val (backW, dotsW, nextW) = measurables.map { it.maxIntrinsicWidth(Constraints.Infinity) }
        val width = if (constraints.hasBoundedWidth) constraints.maxWidth else 2 * maxOf(backW, nextW) + dotsW + 2 * gap
        val fit = Constraints(maxWidth = width)
        val (back, dots, next) = measurables.map { it.measure(fit) }
        when {
            2 * maxOf(backW, nextW) + dotsW + 2 * gap <= width -> {
                val h = maxOf(back.height, dots.height, next.height)
                layout(width, h) {
                    back.place(0, (h - back.height) / 2)
                    dots.place((width - dots.width) / 2, (h - dots.height) / 2)
                    next.place(width - next.width, (h - next.height) / 2)
                }
            }
            backW + nextW + gap <= width -> {
                val row = maxOf(back.height, next.height)
                layout(width, dots.height + gap + row) {
                    dots.place((width - dots.width) / 2, 0)
                    back.place(0, dots.height + gap + (row - back.height) / 2)
                    next.place(width - next.width, dots.height + gap + (row - next.height) / 2)
                }
            }
            else -> layout(width, dots.height + next.height + back.height + 2 * gap) {
                dots.place((width - dots.width) / 2, 0)
                next.place((width - next.width) / 2, dots.height + gap)
                back.place((width - back.width) / 2, dots.height + next.height + 2 * gap)
            }
        }
    }
}

// ---- the pages ----

/** Shorter than this (a phone on its side), a page's picture and gaps shrink so its text fits. */
private val COMPACT_PAGE = 440.dp

/** One page: its picture, its title and its text, centred, scrolling when the text is large. */
@Composable
private fun TourPage(step: TourStep, wide: Boolean) {
    BoxWithConstraints(Modifier.fillMaxSize()) {
        val min = maxHeight
        val compact = maxHeight < COMPACT_PAGE
        val scroll = rememberScrollState()
        // Text out of sight above or below (large text): a hairline marks
        // that edge, so a line cut at it doesn't read as lying under the buttons.
        if (scroll.canScrollBackward) HorizontalDivider(Modifier.align(Alignment.TopCenter).zIndex(1f))
        if (scroll.canScrollForward) HorizontalDivider(Modifier.align(Alignment.BottomCenter).zIndex(1f))
        Column(
            Modifier.fillMaxWidth().verticalScroll(scroll).heightIn(min = min)
                .padding(horizontal = 24.dp, vertical = if (compact) 8.dp else 16.dp),
            verticalArrangement = Arrangement.Center,
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            Column(Modifier.widthIn(max = 520.dp).fillMaxWidth(), horizontalAlignment = Alignment.CenterHorizontally) {
                TourArt(step, compact)
                Spacer(Modifier.height(if (compact) 12.dp else 24.dp))
                Text(
                    tourTitle(step), style = MaterialTheme.typography.headlineSmall, fontWeight = FontWeight.Bold, textAlign = TextAlign.Center,
                    // Read out as the page changes, as a heading.
                    modifier = Modifier.semantics { heading(); liveRegion = LiveRegionMode.Polite },
                )
                Spacer(Modifier.height(if (compact) 8.dp else 16.dp))
                TourContent(step, wide)
            }
        }
    }
}

@Composable
private fun tourTitle(step: TourStep): String = when (step) {
    TourStep.WELCOME -> S.appTourWelcomeTitle()
    TourStep.IMAGES -> S.appShellSectionImages()
    TourStep.BACKUP -> S.appTourBackupTitle()
    TourStep.PEOPLE -> S.appShellSectionPeople()
    TourStep.COLLECTIONS -> S.appShellSectionCollections()
    TourStep.FILES -> S.appShellSectionFiles()
    TourStep.SOCIAL -> S.appShellSectionSocial()
    TourStep.POST -> S.appTourPostTitle()
    TourStep.FRIENDS -> S.appShellSectionFriends()
    TourStep.ALERTS -> S.appTourAlertsTitle()
    TourStep.DONE -> S.appTourDoneTitle()
}.resolve()

@Composable
private fun TourContent(step: TourStep, wide: Boolean) {
    val settings = S.appShellSectionSettings().resolve()
    when (step) {
        TourStep.WELCOME -> Paragraph(AnnotatedString(S.appTourWelcomeText().resolve()))
        TourStep.IMAGES -> Paragraph(AnnotatedString(S.appTourImagesText().resolve()))
        TourStep.BACKUP -> Paragraph(S.appTourBackupText(settings, S.appSettingsSyncTitle().resolve()).resolve(Bold))
        TourStep.PEOPLE -> {
            Paragraph(S.appTourPeopleText(S.appPhotosPeopleAddName().resolve()).resolve(Bold))
            WhereLine((if (wide) S.appTourPeopleWhereWide() else S.appTourPeopleWhere()).resolve())
        }
        TourStep.COLLECTIONS -> {
            Paragraph(S.appTourCollectionsText(S.appShellAddToCollection().resolve()).resolve(Bold))
            WhereLine((if (wide) S.appTourCollectionsWhereWide() else S.appTourCollectionsWhere()).resolve())
        }
        TourStep.FILES -> Paragraph(S.appTourFilesText(S.commonUpload().resolve()).resolve(Bold))
        TourStep.SOCIAL -> Paragraph(AnnotatedString(S.appTourSocialText().resolve()))
        TourStep.POST -> Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(14.dp)) {
            Numbered(1, S.appTourPostStep1().resolve(Bold))
            Numbered(2, S.appTourPostStep2(S.appSocialComposeSourcePhone().resolve(), S.appSocialComposeSourceSynced().resolve()).resolve(Bold))
            Numbered(3, S.appTourPostStep3(S.appSocialComposePublish().resolve()).resolve(Bold))
        }
        TourStep.FRIENDS -> {
            Paragraph(S.appTourFriendsAdd(SecretsStore.bridgeDomain, S.appSocialFriendsAddHeading().resolve(), S.commonSend().resolve()).resolve(Bold))
            Spacer(Modifier.height(12.dp))
            Paragraph(S.appTourFriendsAccept(S.appSocialFriendsWaitingAnswer().resolve(), S.commonAccept().resolve()).resolve(Bold))
            WhereLine((if (wide) S.appTourFriendsWhereWide() else S.appTourFriendsWhere()).resolve())
        }
        TourStep.ALERTS -> Paragraph(S.appTourAlertsText(S.appShellSectionAlerts().resolve(), settings).resolve(Bold))
        TourStep.DONE -> Paragraph(S.appTourDoneText(S.appTourShow().resolve(), settings).resolve(Bold))
    }
}

@Composable
private fun Paragraph(text: AnnotatedString) {
    Text(text, style = MaterialTheme.typography.bodyLarge, textAlign = TextAlign.Center)
}

/** Where the section opens, in the layout on screen. */
@Composable
private fun WhereLine(text: String) {
    Spacer(Modifier.height(12.dp))
    Text(text, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant, textAlign = TextAlign.Center)
}

/** A step of the post page: its number in an ember circle, then the step; read as one. */
@Composable
private fun Numbered(number: Int, text: AnnotatedString) {
    val ember = menuColors().badge
    Row(Modifier.fillMaxWidth().semantics(mergeDescendants = true) {}, verticalAlignment = Alignment.Top) {
        Box(Modifier.size(28.dp).clip(CircleShape).background(ember.copy(alpha = 0.15f)), contentAlignment = Alignment.Center) {
            Text(number.toString(), style = MaterialTheme.typography.labelLarge, fontWeight = FontWeight.Bold, color = ember)
        }
        Spacer(Modifier.width(12.dp))
        Text(text, Modifier.weight(1f).padding(top = 3.dp), style = MaterialTheme.typography.bodyLarge)
    }
}

/**
 * A page's picture: the logo on the first, else the section's own icon in
 * an ember circle (the post page the composer's plus, Alerts and Settings
 * both icons), two thirds of its size on a [compact] page. Decoration only.
 */
@Composable
private fun TourArt(step: TourStep, compact: Boolean) {
    val ember = menuColors().badge
    val circle = if (compact) 64.dp else 96.dp
    val icon = if (compact) 32.dp else 48.dp
    when (step) {
        TourStep.WELCOME -> {
            // The logo at 84dp high, in its own proportions (a height alone
            // leaves a picture at its own size): otc_logo_large, the iOS
            // asset's picture, as otc_logo is drawn for the feed's 28dp.
            val logo = painterResource(R.drawable.otc_logo_large)
            val ratio = logo.intrinsicSize.let { if (it.height > 0f && it.width > 0f) it.width / it.height else 3f }
            Image(logo, null, Modifier.height(if (compact) 56.dp else 84.dp).aspectRatio(ratio), contentScale = ContentScale.Fit)
        }
        TourStep.ALERTS -> Row(
            Modifier.height(circle).width(circle + icon + 8.dp).clip(RoundedCornerShape(circle / 2)).background(ember.copy(alpha = 0.15f)),
            horizontalArrangement = Arrangement.spacedBy(icon / 3, Alignment.CenterHorizontally), verticalAlignment = Alignment.CenterVertically,
        ) {
            Icon(NavIcons.Alerts, null, Modifier.size(icon), tint = ember)
            Icon(NavIcons.Settings, null, Modifier.size(icon), tint = ember)
        }
        else -> Box(Modifier.size(circle).clip(CircleShape).background(ember.copy(alpha = 0.15f)), contentAlignment = Alignment.Center) {
            Icon(tourIcon(step), null, Modifier.size(icon), tint = ember)
        }
    }
}

private fun tourIcon(step: TourStep): ImageVector = when (step) {
    TourStep.IMAGES -> NavIcons.Images
    TourStep.BACKUP -> NavIcons.Storage
    TourStep.PEOPLE -> NavIcons.People
    TourStep.COLLECTIONS -> NavIcons.Collections
    TourStep.FILES -> NavIcons.Files
    TourStep.SOCIAL -> NavIcons.Social
    TourStep.POST -> Icons.Outlined.AddCircleOutline
    TourStep.FRIENDS -> NavIcons.Friends
    else -> NavIcons.Settings
}
