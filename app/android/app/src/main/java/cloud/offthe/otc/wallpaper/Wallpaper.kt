// SPDX-License-Identifier: AGPL-3.0-or-later
// The platform's ExifInterface reads a stream well from API 24 (minSdk is
// 29): no androidx library for one tag.
@file:Suppress("ExifInterface")

package cloud.offthe.otc.wallpaper

import android.app.WallpaperManager
import android.content.Context
import android.content.SharedPreferences
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.graphics.Matrix
import android.hardware.display.DisplayManager
import android.media.ExifInterface
import android.util.Log
import android.view.Display
import androidx.core.content.edit
import androidx.work.BackoffPolicy
import androidx.work.Constraints
import androidx.work.CoroutineWorker
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.ExistingWorkPolicy
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkInfo
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import cloud.offthe.otc.OTCApp
import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.androidWallpaperFailed
import cloud.offthe.otc.i18n.androidWallpaperStarted
import cloud.offthe.otc.i18n.appPhotosWallpaperFailed
import cloud.offthe.otc.i18n.appPhotosWallpaperStopped
import cloud.offthe.otc.net.NetworkWatch
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.proto.ImageGroup
import cloud.offthe.otc.sync.PhotoSync
import cloud.offthe.otc.sync.SyncScheduler
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch
import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.io.File
import java.util.concurrent.TimeUnit

// A collection as the phone's wallpaper (the owner's request, 2026-10-28):
// the copies of its photos in the app's private storage, and WallpaperManager
// showing one at random on the lock screen, the home screen or both.
//
// - Started from the collection's box (ui/gallery/WallpaperBox.kt), which
//   first says, unmissably, that the photos will be stored on this phone.
//   One collection at a time: starting another stops this one and deletes
//   its copies.
// - Copies: filesDir/wallpaper/<collection id>/<photo hash>.jpg, photos
//   only, sized for the screen (GetFile, as the viewer shows a photo, then
//   scaled and cropped here: never the original), at most
//   WallpaperPlan.CAP. The backup rules include otc_settings.xml alone, so
//   neither the copies nor otc_wallpaper's preferences leave the phone. No
//   log names a photo.
// - Fetching (FetchWorker, one at a time) follows the app's "Wi-Fi only"
//   (an unmetered network for WorkManager, as the photo sync) and its
//   connection (OTCConnection: home network, bridge, Tailscale), and keeps
//   the copies in step with the collection (WallpaperFetch): when started,
//   when Images changes the collection, and at most hourly from the
//   rotation. A deleted collection stops it; a renamed one goes on.
// - Rotation (RotateWorker): WorkManager's periodic run every 15 minutes,
//   hour or day sets a random copy, cropped to the screen's shape, none
//   again until all were shown (WallpaperShuffle); no network needed. The
//   first copy stored is set at once.
// - Stop (the box, a deleted collection, Log Out, another device) cancels
//   both runs and deletes the folder; the wallpaper shown stays.
object Wallpaper {
    private const val TAG = "OTC/Wallpaper"
    private const val PREFS = "otc_wallpaper"
    private const val FETCH = "otc-wallpaper-fetch"
    private const val ROTATE = "otc-wallpaper-rotate"
    private const val KEY_SHOWN = "shown"
    private const val KEY_LAST = "last"
    private const val KEY_LAST_FETCH = "last_fetch"
    private const val KEY_STARTED = "started"

    /** How often the rotation refreshes the copies (a new photo in the collection shows up within this). */
    private const val REFRESH_MS = 60 * 60_000L

    /** A note said by the box's screen: once, and only while fresh ([at], ms). */
    data class Note(val text: UiText, val error: Boolean = false, val at: Long = System.currentTimeMillis())

    /** Copies of [groupId] being stored: [done] of [total]; [failed]: the last try failed (it is tried again). */
    data class Progress(val groupId: String, val done: Int, val total: Int, val failed: Boolean = false)

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private val lock = Any()
    // Bumped by every stop: a fetch from before writes nothing after it.
    @Volatile private var generation = 0

    private val _active = MutableStateFlow<WallpaperSettings?>(null)
    private val _progress = MutableStateFlow<Progress?>(null)
    private val _note = MutableStateFlow<Note?>(null)
    private var loaded = false

    /** The collection shown as wallpaper now, if any. */
    val active: StateFlow<WallpaperSettings?> get() { load(); return _active }
    val progress: StateFlow<Progress?> = _progress
    val note: StateFlow<Note?> = _note

