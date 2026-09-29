// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui

import android.annotation.SuppressLint
import android.content.Context
import android.net.Uri
import android.webkit.JavascriptInterface
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.ui.Alignment
import androidx.compose.runtime.setValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.net.BLESetupTransport
import java.io.ByteArrayInputStream
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withContext
import org.json.JSONObject

// Port of BluetoothSetupView.swift (issue #137): the device's own setup
// wizard, shown in a WebView whose requests travel over Bluetooth LE to
// scripts/setup_ble.py on the device. The page itself comes through
// shouldInterceptRequest; its fetch() calls go through a JavaScript
// interface (a WebView interceptor never sees POST bodies), the same
// split as the iOS script bridge. Once the wizard reports the install
// online, "Use this device" hands the address back to the onboarding
// form; the first sign in sets the owner password.

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun BluetoothSetupView(onUseDevice: (String, String) -> Unit, onClose: () -> Unit) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    val transport = remember { BLESetupTransport(context) }
    val phase by transport.phase.collectAsState()
    val readyDomain by transport.readyDomain.collectAsState()
    val chosenPassword by transport.chosenPassword.collectAsState()
    var password by remember { mutableStateOf("") }
    val everReady by transport.everReady.collectAsState()
    val permissions = rememberLauncherForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) { transport.start() }
    LaunchedEffect(Unit) {
        if (BLESetupTransport.hasPermissions(context)) transport.start() else permissions.launch(BLESetupTransport.permissions())
    }
    DisposableEffect(Unit) { onDispose { transport.stop() } }

    Scaffold(topBar = {
        TopAppBar(
            title = { Text("Set up a new device") },
            navigationIcon = { IconButton(onClick = onClose) { Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back") } },
        )
    }) { pad ->
        Column(Modifier.padding(pad).fillMaxSize()) {
            if (everReady) {
                if (phase !is BLESetupTransport.Phase.Ready) {
                    // The page stays; its 3-second state poll picks up
                    // where it left off once the link is back.
                    Surface(color = MaterialTheme.colorScheme.tertiaryContainer, modifier = Modifier.fillMaxWidth()) {
                        Text(
                            "Connection to the device lost - reconnecting. Keep the phone next to it.",
                            style = MaterialTheme.typography.bodySmall, modifier = Modifier.padding(10.dp),
                        )
                    }
                }
                // The install takes about 20 minutes: keepScreenOn stops a
                // locked phone from dropping the Bluetooth link.
                AndroidView(factory = { makeWebView(it, transport, scope).apply { keepScreenOn = true } }, modifier = Modifier.weight(1f).fillMaxWidth())
            } else {
                Waiting(phase, Modifier.weight(1f))
            }
            readyDomain?.let { domain ->
                Surface(tonalElevation = 3.dp, modifier = Modifier.fillMaxWidth()) {
                    Column(Modifier.padding(16.dp), horizontalAlignment = Alignment.CenterHorizontally) {
                        Text(
                            if (domain.isEmpty()) "The device is ready on your home network." else "The device is ready as $domain.",
                            style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                        )
                        // Issue #137: the password is chosen here, in the app,
                        // which then signs in with it straight away - the
                        // first sign-in to a new device sets it. A recovered
                        // device keeps the one it had (as iOS).
                        // The password chosen in the wizard, when the page
                        // handed it over; otherwise (a recovered device, or
                        // the app restarted mid-setup) the owner types it.
                        Spacer(Modifier.height(8.dp))
                        if (chosenPassword.isEmpty()) {
                            Text("Enter the device's password", style = MaterialTheme.typography.titleSmall, modifier = Modifier.fillMaxWidth())
                            Spacer(Modifier.height(6.dp))
                            cloud.offthe.otc.ui.common.OTCTextField(
                                value = password, onValueChange = { password = it }, singleLine = true,
                                placeholder = { Text("Password") },
                                visualTransformation = androidx.compose.ui.text.input.PasswordVisualTransformation(),
                                keyboardOptions = androidx.compose.foundation.text.KeyboardOptions(keyboardType = androidx.compose.ui.text.input.KeyboardType.Password),
                                modifier = Modifier.fillMaxWidth(),
                            )
                        } else {
                            Text("Signing in with the password you chose in the setup.", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.fillMaxWidth())
                        }
                        val valid = chosenPassword.isNotEmpty() || password.isNotEmpty()
                        Spacer(Modifier.height(8.dp))
                        Button(onClick = { onUseDevice(endpointForDomain(domain), chosenPassword.ifEmpty { password }) }, enabled = valid, modifier = Modifier.fillMaxWidth()) {
                            Text("Open my device")
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun Waiting(phase: BLESetupTransport.Phase, modifier: Modifier) {
    Column(modifier.fillMaxWidth().padding(24.dp), horizontalAlignment = Alignment.CenterHorizontally) {
        Spacer(Modifier.weight(1f))
        val (title, hint) = when (phase) {
            is BLESetupTransport.Phase.Connecting -> "Connecting…" to ""
            is BLESetupTransport.Phase.Off -> "Bluetooth is off" to "Turn Bluetooth on to find the device."
            is BLESetupTransport.Phase.Unauthorized -> "Bluetooth access is needed" to "Allow nearby devices for Off The Cloud in Settings to set a device up this way."
            else -> "Looking for a device to set up…" to "Power the device on with its disks connected. Until it is set up it announces itself over Bluetooth; keep the phone next to it."
        }
        if (phase is BLESetupTransport.Phase.Off || phase is BLESetupTransport.Phase.Unauthorized) Spacer(Modifier.height(8.dp)) else CircularProgressIndicator()
        Spacer(Modifier.height(14.dp))
        Text(title, style = MaterialTheme.typography.titleMedium)
        if (hint.isNotEmpty()) {
            Spacer(Modifier.height(6.dp))
            Text(hint, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, textAlign = TextAlign.Center)
        }
        Spacer(Modifier.weight(1f))
        Text(
            "No Bluetooth? Join the device's own \"Off The Cloud\" WiFi instead and the same setup opens in the browser.",
            style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, textAlign = TextAlign.Center,
        )
    }
}

/** A bridge domain becomes the usual wss endpoint; without the bridge the device answers as otc.local. */
fun endpointForDomain(domain: String): String = when {
    domain.isEmpty() -> "ws://otc.local:8080/ws"
    domain.endsWith("." + SecretsStore.bridgeDomain) -> SecretsStore.bridgeEndpoint(domain.dropLast(SecretsStore.bridgeDomain.length + 1))
    else -> "wss://$domain/ws"
}

/** What the page runs before its own script: fetch() → the app. */
private const val FETCH_OVERRIDE = "<script>window.otcApp=1;window.__otcId=0;window.__otcCb={};" +
    "window.__otcAnswer=function(id,j){var cb=window.__otcCb[id];delete window.__otcCb[id];if(cb)cb(JSON.parse(j))};" +
    "window.fetch=function(u,o){o=o||{};var m=(o.method||'GET').toUpperCase();var b=o.body?String(o.body):'';" +
    "return new Promise(function(res){var id=++window.__otcId;window.__otcCb[id]=function(r){res(new Response(r.b,{status:r.s,headers:{'Content-Type':r.t}}))};" +
    "OTCSetup.request(id,m,String(u),b)})};" +
    "window.otcSetupPassword=function(p){OTCSetup.password(String(p))};" +
    "window.otcSetupSignIn=function(p){return new Promise(function(res,rej){var id=++window.__otcId;window.__otcCb[id]=function(r){if(r.ok)res(r.token);else rej(new Error(r.error))};OTCSetup.signIn(id,String(p))})};</script>"

private fun pathOf(url: String): String =
    if (url.contains("://")) Uri.parse(url).let { (it.encodedPath.orEmpty().ifEmpty { "/" }) + (it.encodedQuery?.let { q -> "?$q" } ?: "") } else url

private class Bridge(private val webView: WebView, private val transport: BLESetupTransport, private val scope: CoroutineScope) {
    /** The owner password the page just sealed for the device (as iOS). */
    @JavascriptInterface
    fun password(pw: String) {
        if (pw.length >= 8) transport.chosenPassword.value = pw
    }

    /** "Continue with Apple/Google" (issue #137) - see SetupSignIn. */
    @JavascriptInterface
    fun signIn(id: Int, provider: String) {
        scope.launch(Dispatchers.Main) {
            val reply = try {
                JSONObject().put("ok", true).put("token", SetupSignIn.run(webView.context, provider))
            } catch (e: Exception) {
                JSONObject().put("ok", false).put("error", e.message ?: "The sign-in did not finish")
            }
            webView.evaluateJavascript("window.__otcAnswer($id, ${JSONObject.quote(reply.toString())})", null)
        }
    }

    @JavascriptInterface
    fun request(id: Int, method: String, url: String, body: String) {
        scope.launch(Dispatchers.IO) {
            val reply = try {
                val a = transport.request(method, pathOf(url), body.ifEmpty { null })
                JSONObject().put("s", a.status).put("t", a.contentType).put("b", a.body)
            } catch (e: Exception) {
                JSONObject().put("s", 503).put("t", "application/json").put("b", JSONObject().put("error", e.message ?: "failed").toString())
            }
            withContext(Dispatchers.Main) { webView.evaluateJavascript("window.__otcAnswer($id, ${JSONObject.quote(reply.toString())})", null) }
        }
    }
}

@SuppressLint("SetJavaScriptEnabled")
private fun makeWebView(context: Context, transport: BLESetupTransport, scope: CoroutineScope): WebView = WebView(context).apply {
    settings.javaScriptEnabled = true
    settings.domStorageEnabled = true
    setBackgroundColor(0xFF1E1F22.toInt()) // the wizard's own background
    addJavascriptInterface(Bridge(this, transport, scope), "OTCSetup")
    webViewClient = object : WebViewClient() {
        // Runs on a background thread, so blocking on the round trip is fine.
        // The setup page never leaves itself: a link elsewhere opens in the
        // phone's browser, never in this view, where OTCSetup lives.
        override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
            if (request.url.scheme == "https" && request.url.host == "device") return false
            if (request.isForMainFrame && (request.url.scheme == "https" || request.url.scheme == "http")) {
                try { view.context.startActivity(android.content.Intent(android.content.Intent.ACTION_VIEW, request.url)) } catch (_: Exception) {}
            }
            return true
        }

        override fun shouldInterceptRequest(view: WebView, request: WebResourceRequest): WebResourceResponse? {
            // Nothing but the device loads in here - an embedded frame from
            // elsewhere would get the OTCSetup interface too.
            if (request.url.host != "device") {
                return WebResourceResponse("text/plain", "utf-8", 403, "Forbidden", emptyMap(), ByteArrayInputStream(ByteArray(0)))
            }
            val path = pathOf(request.url.toString())
            val a = try {
                runBlocking { transport.request(request.method, path, null) }
            } catch (e: Exception) {
                BLESetupTransport.Answer(503, "application/json", JSONObject().put("error", e.message ?: "failed").toString())
            }
            var body = a.body
            if (a.contentType.startsWith("text/html")) body = body.replaceFirst("<head>", "<head>$FETCH_OVERRIDE")
            val mime = a.contentType.substringBefore(';').trim()
            return WebResourceResponse(mime, "utf-8", a.status, reason(a.status), mapOf("Cache-Control" to "no-store"), ByteArrayInputStream(body.toByteArray()))
        }
    }
    loadUrl("https://device/")
}

private fun reason(status: Int): String = when (status) {
    200 -> "OK"; 202 -> "Accepted"; 400 -> "Bad Request"; 404 -> "Not Found"; 409 -> "Conflict"; 503 -> "Service Unavailable"
    else -> "Status $status"
}
