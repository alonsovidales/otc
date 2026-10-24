// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.sync

import android.content.Context
import android.content.SharedPreferences
import android.os.Build
import android.os.storage.StorageManager
import android.provider.MediaStore
import cloud.offthe.otc.OTCApp
import cloud.offthe.otc.R
import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.androidSettingsFolderOnVolume
import cloud.offthe.otc.i18n.androidSettingsFoldersAll
import cloud.offthe.otc.i18n.androidSettingsFoldersOff
import cloud.offthe.otc.i18n.appSettingsFoldersPhotos
import cloud.offthe.otc.i18n.appSettingsFoldersVideos
import java.text.Collator

/**
 * Settings > Sync Options > Folders: the phone's photo folders as the
 * MediaStore knows them (its buckets), and which of them the sync leaves
 * out. iPhone's counterpart is PhotoSources.swift (albums: its library has
 * no folders).
 *
 * The owner turns folders off (SecretsStore.skippedFolders); everything
 * else is backed up, a folder that appears later included. Turning one off
 * deletes nothing, on the phone or on the device, and Free Up Space leaves
 * it alone; turning it back on backs up everything it holds, older than the
 * watermark too (PhotoSync.queueBackfill).
 *
 * A folder is its BUCKET_ID, which the MediaStore derives from the folder's
 * path: a choice survives a reinstall, a folder emptied and filled again
 * keeps it, and a renamed folder is a new one - backed up. Folders whose
 * name starts with a dot, or that hold a .nomedia file, aren't in the
 * MediaStore at all, so no app sees them.
 */
object PhotoFolders {
    /** One photo folder as MediaStore knows it (a bucket); [path] is "" at the top of a storage. */
    data class Folder(val id: String, val name: String, val path: String, val volume: String, val photos: Int, val videos: Int)

    /** One MediaStore row, as far as listing goes. */
    data class Row(val bucketId: Long?, val bucketName: String?, val relativePath: String?, val volume: String?, val isVideo: Boolean)

    /** Every folder holding something the sync sends - videos only with [includeVideos]. Blocking: call it off the main thread. */
    fun list(includeVideos: Boolean): List<Folder> {
        val proj = arrayOf(
            MediaStore.MediaColumns.BUCKET_ID, MediaStore.MediaColumns.BUCKET_DISPLAY_NAME, MediaStore.MediaColumns.RELATIVE_PATH,
            MediaStore.MediaColumns.VOLUME_NAME, MediaStore.Files.FileColumns.MEDIA_TYPE,
        )
        // The rows fetchNewAssets reads, with no date: grouped as they come.
        val sel = PhotoSync.selection(includeVideos, 0) ?: return emptyList()
        return OTCApp.instance.contentResolver.query(MediaStore.Files.getContentUri("external"), proj, sel, null, null)?.use { c ->
            group(generateSequence {
                if (!c.moveToNext()) null
                else Row(
                    if (c.isNull(0)) null else c.getLong(0), c.getString(1), c.getString(2), c.getString(3),
                    c.getInt(4) == MediaStore.Files.FileColumns.MEDIA_TYPE_VIDEO,
                )
            }, includeVideos)
        } ?: emptyList()
    }

    /**
     * The rows by folder: a row without a BUCKET_ID is no folder's, and
     * since nothing can turn it off it is always backed up. Each folder's
     * name is its first BUCKET_DISPLAY_NAME (else the end of its path), and
     * one with nothing to send isn't listed. By name, then path.
     */
    fun group(rows: Sequence<Row>, includeVideos: Boolean, collator: Collator = Collator.getInstance()): List<Folder> {
        class Counts(val id: Long) { var name = ""; var path: String? = null; var volume = ""; var photos = 0; var videos = 0 }
        val byId = LinkedHashMap<Long, Counts>()
        for (r in rows) {
            val id = r.bucketId ?: continue
            if (r.isVideo && !includeVideos) continue
            val f = byId.getOrPut(id) { Counts(id) }
            if (f.name.isEmpty() && !r.bucketName.isNullOrEmpty()) f.name = r.bucketName
            if (f.path == null && r.relativePath != null) f.path = r.relativePath.trimEnd('/')
            if (f.volume.isEmpty() && !r.volume.isNullOrEmpty()) f.volume = r.volume
            if (r.isVideo) f.videos++ else f.photos++
        }
        return byId.values
            .map { f -> (f.path ?: "").let { path -> Folder(f.id.toString(), f.name.ifEmpty { path.substringAfterLast('/') }, path, f.volume, f.photos, f.videos) } }
            .filter { it.photos + (if (includeVideos) it.videos else 0) > 0 }
            .sortedWith(compareBy<Folder, String>(collator) { it.name }.thenBy { it.path }.thenBy { it.volume }.thenBy { it.id })
    }