    private fun ctx(): Context = OTCApp.instance
    private fun prefs(): SharedPreferences = ctx().getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    @Synchronized private fun load() {
        if (loaded) return
        // Without the app (JVM tests of the screens that tell it about
        // collections): nothing is shown, nothing to keep in step.
        val p = try { prefs() } catch (_: UninitializedPropertyAccessException) { return }
        loaded = true
        _active.value = WallpaperSettings.fromMap(WallpaperSettings.KEYS.associateWith { p.getString(it, null) })
    }

    private fun dir(groupId: String) = File(File(ctx().filesDir, "wallpaper"), groupId)

    fun noteShown(n: Note) { if (_note.value == n) _note.value = null }
    private fun say(text: UiText, error: Boolean = false) { _note.value = Note(text, error) }

    /** The collection's photos to copy, for the notice ([WallpaperPlan.copies] of it); null when nothing answered. */
    suspend fun countPhotos(groupId: String): Int? =
        WallpaperFetch.countPhotos({ b -> OTCConnection.ask("page", OTCConnection.PAGE_TIMEOUT_MS, b) }, groupId)

    /**
     * The box's "Store photos and start": [group] from now on, shown on
     * [target], changing every [interval]. Another collection shown now
     * stops first (its copies deleted, as the box said). Fetching starts
     * (it sets the first photo at once), and so does the rotation.
     */
    fun start(group: ImageGroup, target: WallpaperTarget, interval: WallpaperInterval) {
        load()
        val endpoint = SecretsStore.loadOrCreate().endpointURLString
        val was = _active.value
        if (was != null && was.groupId != group.id) stopNow(was)
        val s = WallpaperSettings(group.id, group.name, target, interval, endpoint)
        synchronized(lock) {
            prefs().edit(commit = true) {
                for ((k, v) in s.toMap()) putString(k, v)
                remove(KEY_SHOWN); remove(KEY_LAST); remove(KEY_LAST_FETCH); putBoolean(KEY_STARTED, false)
            }
            _active.value = s
        }
        _progress.value = Progress(group.id, 0, 0)
        scheduleFetch(ExistingWorkPolicy.REPLACE)
        scheduleRotation(interval)
    }

    /** The box's Save: new choices for the collection shown now; a new place shows a photo there at once. */
    fun update(target: WallpaperTarget, interval: WallpaperInterval) {
        val s = active.value ?: return
        val next = s.copy(target = target, interval = interval)
        if (next == s) return
        synchronized(lock) {
            prefs().edit(commit = true) { for ((k, v) in next.toMap()) putString(k, v) }
            _active.value = next
        }
        if (next.interval != s.interval) scheduleRotation(next.interval)
        if (next.target != s.target) scope.launch { if (!showNext(next)) say(S.androidWallpaperFailed(), error = true) }
    }

    /** The box's "Stop and delete the copies". */
    fun stop() {
        val s = active.value ?: return
        stopNow(s)
        say(S.appPhotosWallpaperStopped())
    }

    /** Try again, after a fetch that failed: now, not at WorkManager's next try. */
    fun retry() {
        if (active.value == null) return
        _progress.value = _progress.value?.copy(failed = false)
        scheduleFetch(ExistingWorkPolicy.REPLACE)
    }

    /** Images changed collection [groupId] (photos added or taken out): its copies follow. */
    fun collectionChanged(groupId: String) {
        if (active.value?.groupId == groupId) scheduleFetch(ExistingWorkPolicy.REPLACE)
    }

    /** Images deleted collection [groupId]: shown as wallpaper, it stops and its copies go. */
    fun collectionDeleted(groupId: String) {
        val s = active.value ?: return
        if (s.groupId != groupId) return
        stopNow(s)
        say(S.appPhotosWallpaperStopped())
    }

    /** A new list of the collections (Images): the one shown keeps its name in step, or stops if it was deleted elsewhere. */
    fun collectionsListed(groups: List<ImageGroup>) {
        val s = active.value ?: return
        val g = groups.firstOrNull { it.id == s.groupId }
        if (g == null) { collectionDeleted(s.groupId); return }
        if (g.name != s.name) renamed(s, g.name)
    }

    /** Images renamed collection [groupId]: shown as wallpaper, it goes on under its new name. */
    fun collectionRenamed(groupId: String, name: String) {
        val s = active.value ?: return
        if (s.groupId == groupId && s.name != name) renamed(s, name)
    }

    private fun renamed(s: WallpaperSettings, name: String) = synchronized(lock) {
        if (_active.value?.groupId != s.groupId) return@synchronized
        val next = _active.value!!.copy(name = name)
        prefs().edit { putString(WallpaperSettings.KEY_NAME, name) }
        _active.value = next
    }

