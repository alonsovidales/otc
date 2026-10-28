// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui

import android.text.format.Formatter
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.tween
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxScope
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.requiredSize
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.layout.wrapContentSize
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ViewList
import androidx.compose.material.icons.filled.ArrowCircleDown
import androidx.compose.material.icons.filled.Cancel
import androidx.compose.material.icons.filled.Check
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material.icons.filled.Description
import androidx.compose.material.icons.filled.Download
import androidx.compose.material.icons.filled.Favorite
import androidx.compose.material.icons.filled.FavoriteBorder
import androidx.compose.material.icons.filled.Folder
import androidx.compose.material.icons.filled.GridView
import androidx.compose.material.icons.filled.Group
import androidx.compose.material.icons.filled.History
import androidx.compose.material.icons.filled.Image
import androidx.compose.material.icons.filled.Info
import androidx.compose.material.icons.filled.Link
import androidx.compose.material.icons.filled.Lock
import androidx.compose.material.icons.filled.Notifications
import androidx.compose.material.icons.filled.PhotoCamera
import androidx.compose.material.icons.filled.PhotoLibrary
import androidx.compose.material.icons.filled.PieChart
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material.icons.filled.Share
import androidx.compose.material.icons.filled.Upload
import androidx.compose.material.icons.filled.Wifi
import androidx.compose.material.icons.outlined.AddCircleOutline
import androidx.compose.material.icons.outlined.ContentCopy
import androidx.compose.material.icons.outlined.Forum
import androidx.compose.material.icons.outlined.LockOpen
import androidx.compose.material.icons.outlined.Share
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.Immutable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.scale
import androidx.compose.ui.draw.shadow
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.PathEffect
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.TransformOrigin
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.graphics.luminance
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.layout.layout
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.TextUnit
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.i18n.RichTag
import cloud.offthe.otc.i18n.RichText
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appFilesSortAZ
import cloud.offthe.otc.i18n.appFilesSortBy
import cloud.offthe.otc.i18n.appFilesSortLargest
import cloud.offthe.otc.i18n.appFilesSortName
import cloud.offthe.otc.i18n.appFilesSortSize
import cloud.offthe.otc.i18n.appFilesUploadOnly
import cloud.offthe.otc.i18n.appFilesUploading
import cloud.offthe.otc.i18n.appFilesUsageRow
import cloud.offthe.otc.i18n.appFilesUsageTotal
import cloud.offthe.otc.i18n.appFilesViewGrid
import cloud.offthe.otc.i18n.appFilesViewList
import cloud.offthe.otc.i18n.appFilesViewStorage
import cloud.offthe.otc.i18n.appPhotosCollectionsAddTitle
import cloud.offthe.otc.i18n.appPhotosCollectionsNewItem
import cloud.offthe.otc.i18n.appPhotosCollectionsNewTitle
import cloud.offthe.otc.i18n.appPhotosCollectionsPictures
import cloud.offthe.otc.i18n.appPhotosPeopleAddName
import cloud.offthe.otc.i18n.appPhotosPeopleMerge
import cloud.offthe.otc.i18n.appPhotosPeoplePhotoCount
import cloud.offthe.otc.i18n.appPhotosSearchDocuments
import cloud.offthe.otc.i18n.appPhotosSearchFiles
import cloud.offthe.otc.i18n.appPhotosSearchThings
import cloud.offthe.otc.i18n.appPhotosViewerInfoCamera
import cloud.offthe.otc.i18n.appPhotosViewerInfoLocation
import cloud.offthe.otc.i18n.appPhotosViewerInfoTaken
import cloud.offthe.otc.i18n.appPhotosViewerInfoTitle
import cloud.offthe.otc.i18n.appSettingsLanguage
import cloud.offthe.otc.i18n.appSettingsProfileTitle
import cloud.offthe.otc.i18n.appSettingsSyncTitle
import cloud.offthe.otc.i18n.appSettingsSyncVideos
import cloud.offthe.otc.i18n.appSettingsSyncWifiOnly
import cloud.offthe.otc.i18n.appSettingsUsersCreate
import cloud.offthe.otc.i18n.appSettingsUsersPassword
import cloud.offthe.otc.i18n.appSettingsUsersPasswordConfirm
import cloud.offthe.otc.i18n.appSettingsUsersPasswordTitle
import cloud.offthe.otc.i18n.appSettingsUsersPort
import cloud.offthe.otc.i18n.appSettingsUsersPortDomain
import cloud.offthe.otc.i18n.appSettingsUsersRequestBridge
import cloud.offthe.otc.i18n.appSettingsUsersTitle
import cloud.offthe.otc.i18n.appSettingsUsersUsername
import cloud.offthe.otc.i18n.appShellAddToCollection
import cloud.offthe.otc.i18n.appShellNewPost
import cloud.offthe.otc.i18n.appShellSearchField
import cloud.offthe.otc.i18n.appShellSectionAlerts
import cloud.offthe.otc.i18n.appShellSectionCollections
import cloud.offthe.otc.i18n.appShellSelected
import cloud.offthe.otc.i18n.appShellShareAsGallery
import cloud.offthe.otc.i18n.appSocialAlertsCommented
import cloud.offthe.otc.i18n.appSocialAlertsFriendRequest
import cloud.offthe.otc.i18n.appSocialAlertsLikedPost
import cloud.offthe.otc.i18n.appSocialAlertsTitle
import cloud.offthe.otc.i18n.appSocialComposeCaptionPlaceholder
import cloud.offthe.otc.i18n.appSocialComposePublish
import cloud.offthe.otc.i18n.appSocialComposePublishCount
import cloud.offthe.otc.i18n.appSocialComposeSourcePhone
import cloud.offthe.otc.i18n.appSocialComposeSourceSynced
import cloud.offthe.otc.i18n.appSocialComposeTitle
import cloud.offthe.otc.i18n.appSocialFriendsAddHeading
import cloud.offthe.otc.i18n.appSocialFriendsAddressCopied
import cloud.offthe.otc.i18n.appSocialFriendsNamePlaceholder
import cloud.offthe.otc.i18n.appSocialFriendsRequestSent
import cloud.offthe.otc.i18n.appSocialFriendsStatusAccepted
import cloud.offthe.otc.i18n.appSocialFriendsWaitingAnswer
import cloud.offthe.otc.i18n.appSocialFriendsYourAddress
import cloud.offthe.otc.i18n.appSocialPostComment
import cloud.offthe.otc.i18n.appSocialPostCommentPlaceholder
import cloud.offthe.otc.i18n.appSocialPostLikes
import cloud.offthe.otc.i18n.appSocialPostSendComment
import cloud.offthe.otc.i18n.appTourArtCaption
import cloud.offthe.otc.i18n.appTourArtCollection
import cloud.offthe.otc.i18n.appTourArtComment
import cloud.offthe.otc.i18n.appTourArtFriend
import cloud.offthe.otc.i18n.appTourArtPerson
import cloud.offthe.otc.i18n.appTourArtThing
import cloud.offthe.otc.i18n.appTourSettingsTitle
import cloud.offthe.otc.i18n.appTourShow
import cloud.offthe.otc.i18n.commonAccept
import cloud.offthe.otc.i18n.commonAdd
import cloud.offthe.otc.i18n.commonCancel
import cloud.offthe.otc.i18n.commonCreate
import cloud.offthe.otc.i18n.commonDecline
import cloud.offthe.otc.i18n.commonDelete
import cloud.offthe.otc.i18n.commonDownload
import cloud.offthe.otc.i18n.commonSend
import cloud.offthe.otc.i18n.commonShare
import cloud.offthe.otc.i18n.commonUpload
import cloud.offthe.otc.ui.common.NavIcons
import cloud.offthe.otc.ui.gallery.SearchIcons
import cloud.offthe.otc.ui.gallery.monthTitle
import cloud.offthe.otc.ui.gallery.scrubLabel
import cloud.offthe.otc.ui.theme.Ember
import kotlinx.coroutines.delay
import java.text.Normalizer
import java.util.Calendar
import java.util.Locale

// The first-run tour's drawings (Tour.kt shows one in each step's card): a
// small moving mock of the controls the step teaches - the search typed
// into, the dates dragged, photos selected and the bar coming up, Files
// switching views, a post liked, one composed and published, a friend
// added, a user added - so the tour teaches with nothing in the library
// yet. Compose shapes, Canvas and animation, drawn on a 320 x 176 stage
// scaled to the card: every label is the real control's own key, so it
// reads in the app's language; the icons are the real controls' own;
// colours come from the theme, light or dark; "photos" are gradient tiles
// and faces a plain outline, never a real person. Each scene is a few
// states a timer steps through (rememberScene); with animations off
// (ANIMATOR_DURATION_SCALE 0) one still state shows instead. Out of
// TalkBack's way: the step's text says it all. iOS's TourArt.swift draws
// the same scenes.

private const val STAGE_W = 320f
private const val STAGE_H = 176f

/** A step's drawing. [compact]: a short window, drawn smaller. */
@Composable
fun TourArt(step: TourStep, faces: Boolean, wide: Boolean, compact: Boolean) {
    val p = artPalette()
    Box(Modifier.fillMaxWidth().clearAndSetSemantics {}, contentAlignment = Alignment.TopCenter) {
        BoxWithConstraints(
            Modifier.widthIn(max = if (compact) 220.dp else 380.dp).fillMaxWidth().aspectRatio(STAGE_W / STAGE_H)
                .clip(RoundedCornerShape(12.dp)).background(p.stage).border(0.5.dp, p.line, RoundedCornerShape(12.dp)),
        ) {
            val s = maxWidth.value / STAGE_W
            Box(
                Modifier.wrapContentSize(Alignment.TopStart, unbounded = true).requiredSize(STAGE_W.dp, STAGE_H.dp)
                    .graphicsLayer { scaleX = s; scaleY = s; transformOrigin = TransformOrigin(0f, 0f) },
            ) {
                when (step) {
                    TourStep.WELCOME -> WelcomeArt(p, faces, wide)
                    TourStep.SEARCH -> SearchArt(p)
                    TourStep.DATES -> DatesArt(p)
                    TourStep.SELECT -> SelectArt(p)
                    TourStep.VIEWER -> ViewerArt(p)
                    TourStep.PEOPLE -> PeopleArt(p)
                    TourStep.COLLECTIONS -> CollectionsArt(p)
                    TourStep.FILES -> FilesArt(p)
                    TourStep.UPLOAD -> UploadArt(p)
                    TourStep.SOCIAL -> SocialArt(p)
                    TourStep.POST -> PostArt(p, wide)
                    TourStep.FRIENDS -> FriendsArt(p)
                    TourStep.BACKUP -> BackupArt(p)
                    TourStep.USERS -> UsersArt(p)
                    TourStep.ALERTS -> AlertsArt(p, wide)
                }
            }
        }
    }
}

// ---- the kit ----

