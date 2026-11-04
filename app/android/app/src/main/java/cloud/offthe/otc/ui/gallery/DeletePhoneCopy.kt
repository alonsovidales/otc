// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import android.app.Activity
import android.content.ContentUris
import android.content.IntentSender
import android.net.Uri
import android.os.Build
import android.provider.MediaStore
import android.util.Log
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.IntentSenderRequest
import androidx.activity.result.contract.ActivityResultContracts
import androidx.annotation.RequiresApi
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import cloud.offthe.otc.OTCApp
import cloud.offthe.otc.R
import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appPhotosDeletePhoneDelete
import cloud.offthe.otc.i18n.appPhotosDeletePhoneFailed
import cloud.offthe.otc.i18n.appPhotosDeletePhoneKeep
import cloud.offthe.otc.i18n.appPhotosDeletePhonePhoto
import cloud.offthe.otc.i18n.appPhotosDeletePhoneSome
import cloud.offthe.otc.i18n.appPhotosDeletePhoneTitle
import cloud.offthe.otc.i18n.appPhotosDeletePhoneTitleSeveral
import cloud.offthe.otc.i18n.appPhotosDeletePhoneVideo
import cloud.offthe.otc.i18n.commonOk
import cloud.offthe.otc.sync.AssetSyncCache
import cloud.offthe.otc.sync.FreeUpSpace
import cloud.offthe.otc.sync.PhotoFolders
import cloud.offthe.otc.sync.PhotoSync
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch

// A photo or video deleted from the device that is also in this phone's own
// library - the photo sync sent it from here (AssetSyncCache), or Free Up
// Space read it here (free_up_hashes.json): once the device has deleted it,
// the app asks whether to move it to the phone's trash too. Asked only about
// content the device no longer holds (every copy deleted, or the only one),
// only on Android 11 and later with full media access, and never about what
// the app saved there itself (Pictures|Movies/Off The Cloud). Delete from
// Phone is MediaStore's trash request, which Android confirms and whose trash
// keeps them 30 days. The same rules as DeletePhoneCopy.swift on iOS.

/** One photo or video the device accepted to delete, as it was shown. */
data class DeletedOnDevice(
    /** Its content's hash; "" when the app never had it (nothing to match). */
    val hash: String,
    /** At how many paths the device held it (File.copies), null when the device didn't say (an older device, Files' rows). */
    val copies: Int?,
    /** Every copy was asked for (DelFile.every_copy). */
    val everyCopy: Boolean,
)

/** A photo or video in this phone's library recorded under a deleted hash ([id]: its MediaStore id). */
data class PhoneAsset(val id: Long, val hash: String, val isVideo: Boolean)

/** The question asked after a delete: the phone's assets it is about, and how many of the deleted items they are. */
data class PhoneDeleteAsk(
    val assets: List<PhoneAsset>,
    /** How many of the deleted photos and videos are on this phone (1 or more). */
    val found: Int,
    /** How many the device deleted in that go. */
    val deleted: Int,
) {
    val ids: List<Long> get() = assets.map { it.id }

    fun title(): UiText = if (found == 1) S.appPhotosDeletePhoneTitle() else S.appPhotosDeletePhoneTitleSeveral()

    /** The first sentence: the one item deleted, or how many of several. */
    fun also(): UiText = when {
        deleted != 1 -> S.appPhotosDeletePhoneSome(found)
        assets.all { it.isVideo } -> S.appPhotosDeletePhoneVideo()
        else -> S.appPhotosDeletePhonePhoto()
    }

    /** android.photos.delete_phone_text with [also] in it. */
    fun text(): UiText = UiText.Res(R.string.android_photos_delete_phone_text, listOf(also()))
}

/** The pure rules, tested as iOS's DeletePhoneCopyTests. */
object DeletePhoneCopy {
    /** Files per trash request: its URIs travel in one Binder transaction (FreeUpSpace's). */
    const val MAX_PER_REQUEST = 500