    /** Log Out: stopped, its copies and choices gone (the wipe takes filesDir too). Nothing said. */
    fun loggedOut() {
        load()
        val s = _active.value
        if (s != null) stopNow(s) else cancelWork()
        _note.value = null
    }

    /** Signed in to another device ([endpoint]): the copies were the other one's photos. */
    fun deviceChanged(endpoint: String) {
        val s = active.value ?: return
        if (s.endpoint != endpoint) stopNow(s)
    }

    /** "Wi-Fi only" changed: a fetch waiting for its network takes the new rule. */
    fun wifiOnlyChanged() = scope.launch {
        if (active.value == null) return@launch
        try {
            val waiting = WorkManager.getInstance(ctx()).getWorkInfosForUniqueWork(FETCH).get()
                .any { it.state == WorkInfo.State.ENQUEUED || it.state == WorkInfo.State.BLOCKED }
            if (waiting) scheduleFetch(ExistingWorkPolicy.REPLACE)
        } catch (e: Exception) {
            Log.w(TAG, "fetch not updated for Wi-Fi only: ${e.javaClass.simpleName}")
        }
    }

    private fun stopNow(s: WallpaperSettings) {
        cancelWork()
        synchronized(lock) {
            generation++
            prefs().edit(commit = true) { clear() }
            if (_active.value?.groupId == s.groupId) _active.value = null
            dir(s.groupId).deleteRecursively()
            // Anything left by an earlier collection, too.
            File(ctx().filesDir, "wallpaper").listFiles()?.forEach { if (_active.value?.groupId != it.name) it.deleteRecursively() }
        }
        _progress.value = null
        Log.i(TAG, "stopped; copies deleted")
    }

    private fun cancelWork() {
        val wm = WorkManager.getInstance(ctx())
        wm.cancelUniqueWork(FETCH)
        wm.cancelUniqueWork(ROTATE)
    }

    private fun scheduleFetch(policy: ExistingWorkPolicy) {
        val wifiOnly = SecretsStore.loadOrCreate().wifiOnly.value
        val req = OneTimeWorkRequestBuilder<FetchWorker>()
            .setConstraints(Constraints.Builder().setRequiredNetworkType(SyncScheduler.networkType(wifiOnly)).build())
            .setBackoffCriteria(BackoffPolicy.EXPONENTIAL, 1, TimeUnit.MINUTES)
            .build()
        WorkManager.getInstance(ctx()).enqueueUniqueWork(FETCH, policy, req)
    }

    private fun scheduleRotation(interval: WallpaperInterval) {
        val req = PeriodicWorkRequestBuilder<RotateWorker>(interval.minutes, TimeUnit.MINUTES)
            .setInitialDelay(interval.minutes, TimeUnit.MINUTES)
            .build()
        WorkManager.getInstance(ctx()).enqueueUniquePeriodicWork(ROTATE, ExistingPeriodicWorkPolicy.UPDATE, req)
    }

    /** Still the collection being fetched: not stopped, nor replaced, since [gen]. */
    private fun stillOn(groupId: String, gen: Int) = gen == generation && _active.value?.groupId == groupId

    /** FetchWorker's run: the copies in step with the collection. */
    internal suspend fun fetch(): Boolean {
        val s = active.value ?: return true
        val secrets = SecretsStore.loadOrCreate()
        if (!secrets.isConfigured || s.endpoint != secrets.endpointURLString) { stopNow(s); return true }
        // "Wi-Fi only" turned on since it was queued: queued again for Wi-Fi.
        if (PhotoSync.waitsForWifi(secrets.wifiOnly.value, NetworkWatch.syncMetered())) { scheduleFetch(ExistingWorkPolicy.REPLACE); return true }
        val gen = generation
        val copies = DirCopies(dir(s.groupId)) { stillOn(s.groupId, gen) }
        val run = WallpaperFetch({ b -> OTCConnection.request(build = b) }, copies)
        val outcome = run.run(
            s.groupId,
            progress = { done, total -> if (stillOn(s.groupId, gen)) _progress.value = Progress(s.groupId, done, total) },
            stored = { if (stillOn(s.groupId, gen) && !prefs().getBoolean(KEY_STARTED, false)) firstShown(s) },
        )
        if (!stillOn(s.groupId, gen)) return true
        return when (outcome) {
            WallpaperFetch.Outcome.Gone -> { collectionDeleted(s.groupId); true }
            WallpaperFetch.Outcome.Failed -> {
                Log.w(TAG, "copies not stored; tried again later")
                _progress.value = (_progress.value ?: Progress(s.groupId, 0, 0)).copy(failed = true)
                if (!prefs().getBoolean(KEY_STARTED, false)) say(S.appPhotosWallpaperFailed(), error = true)
                false
            }
            is WallpaperFetch.Outcome.Done -> {
                if (outcome.name != s.name) collectionRenamed(s.groupId, outcome.name)
                prefs().edit { putLong(KEY_LAST_FETCH, System.currentTimeMillis()) }
                _progress.value = null
                // Started again with its copies already here (nothing new to
                // store), or none could be stored at all.
                if (!prefs().getBoolean(KEY_STARTED, false)) {
                    if (outcome.stored > 0) firstShown(s) else say(S.appPhotosWallpaperFailed(), error = true)
                }
                Log.i(TAG, "copies in step: ${outcome.stored}")
                true
            }
        }
    }