/** The drawings' colours, from the theme. */
@Immutable
internal data class ArtPalette(
    val dark: Boolean,
    val stage: Color,
    val surface: Color,
    val field: Color,
    val line: Color,
    val text: Color,
    val dim: Color,
    val bar: Color,
    val accent: Color,
    val accentFill: Color,
    val gold: Color,
    val activeFill: Color,
) {
    private fun mix(i: Int, t: Float): Color {
        val (r, g, b) = BASES[i]
        val k = if (dark) 0.16f else 0.92f
        return Color(r * (1 - t) + k * t, g * (1 - t) + k * t, b * (1 - t) + k * t)
    }

    /** A "photo"'s two colours, by hue (0-5). */
    fun tile(hue: Int): Pair<Color, Color> = when (((hue % 6) + 6) % 6) {
        0 -> mix(0, 0.2f) to mix(1, 0.25f)
        1 -> mix(2, 0.3f) to mix(3, 0.4f)
        2 -> mix(1, 0.2f) to mix(0, 0.5f)
        3 -> mix(3, 0.2f) to mix(2, 0.55f)
        4 -> mix(0, 0.35f) to mix(2, 0.45f)
        else -> mix(4, 0.3f) to mix(3, 0.5f)
    }

    private companion object {
        val BASES = listOf(
            Triple(1.0f, 0.78f, 0.34f), Triple(1.0f, 0.42f, 0.29f), Triple(0.33f, 0.70f, 0.48f),
            Triple(0.56f, 0.62f, 0.69f), Triple(0.89f, 0.36f, 0.36f),
        )
    }
}

@Composable
private fun artPalette(): ArtPalette {
    val c = MaterialTheme.colorScheme
    val m = menuColors()
    val dark = c.surface.luminance() < 0.5f
    return remember(c, m, dark) {
        ArtPalette(
            dark = dark,
            stage = if (dark) c.surfaceContainerLowest else c.surfaceContainerHigh,
            surface = if (dark) c.surfaceContainerHigh else c.surface,
            field = c.surfaceVariant,
            line = c.outlineVariant,
            text = c.onSurface,
            dim = c.onSurfaceVariant,
            bar = c.onSurfaceVariant.copy(alpha = 0.32f),
            accent = c.primary,
            accentFill = c.secondaryContainer,
            gold = m.accent,
            activeFill = m.accentFill,
        )
    }
}

/** Steps through a scene's states, [times] (ms) each, over and over; the [still] state with animations off. */
@Composable
private fun rememberScene(times: IntArray, still: Int): Int {
    val context = LocalContext.current
    val reduce = remember { animationsOff(context) }
    var at by remember { mutableIntStateOf(if (reduce) still else 0) }
    LaunchedEffect(reduce) {
        if (reduce) { at = still; return@LaunchedEffect }
        at = 0
        while (true) {
            delay(times[at].toLong())
            at = (at + 1) % times.size
        }
    }
    return at
}

/** A value eased to as the scene moves on. */
@Composable
private fun eased(v: Float): Float = animateFloatAsState(v, tween(350), label = "tourArt").value

/** A text size at its own size, whatever the system's font scale: the drawings are drawn to a size. */
@Composable
private fun fs(size: Float): TextUnit = (size / LocalDensity.current.fontScale).sp

/** Placed by its top left corner on the stage. */
private fun Modifier.at(x: Float, y: Float) = offset(x.dp, y.dp)

/** Placed by its centre on the stage. */
private fun Modifier.centred(x: Float, y: Float) = layout { m, c ->
    val pl = m.measure(c.copy(minWidth = 0, minHeight = 0))
    layout(0, 0) { pl.place(x.dp.roundToPx() - pl.width / 2, y.dp.roundToPx() - pl.height / 2) }
}

/** A "photo": a gradient with a sun and a hill. */
@Composable
private fun Tile(p: ArtPalette, hue: Int, modifier: Modifier, radius: Float = 5f) {
    val (a, b) = p.tile(hue)
    Canvas(modifier.clip(RoundedCornerShape(radius.dp))) {
        drawRect(Brush.linearGradient(listOf(a, b), Offset.Zero, Offset(size.width, size.height)))
        drawCircle(Color.White.copy(alpha = 0.5f), radius = size.width * 0.11f, center = Offset(size.width * 0.77f, size.height * 0.16f + size.width * 0.11f))
        drawOval(
            Color.Black.copy(alpha = if (p.dark) 0.3f else 0.16f),
            topLeft = Offset(-size.width * 0.15f, size.height * 0.7f), size = Size(size.width * 1.3f, size.height * 0.75f),
        )
    }
}

/** A face as People draws one: a plain outline in a tinted circle. */
@Composable
private fun Face(p: ArtPalette, hue: Int, modifier: Modifier) {
    val (a, b) = p.tile(hue)
    Canvas(modifier.clip(CircleShape)) {
        val s = size.width
        drawRect(Brush.verticalGradient(listOf(a.copy(alpha = 0.7f), b.copy(alpha = 0.7f))))
        val stroke = Stroke(width = maxOf(1f, s * 0.05f), cap = StrokeCap.Round)
        drawCircle(Color.White.copy(alpha = 0.85f), radius = s * 0.14f, center = Offset(s * 0.5f, s * 0.36f), style = stroke)
        drawPath(Path().apply { moveTo(s * 0.2f, s * 0.82f); quadraticTo(s * 0.5f, s * 0.42f, s * 0.8f, s * 0.82f) }, Color.White.copy(alpha = 0.85f), style = stroke)
    }
}

/** Text not worth reading (a file's name, a date). */
@Composable
private fun Bar(p: ArtPalette, width: Float, height: Float = 5f, modifier: Modifier = Modifier) {
    Box(modifier.size(width.dp, height.dp).clip(CircleShape).background(p.bar))
}

/** A label, one line, in the drawing's own small sizes. */
@Composable
private fun Words(text: String, size: Float = 10f, weight: FontWeight = FontWeight.Normal, color: Color = Color.Unspecified, modifier: Modifier = Modifier, italic: Boolean = false) {
    Text(
        text, modifier, color = if (color == Color.Unspecified) MaterialTheme.colorScheme.onSurface else color, fontSize = fs(size),
        fontWeight = weight, fontStyle = if (italic) FontStyle.Italic else FontStyle.Normal, maxLines = 1, overflow = TextOverflow.Ellipsis,
        lineHeight = fs(size * 1.25f),
    )
}

@Composable
private fun RichWords(text: AnnotatedString, size: Float = 9f, modifier: Modifier = Modifier) {
    Text(text, modifier, fontSize = fs(size), maxLines = 1, overflow = TextOverflow.Ellipsis, color = MaterialTheme.colorScheme.onSurface, lineHeight = fs(size * 1.25f))
}

private val BoldTag = mapOf("b" to RichTag(SpanStyle(fontWeight = FontWeight.Bold)))

@Composable
private fun RichText.bold(): AnnotatedString = resolve(BoldTag)

/** An icon, as the real control draws it. */
@Composable
private fun Glyph(icon: ImageVector, size: Float = 12f, color: Color = MaterialTheme.colorScheme.onSurface, modifier: Modifier = Modifier) {
    Icon(icon, null, modifier.size(size.dp), tint = color)
}

/** A finger doing what the step says: pressed while [down]. */
@Composable
private fun BoxScope.Finger(x: Float, y: Float, down: Boolean = false, hidden: Boolean = false) {
    val fx = eased(x)
    val fy = eased(y)
    val on = MaterialTheme.colorScheme.onSurface
    val scale = eased(if (down) 0.86f else 1f)
    val alpha = eased(if (hidden) 0f else 1f)
    Canvas(Modifier.centred(fx, fy).size(30.dp).alpha(alpha)) {
        val r = 10.dp.toPx() * scale
        drawCircle(on.copy(alpha = if (down) 0.32f else 0.2f), r)
        drawCircle(on.copy(alpha = 0.55f), r, style = Stroke(1.5.dp.toPx()))
        if (down) drawCircle(Ember.copy(alpha = 0.7f), r * 1.5f, style = Stroke(2.dp.toPx()))
    }
}

/** Text typed in, a character at a time, from when [on] turns true. */
@Composable
private fun Typed(text: String, on: Boolean, size: Float = 10f, caret: Boolean = true) {
    val context = LocalContext.current
    val reduce = remember { animationsOff(context) }
    var n by remember { mutableIntStateOf(0) }
    LaunchedEffect(on, text) {
        if (!on) { n = 0; return@LaunchedEffect }
        if (reduce) { n = text.length; return@LaunchedEffect }
        n = 0
        for (i in 1..text.length) { delay(70); n = i }
    }
    Row(verticalAlignment = Alignment.CenterVertically) {
        Words(text.take(n), size)
        if (caret) Box(Modifier.padding(start = 1.dp).size(1.2.dp, (size + 2).dp).background(MaterialTheme.colorScheme.primary))
    }
}

/** A switch, on or off (Material's). */
@Composable
private fun Switch(p: ArtPalette, on: Boolean) {
    Box(
        Modifier.size(28.dp, 16.dp).clip(CircleShape).background(if (on) p.accent else p.field)
            .border(1.dp, if (on) p.accent else p.dim.copy(alpha = 0.6f), CircleShape),
        contentAlignment = if (on) Alignment.CenterEnd else Alignment.CenterStart,
    ) {
        Box(Modifier.padding(horizontal = 3.dp).size(if (on) 11.dp else 8.dp).clip(CircleShape).background(if (on) MaterialTheme.colorScheme.onPrimary else p.dim))
    }
}

/** A selection's tick, as a selected photo shows it. */
@Composable
private fun Tick(p: ArtPalette, on: Boolean, modifier: Modifier = Modifier) {
    Box(
        modifier.size(13.dp).clip(CircleShape).background(if (on) p.accent else Color.Black.copy(alpha = 0.15f)).border(1.2.dp, Color.White, CircleShape),
        contentAlignment = Alignment.Center,
    ) { if (on) Icon(Icons.Default.Check, null, Modifier.size(9.dp), tint = Color.White) }
}

/** An item of the floating bar (SelectionActionBar): its icon over its label. */
private data class BarItem(val icon: ImageVector, val label: String, val tint: Color? = null, val enabled: Boolean = true)

@Composable
private fun ActionBar(p: ArtPalette, items: List<BarItem>, lit: Int, modifier: Modifier) {
    Row(
        modifier.shadow(3.dp, RoundedCornerShape(24.dp)).clip(RoundedCornerShape(24.dp)).background(p.surface)
            .padding(horizontal = 6.dp, vertical = 3.dp),
    ) {
        items.forEachIndexed { i, it ->
            val tint = (it.tint ?: p.accent).copy(alpha = if (it.enabled) 1f else 0.4f)
            Column(
                Modifier.weight(1f).clip(RoundedCornerShape(8.dp)).background(if (i == lit) p.accent.copy(alpha = 0.18f) else Color.Transparent)
                    .padding(vertical = 4.dp),
                horizontalAlignment = Alignment.CenterHorizontally,
            ) {
                Glyph(it.icon, 13f, tint)
                Words(it.label, 7f, color = tint)
            }
        }
    }
}