    /**
     * The hashes of what the device no longer holds after these deletes:
     * every copy deleted, or one that had no other copy - nothing more than
     * one copy of which stays (Delete only this copy), and nothing without a
     * hash. A device that doesn't count copies (null) is taken at its word;
     * the device is asked about each match before the question
     * (FreeUpSpace.held), so a copy it still holds is never asked about.
     */
    fun gone(deleted: List<DeletedOnDevice>): Set<String> =
        deleted.filter { it.hash.isNotEmpty() && (it.everyCopy || (it.copies ?: 1) <= 1) }.mapTo(LinkedHashSet()) { it.hash }

    /**
     * The phone's assets to ask about: recorded under a gone hash
     * ([recorded]: hash by MediaStore id), still in the library ([inLibrary]:
     * whether each is a video, for those found), and not saved there by this
     * app ([saved]). In id order.
     */
    fun onPhone(gone: Set<String>, recorded: Map<Long, String>, inLibrary: Map<Long, Boolean>, saved: Set<Long>): List<PhoneAsset> =
        recorded.mapNotNull { (id, hash) ->
            val video = inLibrary[id]
            if (hash !in gone || id in saved || video == null) null else PhoneAsset(id, hash, video)
        }.sortedBy { it.id }

    /** The question, once the device has said which of those hashes it still holds ([held]: never asked about); null when nothing is left. */
    fun ask(deleted: List<DeletedOnDevice>, onPhone: List<PhoneAsset>, held: Set<String>): PhoneDeleteAsk? {
        val assets = onPhone.filter { it.hash !in held }
        val hashes = assets.mapTo(HashSet()) { it.hash }
        val gone = gone(deleted)
        val found = deleted.count { it.hash in gone && it.hash in hashes }
        if (found == 0) return null
        return PhoneDeleteAsk(assets, found, deleted.size)
    }

    /**
     * Asked only where the phone's trash can take them (Android 11's trash
     * request) and with full media access: with photos picked one by one
     * (Android 14) the app sees only part of the library, as iOS's limited
     * access, and without any it can move nothing.
     */
    fun asks(sdk: Int, permission: Boolean, limited: Boolean): Boolean = sdk >= 30 && permission && !limited

    /** Files' rows: a photo or a video (or a type the listing didn't say), the only files the library can hold. */
    fun isMedia(mime: String): Boolean = mime.isEmpty() || mime.startsWith("image/") || mime.startsWith("video/")

    /** Of [records] (hash by MediaStore id, as text), those under any of [hashes], by id. */
    fun recordedUnder(records: Map<String, String>, hashes: Set<String>): Map<Long, String> {
        val out = HashMap<Long, String>()
        for ((id, hash) in records) {
            if (hash in hashes) id.toLongOrNull()?.let { out[it] = hash }
        }
        return out
    }
}

/**
 * Looks for the deleted photos in this phone's library and asks, one delete
 * at a time; [DeletePhoneCopyDialog] (RootView) shows the question over
 * whatever is on screen - the viewer it came from may close under it - and
 * launches the trash request, which needs an Activity, as Free Up Space's.
 */
object DeletePhoneCopyModel {
    private const val tag = "OTC/DeletePhoneCopy"
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private val deletes = Channel<Batch>(Channel.UNLIMITED)
    private var started = false

    private class Batch(val deleted: List<DeletedOnDevice>, val endpoint: String)

    private val _ask = MutableStateFlow<PhoneDeleteAsk?>(null)
    /** The question on screen, or null. */
    val ask: StateFlow<PhoneDeleteAsk?> get() = _ask

    private val _request = MutableStateFlow<IntentSender?>(null)
    /** A trash request for RootView to launch. */
    val request: StateFlow<IntentSender?> get() = _request

    private val _failed = MutableStateFlow(false)
    /** The trash request couldn't be made or shown: a short notice. */
    val failed: StateFlow<Boolean> get() = _failed

    // This question's end (kept, moved, declined, failed), which lets the
    // next delete's look-up go; and what is still to go after the request
    // on screen, and that request's own.
    private var done: CompletableDeferred<Unit>? = null
    private var left: List<PhoneAsset> = emptyList()
    private var asked: List<PhoneAsset> = emptyList()

    /** A delete the device accepted. */
    fun deleted(items: List<DeletedOnDevice>) {
        if (Build.VERSION.SDK_INT < 30 || DeletePhoneCopy.gone(items).isEmpty()) return
        val endpoint = SecretsStore.loadOrCreate().endpointURLString
        start()
        deletes.trySend(Batch(items, endpoint))
    }

