// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.selection.selectableGroup
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.outlined.Delete
import androidx.compose.material.icons.outlined.PhoneAndroid
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.RadioButton
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.androidWallpaperBoth
import cloud.offthe.otc.i18n.androidWallpaperChange
import cloud.offthe.otc.i18n.androidWallpaperEvery15
import cloud.offthe.otc.i18n.androidWallpaperEveryDay
import cloud.offthe.otc.i18n.androidWallpaperEveryHour
import cloud.offthe.otc.i18n.androidWallpaperHome
import cloud.offthe.otc.i18n.androidWallpaperLock
import cloud.offthe.otc.i18n.androidWallpaperReplaces
import cloud.offthe.otc.i18n.androidWallpaperReplacesOther
import cloud.offthe.otc.i18n.androidWallpaperShowOn
import cloud.offthe.otc.i18n.androidWallpaperStart
import cloud.offthe.otc.i18n.androidWallpaperStop
import cloud.offthe.otc.i18n.androidWallpaperStopNote
import cloud.offthe.otc.i18n.androidWallpaperTitle
import cloud.offthe.otc.i18n.appPhotosWallpaperEmpty
import cloud.offthe.otc.i18n.appPhotosWallpaperFailed
import cloud.offthe.otc.i18n.appPhotosWallpaperLockNote
import cloud.offthe.otc.i18n.appPhotosWallpaperProgress
import cloud.offthe.otc.i18n.appPhotosWallpaperStoredText
import cloud.offthe.otc.i18n.appPhotosWallpaperStoredTitle
import cloud.offthe.otc.i18n.appPhotosWallpaperWifiOnly
import cloud.offthe.otc.i18n.commonCancel
import cloud.offthe.otc.i18n.commonSave
import cloud.offthe.otc.proto.ImageGroup
import cloud.offthe.otc.ui.common.PHOTOS_PROBLEM_TITLE
import cloud.offthe.otc.ui.common.TRY_AGAIN
import cloud.offthe.otc.ui.theme.Ember
import cloud.offthe.otc.wallpaper.Wallpaper
import cloud.offthe.otc.wallpaper.WallpaperInterval
import cloud.offthe.otc.wallpaper.WallpaperPlan
import cloud.offthe.otc.wallpaper.WallpaperTarget

// The collection open in Images as the phone's wallpaper (Wallpaper.kt):
// "Use as wallpaper…" in its header opens this box, "Wallpaper settings…"
// while it is the wallpaper. Before anything is stored, the box's first
// thing - under its title, in a framed box of its own - is the notice that
// the photos will be stored on this phone (the owner's one hard rule): in
// bold, then how many photos and from which collection, that anyone who
// sees the lock screen sees them (while it shows on the lock screen) and
// that they download only on Wi-Fi (while "Wi-Fi only" is on). Its count
// comes from the device first (WallpaperFetch.countPhotos: the collection's
// photos, at most WallpaperPlan.CAP); until then, nothing can be started.
// Then the two choices, what starting replaces, and "Store photos and
// start" - never started by itself, nothing on by default. While it is the
// wallpaper: the same choices with Save, how far the copies are, and "Stop
// and delete the copies".