    // The first copy stored after Start: shown at once, and said.
    private fun firstShown(s: WallpaperSettings) {
        prefs().edit(commit = true) { putBoolean(KEY_STARTED, true) }
        if (showNext(s)) say(S.androidWallpaperStarted(_active.value?.name ?: s.name))
        else say(S.androidWallpaperFailed(), error = true)
    }

    /** RotateWorker's run: the next photo, and a refresh of the copies once in a while. */
    internal fun rotate() {
        val s = active.value ?: return
        val secrets = SecretsStore.loadOrCreate()
        if (!secrets.isConfigured || s.endpoint != secrets.endpointURLString) { stopNow(s); return }
        if (!showNext(s)) Log.w(TAG, "no photo set")
        if (System.currentTimeMillis() - prefs().getLong(KEY_LAST_FETCH, 0) >= REFRESH_MS) scheduleFetch(ExistingWorkPolicy.KEEP)
    }

    /** Sets the next copy (WallpaperShuffle) on [s]'s screens: false when there is none or Android refused it. */
    private fun showNext(s: WallpaperSettings): Boolean {
        val d = dir(s.groupId)
        val p = prefs()
        var all = DirCopies(d) { true }.hashes().toList()
        var shown = WallpaperShuffle.parse(p.getString(KEY_SHOWN, null))
        val last = p.getString(KEY_LAST, null)
        repeat(3) {
            val (pick, next) = WallpaperShuffle.next(all, shown, last) ?: return false
            val bmp = WallpaperImages.forScreen(File(d, "$pick.jpg"), ctx())
            if (bmp == null) {
                // Gone meanwhile (a refresh), or unreadable: another one.
                all = all - pick
                shown = shown - pick
                return@repeat
            }
            val ok = try {
                val wm = WallpaperManager.getInstance(ctx())
                if (!wm.isWallpaperSupported || !wm.isSetWallpaperAllowed) false
                else { wm.setBitmap(bmp, null, false, s.target.flags); true }
            } catch (e: Exception) {
                Log.w(TAG, "wallpaper not set: ${e.javaClass.simpleName}")
                false
            } finally { bmp.recycle() }
            if (ok) synchronized(lock) {
                if (_active.value?.groupId == s.groupId) p.edit { putString(KEY_SHOWN, WallpaperShuffle.format(next)); putString(KEY_LAST, pick) }
            }
            return ok
        }
        return false
    }

    class FetchWorker(ctx: Context, params: WorkerParameters) : CoroutineWorker(ctx, params) {
        override suspend fun doWork(): Result = try {
            if (fetch()) Result.success() else Result.retry()
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            Log.w(TAG, "fetch failed: ${e.javaClass.simpleName}")
            Result.retry()
        }
    }

    class RotateWorker(ctx: Context, params: WorkerParameters) : CoroutineWorker(ctx, params) {
        override suspend fun doWork(): Result {
            try { rotate() } catch (e: Exception) { Log.w(TAG, "rotation failed: ${e.javaClass.simpleName}") }
            return Result.success()
        }
    }

    /**
     * A collection's copies: [dir]'s files, named by hash. A copy is written
     * aside and moved into place only while [wanted] (not stopped since), so
     * a stop never leaves a copy behind.
     */
    private class DirCopies(private val dir: File, private val wanted: () -> Boolean) : WallpaperCopies {
        override fun hashes(): Set<String> =
            dir.listFiles()?.mapNotNull { f -> f.name.removeSuffix(".jpg").takeIf { f.name.endsWith(".jpg") && isHash(it) } }?.toSet() ?: emptySet()

        override fun save(hash: String, bytes: ByteArray): Boolean {
            if (!isHash(hash)) return false
            val jpeg = WallpaperImages.copyFor(bytes, ctx()) ?: return false
            synchronized(lock) {
                if (!wanted()) return false
                dir.mkdirs()
                val tmp = File(dir, ".$hash.tmp")
                return try {
                    tmp.writeBytes(jpeg)
                    tmp.renameTo(File(dir, "$hash.jpg"))
                } catch (e: Exception) {
                    tmp.delete()
                    false
                }
            }
        }