    @Synchronized private fun start() {
        if (started) return
        started = true
        scope.launch {
            for (b in deletes) {
                try { handle(b) } catch (e: CancellationException) { throw e } catch (e: Exception) { Log.w(tag, "not asked: ${e.javaClass.simpleName}") }
            }
        }
    }

    private suspend fun handle(b: Batch) {
        if (Build.VERSION.SDK_INT < 30) return
        val ask = try {
            lookUp(b.deleted)
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            Log.w(tag, "look-up failed: ${e.javaClass.simpleName}")
            null
        } ?: return
        // Not after a Log Out or another device (those hashes were its), nor
        // with the app away.
        if (SecretsStore.loadOrCreate().endpointURLString != b.endpoint || !PhotoSync.inFront) return
        val end = CompletableDeferred<Unit>()
        synchronized(this) { done = end }
        _ask.value = ask
        end.await()
    }

    /** The question for these deletes, or null. */
    @RequiresApi(30)
    private suspend fun lookUp(deleted: List<DeletedOnDevice>): PhoneDeleteAsk? {
        val gone = DeletePhoneCopy.gone(deleted)
        if (gone.isEmpty() || !DeletePhoneCopy.asks(Build.VERSION.SDK_INT, PhotoSync.hasPermission(), PhotoSync.accessLimited())) return null
        // What the sync sent wins over what Free Up Space read for the same file.
        val recorded = HashMap(FreeUpSpace.idsWithHashes(gone))
        recorded.putAll(AssetSyncCache.idsWithHashes(gone))
        if (recorded.isEmpty()) return null
        val rows = libraryRows(recorded.keys)
        val onPhone = DeletePhoneCopy.onPhone(gone, recorded, rows.associate { it.id to it.isVideo }, rows.filter { it.saved }.mapTo(HashSet()) { it.id })
        if (onPhone.isEmpty()) return null
        // The device's own say, as Free Up Space asks it: a copy it still
        // holds (another path, a device that doesn't count copies) is never
        // asked about. No answer, no question.
        val held = try {
            FreeUpSpace.held(onPhone.mapTo(HashSet()) { it.hash })
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            Log.w(tag, "the device didn't say: ${PhotoSync.logged(e)}")
            return null
        }
        return DeletePhoneCopy.ask(deleted, onPhone, held)
    }

    private class LibraryRow(val id: Long, val isVideo: Boolean, val saved: Boolean)

    /** The photos and videos of [ids] still in the library (not in its trash), and whether this app saved each there. */
    private fun libraryRows(ids: Collection<Long>): List<LibraryRow> {
        val out = mutableListOf<LibraryRow>()
        val proj = arrayOf(MediaStore.Files.FileColumns._ID, MediaStore.Files.FileColumns.MEDIA_TYPE, MediaStore.MediaColumns.RELATIVE_PATH)
        val video = MediaStore.Files.FileColumns.MEDIA_TYPE_VIDEO
        for (chunk in ids.chunked(DeletePhoneCopy.MAX_PER_REQUEST)) {
            val sel = "${MediaStore.Files.FileColumns._ID} IN (${chunk.joinToString(",")}) AND " + // i18n-ignore: a MediaStore selection, SQL
                "${MediaStore.Files.FileColumns.MEDIA_TYPE} IN (${MediaStore.Files.FileColumns.MEDIA_TYPE_IMAGE},$video)" // i18n-ignore: a MediaStore selection, SQL
            OTCApp.instance.contentResolver.query(MediaStore.Files.getContentUri("external"), proj, sel, null, null)?.use { c ->
                while (c.moveToNext()) out += LibraryRow(c.getLong(0), c.getInt(1) == video, PhotoFolders.isSaved(c.getString(2)))
            }
        }
        return out
    }

    private fun uri(a: PhoneAsset): Uri = ContentUris.withAppendedId(
        if (a.isVideo) MediaStore.Video.Media.EXTERNAL_CONTENT_URI else MediaStore.Images.Media.EXTERNAL_CONTENT_URI, a.id,
    )

    /** Keep on Phone (or the question dismissed): nothing more. */
    fun keep() {
        _ask.value = null
        finish()
    }