/** The box for [group]; [onDismiss] closes it (Cancel, Start, Save or Stop). */
@Composable
internal fun WallpaperBox(group: ImageGroup, onDismiss: () -> Unit) {
    val colors = MaterialTheme.colorScheme
    val active by Wallpaper.active.collectAsState()
    val progress by Wallpaper.progress.collectAsState()
    val wifiOnly by SecretsStore.loadOrCreate().wifiOnly.collectAsState()
    val now = active
    val mine = now != null && now.groupId == group.id
    val other = now?.takeIf { it.groupId != group.id }
    var target by rememberSaveable(group.id) { mutableStateOf(if (mine) now.target else WallpaperTarget.DEFAULT) }
    var interval by rememberSaveable(group.id) { mutableStateOf(if (mine) now.interval else WallpaperInterval.DEFAULT) }
    // How many photos the collection has (null until the device says).
    var count by remember(group.id) { mutableStateOf<Int?>(null) }
    var countFailed by remember(group.id) { mutableStateOf(false) }
    var asks by remember(group.id) { mutableIntStateOf(0) }
    LaunchedEffect(group.id, asks) {
        countFailed = false
        val n = Wallpaper.countPhotos(group.id)
        if (n == null) countFailed = true else count = n
    }
    val name = if (mine) now.name else group.name
    val empty = count == 0 && !mine
    val changed = mine && (target != now.target || interval != now.interval)

    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(S.androidWallpaperTitle(name).resolve()) },
        text = {
            Column(Modifier.fillMaxWidth().heightIn(max = 560.dp).verticalScroll(rememberScrollState())) {
                if (empty) {
                    Text(S.appPhotosWallpaperEmpty().resolve(), style = MaterialTheme.typography.bodyMedium)
                    return@Column
                }
                StoredNotice(
                    copies = count?.let { WallpaperPlan.copies(it) }, name = name, lock = target.showsOnLock, wifiOnly = wifiOnly,
                    failed = countFailed && count == null, onRetry = { asks++ },
                )
                val p = progress
                if (mine && p != null && p.groupId == group.id) CopiesProgress(p)
                Spacer(Modifier.height(16.dp))
                Choices(
                    S.androidWallpaperShowOn(),
                    listOf(WallpaperTarget.LOCK to S.androidWallpaperLock(), WallpaperTarget.HOME to S.androidWallpaperHome(), WallpaperTarget.BOTH to S.androidWallpaperBoth()),
                    target,
                ) { target = it }
                Spacer(Modifier.height(12.dp))
                Choices(
                    S.androidWallpaperChange(),
                    listOf(WallpaperInterval.MIN_15 to S.androidWallpaperEvery15(), WallpaperInterval.HOUR to S.androidWallpaperEveryHour(), WallpaperInterval.DAY to S.androidWallpaperEveryDay()),
                    interval,
                ) { interval = it }
                if (!mine) {
                    Spacer(Modifier.height(12.dp))
                    Text(S.androidWallpaperReplaces().resolve(), style = MaterialTheme.typography.bodyMedium, color = colors.onSurfaceVariant)
                    if (other != null) {
                        Spacer(Modifier.height(8.dp))
                        Text(S.androidWallpaperReplacesOther(other.name).resolve(), style = MaterialTheme.typography.bodyMedium, color = colors.onSurfaceVariant)
                    }
                } else {
                    Spacer(Modifier.height(16.dp))
                    val danger = Color(0xFFE53935)
                    OutlinedButton(
                        onClick = { Wallpaper.stop(); onDismiss() }, colors = ButtonDefaults.outlinedButtonColors(contentColor = danger),
                        modifier = Modifier.fillMaxWidth(),
                    ) {
                        Icon(Icons.Outlined.Delete, null, Modifier.size(18.dp))
                        Spacer(Modifier.width(8.dp))
                        Text(S.androidWallpaperStop().resolve())
                    }
                    Text(
                        S.androidWallpaperStopNote().resolve(), style = MaterialTheme.typography.bodySmall, color = colors.onSurfaceVariant,
                        modifier = Modifier.padding(top = 6.dp),
                    )
                }
            }
        },
        confirmButton = {
            when {
                empty -> TextButton(onClick = onDismiss) { Text(S.commonCancel().resolve()) }
                mine -> TextButton(onClick = { Wallpaper.update(target, interval); onDismiss() }, enabled = changed) { Text(S.commonSave().resolve()) }
                else -> Button(onClick = { Wallpaper.start(group, target, interval); onDismiss() }, enabled = (count ?: 0) > 0) {
                    Text(S.androidWallpaperStart().resolve())
                }
            }
        },
        dismissButton = if (empty) null else { { TextButton(onClick = onDismiss) { Text(S.commonCancel().resolve()) } } },
    )
}

/**
 * The notice, first in the box and framed in the app's colour: the photos
 * will be stored on this phone (bold, a heading for TalkBack), then
 * [copies] photos from [name] are kept here sized for its screen until it
 * stops - a spinner until the count comes; [failed]: it didn't, with Try
 * again - that the lock screen shows them to anyone ([lock]), and that
 * they download only on Wi-Fi ([wifiOnly]).
 */
