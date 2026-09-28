// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui

import android.app.Activity
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Bundle
import android.util.Base64
import androidx.browser.customtabs.CustomTabsIntent
import cloud.offthe.otc.MainActivity
import cloud.offthe.otc.data.SecretsStore
import java.security.MessageDigest
import java.security.SecureRandom
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeout
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONObject

/**
 * "Continue with Apple/Google" in the setup wizard (issue #137), as
 * BLESetupSignIn in BluetoothSetupView.swift: the phone has internet while
 * it sets a device up over Bluetooth, so it runs the bridge's sign-in in a
 * Custom Tab and hands the wizard a setup token - no code copied from
 * another device. Any app could register otcsetup://, so the redirect only
 * carries a one-time code, redeemed here with the PKCE verifier that never
 * left the app (the bridge's appsignin.go).
 */
object SetupSignIn {
    private var pending: CompletableDeferred<Uri>? = null

    suspend fun run(context: Context, provider: String): String {
        require(provider == "apple" || provider == "google") { "Unknown sign-in provider" }
        val verifier = randomVerifier()
        val challenge = b64url(MessageDigest.getInstance("SHA-256").digest(verifier.toByteArray()))
        val start = Uri.parse("https://${SecretsStore.bridgeDomain}/account/auth/$provider/start").buildUpon()
            .appendQueryParameter("return", "otcsetup://done")
            .appendQueryParameter("challenge", challenge)
            .build()
        pending?.cancel()
        val waiter = CompletableDeferred<Uri>()
        pending = waiter
        CustomTabsIntent.Builder().build().launchUrl(context, start)
        val callback = try {
            withTimeout(10 * 60 * 1000L) { waiter.await() }
        } finally {
            if (pending === waiter) pending = null
        }
        val code = callback.getQueryParameter("code") ?: error("The sign-in did not finish")
        return exchange(code, verifier)
    }

    /** SetupSignInCallbackActivity hands the otcsetup://done redirect over. */
    fun deliver(uri: Uri) {
        pending?.complete(uri)
    }

    private suspend fun exchange(code: String, verifier: String): String = withContext(Dispatchers.IO) {
        val body = JSONObject().put("code", code).put("verifier", verifier).toString()
            .toRequestBody("application/json".toMediaType())
        val req = Request.Builder().url("https://${SecretsStore.bridgeDomain}/api/account/app-exchange").post(body).build()
        OkHttpClient().newCall(req).execute().use { resp ->
            val obj = runCatching { JSONObject(resp.body.string()) }.getOrNull()
            val token = obj?.optString("setup_token").orEmpty()
            if (!resp.isSuccessful || token.isEmpty()) error(obj?.optString("error")?.ifEmpty { null } ?: "Could not finish the sign-in")
            token
        }
    }

    private fun randomVerifier(): String = ByteArray(32).also { SecureRandom().nextBytes(it) }.let(::b64url)

    private fun b64url(bytes: ByteArray): String = Base64.encodeToString(bytes, Base64.URL_SAFE or Base64.NO_PADDING or Base64.NO_WRAP)
}

/** Receives otcsetup://done from the Custom Tab and brings the app back. */
class SetupSignInCallbackActivity : Activity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        intent?.data?.let { SetupSignIn.deliver(it) }
        startActivity(Intent(this, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_CLEAR_TOP or Intent.FLAG_ACTIVITY_SINGLE_TOP))
        finish()
    }
}
