// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.sizeIn
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.selectableGroup
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.ChatBubble
import androidx.compose.material.icons.filled.Check
import androidx.compose.material.icons.filled.FilterList
import androidx.compose.material.icons.filled.Star
import androidx.compose.material.icons.outlined.ChatBubbleOutline
import androidx.compose.material.icons.outlined.StarOutline
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.PlainTooltip
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TooltipBox
import androidx.compose.material3.TooltipDefaults
import androidx.compose.material3.rememberModalBottomSheetState
import androidx.compose.material3.rememberTooltipState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.onClick
import androidx.compose.ui.semantics.role
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardCapitalization
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appPhotosRatingComment
import cloud.offthe.otc.i18n.appPhotosRatingCommentPlaceholder
import cloud.offthe.otc.i18n.appPhotosRatingFromLink
import cloud.offthe.otc.i18n.appPhotosRatingTitle
import cloud.offthe.otc.i18n.appPhotosRatingOff
import cloud.offthe.otc.i18n.appPhotosRatingOn
import cloud.offthe.otc.i18n.appPhotosRatingRate
import cloud.offthe.otc.i18n.appPhotosRatingShow
import cloud.offthe.otc.i18n.appPhotosRatingStars
import cloud.offthe.otc.i18n.commonSave
import cloud.offthe.otc.proto.PhotoRating
import cloud.offthe.otc.ui.common.OTCTextField

// Rating's screens (Rating.kt has the rules): the collection header's star
// and its Sort by and Show, the tiles' badges, and the viewer's stars and
// comment box. iOS's RatingViews.swift draws the same.

/**
 * The collection header's Rating switch: a star, outlined while off and
 * filled in the app's ember while on, named "Rating" (its tooltip, or its
 * label on a wide window: [labelled]) and read out as "Rating on"/"Rating
 * off". [busy]: the change is on its way.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
internal fun RatingToggle(on: Boolean, busy: Boolean, labelled: Boolean, onClick: () -> Unit) {
    val colors = MaterialTheme.colorScheme
    val name = S.appPhotosRatingTitle().resolve()
    val said = (if (on) S.appPhotosRatingOn() else S.appPhotosRatingOff()).resolve()
    val icon = if (on) Icons.Filled.Star else Icons.Outlined.StarOutline
    if (labelled) {
        val tint = if (on) colors.primary else colors.onSurfaceVariant
        OutlinedButton(
            onClick = onClick, enabled = !busy, contentPadding = PaddingValues(start = 14.dp, end = 18.dp),
            colors = if (on) ButtonDefaults.outlinedButtonColors(containerColor = colors.primary.copy(alpha = 0.12f)) else ButtonDefaults.outlinedButtonColors(),
            modifier = Modifier.semantics { contentDescription = said },
        ) {
            Row(Modifier.clearAndSetSemantics {}, verticalAlignment = Alignment.CenterVertically) {
                Icon(icon, null, Modifier.size(20.dp), tint = tint)
                Spacer(Modifier.width(8.dp))
                Text(name)
            }
        }
    } else {
        TooltipBox(positionProvider = TooltipDefaults.rememberPlainTooltipPositionProvider(), tooltip = { PlainTooltip { Text(name) } }, state = rememberTooltipState()) {
            IconButton(onClick = onClick, enabled = !busy) { Icon(icon, said, tint = if (on) colors.primary else colors.onSurfaceVariant) }
        }
    }
}

/**
 * Under a rated collection's name: Sort by (Date / Rating) and Show (All
 * photos, 5 stars...), each the icon and its choice in words. A Show other
 * than All photos is drawn filled, so it shows the grid is filtered.
 */
@Composable
internal fun RatingControls(sort: RatingSort, show: RatingShow, onSort: (RatingSort) -> Unit, onShow: (RatingShow) -> Unit, modifier: Modifier = Modifier) {
    Row(modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(4.dp), verticalAlignment = Alignment.CenterVertically) {
        SortByMenu(RatingSort.entries, sort, { it.label }, onPick = onSort, labelled = true, modifier = Modifier.weight(1f, fill = false))
        RatingShowMenu(show, onShow, Modifier.weight(1f, fill = false))
    }
}