/** "3 selected", with its ×. */
@Composable
private fun SelectedPill(p: ArtPalette, count: Int, modifier: Modifier) {
    Row(
        modifier.shadow(2.dp, CircleShape).clip(CircleShape).background(p.surface).padding(horizontal = 8.dp, vertical = 3.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Glyph(Icons.Default.Close, 9f, p.dim)
        Spacer(Modifier.width(4.dp))
        Words(S.appShellSelected(count).resolve(), 8f, color = p.dim)
    }
}

/** A toast at the foot. */
@Composable
private fun Toast(text: String, on: Boolean, modifier: Modifier) {
    val a = eased(if (on) 1f else 0f)
    Box(modifier.alpha(a).clip(CircleShape).background(Color.Black.copy(alpha = 0.78f)).padding(horizontal = 10.dp, vertical = 5.dp)) {
        Words(text, 9f, color = Color.White)
    }
}

/** A made-up device name from a made-up first name: what Friends takes (letters, digits and dashes). */
private fun deviceName(name: String): String {
    val folded = Normalizer.normalize(name, Normalizer.Form.NFD).replace(Regex("\\p{M}"), "").lowercase(Locale.ROOT)
    return folded.filter { it in 'a'..'z' || it in '0'..'9' || it == '-' }.ifEmpty { "sam" } // i18n-ignore: a fallback device name
}

/** The month [back] months before this one, as PhotoMonths keys it. */
private fun monthKey(back: Int): String {
    val c = Calendar.getInstance()
    c.add(Calendar.MONTH, -back)
    return String.format(Locale.ROOT, "%04d-%02d", c.get(Calendar.YEAR), c.get(Calendar.MONTH) + 1) // i18n-ignore: a month's key
}

/** [text] with the part typed so far in bold. */
private fun highlighted(text: String, typed: String): AnnotatedString = buildAnnotatedString {
    val i = text.indexOf(typed)
    if (i < 0) { append(text); return@buildAnnotatedString }
    append(text.substring(0, i))
    withStyle(SpanStyle(fontWeight = FontWeight.Bold)) { append(typed) }
    append(text.substring(i + typed.length))
}

private fun fileSize(context: android.content.Context, bytes: Long): String = Formatter.formatShortFileSize(context, bytes)

// ---- welcome: where the sections are ----

private enum class Kind { ALERTS, POST, ROWS, TILES, SETTINGS, FACES, CARDS, FRIENDS }

private data class SectionItem(val label: UiText, val icon: ImageVector, val kind: Kind)

@Composable
private fun BoxScope.WelcomeArt(p: ArtPalette, faces: Boolean, wide: Boolean) {
    // The bottom bar's tabs (MainView's order and icons), or the menu's items.
    val items = if (!wide) listOf(
        SectionItem(Section.Alerts.label, Icons.Default.Notifications, Kind.ALERTS),
        SectionItem(Section.Social.label, Icons.Outlined.Forum, Kind.POST),
        SectionItem(Section.Files.label, Icons.Default.Folder, Kind.ROWS),
        SectionItem(Section.Images.label, Icons.Default.PhotoLibrary, Kind.TILES),
        SectionItem(Section.Settings.label, Icons.Default.Settings, Kind.SETTINGS),
    ) else listOfNotNull(
        SectionItem(Section.Images.label, NavIcons.Images, Kind.TILES),
        if (faces) SectionItem(Section.People.label, NavIcons.People, Kind.FACES) else null,
        SectionItem(Section.Collections.label, NavIcons.Collections, Kind.CARDS),
        SectionItem(Section.Files.label, NavIcons.Files, Kind.ROWS),
        SectionItem(Section.Social.label, NavIcons.Social, Kind.POST),
        SectionItem(Section.Friends.label, NavIcons.Friends, Kind.FRIENDS),
        SectionItem(Section.Alerts.label, NavIcons.Alerts, Kind.ALERTS),
        SectionItem(Section.Settings.label, NavIcons.Settings, Kind.SETTINGS),
    )
    val at = rememberScene(IntArray(items.size) { 1000 }, if (wide) 0 else 3)
    if (!wide) {
        val w = STAGE_W / items.size
        Sketch(p, items[at].kind, Modifier.at(10f, 8f).size(300.dp, 116.dp))
        Box(Modifier.at(0f, 128f).size(STAGE_W.dp, 48.dp).background(p.surface))
        items.forEachIndexed { i, it ->
            Column(Modifier.centred(w * (i + 0.5f), 152f).width(w.dp), horizontalAlignment = Alignment.CenterHorizontally) {
                Box(
                    Modifier.size(34.dp, 18.dp).clip(CircleShape).background(if (i == at) p.accentFill else Color.Transparent),
                    contentAlignment = Alignment.Center,
                ) { Glyph(it.icon, 14f, if (i == at) p.text else p.dim) }
                Words(it.label.resolve(), 8f, if (i == at) FontWeight.SemiBold else FontWeight.Normal, if (i == at) p.text else p.dim)
            }
        }
        Finger(w * (at + 0.5f) + 6f, 150f, down = true)
    } else {
        val row = 168f / items.size
        Box(Modifier.at(112f, 0f).size(0.5.dp, STAGE_H.dp).background(p.line))
        items.forEachIndexed { i, it ->
            Row(
                Modifier.at(4f, 4f + i * row).size(104.dp, (row - 2).dp).clip(CircleShape)
                    .background(if (i == at) p.activeFill else Color.Transparent).padding(horizontal = 8.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Glyph(it.icon, 11f, if (i == at) p.gold else p.dim)
                Spacer(Modifier.width(6.dp))
                Words(it.label.resolve(), 9f, if (i == at) FontWeight.SemiBold else FontWeight.Normal, if (i == at) p.gold else p.text)
            }
        }
        Sketch(p, items[at].kind, Modifier.at(122f, 13f).size(192.dp, 150.dp))
        Finger(40f, 4f + (at + 0.5f) * row + 4f, down = true)
    }
}

/** What a section looks like, at a glance. */
@Composable
private fun Sketch(p: ArtPalette, kind: Kind, modifier: Modifier) {
    Box(modifier) {
        when (kind) {
            Kind.TILES -> Column(verticalArrangement = Arrangement.spacedBy(4.dp)) {
                for (r in 0 until 2) Row(horizontalArrangement = Arrangement.spacedBy(4.dp)) {
                    for (c in 0 until 3) Tile(p, r * 3 + c, Modifier.weight(1f).height(44.dp))
                }
            }
            Kind.FACES -> Row(horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                for (i in 0 until 4) Column(horizontalAlignment = Alignment.CenterHorizontally) {
                    Face(p, i + 1, Modifier.size(36.dp)); Spacer(Modifier.height(4.dp)); Bar(p, 30f)
                }
            }
            Kind.CARDS -> Row(horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                for (h in listOf(1, 3)) Column(Modifier.weight(1f)) {
                    Tile(p, h, Modifier.fillMaxWidth().height(70.dp)); Spacer(Modifier.height(4.dp)); Bar(p, 50f); Spacer(Modifier.height(3.dp)); Bar(p, 30f, 4f)
                }
            }
            Kind.POST -> Column(verticalArrangement = Arrangement.spacedBy(5.dp)) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Box(Modifier.size(14.dp).clip(CircleShape).background(p.dim.copy(alpha = 0.4f))); Spacer(Modifier.width(6.dp)); Bar(p, 60f)
                }
                Tile(p, 2, Modifier.fillMaxWidth().height(64.dp))
                Glyph(Icons.Default.FavoriteBorder, 12f)
            }
            else -> Column(verticalArrangement = Arrangement.spacedBy(9.dp)) {
                val widths = listOf(110f, 80f, 96f, 64f)
                for (i in 0 until 4) Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                    when (kind) {
                        Kind.ROWS -> Glyph(if (i < 2) Icons.Default.Folder else Icons.Default.Description, 12f, if (i < 2) Ember else p.dim)
                        Kind.ALERTS -> Box(Modifier.size(7.dp).clip(CircleShape).background(Ember))
                        Kind.FRIENDS -> Box(Modifier.size(14.dp).clip(CircleShape).background(p.dim.copy(alpha = 0.4f)))
                        else -> {}
                    }
                    Spacer(Modifier.width(8.dp))
                    Bar(p, widths[i])
                    Spacer(Modifier.weight(1f))
                    if (kind == Kind.SETTINGS) Switch(p, i % 2 == 0)
                }
            }
        }
    }
}

// ---- search: typed, suggested, picked as a chip ----