    /**
     * The selection that leaves [skipped] out, or null when none is: rows
     * without a BUCKET_ID stay (NOT IN alone would drop them). The ids go in
     * as numbers, never as the stored text.
     */
    fun skipClause(skipped: Set<String>): String? = numbers(skipped)?.let {
        "(${MediaStore.MediaColumns.BUCKET_ID} IS NULL OR ${MediaStore.MediaColumns.BUCKET_ID} NOT IN ($it))" // i18n-ignore: a MediaStore selection, SQL
    }

    /** The selection of [buckets] alone, or null when it names none. */
    fun onlyClause(buckets: Set<String>): String? = numbers(buckets)?.let { "${MediaStore.MediaColumns.BUCKET_ID} IN ($it)" }

    private fun numbers(ids: Set<String>): String? =
        ids.mapNotNull { it.toLongOrNull() }.distinct().takeIf { it.isNotEmpty() }?.joinToString(",")

    /** The stored ids as BUCKET_IDs; anything else is no folder's. */
    fun bucketIds(stored: Set<String>): Set<Long> = stored.mapNotNullTo(HashSet()) { it.toLongOrNull() }

    /**
     * A set kept in preferences (the skip list, the one-off passes), as a
     * copy: the one getStringSet returns must not be changed.
     */
    fun storedSet(p: SharedPreferences, key: String): Set<String> = p.getStringSet(key, null)?.toSet() ?: emptySet()

    /** Keeps [v] under [key]; an empty one leaves no key, as before there were any. */
    fun storeSet(e: SharedPreferences.Editor, key: String, v: Set<String>): SharedPreferences.Editor =
        if (v.isEmpty()) e.remove(key) else e.putStringSet(key, v)

    /** Under a folder's name: how many photos it holds, and videos while they are synced. */
    fun countLine(f: Folder, includeVideos: Boolean): UiText {
        val videos = if (includeVideos) f.videos else 0
        return when {
            // app.settings.folders.photos_videos joins the two counts, each worded by its own key.
            f.photos > 0 && videos > 0 ->
                UiText.Res(R.string.app_settings_folders_photos_videos, listOf(S.appSettingsFoldersPhotos(f.photos), S.appSettingsFoldersVideos(videos)))
            videos > 0 -> S.appSettingsFoldersVideos(videos)
            else -> S.appSettingsFoldersPhotos(f.photos)
        }
    }

    /**
     * Settings' Folders row: All, or how many of the folders there now are
     * turned off. A turned-off folder that is gone isn't counted, but stays
     * off: emptied and filled again, it is still the owner's choice.
     */
    fun rowValue(folders: List<Folder>, skipped: Set<String>): UiText {
        val off = folders.count { it.id in skipped }
        return if (off == 0) S.androidSettingsFoldersAll() else S.androidSettingsFoldersOff(off)
    }

    /**
     * A folder's name and the line under it (null: none), as the screen
     * shows them. Files at the top of a storage take the storage's own name
     * and no line; a folder that isn't on the phone's own storage says which
     * storage it is on. [describe] names a storage by its volume.
     */
    fun labels(f: Folder, describe: (String) -> String): Pair<String, UiText?> = when {
        f.path.isEmpty() -> describe(f.volume) to null
        f.volume.isEmpty() || f.volume == MediaStore.VOLUME_EXTERNAL_PRIMARY -> f.name to UiText.Raw(f.path)
        else -> f.name to S.androidSettingsFolderOnVolume(describe(f.volume), f.path)
    }

    /** A storage's own name as the system words it ("SD card"), or its volume name when it has none. */
    fun describeVolume(context: Context, volume: String): String {
        val sm = context.getSystemService(StorageManager::class.java) ?: return volume
        val v = try {
            if (Build.VERSION.SDK_INT >= 30) sm.getStorageVolume(MediaStore.Files.getContentUri(volume))
            else sm.storageVolumes.firstOrNull { if (volume == MediaStore.VOLUME_EXTERNAL_PRIMARY) it.isPrimary else it.uuid?.lowercase() == volume }
        } catch (e: Exception) { null }
        return v?.getDescription(context)?.takeIf { it.isNotEmpty() } ?: volume
    }

    /**
     * A folder's switch: off leaves it out of every pass from now on (and
     * drops a pass for it not yet run); on backs up everything it already
     * holds in a one-off pass, which a sync starts now - or, when one is
     * running, the next.
     */
    fun setSkipped(secrets: SecretsStore, id: String, skip: Boolean) {
        secrets.setFolderSkipped(id, skip)
        if (skip) {
            PhotoSync.dropBackfill(id)
        } else {
            PhotoSync.queueBackfill(id)
            PhotoSync.runForegroundAsync()
        }
    }
}