/** The Show menu: heading "Show", each choice with the check on the one in use; TalkBack hears "Show" and the choice. */
@Composable
private fun RatingShowMenu(current: RatingShow, onPick: (RatingShow) -> Unit, modifier: Modifier = Modifier) {
    var open by remember { mutableStateOf(false) }
    val title = S.appPhotosRatingShow().resolve()
    val now = current.label.resolve()
    val active = current != RatingShow.ALL
    val colors = MaterialTheme.colorScheme
    Box(modifier) {
        val described = Modifier.heightIn(min = 48.dp).semantics { contentDescription = title; stateDescription = now }
        val content: @Composable () -> Unit = {
            Icon(Icons.Filled.FilterList, null, Modifier.size(20.dp))
            Spacer(Modifier.width(8.dp))
            Text(now, fontWeight = FontWeight.SemiBold, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
        if (active) FilledTonalButton(onClick = { open = true }, contentPadding = PaddingValues(start = 12.dp, end = 16.dp), modifier = described) { content() }
        else TextButton(onClick = { open = true }, contentPadding = PaddingValues(start = 12.dp, end = 16.dp), modifier = described) { content() }
        DropdownMenu(expanded = open, onDismissRequest = { open = false }) {
            Text(
                title, style = MaterialTheme.typography.labelMedium, color = colors.onSurfaceVariant,
                modifier = Modifier.padding(horizontal = 12.dp, vertical = 8.dp).semantics { heading() },
            )
            for (o in RatingShow.entries) {
                val on = o == current
                DropdownMenuItem(
                    text = { Text(o.label.resolve()) },
                    onClick = { open = false; if (!on) onPick(o) },
                    leadingIcon = { if (on) Icon(Icons.Default.Check, null) else Spacer(Modifier.size(24.dp)) },
                    modifier = Modifier.semantics { selected = on },
                )
            }
        }
    }
}

/**
 * A rated photo's tile mark, at its bottom corner: "★ 4" while it has
 * stars, and a small speech bubble while it has a comment. Nothing for
 * an unrated photo (RatingRules.shows). Read out with the tile (its label).
 */
@Composable
internal fun RatingBadge(r: PhotoRating, modifier: Modifier = Modifier) {
    if (!RatingRules.shows(r)) return
    Row(
        modifier.clearAndSetSemantics {}.background(Color.Black.copy(alpha = 0.6f), RoundedCornerShape(50)).padding(horizontal = 6.dp, vertical = 2.dp),
        verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(3.dp),
    ) {
        if (r.stars > 0) {
            Icon(Icons.Filled.Star, null, Modifier.size(12.dp), tint = MaterialTheme.colorScheme.primary)
            Text("${r.stars}", color = Color.White, fontSize = 11.sp, lineHeight = 12.sp, fontWeight = FontWeight.SemiBold)
        }
        if (r.comment.isNotEmpty()) Icon(Icons.Filled.ChatBubble, null, Modifier.size(11.dp), tint = Color.White)
    }
}

/**
 * Five stars to tap: the [stars]th and those before it filled. A tap on
 * one gives that many (a tap on the photo's own number takes them off:
 * RatingRules.stars). For TalkBack, one choice among five ("3 stars,
 * selected").
 */
@Composable
internal fun RatingStars(stars: Int, size: Dp, emptyTint: Color, onTap: (Int) -> Unit, modifier: Modifier = Modifier, enabled: Boolean = true) {
    val ember = MaterialTheme.colorScheme.primary
    Row(modifier.selectableGroup(), verticalAlignment = Alignment.CenterVertically) {
        for (i in 1..5) {
            val label = S.appPhotosRatingStars(i).resolve()
            Box(
                Modifier.size(size).clip(CircleShape)
                    .clickable(enabled = enabled, role = Role.RadioButton) { onTap(i) }
                    .semantics { contentDescription = label; selected = i == stars },
                contentAlignment = Alignment.Center,
            ) {
                Icon(if (i <= stars) Icons.Filled.Star else Icons.Outlined.StarOutline, null, Modifier.size(size * 0.6f), tint = if (i <= stars) ember else emptyTint)
            }
        }
    }
}

/**
 * The viewer's rating in portrait (a collection with Rating on): the five
 * stars, saved at a tap, and a speech bubble that opens the comment box
 * ([onComment]; filled while the photo has a comment); under them "From the
 * shared link" while the last change came through it - its line kept empty
 * otherwise, so the photo above doesn't move from one photo to the next.
 */
@Composable
internal fun ViewerRatingBar(rating: PhotoRating?, onStar: (Int) -> Unit, onComment: () -> Unit, modifier: Modifier = Modifier) {
    val hasComment = rating?.comment?.isNotEmpty() == true
    Column(modifier, horizontalAlignment = Alignment.CenterHorizontally) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            RatingStars(rating?.stars ?: 0, 44.dp, Color.White, onStar)
            Spacer(Modifier.width(4.dp))
            IconButton(onClick = onComment) {
                Icon(
                    if (hasComment) Icons.Filled.ChatBubble else Icons.Outlined.ChatBubbleOutline,
                    (if (hasComment) S.appPhotosRatingComment() else S.appPhotosRatingCommentPlaceholder()).resolve(), tint = Color.White,
                )
            }
        }
        Text(
            if (rating?.fromLink == true) S.appPhotosRatingFromLink().resolve() else "",
            color = Color.White.copy(alpha = 0.75f), fontSize = 12.sp, lineHeight = 16.sp, maxLines = 1, overflow = TextOverflow.Ellipsis,
            modifier = Modifier.height(16.dp),
        )
    }
}