@Composable
private fun BoxScope.SearchArt(p: ArtPalette) {
    // rest, typing, suggestions, on the thing, pressed, the chip
    val at = rememberScene(intArrayOf(700, 1000, 1100, 700, 250, 2200), 3)
    val word = S.appTourArtThing().resolve()
    val typed = word.take(maxOf(2, (word.length + 1) / 2))
    val chip = at == 5
    val panel = at in 2..4
    val kept = setOf(0, 2, 3)
    // In the field, Android's chips (TopSearch.kt).
    Row(
        Modifier.at(10f, 8f).size(300.dp, 26.dp).clip(RoundedCornerShape(13.dp)).background(p.field).padding(horizontal = 10.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Glyph(SearchIcons.Search, 13f, p.dim)
        Spacer(Modifier.width(6.dp))
        when {
            chip -> Row(Modifier.clip(CircleShape).background(p.accent.copy(alpha = 0.18f)).padding(horizontal = 7.dp, vertical = 2.dp), verticalAlignment = Alignment.CenterVertically) {
                Words(word, 9f); Spacer(Modifier.width(4.dp)); Glyph(Icons.Default.Close, 8f, p.dim)
            }
            at == 0 -> Words(S.appShellSearchField().resolve(), 10f, color = p.dim)
            else -> Typed(typed, at >= 1)
        }
    }
    for (i in 0 until 6) {
        val a = eased(if (chip && i !in kept) 0.12f else 1f)
        Tile(p, i, Modifier.at(10f + (i % 3) * 102f, 58f + (i / 3) * 58f).size(96.dp, 54.dp).alpha(a))
    }
    val pa = eased(if (panel) 1f else 0f)
    Column(
        Modifier.at(10f, 40f).width(300.dp).alpha(pa).shadow(6.dp, RoundedCornerShape(12.dp)).clip(RoundedCornerShape(12.dp)).background(p.surface).padding(10.dp),
        verticalArrangement = Arrangement.spacedBy(5.dp),
    ) {
        Words(S.appPhotosSearchThings().resolve().uppercase(), 7f, FontWeight.SemiBold, p.dim)
        Row(
            Modifier.fillMaxWidth().clip(RoundedCornerShape(6.dp)).background(if (at >= 3) p.accent.copy(alpha = 0.16f) else Color.Transparent).padding(horizontal = 6.dp, vertical = 3.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) { Glyph(SearchIcons.Tag, 11f); Spacer(Modifier.width(6.dp)); RichWords(highlighted(word, typed), 10f) }
        Words(S.appPhotosSearchFiles().resolve().uppercase(), 7f, FontWeight.SemiBold, p.dim)
        Row(Modifier.padding(horizontal = 6.dp), verticalAlignment = Alignment.CenterVertically) {
            Glyph(Icons.Default.Image, 11f); Spacer(Modifier.width(6.dp)); RichWords(highlighted("$word.jpg", typed), 10f) // i18n-ignore: a file's name
        }
        Row(Modifier.padding(horizontal = 6.dp), verticalAlignment = Alignment.CenterVertically) {
            Glyph(SearchIcons.DocSearch, 11f); Spacer(Modifier.width(6.dp)); RichWords(S.appPhotosSearchDocuments(typed).bold(), 10f)
        }
    }
    Finger(if (at in 3..4) 70f else 250f, if (at in 3..4) 70f else 150f, down = at == 4, hidden = at < 2)
}

// ---- dates: the right edge dragged, the grid jumping ----

@Composable
private fun BoxScope.DatesArt(p: ArtPalette) {
    // rest, to the edge, pressed, dragged, dragged further, let go, the jump
    val at = rememberScene(intArrayOf(800, 500, 350, 600, 700, 250, 1900), 4)
    val months = remember { (0 until 12).map { monthKey(it * 3) } }
    val block = 62f
    val drag = floatArrayOf(0.04f, 0.04f, 0.04f, 0.34f, 0.7f, 0.7f, 0.7f)[at]
    val under = minOf(months.size - 1, Math.round(drag * (months.size - 1)))
    val jumped = if (at >= 6) under else 0
    val y = 8f + drag * 160f
    val dragging = at in 2..4
    val shift = eased(8f - jumped * block)
    // Each month at its own place on the stage, those in sight only.
    months.forEachIndexed { k, key ->
        val top = shift + k * block
        if (top > -block && top < STAGE_H) Column(Modifier.at(10f, top).height(block.dp)) {
            Words(monthTitle(key).resolve(), 10f, FontWeight.SemiBold)
            Spacer(Modifier.height(4.dp))
            Row(horizontalArrangement = Arrangement.spacedBy(4.dp)) { for (i in 0 until 4) Tile(p, k + i, Modifier.size(66.dp, 38.dp)) }
        }
    }
    Box(Modifier.at(306f, 8f).size(3.dp, 160.dp).clip(CircleShape).background(p.dim.copy(alpha = 0.35f)))
    if (dragging) {
        val seen = mutableSetOf<String>()
        months.forEachIndexed { k, key ->
            val year = key.take(4)
            if (seen.add(year)) Words(year, 8f, color = p.dim, modifier = Modifier.at(270f, 2f + k / (months.size - 1f) * 160f))
        }
        Box(Modifier.centred(240f, y).clip(RoundedCornerShape(5.dp)).background(p.surface).padding(horizontal = 6.dp, vertical = 3.dp)) {
            Words(scrubLabel(months[under]).resolve(), 9f, FontWeight.Bold)
        }
        Box(Modifier.centred(307.5f, y).size(9.dp).clip(CircleShape).background(Color.Yellow))
    }
    Finger(if (at == 0) 200f else 300f, if (at == 0) 120f else y + 6f, down = dragging)
}

// ---- select: a long press, a drag across, the bar ----

@Composable
private fun BoxScope.SelectArt(p: ArtPalette) {
    // rest, on the first, held, selected, across two, three, four, then each of the bar's buttons
    val at = rememberScene(intArrayOf(600, 500, 600, 300, 300, 300, 600, 900, 900, 900, 900, 500), 8)
    val picked = when { at >= 6 -> 4; at >= 5 -> 3; at >= 4 -> 2; at >= 3 -> 1; else -> 0 }
    val lit = if (at in 7..10) intArrayOf(0, 2, 3, 4)[at - 7] else -1
    val cols = floatArrayOf(10f, 86f, 162f, 238f)
    for (i in 0 until 8) {
        val on = i < picked
        Box(Modifier.at(cols[i % 4], if (i < 4) 8f else 62f).size(72.dp, 50.dp)) {
            Tile(p, i, Modifier.fillMaxSize().scale(eased(if (on) 0.9f else 1f)))
            if (on) {
                Box(Modifier.fillMaxSize().border(3.dp, p.accent, RoundedCornerShape(5.dp)))
                Tick(p, true, Modifier.padding(4.dp))
            }
        }
    }
    if (picked > 0) {
        SelectedPill(p, picked, Modifier.centred(160f, 124f))
        ActionBar(
            p, listOf(
                BarItem(Icons.Default.Share, S.commonShare().resolve()),
                BarItem(Icons.Default.PhotoLibrary, S.appShellShareAsGallery().resolve()),
                BarItem(Icons.Default.Download, S.commonDownload().resolve()),
                BarItem(NavIcons.AddToCollection, S.appShellAddToCollection().resolve()),
                BarItem(Icons.Default.Delete, S.commonDelete().resolve(), Color(0xFFE53935)),
            ), lit, Modifier.at(10f, 136f).width(300.dp),
        )
    }
    val (fx, fy) = when (at) {
        0 -> 200f to 150f
        1, 2, 3 -> 46f to 33f
        4 -> 122f to 33f
        5 -> 198f to 33f
        6 -> 274f to 33f
        in 7..10 -> 40f + lit * 60f to 152f
        else -> 200f to 110f
    }
    Finger(fx, fy, down = at in 2..6 || lit >= 0)
}

// ---- viewer: opened, swiped, Info ----

@Composable
private fun BoxScope.ViewerArt(p: ArtPalette) {
    // the grid, tapped, open, swiped, the next one, to Info, pressed, Info
    val at = rememberScene(intArrayOf(700, 300, 600, 600, 500, 500, 250, 2100), 7)
    val context = LocalContext.current
    for (i in 0 until 6) Tile(p, i + 1, Modifier.at(10f + (i % 3) * 102f, 8f + (i / 3) * 82f).size(96.dp, 76.dp))
    val open = eased(if (at >= 2) 1f else 0f)
    Box(Modifier.at(0f, 0f).size(STAGE_W.dp, STAGE_H.dp).graphicsLayer { alpha = open; scaleX = 0.4f + 0.6f * open; scaleY = 0.4f + 0.6f * open; transformOrigin = TransformOrigin(0.5f, 0.3f) }.background(Color.Black)) {
        val strip = eased(60f - (if (at >= 4) 220f else 0f) - (if (at == 3) 40f else 0f))
        Row(Modifier.at(strip, 30f), horizontalArrangement = Arrangement.spacedBy(20.dp)) {
            Tile(p, 2, Modifier.size(200.dp, 112.dp)); Tile(p, 3, Modifier.size(200.dp, 112.dp))
        }
        Box(Modifier.centred(18f, 16f).clip(CircleShape).background(if (at == 6) Color.White.copy(alpha = 0.3f) else Color.Transparent).padding(3.dp)) {
            Glyph(Icons.Default.Info, 15f, Color.White)
        }
        Glyph(Icons.Default.Cancel, 16f, Color.White, Modifier.centred(302f, 16f))
        Row(Modifier.centred(160f, 160f), horizontalArrangement = Arrangement.spacedBy(46.dp)) {
            Glyph(Icons.Default.Share, 14f, Color.White); Glyph(Icons.Default.ArrowCircleDown, 14f, Color.White); Glyph(Icons.Default.Delete, 14f, Color.White)
        }
        val sheet = eased(if (at >= 7) 72f else 180f)
        Column(
            Modifier.at(0f, sheet).size(STAGE_W.dp, 104.dp).clip(RoundedCornerShape(topStart = 12.dp, topEnd = 12.dp)).background(p.surface).padding(10.dp),
            verticalArrangement = Arrangement.spacedBy(6.dp),
        ) {
            Box(Modifier.align(Alignment.CenterHorizontally).size(30.dp, 3.dp).clip(CircleShape).background(p.dim.copy(alpha = 0.4f)))
            Words(S.appPhotosViewerInfoTitle().resolve(), 11f, FontWeight.Bold)
            Row(horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                Column(verticalArrangement = Arrangement.spacedBy(5.dp)) {
                    val taken = remember {
                        val c = Calendar.getInstance(); c.add(Calendar.MONTH, -2)
                        java.text.DateFormat.getDateInstance(java.text.DateFormat.MEDIUM, context.resources.configuration.locales[0]).format(c.time)
                    }
                    InfoRow(p, S.appPhotosViewerInfoTaken().resolve()) { Words(taken, 8f) }
                    InfoRow(p, S.appPhotosViewerInfoCamera().resolve()) { Bar(p, 50f, 4f) }
                    InfoRow(p, S.appPhotosViewerInfoLocation().resolve()) { Bar(p, 40f, 4f) }
                }
                Canvas(Modifier.size(90.dp, 56.dp).clip(RoundedCornerShape(6.dp))) {
                    drawRect(Color(0xFF4CAF50).copy(alpha = 0.25f))
                    drawLine(Color.Gray.copy(alpha = 0.5f), Offset(0f, size.height * 0.55f), Offset(size.width, size.height * 0.2f), 2.dp.toPx())
                    drawCircle(Color(0xFFE53935), 5.dp.toPx(), Offset(size.width / 2, size.height / 2))
                }
            }
        }
    }
    val (fx, fy) = when (at) {
        0, 1 -> 160f to 46f
        2 -> 230f to 90f
        3 -> 110f to 90f
        4 -> 160f to 120f
        5, 6 -> 22f to 20f
        else -> 250f to 60f
    }
    Finger(fx, fy, down = at == 1 || at == 3 || at == 6, hidden = at == 7)
}

@Composable
private fun InfoRow(p: ArtPalette, label: String, value: @Composable () -> Unit) {
    Row(verticalAlignment = Alignment.CenterVertically) {
        Words(label, 8f, color = p.dim, modifier = Modifier.width(64.dp))
        Spacer(Modifier.width(6.dp))
        value()
    }
}

// ---- people: a name given, two faces merged ----

@Composable
private fun BoxScope.PeopleArt(p: ArtPalette) {
    // rest, Add a name, typing, named, a long press, another, the bar
    val at = rememberScene(intArrayOf(700, 500, 1300, 600, 450, 450, 1900), 6)
    val picked = when { at >= 5 -> 2; at >= 4 -> 1; else -> 0 }
    val friend = S.appTourArtFriend().resolve()
    // Android's People page: its top bar becomes the selection's, with Merge and Delete (PeopleView.kt's SelectionBar).
    Row(
        Modifier.at(0f, 0f).size(STAGE_W.dp, 26.dp).background(if (picked > 0) p.accentFill else Color.Transparent).padding(horizontal = 10.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        if (picked > 0) {
            Glyph(Icons.Default.Close, 11f); Spacer(Modifier.width(8.dp))
            Words(S.appShellSelected(picked).resolve(), 10f, FontWeight.SemiBold, modifier = Modifier.weight(1f))
            Words(S.appPhotosPeopleMerge().resolve(), 9f, FontWeight.SemiBold, if (picked >= 2) p.accent else p.dim,
                modifier = Modifier.clip(CircleShape).background(if (at == 6) p.accent.copy(alpha = 0.2f) else Color.Transparent).padding(horizontal = 6.dp, vertical = 2.dp))
            Spacer(Modifier.width(10.dp))
            Glyph(Icons.Default.Delete, 12f)
        } else {
            Words(Section.People.label.resolve(), 11f, FontWeight.Bold)
        }
    }
    val xs = floatArrayOf(44f, 120f, 196f, 272f)
    for (i in 0 until 4) {
        val on = i >= 2 && i - 2 < picked
        Column(Modifier.centred(xs[i], 82f).width(74.dp), horizontalAlignment = Alignment.CenterHorizontally) {
            Box(Modifier.size(50.dp)) {
                Face(p, i + 1, Modifier.fillMaxSize())
                if (on) { Box(Modifier.fillMaxSize().border(2.5.dp, p.accent, CircleShape)); Tick(p, true) }
            }
            Spacer(Modifier.height(3.dp))
            Box(Modifier.height(16.dp), contentAlignment = Alignment.Center) {
                when {
                    i == 0 -> Words(S.appTourArtPerson().resolve(), 9f, FontWeight.SemiBold)
                    i == 1 && at == 2 -> Box(Modifier.clip(RoundedCornerShape(4.dp)).background(p.field).padding(horizontal = 4.dp)) { Typed(friend, true, 9f) }
                    i == 1 && at >= 3 -> Words(friend, 9f, FontWeight.SemiBold)
                    else -> Words(S.appPhotosPeopleAddName().resolve(), 9f, color = p.accent, italic = true)
                }
            }
            Words(S.appPhotosPeoplePhotoCount(intArrayOf(12, 8, 5, 3)[i]).resolve(), 7f, color = p.dim)
        }
    }
    val (fx, fy) = when (at) {
        1, 2 -> 120f to 120f
        4 -> 196f to 66f
        5 -> 272f to 66f
        6 -> 236f to 13f
        else -> 240f to 150f
    }
    Finger(fx, fy, down = at == 1 || at == 4 || at == 5 || at == 6)
}

// ---- collections: added to a new one, found, opened ----

@Composable
private fun BoxScope.CollectionsArt(p: ArtPalette) {
    // selected, Add to collection, its choices, the name, the list, the card tapped, the collection open
    val at = rememberScene(intArrayOf(700, 700, 900, 1300, 1300, 450, 1900), 4)
    val name = S.appTourArtCollection().resolve()
    val page = when { at >= 6 -> 2; at >= 4 -> 1; else -> 0 }
    Box(Modifier.fillMaxSize().alpha(eased(if (page == 0) 1f else 0f))) {
        for (i in 0 until 6) Box(Modifier.at(10f + (i % 3) * 102f, 6f + (i / 3) * 50f).size(96.dp, 44.dp)) {
            Tile(p, i + 2, Modifier.fillMaxSize().scale(if (i < 3) 0.9f else 1f))
            if (i < 3) Tick(p, true, Modifier.padding(4.dp))
        }
        SelectedPill(p, 3, Modifier.centred(160f, 116f))
        ActionBar(
            p, listOf(
                BarItem(Icons.Default.Share, S.commonShare().resolve()),
                BarItem(Icons.Default.Download, S.commonDownload().resolve()),
                BarItem(NavIcons.AddToCollection, S.appShellAddToCollection().resolve()),
                BarItem(Icons.Default.Delete, S.commonDelete().resolve(), Color(0xFFE53935)),
            ), if (at >= 1) 2 else -1, Modifier.at(10f, 130f).width(300.dp),
        )
        // The choices, as Android's bottom sheet.
        Column(
            Modifier.at(0f, eased(if (at == 2) 92f else 180f)).size(STAGE_W.dp, 90.dp).clip(RoundedCornerShape(topStart = 14.dp, topEnd = 14.dp))
                .background(p.surface).padding(horizontal = 14.dp, vertical = 8.dp),
            verticalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            Words(S.appPhotosCollectionsAddTitle(3).resolve(), 9f, FontWeight.SemiBold)
            Words(S.appPhotosCollectionsNewItem().resolve(), 10f, color = p.accent,
                modifier = Modifier.clip(RoundedCornerShape(6.dp)).background(if (at == 2) p.accent.copy(alpha = 0.12f) else Color.Transparent).padding(4.dp))
            Bar(p, 70f)
        }
        // Its name, as the dialog asks it.
        Column(
            Modifier.centred(160f, 80f).alpha(eased(if (at == 3) 1f else 0f)).shadow(8.dp, RoundedCornerShape(14.dp)).clip(RoundedCornerShape(14.dp))
                .background(p.surface).padding(12.dp),
            horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.spacedBy(6.dp),
        ) {
            Words(S.appPhotosCollectionsNewTitle().resolve(), 10f, FontWeight.SemiBold)
            Box(Modifier.size(150.dp, 18.dp).border(1.dp, p.line, RoundedCornerShape(4.dp)).padding(horizontal = 6.dp), contentAlignment = Alignment.CenterStart) {
                Typed(name, at == 3, 9f)
            }
            Words(S.commonCreate().resolve(), 10f, FontWeight.SemiBold, p.accent, modifier = Modifier.align(Alignment.End))
        }
    }
    // The collections.
    Column(Modifier.at(10f, 8f).alpha(eased(if (page == 1) 1f else 0f)), verticalArrangement = Arrangement.spacedBy(8.dp)) {
        Words(S.appShellSectionCollections().resolve(), 11f, FontWeight.Bold)
        Row(horizontalArrangement = Arrangement.spacedBy(10.dp)) {
            CollectionCard(p, 2, name, at == 5); CollectionCard(p, 5, null, false); CollectionCard(p, 1, null, false)
        }
    }
    // One open: its header (CollectionHeader) over its photos.
    Column(Modifier.at(10f, 8f).width(300.dp).alpha(eased(if (page == 2) 1f else 0f)), verticalArrangement = Arrangement.spacedBy(8.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Column(Modifier.weight(1f)) {
                Words(name, 11f, FontWeight.Bold)
                Words(S.appPhotosCollectionsPictures(3).resolve(), 8f, color = p.dim)
            }
            Glyph(androidx.compose.material.icons.Icons.Outlined.Share, 12f, p.accent); Spacer(Modifier.width(12.dp)); Glyph(Icons.Default.Delete, 12f, Color(0xFFE53935))
        }
        Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) { for (i in 0 until 3) Tile(p, i + 2, Modifier.size(96.dp, 70.dp)) }
    }
    val (fx, fy) = when (at) {
        1 -> 198f to 150f
        2 -> 60f to 120f
        3 -> 230f to 120f
        5 -> 52f to 70f
        else -> 260f to 160f
    }
    Finger(fx, fy, down = at == 1 || at == 2 || at == 5)
}

@Composable
private fun CollectionCard(p: ArtPalette, hue: Int, name: String?, lit: Boolean) {
    Column(verticalArrangement = Arrangement.spacedBy(3.dp)) {
        Box(Modifier.size(92.dp, 80.dp)) {
            Tile(p, hue, Modifier.fillMaxSize())
            if (lit) Box(Modifier.fillMaxSize().border(2.dp, p.accent, RoundedCornerShape(5.dp)))
        }
        if (name != null) {
            Words(name, 9f, FontWeight.SemiBold)
            Words(S.appPhotosCollectionsPictures(3).resolve(), 7f, color = p.dim)
        } else {
            Bar(p, 56f); Bar(p, 34f, 4f)
        }
    }
}

// ---- files: List, Grid, Storage, and an order ----

private data class FileRow(val folder: Boolean, val icon: ImageVector, val w: Float, val bytes: Long, val share: Float)

private val FILE_ROWS = listOf(
    FileRow(true, Icons.Default.Folder, 80f, 2_400_000_000, 0.62f),
    FileRow(false, Icons.Default.Image, 96f, 4_300_000, 0.04f),
    FileRow(false, Icons.Default.Description, 64f, 128_000, 0.01f),
    FileRow(false, Icons.Default.Image, 88f, 890_000_000, 0.33f),
)

@Composable
private fun BoxScope.FilesArt(p: ArtPalette) {
    // list, the menu (Grid), grid, the menu (Storage), storage, the menu (List), list, the menu (Size), by size
    val at = rememberScene(intArrayOf(800, 900, 1100, 900, 1300, 700, 500, 900, 1900), 1)
    val context = LocalContext.current
    val mode = intArrayOf(0, 0, 1, 1, 2, 2, 0, 0, 0)[at]
    val menu = at == 1 || at == 3 || at == 5 || at == 7
    val hl = intArrayOf(-1, 1, -1, 2, -1, 0, -1, 4, -1)[at]
    val bySize = at >= 8
    Row(Modifier.at(10f, 6f), verticalAlignment = Alignment.CenterVertically) {
        Box(Modifier.size(262.dp, 22.dp).border(1.dp, p.dim, RoundedCornerShape(5.dp)).padding(horizontal = 8.dp), contentAlignment = Alignment.CenterStart) {
            Words("/", 10f) // i18n-ignore: the root folder's path
        }
        Spacer(Modifier.width(10.dp))
        Box(Modifier.clip(CircleShape).background(if (menu) p.accent.copy(alpha = 0.18f) else Color.Transparent).padding(3.dp)) {
            Glyph(when (mode) { 1 -> Icons.Default.GridView; 2 -> Icons.Default.PieChart; else -> Icons.AutoMirrored.Filled.ViewList }, 15f)
        }
    }
    Box(Modifier.fillMaxSize().alpha(if (menu) 0.45f else 1f)) {
        when (mode) {
            0 -> {
                val order = if (bySize) listOf(0, 3, 1, 2) else listOf(0, 1, 2, 3)
                FILE_ROWS.forEachIndexed { i, r ->
                    Row(Modifier.at(12f, eased(40f + order.indexOf(i) * 32f)).width(296.dp), verticalAlignment = Alignment.CenterVertically) {
                        Box(Modifier.size(12.dp).border(1.dp, p.dim.copy(alpha = 0.6f), CircleShape))
                        Spacer(Modifier.width(8.dp))
                        Glyph(r.icon, 12f, if (r.folder) Ember else p.dim)
                        Spacer(Modifier.width(8.dp))
                        Bar(p, r.w)
                        Spacer(Modifier.weight(1f))
                        if (r.folder) Glyph(Icons.Outlined.LockOpen, 11f, p.dim) else Words(fileSize(context, r.bytes), 8f, color = p.dim)
                    }
                }
            }
            1 -> FILE_ROWS.forEachIndexed { i, r ->
                Column(Modifier.at(12f + i * 75f, 44f), horizontalAlignment = Alignment.CenterHorizontally) {
                    if (i == 1 || i == 3) Tile(p, i + 1, Modifier.size(66.dp, 56.dp))
                    else Box(Modifier.size(66.dp, 56.dp).clip(RoundedCornerShape(6.dp)).background(p.field), contentAlignment = Alignment.Center) {
                        Glyph(r.icon, 22f, if (r.folder) Ember else p.dim)
                    }
                    Spacer(Modifier.height(4.dp))
                    Bar(p, 46f)
                }
            }
            else -> {
                Words(S.appFilesUsageTotal(fileSize(context, 3_294_428_000), 4).resolve(), 8f, color = p.dim, modifier = Modifier.at(12f, 38f))
                listOf(0, 3, 1, 2).forEachIndexed { k, i ->
                    val r = FILE_ROWS[i]
                    Row(Modifier.at(12f, 56f + k * 29f).width(296.dp), verticalAlignment = Alignment.CenterVertically) {
                        Glyph(r.icon, 12f, if (r.folder) Ember else p.dim)
                        Spacer(Modifier.width(8.dp))
                        Column(verticalArrangement = Arrangement.spacedBy(3.dp)) {
                            Bar(p, r.w * 0.8f, 4f)
                            Box(Modifier.size(150.dp, 4.dp).clip(CircleShape).background(p.dim.copy(alpha = 0.18f))) {
                                Box(Modifier.size(maxOf(4f, 150f * r.share).dp, 4.dp).clip(CircleShape).background(p.accent))
                            }
                        }
                        Spacer(Modifier.weight(1f))
                        Words(S.appFilesUsageRow(fileSize(context, r.bytes), "${Math.round(r.share * 100)}%").resolve(), 8f, color = p.dim) // i18n-ignore: a share as a percentage
                    }
                }
            }
        }
    }
    // The View options menu (ViewOptionsMenu).
    val ma = eased(if (menu) 1f else 0f)
    Column(
        Modifier.at(158f, 30f).width(150.dp).graphicsLayer { alpha = ma; scaleX = 0.8f + 0.2f * ma; scaleY = 0.8f + 0.2f * ma; transformOrigin = TransformOrigin(1f, 0f) }
            .shadow(6.dp, RoundedCornerShape(8.dp)).clip(RoundedCornerShape(8.dp)).background(p.surface).padding(vertical = 4.dp),
    ) {
        MenuItem(p, S.appFilesViewList().resolve(), Icons.AutoMirrored.Filled.ViewList, mode == 0, hl == 0)
        MenuItem(p, S.appFilesViewGrid().resolve(), Icons.Default.GridView, mode == 1, hl == 1)
        MenuItem(p, S.appFilesViewStorage().resolve(), Icons.Default.PieChart, mode == 2, hl == 2)
        if (mode != 2) {
            HorizontalDivider(Modifier.padding(vertical = 2.dp), color = p.line)
            Words(S.appFilesSortBy().resolve(), 7f, FontWeight.SemiBold, p.dim, modifier = Modifier.padding(horizontal = 8.dp, vertical = 2.dp))
            MenuItem(p, S.appFilesSortName().resolve(), null, !bySize, false, if (!bySize) S.appFilesSortAZ().resolve() else null)
            MenuItem(p, S.appFilesSortSize().resolve(), null, bySize, hl == 4, if (bySize) S.appFilesSortLargest().resolve() else null)
        }
    }
    val (fx, fy) = when (hl) {
        0 -> 210f to 44f
        1 -> 210f to 62f
        2 -> 210f to 80f
        4 -> 210f to 140f
        else -> 292f to 17f
    }
    Finger(fx, fy, down = menu)
}

@Composable
private fun MenuItem(p: ArtPalette, label: String, icon: ImageVector?, checked: Boolean, lit: Boolean, detail: String? = null) {
    Row(
        Modifier.fillMaxWidth().background(if (lit) p.accent.copy(alpha = 0.16f) else Color.Transparent).padding(horizontal = 8.dp, vertical = 3.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Glyph(Icons.Default.Check, 9f, if (checked) p.text else Color.Transparent)
        Spacer(Modifier.width(6.dp))
        Column(Modifier.weight(1f)) {
            Words(label, 9f)
            if (detail != null) Words(detail, 7f, color = p.dim)
        }
        if (icon != null) Glyph(icon, 10f, p.dim)
    }
}

// ---- upload: added, ticked, shared, kept upload only ----

@Composable
private fun BoxScope.UploadArt(p: ArtPalette) {
    // rest, Upload, uploading, done, a tick, another, Share, its chooser, the lock, locked, the versions
    val at = rememberScene(intArrayOf(600, 300, 1100, 500, 450, 450, 300, 1300, 500, 900, 1600), 5)
    val ticks = when { at >= 5 -> 2; at >= 4 -> 1; else -> 0 }
    val locked = at >= 9
    data class Row4(val key: String, val icon: ImageVector, val folder: Boolean, val w: Float)
    val rows = (if (at >= 2) listOf(Row4("new", Icons.Default.Description, false, 86f)) else emptyList()) +
        listOf(Row4("f", Icons.Default.Folder, true, 70f), Row4("p", Icons.Default.Image, false, 92f), Row4("d", Icons.Default.Description, false, 60f))
    rows.forEachIndexed { i, r ->
        val checked = (i == 1 && ticks >= 1) || (i == 2 && ticks >= 2)
        Row(Modifier.at(12f, eased(8f + i * 26f)).width(296.dp), verticalAlignment = Alignment.CenterVertically) {
            Box(Modifier.size(12.dp).clip(CircleShape).background(if (checked) p.accent else Color.Transparent).border(1.dp, if (checked) p.accent else p.dim.copy(alpha = 0.6f), CircleShape), contentAlignment = Alignment.Center) {
                if (checked) Glyph(Icons.Default.Check, 8f, Color.White)
            }
            Spacer(Modifier.width(8.dp))
            Glyph(r.icon, 12f, if (r.folder) Ember else p.dim)
            Spacer(Modifier.width(8.dp))
            Bar(p, r.w)
            if (r.key == "d" && at >= 10) {
                Spacer(Modifier.width(6.dp))
                Row(Modifier.clip(CircleShape).background(p.field).padding(horizontal = 4.dp, vertical = 1.dp), verticalAlignment = Alignment.CenterVertically) {
                    Glyph(Icons.Default.History, 9f, p.dim); Words("2", 8f, color = p.dim) // i18n-ignore: a count of versions
                }
            }
            Spacer(Modifier.weight(1f))
            if (r.key == "new" && at == 2) Box(Modifier.size(60.dp, 4.dp).clip(CircleShape).background(p.dim.copy(alpha = 0.2f))) {
                Box(Modifier.size(40.dp, 4.dp).clip(CircleShape).background(p.accent))
            }
            if (r.folder) Box(Modifier.clip(CircleShape).background(if (at == 8) p.accent.copy(alpha = 0.2f) else Color.Transparent).padding(3.dp)) {
                Glyph(if (locked) Icons.Default.Lock else Icons.Outlined.LockOpen, 12f, if (locked) p.accent else p.dim)
            }
        }
    }
    if (at == 2) Words(S.appFilesUploading(1, 1).resolve(), 8f, color = p.dim, modifier = Modifier.centred(160f, 116f))
    if (ticks > 0 && at <= 7) SelectedPill(p, ticks, Modifier.centred(160f, 118f))
    val on = ticks > 0 && at <= 7
    ActionBar(
        p, listOf(
            BarItem(Icons.Default.Upload, S.commonUpload().resolve()),
            BarItem(Icons.Default.Share, S.commonShare().resolve(), enabled = on),
            BarItem(Icons.Default.Download, S.commonDownload().resolve(), enabled = on),
            BarItem(Icons.Default.Delete, S.commonDelete().resolve(), Color(0xFFE53935), enabled = on),
        ), when (at) { 1 -> 0; 6 -> 1; else -> -1 }, Modifier.at(20f, 134f).width(280.dp),
    )
    // The system's chooser, with the link.
    Column(
        Modifier.at(0f, eased(if (at == 7) 100f else 190f)).width(STAGE_W.dp).clip(RoundedCornerShape(topStart = 14.dp, topEnd = 14.dp))
            .background(p.surface).padding(12.dp),
        horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) { Glyph(Icons.Default.Link, 11f, p.accent); Spacer(Modifier.width(6.dp)); Bar(p, 120f) }
        Row(horizontalArrangement = Arrangement.spacedBy(14.dp)) { for (i in 0 until 5) Box(Modifier.size(26.dp).clip(CircleShape).background(p.tile(i).first)) }
    }
    if (at == 9) Box(Modifier.at(220f, 56f).clip(CircleShape).background(Color.Black.copy(alpha = 0.75f)).padding(horizontal = 6.dp, vertical = 2.dp)) {
        Words(S.appFilesUploadOnly().resolve(), 8f, color = Color.White)
    }
    val (fx, fy) = when (at) {
        1 -> 55f to 152f
        4 -> 18f to 40f
        5 -> 18f to 66f
        6 -> 125f to 152f
        8 -> 302f to 40f
        else -> 250f to 100f
    }
    Finger(fx, fy, down = at == 1 || at == 4 || at == 5 || at == 6 || at == 8, hidden = at == 7)
}

// ---- social: liked, commented ----

@Composable
private fun BoxScope.SocialArt(p: ArtPalette) {
    // rest, the heart, liked, to the field, typing, Post, the comment
    val at = rememberScene(intArrayOf(800, 300, 700, 500, 1200, 300, 2000), 6)
    val liked = at >= 1
    val comment = S.appTourArtComment().resolve()
    Row(Modifier.at(10f, 4f), verticalAlignment = Alignment.CenterVertically) {
        Box(Modifier.size(18.dp).clip(CircleShape).background(p.tile(4).first))
        Spacer(Modifier.width(6.dp))
        Column { Words(S.appTourArtFriend().resolve(), 10f, FontWeight.SemiBold); Bar(p, 50f, 4f) }
    }
    Tile(p, 3, Modifier.at(10f, 28f).size(300.dp, 66.dp), radius = 0f)
    Glyph(if (liked) Icons.Default.Favorite else Icons.Default.FavoriteBorder, 15f, if (liked) Color.Red else p.text, Modifier.at(12f, 98f).scale(eased(if (at == 1) 1.3f else 1f)))
    Words(S.appSocialPostLikes(if (liked) 4 else 3).resolve(), 9f, FontWeight.Bold, modifier = Modifier.at(12f, 116f))
    if (at >= 6) RichWords(S.appSocialPostComment(S.appTourArtPerson().resolve(), comment).bold(), 9f, Modifier.at(12f, 130f))
    Row(Modifier.at(10f, 146f), verticalAlignment = Alignment.CenterVertically) {
        Box(Modifier.size(250.dp, 22.dp).border(1.dp, p.dim, RoundedCornerShape(5.dp)).padding(horizontal = 6.dp), contentAlignment = Alignment.CenterStart) {
            if (at in 4..5) Typed(comment, true, 9f) else Words(S.appSocialPostCommentPlaceholder().resolve(), 9f, color = p.dim)
        }
        Spacer(Modifier.width(10.dp))
        Words(S.appSocialPostSendComment().resolve(), 9f, FontWeight.SemiBold, if (at in 4..5) p.accent else p.dim)
    }
    val (fx, fy) = when (at) {
        1, 2 -> 20f to 106f
        3, 4 -> 120f to 157f
        5 -> 282f to 157f
        else -> 220f to 110f
    }
    Finger(fx, fy, down = at == 1 || at == 5)
}

// ---- post: composed and published ----

@Composable
private fun BoxScope.PostArt(p: ArtPalette, wide: Boolean) {
    // the feed, +, the composer, a photo, another, the caption, Publish, the new post
    val at = rememberScene(intArrayOf(700, 300, 700, 450, 450, 1300, 400, 2100), 5)
    val composing = at in 2..6
    val picks = when { at >= 4 -> 2; at >= 3 -> 1; else -> 0 }
    val caption = S.appTourArtCaption().resolve()
    // The bar: Friends and + (narrow), or the top bar's + New post.
    Row(Modifier.at(10f, 6f).width(300.dp), verticalAlignment = Alignment.CenterVertically) {
        Box(Modifier.size(18.dp, 14.dp).clip(RoundedCornerShape(4.dp)).background(Ember.copy(alpha = 0.8f)))
        Spacer(Modifier.width(6.dp)); Bar(p, 46f); Spacer(Modifier.weight(1f))
        if (wide) {
            Row(
                Modifier.clip(CircleShape).background(if (at == 1) Ember.copy(alpha = 0.2f) else Color.Transparent).border(1.dp, Ember, CircleShape)
                    .padding(horizontal = 8.dp, vertical = 3.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) { Words("+ " + S.appShellNewPost().resolve(), 9f, FontWeight.SemiBold, Ember) } // i18n-ignore: the button's plus, as MainView's
        } else {
            Glyph(Icons.Default.Group, 14f)
            Spacer(Modifier.width(14.dp))
            Box(Modifier.clip(CircleShape).background(if (at == 1) Ember.copy(alpha = 0.25f) else Color.Transparent)) { Glyph(Icons.Outlined.AddCircleOutline, 16f, Ember) }
        }
    }
    Column(Modifier.at(10f, 30f), verticalArrangement = Arrangement.spacedBy(8.dp)) {
        if (at >= 7) Column(verticalArrangement = Arrangement.spacedBy(3.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Box(Modifier.size(14.dp).clip(CircleShape).background(p.tile(0).first)); Spacer(Modifier.width(5.dp)); Words(S.appTourArtPerson().resolve(), 9f, FontWeight.SemiBold)
            }
            Words(caption, 9f)
            Row(horizontalArrangement = Arrangement.spacedBy(4.dp)) { Tile(p, 1, Modifier.size(90.dp, 50.dp)); Tile(p, 3, Modifier.size(90.dp, 50.dp)) }
        }
        Column(verticalArrangement = Arrangement.spacedBy(3.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) { Box(Modifier.size(14.dp).clip(CircleShape).background(p.tile(3).first)); Spacer(Modifier.width(5.dp)); Bar(p, 40f) }
            Tile(p, 4, Modifier.size(300.dp, 60.dp))
        }
    }
    // The composer: Android's full-screen dialog.
    Column(
        Modifier.at(0f, eased(if (composing) 0f else 180f)).size(STAGE_W.dp, STAGE_H.dp).background(p.surface).padding(10.dp),
        horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.spacedBy(6.dp),
    ) {
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
            Words(S.commonCancel().resolve(), 9f, color = p.accent); Spacer(Modifier.width(14.dp)); Words(S.appSocialComposeTitle().resolve(), 11f, FontWeight.SemiBold)
        }
        Row(Modifier.width(180.dp).border(1.dp, p.dim, CircleShape).clip(CircleShape)) {
            Box(Modifier.weight(1f).background(p.accentFill).padding(vertical = 2.dp), contentAlignment = Alignment.Center) {
                Row(verticalAlignment = Alignment.CenterVertically) { Glyph(Icons.Default.Check, 8f); Words(S.appSocialComposeSourcePhone().resolve(), 8f, FontWeight.SemiBold) }
            }
            Box(Modifier.weight(1f).padding(vertical = 2.dp), contentAlignment = Alignment.Center) { Words(S.appSocialComposeSourceSynced().resolve(), 8f) }
        }
        Row(horizontalArrangement = Arrangement.spacedBy(3.dp)) {
            for (i in 0 until 5) {
                val order = if (i == 0 && picks >= 1) 1 else if (i == 2 && picks >= 2) 2 else 0
                Box(Modifier.size(56.dp, 48.dp), contentAlignment = Alignment.TopEnd) {
                    Tile(p, i + 1, Modifier.fillMaxSize(), radius = 2f)
                    if (order > 0) Box(Modifier.padding(2.dp).size(12.dp).clip(CircleShape).background(p.accent), contentAlignment = Alignment.Center) {
                        Words("$order", 7f, FontWeight.Bold, Color.White) // i18n-ignore: a pick's place in the post
                    }
                }
            }
        }
        Spacer(Modifier.weight(1f))
        Row(verticalAlignment = Alignment.CenterVertically) {
            Glyph(Icons.Default.PhotoCamera, 12f, p.dim)
            Spacer(Modifier.width(6.dp))
            Box(Modifier.size(170.dp, 20.dp).border(1.dp, p.dim, RoundedCornerShape(5.dp)).padding(horizontal = 5.dp), contentAlignment = Alignment.CenterStart) {
                if (at >= 5) Typed(caption, true, 8f, at == 5) else Words(S.appSocialComposeCaptionPlaceholder().resolve(), 8f, color = p.dim)
            }
            Spacer(Modifier.width(6.dp))
            Box(Modifier.scale(if (at == 6) 0.92f else 1f).clip(CircleShape).background(p.accent.copy(alpha = if (at >= 5) 1f else 0.4f)).padding(horizontal = 8.dp, vertical = 3.dp)) {
                Words((if (picks > 0) S.appSocialComposePublishCount(picks) else S.appSocialComposePublish()).resolve(), 8f, FontWeight.SemiBold, MaterialTheme.colorScheme.onPrimary)
            }
        }
    }
    val (fx, fy) = when (at) {
        1 -> (if (wide) 270f else 302f) to 14f
        3 -> 42f to 72f
        4 -> 160f to 72f
        5 -> 120f to 160f
        6 -> 284f to 160f
        else -> 230f to 120f
    }
    Finger(fx, fy, down = at == 1 || at == 3 || at == 4 || at == 6)
}

// ---- friends: the address, a request sent, one accepted ----

@Composable
private fun BoxScope.FriendsArt(p: ArtPalette) {
    // rest, Copy, copied, the name typed, Send, sent, Accept, accepted
    val at = rememberScene(intArrayOf(700, 300, 900, 1200, 300, 1100, 400, 1800), 3)
    val me = deviceName(S.appTourArtPerson().resolve())
    val friend = S.appTourArtFriend().resolve()
    val suffix = ".${SecretsStore.bridgeDomain}"
    Column(Modifier.at(10f, 6f).width(300.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
        Row(Modifier.fillMaxWidth().clip(RoundedCornerShape(8.dp)).background(p.surface).padding(8.dp), verticalAlignment = Alignment.CenterVertically) {
            Words(S.appSocialFriendsYourAddress(me + suffix).resolve(), 9f, modifier = Modifier.weight(1f))
            Box(Modifier.clip(CircleShape).background(if (at == 1) p.accent.copy(alpha = 0.2f) else Color.Transparent).padding(3.dp)) { Glyph(Icons.Outlined.ContentCopy, 11f, p.accent) }
            Spacer(Modifier.width(6.dp))
            Glyph(Icons.Outlined.Share, 11f, p.accent)
        }
        Words(S.appSocialFriendsWaitingAnswer().resolve().uppercase(), 7f, FontWeight.SemiBold, p.dim)
        Row(Modifier.fillMaxWidth().clip(RoundedCornerShape(8.dp)).background(p.surface).padding(8.dp), verticalAlignment = Alignment.CenterVertically) {
            Box(Modifier.size(16.dp).clip(CircleShape).background(p.tile(2).first))
            Spacer(Modifier.width(6.dp))
            Column(Modifier.weight(1f)) { Words(friend, 9f, FontWeight.SemiBold); Words(deviceName(friend) + suffix, 7f, color = p.dim) }
            if (at >= 7) Words(S.appSocialFriendsStatusAccepted().resolve(), 8f, FontWeight.SemiBold, Color(0xFF2E7D32))
            else {
                Words(S.commonDecline().resolve(), 8f, color = p.accent, modifier = Modifier.border(1.dp, p.dim, CircleShape).padding(horizontal = 6.dp, vertical = 2.dp))
                Spacer(Modifier.width(6.dp))
                Box(Modifier.scale(if (at == 6) 0.9f else 1f).clip(CircleShape).background(p.accent).padding(horizontal = 6.dp, vertical = 3.dp)) {
                    Words(S.commonAccept().resolve(), 8f, FontWeight.SemiBold, MaterialTheme.colorScheme.onPrimary)
                }
            }
        }
        Words(S.appSocialFriendsAddHeading().resolve().uppercase(), 7f, FontWeight.SemiBold, p.dim)
        Row(Modifier.fillMaxWidth().clip(RoundedCornerShape(8.dp)).background(p.surface).padding(8.dp), verticalAlignment = Alignment.CenterVertically) {
            Box(Modifier.weight(1f).border(1.dp, p.dim, RoundedCornerShape(4.dp)).padding(horizontal = 6.dp, vertical = 2.dp)) {
                if (at in 3..4) Typed(deviceName(friend), true, 9f) else Words(S.appSocialFriendsNamePlaceholder().resolve(), 9f, color = p.dim)
            }
            Spacer(Modifier.width(4.dp))
            Words(suffix, 9f, color = p.dim)
            Spacer(Modifier.width(8.dp))
            Words(S.commonSend().resolve(), 9f, FontWeight.SemiBold, if (at in 3..4) p.accent else p.dim)
        }
    }
    Toast(S.appSocialFriendsAddressCopied().resolve(), at == 2, Modifier.centred(160f, 160f))
    Toast(S.appSocialFriendsRequestSent().resolve(), at == 5, Modifier.centred(160f, 160f))
    val (fx, fy) = when (at) {
        1 -> 270f to 20f
        3 -> 150f to 149f
        4 -> 292f to 149f
        6 -> 290f to 76f
        else -> 120f to 150f
    }
    Finger(fx, fy, down = at == 1 || at == 4 || at == 6, hidden = at == 2 || at == 5)
}

// ---- backup: the phone's photos going to the device, Wi-Fi only ----

@Composable
private fun BoxScope.BackupArt(p: ArtPalette) {
    // rest, one photo sent, another, to Wi-Fi only, on, sent over Wi-Fi
    val at = rememberScene(intArrayOf(700, 900, 900, 500, 900, 1700), 4)
    val wifiOnly = at >= 4
    // The phone, with its photos.
    Box(Modifier.at(24f, 8f).size(50.dp, 84.dp).border(1.5.dp, p.text.copy(alpha = 0.6f), RoundedCornerShape(9.dp)), contentAlignment = Alignment.Center) {
        Column(verticalArrangement = Arrangement.spacedBy(3.dp)) {
            for (r in 0 until 3) Row(horizontalArrangement = Arrangement.spacedBy(3.dp)) { for (c in 0 until 2) Tile(p, r * 2 + c, Modifier.size(16.dp), 2f) }
        }
    }
    // The device at home.
    Box(Modifier.at(236f, 24f).size(60.dp, 50.dp).clip(RoundedCornerShape(8.dp)).background(p.surface).border(1.2.dp, p.text.copy(alpha = 0.5f), RoundedCornerShape(8.dp)), contentAlignment = Alignment.Center) {
        Column(verticalArrangement = Arrangement.spacedBy(5.dp)) { for (i in 0 until 3) Box(Modifier.size(34.dp, 3.dp).clip(CircleShape).background(p.dim.copy(alpha = 0.4f))) }
        Box(Modifier.align(Alignment.BottomEnd).padding(6.dp).size(5.dp).clip(CircleShape).background(Color(0xFF4CAF50)))
    }
    Canvas(Modifier.at(82f, 49f).size(146.dp, 2.dp)) {
        drawLine(p.dim.copy(alpha = 0.5f), Offset.Zero, Offset(size.width, 0f), 1.2.dp.toPx(), pathEffect = PathEffect.dashPathEffect(floatArrayOf(4.dp.toPx(), 4.dp.toPx())))
    }
    Glyph(Icons.Default.Wifi, 14f, if (wifiOnly) p.accent else p.dim, Modifier.centred(155f, 34f))
    // A photo on its way.
    val sending = at == 1 || at == 2 || at == 5
    val tx = animateFloatAsState(if (sending) 228f else 82f, if (sending) tween(800) else tween(0), label = "tourBackup").value
    if (sending) Tile(p, at % 3, Modifier.centred(tx, 50f).size(16.dp), 2f)
    Column(Modifier.at(10f, 104f).width(300.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
        Words(S.appSettingsSyncTitle().resolve().uppercase(), 7f, FontWeight.SemiBold, p.dim)
        Column(Modifier.fillMaxWidth().clip(RoundedCornerShape(8.dp)).background(p.surface)) {
            Row(Modifier.fillMaxWidth().height(26.dp).background(if (at == 3) p.accent.copy(alpha = 0.12f) else Color.Transparent).padding(horizontal = 8.dp), verticalAlignment = Alignment.CenterVertically) {
                Words(S.appSettingsSyncWifiOnly().resolve(), 9f, modifier = Modifier.weight(1f)); Switch(p, wifiOnly)
            }
            HorizontalDivider(color = p.line)
            Row(Modifier.fillMaxWidth().height(26.dp).padding(horizontal = 8.dp), verticalAlignment = Alignment.CenterVertically) {
                Words(S.appSettingsSyncVideos().resolve(), 9f, modifier = Modifier.weight(1f)); Switch(p, true)
            }
        }
    }
    Finger(if (at == 3 || at == 4) 296f else 200f, if (at == 3 || at == 4) 129f else 90f, down = at == 3)
}

// ---- users: one added ----

@Composable
private fun BoxScope.UsersArt(p: ArtPalette) {
    // rest, the name typed, Add, the first password, typed, Create, added
    val at = rememberScene(intArrayOf(700, 1100, 300, 600, 900, 300, 2000), 6)
    val me = deviceName(S.appTourArtPerson().resolve())
    val friend = deviceName(S.appTourArtFriend().resolve())
    val suffix = ".${SecretsStore.bridgeDomain}"
    Column(Modifier.at(10f, 6f).width(300.dp), verticalArrangement = Arrangement.spacedBy(5.dp)) {
        Words(S.appSettingsUsersTitle().resolve().uppercase(), 7f, FontWeight.SemiBold, p.dim)
        Column(Modifier.fillMaxWidth().clip(RoundedCornerShape(8.dp)).background(p.surface).padding(8.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
            UserRow(p, me, 8081, me + suffix)
            if (at >= 6) { HorizontalDivider(color = p.line); UserRow(p, friend, 8082, friend + suffix) }
            HorizontalDivider(color = p.line)
            Row(verticalAlignment = Alignment.CenterVertically) {
                Box(Modifier.weight(1f).border(1.dp, p.dim, RoundedCornerShape(4.dp)).padding(horizontal = 6.dp, vertical = 2.dp)) {
                    if (at in 1..5) Typed(friend, true, 9f, at == 1) else Words(S.appSettingsUsersUsername().resolve(), 9f, color = p.dim)
                }
                Spacer(Modifier.width(6.dp))
                Box(Modifier.width(44.dp).border(1.dp, p.dim, RoundedCornerShape(4.dp)).padding(horizontal = 6.dp, vertical = 2.dp)) { Words(S.appSettingsUsersPort().resolve(), 9f, color = p.dim) }
                Spacer(Modifier.width(6.dp))
                Box(Modifier.clip(CircleShape).background(p.accentFill.copy(alpha = if (at == 2) 1f else 0.7f)).padding(horizontal = 8.dp, vertical = 3.dp)) {
                    Words(S.commonAdd().resolve(), 9f, FontWeight.SemiBold)
                }
            }
            HorizontalDivider(color = p.line)
            Row(verticalAlignment = Alignment.CenterVertically) { Words(S.appSettingsUsersRequestBridge().resolve(), 9f, modifier = Modifier.weight(1f)); Switch(p, true) }
        }
    }
    // The first password, asked as the user is added (FirstPasswordDialog).
    val da = eased(if (at in 3..5) 1f else 0f)
    Column(
        Modifier.centred(160f, 96f).graphicsLayer { alpha = da; scaleX = 0.9f + 0.1f * da; scaleY = 0.9f + 0.1f * da }
            .shadow(10.dp, RoundedCornerShape(16.dp)).clip(RoundedCornerShape(16.dp)).background(p.surface).padding(12.dp),
        verticalArrangement = Arrangement.spacedBy(6.dp),
    ) {
        Words(S.appSettingsUsersPasswordTitle(friend).resolve(), 10f, FontWeight.SemiBold)
        for (i in 0 until 2) Box(Modifier.size(170.dp, 18.dp).border(1.dp, p.dim, RoundedCornerShape(4.dp)).padding(horizontal = 6.dp), contentAlignment = Alignment.CenterStart) {
            if (at >= 4) Words("••••••••", 9f) // i18n-ignore: a password's dots
            else Words((if (i == 0) S.appSettingsUsersPassword() else S.appSettingsUsersPasswordConfirm()).resolve(), 9f, color = p.dim)
        }
        Row(Modifier.align(Alignment.End)) {
            Words(S.commonCancel().resolve(), 9f, color = p.accent)
            Spacer(Modifier.width(12.dp))
            Words(S.appSettingsUsersCreate().resolve(), 9f, FontWeight.SemiBold, p.accent, modifier = Modifier.scale(if (at == 5) 0.9f else 1f))
        }
    }
    val (fx, fy) = when (at) {
        1 -> 80f to 68f
        2 -> 290f to 68f
        5 -> 232f to 140f
        else -> 230f to 160f
    }
    Finger(fx, fy, down = at == 2 || at == 5, hidden = at == 3 || at == 4)
}

@Composable
private fun UserRow(p: ArtPalette, name: String, port: Int, domain: String) {
    Column {
        Words(name, 9f, FontWeight.Bold)
        Words(S.appSettingsUsersPortDomain("$port", domain).resolve(), 7f, color = p.dim)
    }
}

// ---- alerts and settings ----

@Composable
private fun BoxScope.AlertsArt(p: ArtPalette, wide: Boolean) {
    // rest, Alerts, its list, Settings, Settings' Show the tour
    val at = rememberScene(intArrayOf(700, 400, 1800, 600, 2000), 2)
    val page = when { at >= 4 -> 2; at >= 1 -> 1; else -> 0 }
    val friend = S.appTourArtFriend().resolve()
    val left = if (wide) 104f else 0f
    // The alerts.
    Column(Modifier.at(10f + left, 6f).width((300f - left).dp).alpha(eased(if (page == 1) 1f else 0f)), verticalArrangement = Arrangement.spacedBy(6.dp)) {
        Words(S.appSocialAlertsTitle().resolve(), 11f, FontWeight.Bold)
        AlertRow(p, S.appSocialAlertsLikedPost(friend).bold()) { Tile(p, 2, Modifier.size(22.dp), 3f) }
        AlertRow(p, S.appSocialAlertsCommented(friend).bold()) { Tile(p, 4, Modifier.size(22.dp), 3f) }
        AlertRow(p, S.appSocialAlertsFriendRequest(friend).bold()) {
            Box(Modifier.clip(CircleShape).background(p.accent).padding(horizontal = 6.dp, vertical = 3.dp)) { Words(S.commonAccept().resolve(), 8f, FontWeight.SemiBold, MaterialTheme.colorScheme.onPrimary) }
        }
    }
    // Settings.
    Column(Modifier.at(10f + left, 4f).width((300f - left).dp).alpha(eased(if (page == 2) 1f else 0f)), verticalArrangement = Arrangement.spacedBy(5.dp)) {
        Words(S.appSettingsProfileTitle().resolve().uppercase(), 7f, FontWeight.SemiBold, p.dim)
        Row(Modifier.fillMaxWidth().clip(RoundedCornerShape(7.dp)).background(p.surface).padding(6.dp), verticalAlignment = Alignment.CenterVertically) {
            Box(Modifier.size(18.dp).clip(CircleShape).background(p.tile(0).first)); Spacer(Modifier.width(6.dp)); Bar(p, 70f)
        }
        Words(S.appSettingsLanguage().resolve().uppercase(), 7f, FontWeight.SemiBold, p.dim)
        Box(Modifier.fillMaxWidth().clip(RoundedCornerShape(7.dp)).background(p.surface).padding(6.dp)) { Bar(p, 40f) }
        Words(S.appTourSettingsTitle().resolve().uppercase(), 7f, FontWeight.SemiBold, p.dim)
        Box(Modifier.fillMaxWidth().clip(RoundedCornerShape(7.dp)).background(if (at >= 4) p.accent.copy(alpha = 0.16f) else p.surface).padding(6.dp)) {
            Words(S.appTourShow().resolve(), 9f, color = p.accent)
        }
    }
    if (wide) {
        Box(Modifier.at(100f, 0f).size(0.5.dp, STAGE_H.dp).background(p.line))
        for (i in 0 until 2) {
            val on = (i == 0 && page == 1) || (i == 1 && page == 2)
            Row(
                Modifier.at(4f, 50f + i * 30f).size(92.dp, 24.dp).clip(CircleShape).background(if (on) p.activeFill else Color.Transparent).padding(horizontal = 8.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Glyph(if (i == 0) NavIcons.Alerts else NavIcons.Settings, 11f, if (on) p.gold else p.dim)
                Spacer(Modifier.width(6.dp))
                Words((if (i == 0) Section.Alerts.label else Section.Settings.label).resolve(), 9f, if (on) FontWeight.SemiBold else FontWeight.Normal, if (on) p.gold else p.text, modifier = Modifier.weight(1f))
                if (i == 0 && page == 0) Badge3()
            }
        }
        Finger(40f, if (at >= 3) 92f else 62f, down = at == 1 || at == 3)
    } else {
        Box(Modifier.at(0f, 128f).size(STAGE_W.dp, 48.dp).background(p.surface))
        val tabs = listOf(
            Icons.Default.Notifications to Section.Alerts.label, Icons.Outlined.Forum to Section.Social.label, Icons.Default.Folder to Section.Files.label,
            Icons.Default.PhotoLibrary to Section.Images.label, Icons.Default.Settings to Section.Settings.label,
        )
        tabs.forEachIndexed { i, (icon, label) ->
            val on = (i == 0 && page == 1) || (i == 4 && page == 2)
            Column(Modifier.centred(64f * (i + 0.5f), 152f).width(64.dp), horizontalAlignment = Alignment.CenterHorizontally) {
                Box(Modifier.size(34.dp, 18.dp).clip(CircleShape).background(if (on) p.accentFill else Color.Transparent), contentAlignment = Alignment.Center) {
                    Glyph(icon, 14f, if (on) p.text else p.dim)
                    if (i == 0 && page == 0) Box(Modifier.align(Alignment.TopEnd)) { Badge3() }
                }
                Words(label.resolve(), 8f, if (on) FontWeight.SemiBold else FontWeight.Normal, if (on) p.text else p.dim)
            }
        }
        Finger(if (at >= 3) 294f else 38f, 150f, down = at == 1 || at == 3)
    }
}

@Composable
private fun Badge3() {
    Box(Modifier.size(13.dp).clip(CircleShape).background(Color(0xFFB3261E)), contentAlignment = Alignment.Center) {
        Words("3", 7f, FontWeight.Bold, Color.White) // i18n-ignore: a count of new alerts
    }
}

@Composable
private fun AlertRow(p: ArtPalette, text: AnnotatedString, trailing: @Composable () -> Unit) {
    Row(Modifier.fillMaxWidth().clip(RoundedCornerShape(7.dp)).background(p.surface).padding(6.dp), verticalAlignment = Alignment.CenterVertically) {
        Box(Modifier.size(18.dp).clip(CircleShape).background(p.tile(2).first))
        Spacer(Modifier.width(6.dp))
        RichWords(text, 8f, Modifier.weight(1f))
        Spacer(Modifier.width(4.dp))
        trailing()
    }
}
