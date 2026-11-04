// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.data

import android.content.Context
import android.content.SharedPreferences
import android.util.Log
import androidx.security.crypto.EncryptedSharedPreferences
import androidx.security.crypto.MasterKey
import cloud.offthe.otc.OTCApp
import cloud.offthe.otc.net.HomeEndpoint
import cloud.offthe.otc.net.PinnedAddress
import cloud.offthe.otc.push.FCMPush
import cloud.offthe.otc.sync.AssetSyncCache
import cloud.offthe.otc.sync.PhotoFolders
import cloud.offthe.otc.sync.PhotoSync
import cloud.offthe.otc.sync.SyncScheduler
import cloud.offthe.otc.wallpaper.Wallpaper
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import java.net.URI
import java.security.KeyStore
import java.util.UUID

// Port of SecretsStore.swift. The endpoint, password and device id live in
// EncryptedSharedPreferences (the Keychain's counterpart: encrypted with a
// key in the Android Keystore); the sync toggles and the folders left out
// of the sync in plain preferences, auto-persisted on change like on iOS
// (persistOptions). endpoint/password only persist on an explicit Save
// (saveCredentials), so the connection form doesn't write a secret per
// keystroke.
class SecretsStore private constructor(
    endpoint: String, password: String, deviceId: String,
    wifiOnly: Boolean, includeVideos: Boolean,
    home: Pair<String, HomeEndpoint>?, skippedFolders: Set<String>,
) {
    private val _endpoint = MutableStateFlow(endpoint)
    private val _password = MutableStateFlow(password)
    private val _deviceId = MutableStateFlow(deviceId)
    private val _wifiOnly = MutableStateFlow(wifiOnly)
    private val _includeVideos = MutableStateFlow(includeVideos)
    private val _skippedFolders = MutableStateFlow(skippedFolders)

    val endpoint: StateFlow<String> = _endpoint
    val password: StateFlow<String> = _password
    val deviceId: StateFlow<String> = _deviceId
    val wifiOnly: StateFlow<Boolean> = _wifiOnly
    val includeVideos: StateFlow<Boolean> = _includeVideos
    /**
     * Settings > Sync Options > Folders: the MediaStore buckets (BUCKET_ID,
     * as text) the owner turned off. A list of what to leave out, never of
     * what to include, so a folder that appears later is backed up.
     */
    val skippedFolders: StateFlow<Set<String>> = _skippedFolders

    fun setWifiOnly(v: Boolean) {
        val was = _wifiOnly.value
        _wifiOnly.value = v
        persistOptions()
        if (v == was) return
        // A1: the background runs' network follows, and a run that waited
        // for Wi-Fi goes now that it doesn't have to.
        SyncScheduler.wifiOnlyChanged()
        Wallpaper.wifiOnlyChanged()
        if (!v) PhotoSync.wifiOnlyOff()
    }
    fun setIncludeVideos(v: Boolean) {
        val was = _includeVideos.value
        _includeVideos.value = v
        persistOptions()
        // Back on: the videos taken while it was off are sent too
        // (PhotoSync.videosTurnedOn). Log Out puts it back on signed out.
        if (v && !was && isConfigured) PhotoSync.videosTurnedOn()
    }
    fun setFolderSkipped(id: String, skipped: Boolean) {
        _skippedFolders.value = if (skipped) _skippedFolders.value + id else _skippedFolders.value - id
        persistOptions()
    }

    val isConfigured: Boolean get() = _endpoint.value.isNotEmpty() && _password.value.isNotEmpty()

    /**
     * Bumped when the saved address changes to another one (not Leave the
     * Bridge): RootView makes MainView afresh, its view models with it, so
     * no screen keeps the last device's lists (DeviceChange).
     */
    private val _deviceSession = MutableStateFlow(0)
    val deviceSession: StateFlow<Int> = _deviceSession

    // Issue #190: where the device answers on the home network, with the
    // endpoint it was learnt through - only ever used for that endpoint.
    @Volatile private var home: Pair<String, HomeEndpoint>? = home

    /** The home endpoint the device gave while signed in through [endpointURL], if any. */
    fun homeEndpoint(endpointURL: String): HomeEndpoint? = home?.takeIf { it.first == endpointURL }?.second

    fun saveHomeEndpoint(endpointURL: String, ep: HomeEndpoint) {
        home = endpointURL to ep
        secure().edit().putString("home_for", endpointURL).putString("home_endpoint", ep.encode()).apply()
    }

    fun clearHomeEndpoint() {
        home = null
        secure().edit().remove("home_for").remove("home_endpoint").apply()
    }


    /**
     * Log Out (SettingsView, as on iOS): forget the connection and wipe
     * everything this phone holds about the device - the secrets, the
     * settings, the sync history, the caches - and start over with a fresh
     * device id, so the next sign-in looks like a first install. Nothing
     * on the device itself is touched.
     */
    fun logOut() {
        val ctx = OTCApp.instance
        secure().edit().clear().apply()
        home = null
        plain().edit().clear().apply()
        ctx.getSharedPreferences("otc_sync", Context.MODE_PRIVATE).edit().clear().apply()
        for (dir in listOf(ctx.cacheDir, ctx.filesDir)) dir.listFiles()?.forEach { it.deleteRecursively() }
        _endpoint.value = ""
        _password.value = ""
        _wifiOnly.value = false
        _includeVideos.value = true
        _skippedFolders.value = emptySet()
        _deviceId.value = UUID.randomUUID().toString().also { secure().edit().putString("device_id", it).apply() }
    }

    /** endpoint, normalized - what every connection should dial. */
    val endpointURLString: String get() = normalizedEndpoint(_endpoint.value)

    /**
     * Save, Save & Retry, Save & Continue and the Bluetooth setup's "Use
     * this device": the address and password they were given become the
     * ones every connection dials. Nothing else writes them: the forms edit
     * drafts until then (IOS-03 on iOS; ConnectionFormModel here). As iOS's
     * saveCredentials. [sameDevice] (Leave the Bridge: the device itself
     * gave its new address) keeps what was learnt about its home network.
     */
    fun saveCredentials(endpoint: String, password: String, sameDevice: Boolean = false) {
        val before = normalizedEndpoint(secure().getString("endpoint", "") ?: "")
        // Another address than the one signed in to before is a change of
        // device until its device says otherwise (DeviceChange): its
        // screens are made afresh and the photo sync waits for the answer.
        val changed = DeviceChange.isChange(before, normalizedEndpoint(endpoint), sameDevice)
        // Read before the old address's home endpoint goes below.
        val pinBefore = if (changed) homeEndpoint(before)?.certSha256 else null
        _endpoint.value = endpoint
        _password.value = password
        if (isConfigured) {
            clearPendingSetup()
            clearLastDevice()
        }
        if (before != endpointURLString) {
            // Issue #190: the home-network endpoint belongs to the endpoint
            // it was learnt on; another device or address starts without
            // one - except the same device at its new address, which keeps
            // its pin (its first sign-in there is never in the clear), and
            // the same address spelled otherwise (a host's case), which is
            // no change: dropping its pin would send the next sign-in in the
            // clear.
            val h = home
            when (val kept = PinnedAddress.homeAfterSave(h, before, endpointURLString, sameDevice)) {
                null -> clearHomeEndpoint()
                h -> {}
                else -> saveHomeEndpoint(kept.first, kept.second)
            }
            when {
                changed -> {
                    DeviceChange.begin(pinBefore)
                    DeviceChange.addressChanged()
                    _deviceSession.value += 1
                }
                // The same device at its new address (Leave the Bridge), or
                // the same address spelled otherwise: the wallpaper goes on.
                before.isNotEmpty() && endpointURLString.isNotEmpty() -> Wallpaper.sameDeviceAt(endpointURLString)
                // A first sign-in: a collection left from another endpoint
                // stops (its copies were that one's photos).
                else -> Wallpaper.deviceChanged(endpointURLString)
            }
        }
        secure().edit()
            .putString("endpoint", _endpoint.value)
            .putString("password", _password.value)
            .putString("device_id", _deviceId.value)
            .apply()
    }

    /** The options, each time one changes: plain preferences only, never the secrets. */
    fun persistOptions() {
        val p = plain().edit()
            .putBoolean("wifiOnly", _wifiOnly.value)
            .putBoolean("includeVideos", _includeVideos.value)
            // A15: the old "Sync from cloud" switch, never read.
            .remove("downloadFromCloud")
        PhotoFolders.storeSet(p, "skippedFolders", _skippedFolders.value)
        p.apply()
    }

    companion object {
        const val bridgeDomain = "off-the.cloud"
        private const val TAG = "OTC/SecretsStore"

        @Volatile private var shared: SecretsStore? = null

        /**
         * A device set up from this phone whose install may still be
         * running (BluetoothSetupView saves it as soon as the wizard has the
         * address and password): Onboarding fills its form from it, and it
         * goes once the app is configured.
         */
        fun savePendingSetup(endpoint: String, password: String) {
            secure().edit().putString("setup_endpoint", endpoint).putString("setup_password", password).apply()
        }

        fun pendingSetup(): Pair<String, String>? {
            val s = secure()
            val p = s.getString("setup_password", null)
            if (p.isNullOrEmpty()) return null
            return (s.getString("setup_endpoint", "") ?: "") to p
        }

        fun clearPendingSetup() {
            secure().edit().remove("setup_endpoint").remove("setup_password").apply()
        }

        /**
         * The device this phone left from the "isn't available" or "can't
         * connect" screens: leaving wipes everything else, but a device that
         * is down for a while (a restart, a re-image) is usually the one the
         * phone comes back to, so Onboarding fills its form from it. Same as
         * SecretsStore.lastDevice on iOS.
         */
        fun saveLastDevice(endpoint: String, password: String) {
            if (endpoint.isEmpty()) return
            secure().edit().putString("last_endpoint", endpoint).putString("last_password", password).apply()
        }

        fun lastDevice(): Pair<String, String>? {
            val s = secure()
            val e = s.getString("last_endpoint", null)
            if (e.isNullOrEmpty()) return null
            return e to (s.getString("last_password", "") ?: "")
        }

        fun clearLastDevice() {
            secure().edit().remove("last_endpoint").remove("last_password").apply()
        }

        /** One instance per process, loaded on first use (the process's one
         *  Keystore round trip: RootView makes it, before the first frame).
         *  Throws [Unavailable] while the secure store can't be opened. */
        fun loadOrCreate(): SecretsStore = shared ?: synchronized(this) {
            shared ?: load().also { shared = it }
        }

        /** [loadOrCreate], or null while the secure store can't be opened (RootView says so). */
        fun openOrNull(): SecretsStore? = try { loadOrCreate() } catch (e: Unavailable) { null }

        private fun load(): SecretsStore {
            val s = secure()
            val p = plain()
            val deviceId = s.getString("device_id", null) ?: UUID.randomUUID().toString().also {
                s.edit().putString("device_id", it).apply()
            }
            return SecretsStore(
                endpoint = s.getString("endpoint", "") ?: "",
                password = s.getString("password", "") ?: "",
                deviceId = deviceId,
                wifiOnly = p.getBoolean("wifiOnly", false),
                includeVideos = p.getBoolean("includeVideos", true),
                home = s.getString("home_for", null)?.let { f -> HomeEndpoint.decode(s.getString("home_endpoint", null))?.let { f to it } },
                skippedFolders = PhotoFolders.storedSet(p, "skippedFolders"),
            )
        }

        // One instance per process: each create() unwraps the Tink keysets
        // through the Keystore (tens of ms), and persist() alone used to
        // need three of them, on the main thread.
        @Volatile private var securePrefs: SharedPreferences? = null

        private fun secure(): SharedPreferences =
            securePrefs ?: synchronized(this) { securePrefs ?: openSecure().also { securePrefs = it } }

        private fun createSecure(ctx: Context): SharedPreferences {
            val key = MasterKey.Builder(ctx).setKeyScheme(MasterKey.KeyScheme.AES256_GCM).build()
            return EncryptedSharedPreferences.create(
                ctx, "otc_secrets", key,
                EncryptedSharedPreferences.PrefKeyEncryptionScheme.AES256_SIV,
                EncryptedSharedPreferences.PrefValueEncryptionScheme.AES256_GCM,
            )
        }

        /**
         * The secure store couldn't be opened, and nothing says its keyset is
         * unusable (a Keystore busy or not ready - after an OS update, at
         * boot): the file is kept, and the next use tries again. RootView
         * says so with Try again; a background run is retried later.
         */
        class Unavailable(cause: Throwable) : IllegalStateException("secure storage unavailable", cause) // i18n-ignore: logged, never shown

        /**
         * Pure: whether [e] (opening the store failed) means its keyset can
         * never be read again - the Keystore key it is wrapped in isn't this
         * one (a file restored or moved from another phone), or the file is
         * corrupt - rather than a Keystore error that passes.
         */
        internal fun keysetUnusable(e: Throwable): Boolean {
            var c: Throwable? = e
            var depth = 0
            while (c != null && depth++ < 16) {
                if (c is javax.crypto.BadPaddingException || c is java.security.UnrecoverableKeyException ||
                    c is android.security.keystore.KeyPermanentlyInvalidatedException ||
                    c.javaClass.simpleName == "InvalidProtocolBufferException"
                ) return true
                c = c.cause.takeIf { it !== c }
            }
            return false
        }

        // A Keystore error that may pass is tried again after these.
        private val retryPauses = longArrayOf(200, 500, 1_000)

        /**
         * otc_secrets restored from a backup or moved from another phone
         * (releases before the backup rules) arrives without the Keystore
         * key its keyset is wrapped in, and create() throws on every launch:
         * then the file is dropped and the app starts as a fresh install, as
         * iOS does (its Keychain items are ThisDeviceOnly). Only then
         * ([keysetUnusable]): any other error - a Keystore busy, or not ready
         * after an OS update - is tried again a few times, then fails this
         * use ([Unavailable]) and keeps the file. It used to wipe the
         * sign-in, the device id (a second folder on the device at the next
         * sign-in) and the sync's watermark after two quick tries.
         */
        private fun openSecure(): SharedPreferences {
            val ctx = OTCApp.instance
            var last: Exception? = null
            for (pause in longArrayOf(0) + retryPauses) {
                if (pause > 0) Thread.sleep(pause)
                try { return createSecure(ctx) } catch (e: Exception) {
                    last = e
                    if (keysetUnusable(e)) break
                    Log.w(TAG, "secrets unreadable, trying again: ${e.javaClass.simpleName}")
                }
            }
            if (last != null && !keysetUnusable(last)) throw Unavailable(last)
            Log.w(TAG, "secrets can't be read on this phone, starting over: ${last?.javaClass?.simpleName}")
            ctx.deleteSharedPreferences("otc_secrets") // the keyset lives in the same file
            forgetRestoredState(ctx)
            return try {
                createSecure(ctx)
            } catch (e: Exception) {
                // The master key itself is broken. Only otc_secrets uses it,
                // and that is already gone.
                Log.w(TAG, "secrets: recreating the master key: $e")
                KeyStore.getInstance("AndroidKeyStore").apply { load(null) }.deleteEntry(MasterKey.DEFAULT_MASTER_KEY_ALIAS)
                createSecure(ctx)
            }
        }

        // What came back from the old phone with the secrets: its sync
        // watermark (would hide every photo here taken before it), its
        // MediaStore id -> hash map (ids are small integers, so they'd point
        // this phone's photos at other photos' hashes) and its FCM token.
        // Not logOut(): that calls secure() again.
        private fun forgetRestoredState(ctx: Context) {
            ctx.getSharedPreferences("otc_sync", Context.MODE_PRIVATE).edit().clear().apply()
            AssetSyncCache.clear()
            FCMPush.forgetToken(ctx)
        }

        private fun plain(): SharedPreferences =
            OTCApp.instance.getSharedPreferences("otc_settings", Context.MODE_PRIVATE)

        /**
         * The endpoint as a URL the device will actually accept: no scheme
         * becomes wss, a missing or bare "/" path becomes /ws. Anything
         * explicit is kept.
         */
        fun normalizedEndpoint(raw: String): String {
            var s = raw.trim()
            if (s.isEmpty()) return s
            if (!s.contains("://")) s = "wss://$s"
            return try {
                val u = URI(s)
                if (u.path.isNullOrEmpty() || u.path == "/") {
                    URI(u.scheme, u.userInfo, u.host, u.port, "/ws", u.query, u.fragment).toString()
                } else s
            } catch (e: Exception) {
                s
            }
        }

        /** Whether two saved addresses are the same, as dialled (normalized; a host's case doesn't count). */
        fun sameAddress(a: String, b: String): Boolean = normalizedEndpoint(a).lowercase() == normalizedEndpoint(b).lowercase()

        /** Issue #121: a device name -> its bridge address. */
        fun bridgeEndpoint(forName: String): String =
            "wss://" + forName.trim().lowercase() + "." + bridgeDomain + "/ws"

        /** The device name if endpoint is a plain bridge address, else null. */
        fun bridgeName(fromEndpoint: String): String? {
            val u = try { URI(normalizedEndpoint(fromEndpoint)) } catch (e: Exception) { return null }
            val host = u.host ?: return null
            if (u.scheme != "wss" || u.port != -1 || u.path != "/ws" || u.query != null) return null
            if (!host.endsWith(".$bridgeDomain")) return null
            val name = host.dropLast(bridgeDomain.length + 1)
            return if (name.isEmpty() || name.contains(".")) null else name
        }
    }
}
