// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.settings

import android.Manifest
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.provider.Settings
import android.util.Log
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.systemBarsPadding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.toggleable
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalLifecycleOwner
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.androidSettingsFolders
import cloud.offthe.otc.i18n.androidSettingsFoldersEmpty
import cloud.offthe.otc.i18n.androidSettingsFoldersFooter
import cloud.offthe.otc.i18n.androidSettingsFoldersHidden
import cloud.offthe.otc.i18n.androidSettingsFoldersIntro
import cloud.offthe.otc.i18n.androidSettingsFoldersLoadFailed
import cloud.offthe.otc.i18n.appSettingsFoldersNoAccess
import cloud.offthe.otc.i18n.appSettingsLimitedAllowAll
import cloud.offthe.otc.i18n.appSettingsLimitedSelectMore
import cloud.offthe.otc.i18n.appSettingsLimitedWarning
import cloud.offthe.otc.i18n.appSettingsSyncAuthorize
import cloud.offthe.otc.i18n.commonDone
import cloud.offthe.otc.sync.PhotoFolders
import cloud.offthe.otc.sync.PhotoSync
import cloud.offthe.otc.ui.compose.mediaPermissions
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

/*
 * Settings > Sync Options > Folders (PhotoFolders): every folder the
 * phone's media library knows, with what it holds and a switch, all on
 * unless the owner turned them off. The iPhone's counterpart is
 * AlbumsView.swift, by album. Read again as the screen opens, when Include
 * videos changes and when the app comes back (photos taken, folders moved
 * meanwhile).
 */

private sealed interface FoldersState {
    data object Loading : FoldersState
    data object NoAccess : FoldersState
    data object Failed : FoldersState
    /** [volumes]: each storage's own name, by volume. */
    class Loaded(val folders: List<PhotoFolders.Folder>, val volumes: Map<String, String>) : FoldersState
}

private suspend fun loadFolders(context: Context, includeVideos: Boolean): FoldersState = withContext(Dispatchers.IO) {
    if (!PhotoSync.hasPermission(context)) return@withContext FoldersState.NoAccess
    try {
        val folders = PhotoFolders.list(includeVideos)
        FoldersState.Loaded(folders, folders.map { it.volume }.distinct().associateWith { PhotoFolders.describeVolume(context, it) })
    } catch (e: SecurityException) {
        FoldersState.NoAccess
    } catch (e: CancellationException) {
        throw e
    } catch (e: Exception) {
        Log.w("OTC/Folders", "could not list the folders: $e")
        FoldersState.Failed
    }
}

/** Full screen, over Settings - the counterpart of iOS's pushed list. */
@Composable
fun FoldersView(secrets: SecretsStore, onClose: () -> Unit) {
    val context = LocalContext.current
    val includeVideos by secrets.includeVideos.collectAsState()
    val skipped by secrets.skippedFolders.collectAsState()
    var state by remember { mutableStateOf<FoldersState>(FoldersState.Loading) }
    var limited by remember { mutableStateOf(PhotoSync.accessLimited(context)) }
    var reloads by remember { mutableIntStateOf(0) }
    val lifecycle = LocalLifecycleOwner.current
    DisposableEffect(lifecycle) {
        // The first resume comes as the screen opens, which the load below covers.
        var opened = false
        val obs = LifecycleEventObserver { _, e ->
            if (e != Lifecycle.Event.ON_RESUME) return@LifecycleEventObserver
            limited = PhotoSync.accessLimited(context)
            if (opened) reloads++ else opened = true
        }
        lifecycle.lifecycle.addObserver(obs)
        onDispose { lifecycle.lifecycle.removeObserver(obs) }
    }
    // A reload keeps the list on screen until the new one is there.
    LaunchedEffect(includeVideos, reloads) { state = loadFolders(context, includeVideos) }

    // As Settings' Authorize Photos Access: the first tap asks; after a
    // refusal Android shows no dialog again, so the next opens the app's page.
    var asked by remember { mutableStateOf(false) }
    val permission = rememberLauncherForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) {
        asked = true
        limited = PhotoSync.accessLimited(context)
        reloads++
    }

    Dialog(onDismissRequest = onClose, properties = DialogProperties(usePlatformDefaultWidth = false)) {
        Column(Modifier.fillMaxSize().background(MaterialTheme.colorScheme.surface).systemBarsPadding()) {
            Row(Modifier.fillMaxWidth().padding(horizontal = 8.dp, vertical = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                Text(S.androidSettingsFolders().resolve(), style = MaterialTheme.typography.titleLarge, modifier = Modifier.weight(1f).padding(start = 8.dp))
                TextButton(onClick = onClose) { Text(S.commonDone().resolve()) }
            }
            when (val st = state) {
                FoldersState.Loading -> Box(Modifier.weight(1f).fillMaxWidth(), contentAlignment = Alignment.Center) { CircularProgressIndicator() }
                FoldersState.NoAccess -> Column(Modifier.weight(1f).fillMaxWidth().verticalScroll(rememberScrollState()).padding(16.dp)) {
                    Text(S.appSettingsFoldersNoAccess().resolve(), style = MaterialTheme.typography.bodyLarge, modifier = Modifier.padding(bottom = 8.dp))
                    RowButton(S.appSettingsSyncAuthorize().resolve()) {
                        if (!asked) permission.launch(mediaPermissions()) else openAppSettings(context)
                    }
                }
                else -> LazyColumn(Modifier.weight(1f).fillMaxWidth(), contentPadding = PaddingValues(bottom = 16.dp)) {
                    if (limited) item {
                        Column(Modifier.padding(horizontal = 16.dp, vertical = 4.dp)) {
                            LimitedAccessNotice { limited = PhotoSync.accessLimited(context); reloads++ }
                        }
                    }
                    item { Note(S.androidSettingsFoldersIntro().resolve()) }
                    when {
                        st is FoldersState.Loaded && st.folders.isNotEmpty() -> itemsIndexed(st.folders, key = { _, f -> f.id }) { i, f ->
                            if (i > 0) HorizontalDivider(Modifier.padding(horizontal = 16.dp))
                            val on = f.id !in skipped
                            FolderRow(f, st.volumes, includeVideos, on) { PhotoFolders.setSkipped(secrets, f.id, skip = !it) }
                        }
                        st is FoldersState.Loaded -> item { Note(S.androidSettingsFoldersEmpty().resolve()) }
                        else -> item { Note(S.androidSettingsFoldersLoadFailed().resolve(), MaterialTheme.colorScheme.error) }
                    }
                    item { Note(S.androidSettingsFoldersFooter().resolve()) }
                    item { Note(S.androidSettingsFoldersHidden().resolve()) }
                }
            }
        }
    }
}

