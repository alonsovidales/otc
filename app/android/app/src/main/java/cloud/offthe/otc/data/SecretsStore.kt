// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.data

import android.content.Context
import android.content.SharedPreferences
import android.util.Log
import androidx.security.crypto.EncryptedSharedPreferences
import androidx.security.crypto.MasterKey
import cloud.offthe.otc.OTCApp
import cloud.offthe.otc.push.FCMPush
import cloud.offthe.otc.sync.AssetSyncCache
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import java.net.URI
import java.security.KeyStore
import java.util.UUID

// Port of SecretsStore.swift. The endpoint, password and device id live in
// EncryptedSharedPreferences (the Keychain's counterpart: encrypted with a
// key in the Android Keystore); the three sync toggles in plain
// preferences, auto-persisted on change like on iOS. endpoint/password/
// deviceId only persist on an explicit persist(), so the connection form
// doesn't write a secret per keystroke.
class SecretsStore private constructor(
    endpoint: String, password: String, deviceId: String,
    wifiOnly: Boolean, includeVideos: Boolean, downloadFromCloud: Boolean,
) {
    private val _endpoint = MutableStateFlow(endpoint)
    private val _password = MutableStateFlow(password)
    private val _deviceId = MutableStateFlow(deviceId)
    private val _wifiOnly = MutableStateFlow(wifiOnly)
    private val _includeVideos = MutableStateFlow(includeVideos)
    private val _downloadFromCloud = MutableStateFlow(downloadFromCloud)

    val endpoint: StateFlow<String> = _endpoint
    val password: StateFlow<String> = _password
    val deviceId: StateFlow<String> = _deviceId
    val wifiOnly: StateFlow<Boolean> = _wifiOnly
    val includeVideos: StateFlow<Boolean> = _includeVideos
    val downloadFromCloud: StateFlow<Boolean> = _downloadFromCloud

    fun setEndpoint(v: String) { _endpoint.value = v }
    fun setPassword(v: String) { _password.value = v }
    fun setWifiOnly(v: Boolean) { _wifiOnly.value = v; persist() }
    fun setIncludeVideos(v: Boolean) { _includeVideos.value = v; persist() }
    fun setDownloadFromCloud(v: Boolean) { _downloadFromCloud.value = v; persist() }

    val isConfigured: Boolean get() = _endpoint.value.isNotEmpty() && _password.value.isNotEmpty()

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
        plain().edit().clear().apply()
        ctx.getSharedPreferences("otc_sync", Context.MODE_PRIVATE).edit().clear().apply()
        for (dir in listOf(ctx.cacheDir, ctx.filesDir)) dir.listFiles()?.forEach { it.deleteRecursively() }
        _endpoint.value = ""
        _password.value = ""
        _wifiOnly.value = false
        _includeVideos.value = true
        _downloadFromCloud.value = true
        _deviceId.value = UUID.randomUUID().toString().also { secure().edit().putString("device_id", it).apply() }
    }

    /** endpoint, normalized - what every connection should dial. */
    val endpointURLString: String get() = normalizedEndpoint(_endpoint.value)

    fun persist() {
        if (isConfigured) {
            clearPendingSetup()
            clearLastDevice()
        }
        secure().edit()
            .putString("endpoint", _endpoint.value)
            .putString("password", _password.value)
            .putString("device_id", _deviceId.value)
            .apply()
        plain().edit()
            .putBoolean("wifiOnly", _wifiOnly.value)
            .putBoolean("includeVideos", _includeVideos.value)
            .putBoolean("downloadFromCloud", _downloadFromCloud.value)
            .apply()
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
         *  Keystore round trip: RootView makes it, before the first frame). */
        fun loadOrCreate(): SecretsStore = shared ?: synchronized(this) {
            shared ?: load().also { shared = it }
        }

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
                downloadFromCloud = p.getBoolean("downloadFromCloud", true),
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
         * otc_secrets restored from a backup or moved from another phone
         * (releases before the backup rules) arrives without the Keystore
         * key its keyset is wrapped in, and create() throws on every launch.
         * Retried once first, so a passing Keystore error on the phone that
         * wrote it wipes nothing; after that the file is dropped and the app
         * starts as a fresh install, as iOS does (its Keychain items are
         * ThisDeviceOnly).
         */
        private fun openSecure(): SharedPreferences {
            val ctx = OTCApp.instance
            try { return createSecure(ctx) } catch (e: Exception) { Log.w(TAG, "secrets unreadable, retrying: $e") }
            try { return createSecure(ctx) } catch (e: Exception) { Log.w(TAG, "secrets still unreadable, starting over: $e") }
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