/**
 * The viewer's rating in landscape, in the side column: a star with the
 * photo's number under it (and a small bubble while it has a comment),
 * opening the stars and the comment box ([onClick]); "Rate" to TalkBack.
 */
@Composable
internal fun ViewerRatingButton(rating: PhotoRating?, onClick: () -> Unit) {
    val stars = rating?.stars ?: 0
    val hasComment = rating?.comment?.isNotEmpty() == true
    val label = buildString {
        append(S.appPhotosRatingRate().resolve())
        if (stars > 0) append(", ").append(S.appPhotosRatingStars(stars).resolve())
        if (hasComment) append(", ").append(S.appPhotosRatingComment().resolve())
    }
    Column(
        Modifier.sizeIn(minWidth = 48.dp, minHeight = 48.dp).clip(RoundedCornerShape(12.dp))
            .clickable(role = Role.Button, onClick = onClick).padding(vertical = 4.dp)
            .clearAndSetSemantics { contentDescription = label; role = Role.Button; this.onClick { onClick(); true } },
        horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.Center,
    ) {
        Icon(if (stars > 0) Icons.Filled.Star else Icons.Outlined.StarOutline, null, Modifier.size(24.dp), tint = if (stars > 0) MaterialTheme.colorScheme.primary else Color.White)
        if (stars > 0 || hasComment) Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(2.dp)) {
            if (stars > 0) Text("$stars", color = Color.White, fontSize = 12.sp, lineHeight = 14.sp, fontWeight = FontWeight.SemiBold)
            if (hasComment) Icon(Icons.Filled.ChatBubble, null, Modifier.size(11.dp), tint = Color.White)
        }
    }
}

/**
 * The stars and the comment box, in a sheet over the viewer: the stars
 * save at a tap; the comment with Save (enabled while it differs from
 * the one kept) or by itself as the sheet closes ([onClose]) or the
 * viewer moves on. "From the shared link" under the stars while the last
 * change came through it.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
internal fun RatingSheet(
    rating: PhotoRating?, draft: String?, onStar: (Int) -> Unit, onEdit: (String) -> Unit, onSave: () -> Unit, onClose: () -> Unit,
) {
    val saved = rating?.comment ?: ""
    val text = draft ?: saved
    ModalBottomSheet(onDismissRequest = onClose, sheetState = rememberModalBottomSheetState(skipPartiallyExpanded = true)) {
        Column(Modifier.fillMaxWidth().verticalScroll(rememberScrollState()).imePadding().padding(start = 16.dp, end = 16.dp, bottom = 16.dp)) {
            Text(S.appPhotosRatingTitle().resolve(), style = MaterialTheme.typography.titleMedium, modifier = Modifier.semantics { heading() })
            Spacer(Modifier.height(4.dp))
            RatingStars(rating?.stars ?: 0, 48.dp, MaterialTheme.colorScheme.onSurfaceVariant, onStar, Modifier.padding(start = 0.dp))
            if (rating?.fromLink == true) Text(
                S.appPhotosRatingFromLink().resolve(), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                modifier = Modifier.padding(start = 4.dp, bottom = 4.dp),
            )
            Spacer(Modifier.height(8.dp))
            OTCTextField(
                value = text, onValueChange = onEdit, minLines = 3,
                placeholder = { Text(S.appPhotosRatingCommentPlaceholder().resolve()) },
                keyboardOptions = KeyboardOptions(capitalization = KeyboardCapitalization.Sentences),
                modifier = Modifier.fillMaxWidth().heightIn(max = 180.dp),
            )
            Spacer(Modifier.height(12.dp))
            Button(onClick = onSave, enabled = RatingRules.changed(saved, text), modifier = Modifier.align(Alignment.End)) { Text(S.commonSave().resolve()) }
        }
    }
}