@Composable
private fun Note(text: String, color: Color = MaterialTheme.colorScheme.onSurfaceVariant) =
    Text(text, style = MaterialTheme.typography.bodySmall, color = color, modifier = Modifier.padding(horizontal = 16.dp, vertical = 8.dp))

/** One folder: its name, where it is and what it holds; the whole row is its switch. */
@Composable
private fun FolderRow(f: PhotoFolders.Folder, volumes: Map<String, String>, includeVideos: Boolean, on: Boolean, onChange: (Boolean) -> Unit) {
    val (name, where) = PhotoFolders.labels(f) { volumes[it] ?: it }
    val muted = MaterialTheme.colorScheme.onSurfaceVariant
    Row(
        Modifier.fillMaxWidth().toggleable(value = on, onValueChange = onChange, role = Role.Switch).padding(horizontal = 16.dp, vertical = 10.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Column(Modifier.weight(1f)) {
            Text(name, style = MaterialTheme.typography.bodyLarge)
            where?.let { Text(it.resolve(), style = MaterialTheme.typography.bodySmall, color = muted, maxLines = 2, overflow = TextOverflow.Ellipsis) }
            Text(PhotoFolders.countLine(f, includeVideos).resolve(), style = MaterialTheme.typography.bodySmall, color = muted)
        }
        Spacer(Modifier.width(12.dp))
        Switch(checked = on, onCheckedChange = null)
    }
}

/**
 * Android 14's limited access (Settings > Sync Options and the Folders
 * screen): only the photos picked are backed up. Select More Photos asks
 * again, which shows the system's own choice of photos; Allow Access to All
 * Photos opens the app's page in the system settings. [onChange]: after the
 * system's answer. A sync starts too, for what was just picked.
 */
@Composable
fun LimitedAccessNotice(onChange: () -> Unit) {
    val context = LocalContext.current
    val ask = rememberLauncherForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) {
        onChange()
        PhotoSync.runForegroundAsync()
    }
    // The orange of the bridge's "left the bridge" line.
    Caption(S.appSettingsLimitedWarning().resolve(), Color(0xFFFF9800))
    // Side by side while they fit.
    FlowRow(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
        // Asked with it, Android 14 offers its choice of photos again (only
        // Android 14 and later have limited access).
        RowButton(S.appSettingsLimitedSelectMore().resolve()) {
            ask.launch(if (Build.VERSION.SDK_INT >= 34) mediaPermissions() + Manifest.permission.READ_MEDIA_VISUAL_USER_SELECTED else mediaPermissions())
        }
        RowButton(S.appSettingsLimitedAllowAll().resolve()) { openAppSettings(context) }
    }
}

/** This app's page in the system settings, where its permissions are. */
private fun openAppSettings(context: Context) =
    context.startActivity(Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS, Uri.fromParts("package", context.packageName, null)))