        override fun delete(hash: String) {
            if (isHash(hash)) File(dir, "$hash.jpg").delete()
        }
    }
}

/** The copies' pictures: made from what the device sent, and read back for the screen. */
internal object WallpaperImages {
    /** The screen's size in pixels, as it is held by default (its natural orientation). */
    fun screen(ctx: Context): Pair<Int, Int> {
        val d = ctx.getSystemService(DisplayManager::class.java)?.getDisplay(Display.DEFAULT_DISPLAY)
        val m = d?.mode
        if (m != null && m.physicalWidth > 0 && m.physicalHeight > 0) return m.physicalWidth to m.physicalHeight
        val dm = ctx.resources.displayMetrics
        return dm.widthPixels to dm.heightPixels
    }

    /** A copy of [bytes] (a photo as GetFile sends it) for this screen: upright, cropped to its shape, at most its size, JPEG. */
    fun copyFor(bytes: ByteArray, ctx: Context): ByteArray? = try {
        val (sw, sh) = screen(ctx)
        val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
        BitmapFactory.decodeByteArray(bytes, 0, bytes.size, bounds)
        val turn = orientation(bytes)
        val quarter = turn.degrees % 180 != 0
        val w = if (quarter) bounds.outHeight else bounds.outWidth
        val h = if (quarter) bounds.outWidth else bounds.outHeight
        val sample = wallpaperSample(w, h, sw, sh)
        val decoded = BitmapFactory.decodeByteArray(bytes, 0, bytes.size, BitmapFactory.Options().apply { inSampleSize = sample })
        decoded?.let { upright(it, turn) }?.let { pic ->
            val out = fit(pic, sw, sh)
            val buf = ByteArrayOutputStream()
            out.compress(Bitmap.CompressFormat.JPEG, 90, buf)
            if (out !== pic) out.recycle()
            pic.recycle()
            buf.toByteArray()
        }
    } catch (e: Exception) { null } catch (e: OutOfMemoryError) { null }

    /** A stored copy, fitted to the screen as it is now (a foldable's other screen has another shape). */
    fun forScreen(file: File, ctx: Context): Bitmap? = try {
        val (sw, sh) = screen(ctx)
        BitmapFactory.decodeFile(file.path)?.let { b -> fit(b, sw, sh).also { if (it !== b) b.recycle() } }
    } catch (e: Exception) { null } catch (e: OutOfMemoryError) { null }

    private fun fit(b: Bitmap, sw: Int, sh: Int): Bitmap {
        val c = WallpaperCrop.of(b.width, b.height, sw, sh) ?: return b
        if (c.left == 0 && c.top == 0 && c.width == b.width && c.height == b.height && c.outW == b.width && c.outH == b.height) return b
        val m = Matrix().apply { setScale(c.outW.toFloat() / c.width, c.outH.toFloat() / c.height) }
        return Bitmap.createBitmap(b, c.left, c.top, c.width, c.height, m, true)
    }

    private data class Turn(val degrees: Int, val mirror: Boolean)

    private fun orientation(bytes: ByteArray): Turn = try {
        when (ExifInterface(ByteArrayInputStream(bytes)).getAttributeInt(ExifInterface.TAG_ORIENTATION, ExifInterface.ORIENTATION_NORMAL)) {
            ExifInterface.ORIENTATION_ROTATE_90 -> Turn(90, false)
            ExifInterface.ORIENTATION_ROTATE_180 -> Turn(180, false)
            ExifInterface.ORIENTATION_ROTATE_270 -> Turn(270, false)
            ExifInterface.ORIENTATION_FLIP_HORIZONTAL -> Turn(0, true)
            ExifInterface.ORIENTATION_FLIP_VERTICAL -> Turn(180, true)
            ExifInterface.ORIENTATION_TRANSPOSE -> Turn(90, true)
            ExifInterface.ORIENTATION_TRANSVERSE -> Turn(270, true)
            else -> Turn(0, false)
        }
    } catch (e: Exception) { Turn(0, false) }

    private fun upright(b: Bitmap, t: Turn): Bitmap {
        if (t.degrees == 0 && !t.mirror) return b
        val m = Matrix()
        if (t.mirror) m.postScale(-1f, 1f)
        if (t.degrees != 0) m.postRotate(t.degrees.toFloat())
        return Bitmap.createBitmap(b, 0, 0, b.width, b.height, m, true).also { if (it !== b) b.recycle() }
    }
}