    /** Delete from Phone: those still there (and still not the app's own) go in Android's trash request. */
    fun delete() {
        val ask = _ask.value ?: return
        _ask.value = null
        scope.launch {
            val now = try {
                libraryRows(ask.ids).filter { !it.saved }.map { r -> PhoneAsset(r.id, ask.assets.first { it.id == r.id }.hash, r.isVideo) }
            } catch (e: Exception) {
                Log.w(tag, "can't read the library: ${e.javaClass.simpleName}")
                return@launch trashFailed()
            }
            synchronized(this@DeletePhoneCopyModel) { left = now }
            nextRequest()
        }
    }

    /** The next batch's trash request, or the end. */
    private fun nextRequest() {
        val batch = synchronized(this) {
            val b = left.take(DeletePhoneCopy.MAX_PER_REQUEST)
            left = left.drop(b.size)
            asked = b
            b
        }
        if (batch.isEmpty() || Build.VERSION.SDK_INT < 30) return finish()
        try {
            _request.value = MediaStore.createTrashRequest(OTCApp.instance.contentResolver, batch.map(::uri), true).intentSender
        } catch (e: Exception) {
            Log.w(tag, "trash request not made: ${e.javaClass.simpleName}")
            trashFailed()
        }
    }

    /** RootView, about to launch it: null when there is none. */
    fun takeRequest(): IntentSender? {
        val r = _request.value ?: return null
        _request.value = null
        return r
    }

    /** Android's answer: moved (off the sync's retry list, as the sync does for a file it no longer finds, then the next batch), or declined (nothing more). */
    fun trashAnswered(moved: Boolean) {
        val gone = synchronized(this) { asked.also { asked = emptyList() } }
        if (!moved) {
            synchronized(this) { left = emptyList() }
            return finish()
        }
        scope.launch {
            AssetSyncCache.pending.resolve(gone.map { it.id })
            nextRequest()
        }
    }

    /** The request couldn't be made or launched. */
    fun trashFailed() {
        synchronized(this) {
            left = emptyList()
            asked = emptyList()
        }
        _failed.value = true
    }

    /** The notice was read. */
    fun failedSeen() {
        _failed.value = false
        finish()
    }

    private fun finish() {
        val end = synchronized(this) { done.also { done = null } }
        end?.complete(Unit)
    }
}

/**
 * The question after a delete, and the trash request behind Delete from
 * Phone: in RootView, over whatever is on screen (a later dialog window lies
 * above the viewer's). Keep on Phone is the dismiss button, as a tap outside.
 */
@Composable
fun DeletePhoneCopyDialog() {
    val ask by DeletePhoneCopyModel.ask.collectAsState()
    val failed by DeletePhoneCopyModel.failed.collectAsState()
    val request by DeletePhoneCopyModel.request.collectAsState()
    val trash = rememberLauncherForActivityResult(ActivityResultContracts.StartIntentSenderForResult()) {
        DeletePhoneCopyModel.trashAnswered(it.resultCode == Activity.RESULT_OK)
    }
    LaunchedEffect(request) {
        val sender = DeletePhoneCopyModel.takeRequest() ?: return@LaunchedEffect
        try { trash.launch(IntentSenderRequest.Builder(sender).build()) } catch (e: Exception) { DeletePhoneCopyModel.trashFailed() }
    }
    ask?.let { a ->
        AlertDialog(
            onDismissRequest = DeletePhoneCopyModel::keep,
            title = { Text(a.title().resolve()) },
            text = { Text(a.text().resolve()) },
            confirmButton = {
                TextButton(onClick = DeletePhoneCopyModel::delete) { Text(S.appPhotosDeletePhoneDelete().resolve(), color = MaterialTheme.colorScheme.error) }
            },
            dismissButton = { TextButton(onClick = DeletePhoneCopyModel::keep) { Text(S.appPhotosDeletePhoneKeep().resolve()) } },
        )
    }
    if (failed) {
        AlertDialog(
            onDismissRequest = DeletePhoneCopyModel::failedSeen,
            title = { Text(S.appPhotosDeletePhoneFailed().resolve()) },
            confirmButton = { TextButton(onClick = DeletePhoneCopyModel::failedSeen) { Text(S.commonOk().resolve()) } },
        )
    }
}
