// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.data

import android.content.Context
import android.content.SharedPreferences
import androidx.security.crypto.EncryptedSharedPreferences
import androidx.security.crypto.MasterKey
import cloud.offthe.otc.OTCApp
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import java.net.URI
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

    /** endpoint, normalized - what every connection should dial. */
    val endpointURLString: String get() = normalizedEndpoint(_endpoint.value)

    fun persist() {
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

        @Volatile private var shared: SecretsStore? = null

        /** One instance per process, loaded on first use (a Keystore round
         *  trip, so call it off the main thread the first time). */
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

        private fun secure(): SharedPreferences {
            val ctx = OTCApp.instance
            val key = MasterKey.Builder(ctx).setKeyScheme(MasterKey.KeyScheme.AES256_GCM).build()
            return EncryptedSharedPreferences.create(
                ctx, "otc_secrets", key,
                EncryptedSharedPreferences.PrefKeyEncryptionScheme.AES256_SIV,
                EncryptedSharedPreferences.PrefValueEncryptionScheme.AES256_GCM,
            )
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