@Composable
private fun StoredNotice(copies: Int?, name: String, lock: Boolean, wifiOnly: Boolean, failed: Boolean, onRetry: () -> Unit) {
    val colors = MaterialTheme.colorScheme
    val shape = RoundedCornerShape(12.dp)
    Row(
        Modifier.fillMaxWidth().background(Ember.copy(alpha = 0.12f), shape).border(1.5.dp, Ember, shape).padding(14.dp),
        horizontalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        Icon(Icons.Outlined.PhoneAndroid, null, Modifier.size(24.dp), tint = Ember)
        Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Text(
                S.appPhotosWallpaperStoredTitle().resolve(), style = MaterialTheme.typography.titleSmall, fontWeight = FontWeight.Bold,
                color = colors.onSurface, modifier = Modifier.semantics { heading() },
            )
            when {
                copies != null -> Text(S.appPhotosWallpaperStoredText(copies, name).resolve(), style = MaterialTheme.typography.bodyMedium, color = colors.onSurface)
                failed -> Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(PHOTOS_PROBLEM_TITLE.resolve(), style = MaterialTheme.typography.bodyMedium, color = colors.error, modifier = Modifier.weight(1f))
                    TextButton(onClick = onRetry) { Text(TRY_AGAIN.resolve()) }
                }
                else -> CircularProgressIndicator(Modifier.size(20.dp), strokeWidth = 2.dp)
            }
            if (lock) Text(S.appPhotosWallpaperLockNote().resolve(), style = MaterialTheme.typography.bodyMedium, color = colors.onSurface)
            if (wifiOnly) Text(S.appPhotosWallpaperWifiOnly().resolve(), style = MaterialTheme.typography.bodyMedium, color = colors.onSurfaceVariant)
        }
    }
}

/** How far the copies are ("Storing photos: 3 of 40"), or that they couldn't be stored, with Try again. */
@Composable
internal fun CopiesProgress(p: Wallpaper.Progress, modifier: Modifier = Modifier) {
    val colors = MaterialTheme.colorScheme
    Column(modifier.fillMaxWidth().padding(top = 12.dp)) {
        if (p.failed) Row(verticalAlignment = Alignment.CenterVertically) {
            Text(S.appPhotosWallpaperFailed().resolve(), style = MaterialTheme.typography.bodyMedium, color = colors.error, modifier = Modifier.weight(1f))
            TextButton(onClick = { Wallpaper.retry() }) { Text(TRY_AGAIN.resolve()) }
        } else {
            Text(S.appPhotosWallpaperProgress(p.done, p.total).resolve(), style = MaterialTheme.typography.bodyMedium, color = colors.onSurfaceVariant)
            Spacer(Modifier.height(6.dp))
            if (p.total > 0) LinearProgressIndicator(progress = { p.done.toFloat() / p.total }, modifier = Modifier.fillMaxWidth())
            else LinearProgressIndicator(Modifier.fillMaxWidth())
        }
    }
}

/** A labelled group of choices, one picked ([selected]); TalkBack reads them as radio buttons. */
@Composable
private fun <T> Choices(label: UiText, options: List<Pair<T, UiText>>, selected: T, onPick: (T) -> Unit) {
    Text(
        label.resolve(), style = MaterialTheme.typography.labelLarge, color = MaterialTheme.colorScheme.onSurfaceVariant,
        modifier = Modifier.semantics { heading() }.padding(bottom = 2.dp),
    )
    Column(Modifier.selectableGroup()) {
        for ((value, text) in options) {
            Row(
                Modifier.fillMaxWidth().heightIn(min = 44.dp)
                    .selectable(selected = value == selected, role = Role.RadioButton, onClick = { onPick(value) }),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                RadioButton(selected = value == selected, onClick = null)
                Spacer(Modifier.width(12.dp))
                Text(text.resolve(), style = MaterialTheme.typography.bodyLarge)
            }
        }
    }
}
