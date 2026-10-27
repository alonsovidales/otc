// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui

import android.annotation.SuppressLint
import android.content.Context
import android.net.Uri
import android.webkit.JavascriptInterface
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.activity.compose.BackHandler
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.consumeWindowInsets
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.TextButton
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.liveRegion
import cloud.offthe.otc.i18n.androidBluetoothScanFailed
import cloud.offthe.otc.i18n.androidBluetoothTurnOn
import cloud.offthe.otc.i18n.androidLocationOffHint
import cloud.offthe.otc.i18n.androidOpenSettings
import cloud.offthe.otc.i18n.appShellSetupLeave
import cloud.offthe.otc.i18n.appShellSetupLeaveText
import cloud.offthe.otc.i18n.appShellSetupLeaveTitle
import cloud.offthe.otc.i18n.appShellSetupNotFound
import cloud.offthe.otc.i18n.commonCancel
import cloud.offthe.otc.i18n.commonTryAgain
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.text.BasicText
import androidx.compose.foundation.text.TextAutoSize
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LocalContentColor
import androidx.compose.material3.LocalTextStyle
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
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.viewinterop.AndroidView
import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.i18n.LanguageSettings
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.androidBluetoothNeededHint
import cloud.offthe.otc.i18n.androidBluetoothOffHint
import cloud.offthe.otc.i18n.androidSetupLanguageLater
import cloud.offthe.otc.i18n.androidSignalGood
import cloud.offthe.otc.i18n.androidSignalStrong
import cloud.offthe.otc.i18n.androidSignalWeak
import cloud.offthe.otc.i18n.appShellBleLinkLost
import cloud.offthe.otc.i18n.appShellBluetoothNeeded
import cloud.offthe.otc.i18n.appShellBluetoothOff
import cloud.offthe.otc.i18n.appShellSetupDevice
import cloud.offthe.otc.i18n.appShellSetupDeviceOlder
import cloud.offthe.otc.i18n.appShellSetupEnterPassword
import cloud.offthe.otc.i18n.appShellSetupLooking
import cloud.offthe.otc.i18n.appShellSetupNewDevice
import cloud.offthe.otc.i18n.appShellSetupOpenDevice
import cloud.offthe.otc.i18n.appShellSetupPowerOn
import cloud.offthe.otc.i18n.appShellSetupReadyAs
import cloud.offthe.otc.i18n.appShellSetupReadyHome
import cloud.offthe.otc.i18n.appShellSetupSeveral
import cloud.offthe.otc.i18n.appShellSetupSigninUnfinished
import cloud.offthe.otc.i18n.appShellSetupSigningIn
import cloud.offthe.otc.i18n.appShellSetupWhich
import cloud.offthe.otc.i18n.commonBack
import cloud.offthe.otc.i18n.commonConnecting
import cloud.offthe.otc.i18n.commonPassword
import cloud.offthe.otc.net.BLESetupTransport
import cloud.offthe.otc.ui.common.now
import cloud.offthe.otc.ui.settings.LanguageMenu
import java.io.ByteArrayInputStream
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withContext
import org.json.JSONObject
import java.net.URLDecoder
import java.net.URLEncoder

// Port of BluetoothSetupView.swift (issue #137): the device's own setup
// wizard, shown in a WebView whose requests travel over Bluetooth LE to
// scripts/setup_ble.py on the device. The page itself comes through
// shouldInterceptRequest; its fetch() calls go through a JavaScript
// interface (a WebView interceptor never sees POST bodies), the same
// split as the iOS script bridge. Once the wizard reports the install
// online, "Use this device" hands the address back to the onboarding
// form; the first sign in sets the owner password. The top bar's language
// menu re-words the screen in place, and the page follows (SetupPage).

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun BluetoothSetupView(onUseDevice: (String, String) -> Unit, onClose: () -> Unit) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    val transport = remember { BLESetupTransport(context) }
    val page = remember { SetupPage(transport) }
    val phase by transport.phase.collectAsState()
    val readyDomain by transport.readyDomain.collectAsState()
    val chosenPassword by transport.chosenPassword.collectAsState()
    val setupDomain by transport.setupDomain.collectAsState()
    // Kept until the app is signed in to the device: Onboarding fills its
    // form from it (the Bluetooth link doesn't survive the install).
    LaunchedEffect(chosenPassword, setupDomain) {
        val d = setupDomain
        if (chosenPassword.isNotEmpty() && d != null) {
            val pw = chosenPassword
            kotlinx.coroutines.withContext(kotlinx.coroutines.Dispatchers.IO) { SecretsStore.savePendingSetup(endpointForDomain(d), pw) }
        }
    }
    var password by remember { mutableStateOf("") }
    val everReady by transport.everReady.collectAsState()
    val permissions = rememberLauncherForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) { transport.start() }
    LaunchedEffect(Unit) {
        if (BLESetupTransport.hasPermissions(context)) transport.start() else permissions.launch(BLESetupTransport.permissions())
    }
    // Bluetooth turned on (here, from the quick settings, or by "Turn on
    // Bluetooth"): looked for at once - "Bluetooth is off" used to stay.
    DisposableEffect(Unit) {
        val receiver = object : android.content.BroadcastReceiver() {
            override fun onReceive(c: Context, intent: android.content.Intent) {
                when (intent.getIntExtra(android.bluetooth.BluetoothAdapter.EXTRA_STATE, -1)) {
                    android.bluetooth.BluetoothAdapter.STATE_ON -> transport.bluetoothChanged(on = true)
                    android.bluetooth.BluetoothAdapter.STATE_OFF -> transport.bluetoothChanged(on = false)
                }
            }
        }
        val app = context.applicationContext
        androidx.core.content.ContextCompat.registerReceiver(
            app, receiver, android.content.IntentFilter(android.bluetooth.BluetoothAdapter.ACTION_STATE_CHANGED),
            androidx.core.content.ContextCompat.RECEIVER_NOT_EXPORTED,
        )
        onDispose { runCatching { app.unregisterReceiver(receiver) } }
    }
    // Back from Settings (access allowed, Location on) or anywhere else:
    // looked for again, and Location read again.
    var locationOn by remember { mutableStateOf(setupLocationOn(context)) }
    val lifecycle = androidx.compose.ui.platform.LocalLifecycleOwner.current.lifecycle
    DisposableEffect(lifecycle) {
        val observer = androidx.lifecycle.LifecycleEventObserver { _, event ->
            if (event == androidx.lifecycle.Lifecycle.Event.ON_RESUME) {
                locationOn = setupLocationOn(context)
                // Not while the access is still being asked for (the
                // question itself pauses the screen): its answer starts it.
                when (transport.phase.value) {
                    is BLESetupTransport.Phase.Off, is BLESetupTransport.Phase.ScanFailed -> transport.start()
                    is BLESetupTransport.Phase.Unauthorized -> if (BLESetupTransport.hasPermissions(context)) transport.start()
                    else -> {}
                }
            }
        }
        lifecycle.addObserver(observer)
        onDispose { lifecycle.removeObserver(observer) }
    }
    val enableBluetooth = rememberLauncherForActivityResult(ActivityResultContracts.StartActivityForResult()) { transport.start() }
    val actions = SetupActions(
        turnOnBluetooth = {
            // Android's own "allow to turn on Bluetooth?"; without the
            // nearby-devices access it may not ask, and its settings open.
            try {
                enableBluetooth.launch(android.content.Intent(android.bluetooth.BluetoothAdapter.ACTION_REQUEST_ENABLE))
            } catch (e: Exception) {
                openSettings(context, android.provider.Settings.ACTION_BLUETOOTH_SETTINGS)
            }
        },
        openAppSettings = {
            openSettings(context, android.provider.Settings.ACTION_APPLICATION_DETAILS_SETTINGS, android.net.Uri.fromParts("package", context.packageName, null))
        },
        openLocationSettings = { openSettings(context, android.provider.Settings.ACTION_LOCATION_SOURCE_SETTINGS) },
        tryAgain = { transport.start() },
    )
    DisposableEffect(Unit) {
        // A change of language is shown here in place: recreating the
        // Activity would drop the link and the page with what was typed in
        // it (LanguageSettings.holdInPlace).
        val release = LanguageSettings.holdInPlace()
        onDispose {
            release()
            page.view = null
            kotlinx.coroutines.CoroutineScope(kotlinx.coroutines.Dispatchers.Main).launch { transport.releaseAndStop() }
        }
    }
    // The page follows the app's language (the top bar's language menu).
    val code by LanguageSettings.wire.collectAsState()
    var pageCode by remember { mutableStateOf(code) }
    LaunchedEffect(code) {
        if (code != pageCode) {
            pageCode = code
            page.appLanguageChanged(code)
        }
    }
    val waitingLang by page.waitingLang.collectAsState()
    val installStarted by transport.installStarted.collectAsState()
    // An older wizard's page, reloaded in the app's language once that
    // loses nothing: the device installing and the link up.
    LaunchedEffect(waitingLang, installStarted, phase is BLESetupTransport.Phase.Ready) { page.reloadWhenSafe() }
    // The page's profile-picture field (issue #178): a WebView opens no
    // file picker on its own, so the chooser request comes here.
    val pendingFile = remember { arrayOfNulls<ValueCallback<Array<Uri>>>(1) }
    val pickImage = rememberLauncherForActivityResult(ActivityResultContracts.GetContent()) { uri ->
        pendingFile[0]?.onReceiveValue(if (uri != null) arrayOf(uri) else null)
        pendingFile[0] = null
    }

    // Leaving while the device installs asks first: the install goes on,
    // and this screen is where it is followed (as iOS).
    var confirmLeave by remember { mutableStateOf(false) }
    fun leave() { if (setupLeaveAsks(installStarted, readyDomain)) confirmLeave = true else onClose() }
    // The system Back: a step back in the wizard, where its page has a
    // Back of its own; otherwise out of the setup. It used to leave the app
    // in the middle of an install.
    BackHandler {
        val view = page.view
        if (everReady && view != null && page.loaded) {
            view.evaluateJavascript(WIZARD_BACK) { result -> if (result != "true") leave() }
        } else leave()
    }
    if (confirmLeave) {
        AlertDialog(
            onDismissRequest = { confirmLeave = false },
            title = { Text(S.appShellSetupLeaveTitle().resolve()) },
            text = { Text(S.appShellSetupLeaveText().resolve()) },
            confirmButton = { TextButton(onClick = { confirmLeave = false; onClose() }) { Text(S.appShellSetupLeave().resolve(), color = Color(0xFFE53935)) } },
            dismissButton = { TextButton(onClick = { confirmLeave = false }) { Text(S.commonCancel().resolve()) } },
        )
    }

    Scaffold(topBar = {
        TopAppBar(
            title = {
                // On two lines, and smaller if need be, rather than cut short
                // beside the language menu ("Configurar dispositivo nuevo").
                val style = LocalTextStyle.current
                BasicText(
                    S.appShellSetupNewDevice().resolve(),
                    style = style.merge(color = LocalContentColor.current),
                    maxLines = 2, overflow = TextOverflow.Ellipsis,
                    autoSize = TextAutoSize.StepBased(minFontSize = style.fontSize * 0.7f, maxFontSize = style.fontSize, stepSize = 0.5.sp),
                    modifier = Modifier.semantics { heading() },
                )
            },
            navigationIcon = { IconButton(onClick = ::leave) { Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = S.commonBack().resolve()) } },
            // The wizard's own language list is hidden in the app: this one
            // counts, and the device set up gets it at the first sign-in.
            actions = { LanguageMenu() },
        )
    }) { pad ->
        // Over the keyboard (the window is edge to edge): the password
        // field and "Open my device" stayed under it.
        Column(Modifier.padding(pad).consumeWindowInsets(pad).fillMaxSize().imePadding()) {
            if (everReady) {
                if (phase !is BLESetupTransport.Phase.Ready) {
                    // The page stays; its 3-second state poll picks up
                    // where it left off once the link is back.
                    Surface(color = MaterialTheme.colorScheme.tertiaryContainer, modifier = Modifier.fillMaxWidth()) {
                        Text(
                            S.appShellBleLinkLost().resolve(),
                            style = MaterialTheme.typography.bodySmall, modifier = Modifier.padding(10.dp),
                        )
                    }
                }
                val waiting = waitingLang
                if (waiting != null && !installStarted) {
                    // The app speaks another language now; an older
                    // wizard's page follows once it loses nothing by it.
                    Text(
                        S.androidSetupLanguageLater(LanguageSettings.nameOf(waiting)).resolve(),
                        style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                        modifier = Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 8.dp),
                    )
                }
                // The install takes about 20 minutes: keepScreenOn stops a
                // locked phone from dropping the Bluetooth link.
                AndroidView(factory = { makeWebView(it, transport, page, scope) { cb ->
                    pendingFile[0]?.onReceiveValue(null)
                    pendingFile[0] = cb
                    pickImage.launch("image/*")
                }.apply { keepScreenOn = true } }, modifier = Modifier.weight(1f).fillMaxWidth())
            } else {
                Waiting(phase, transport, locationOn, actions, Modifier.weight(1f))
            }
            readyDomain?.let { domain ->
                Surface(tonalElevation = 3.dp, modifier = Modifier.fillMaxWidth()) {
                    Column(Modifier.padding(16.dp), horizontalAlignment = Alignment.CenterHorizontally) {
                        Text(
                            (if (domain.isEmpty()) S.appShellSetupReadyHome() else S.appShellSetupReadyAs(domain)).resolve(),
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
                            Text(S.appShellSetupEnterPassword().resolve(), style = MaterialTheme.typography.titleSmall, modifier = Modifier.fillMaxWidth())
                            Spacer(Modifier.height(6.dp))
                            cloud.offthe.otc.ui.common.OTCTextField(
                                value = password, onValueChange = { password = it }, singleLine = true,
                                placeholder = { Text(S.commonPassword().resolve()) },
                                visualTransformation = androidx.compose.ui.text.input.PasswordVisualTransformation(),
                                keyboardOptions = androidx.compose.foundation.text.KeyboardOptions(keyboardType = androidx.compose.ui.text.input.KeyboardType.Password),
                                modifier = Modifier.fillMaxWidth(),
                            )
                        } else {
                            Text(S.appShellSetupSigningIn().resolve(), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.fillMaxWidth())
                        }
                        val valid = chosenPassword.isNotEmpty() || password.isNotEmpty()
                        Spacer(Modifier.height(8.dp))
                        Button(onClick = { onUseDevice(endpointForDomain(domain), chosenPassword.ifEmpty { password }) }, enabled = valid, modifier = Modifier.fillMaxWidth()) {
                            Text(S.appShellSetupOpenDevice().resolve())
                        }
                    }
                }
            }
        }
    }
}

/** What the waiting screen's buttons do. */
private class SetupActions(
    val turnOnBluetooth: () -> Unit,
    val openAppSettings: () -> Unit,
    val openLocationSettings: () -> Unit,
    val tryAgain: () -> Unit,
)

@Composable
private fun Waiting(phase: BLESetupTransport.Phase, transport: BLESetupTransport, locationOn: Boolean, actions: SetupActions, modifier: Modifier) {
    if (phase is BLESetupTransport.Phase.Choosing) {
        val found by transport.found.collectAsState()
        Column(modifier.fillMaxWidth().verticalScroll(rememberScrollState()).padding(24.dp), horizontalAlignment = Alignment.CenterHorizontally) {
            Spacer(Modifier.height(24.dp))
            Text(S.appShellSetupWhich().resolve(), style = MaterialTheme.typography.titleMedium)
            Spacer(Modifier.height(6.dp))
            Text(
                S.appShellSetupSeveral().resolve(),
                style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, textAlign = TextAlign.Center,
            )
            Spacer(Modifier.height(16.dp))
            for (dev in found.sortedByDescending { it.rssi }) {
                androidx.compose.material3.OutlinedButton(onClick = { transport.choose(dev.address) }, modifier = Modifier.fillMaxWidth().padding(vertical = 4.dp)) {
                    Text((if (dev.shortId.isEmpty()) S.appShellSetupDeviceOlder() else S.appShellSetupDevice(dev.shortId)).resolve(), modifier = Modifier.weight(1f))
                    Text(signal(dev.rssi).resolve(), color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
            }
        }
        return
    }
    // Half a minute of looking with nothing found says what to check.
    var notFound by remember { mutableStateOf(false) }
    LaunchedEffect(phase) {
        notFound = false
        if (phase is BLESetupTransport.Phase.Scanning) {
            kotlinx.coroutines.delay(SETUP_NOT_FOUND_MS)
            notFound = true
        }
    }
    val looking = phase is BLESetupTransport.Phase.Scanning || phase is BLESetupTransport.Phase.Starting || phase is BLESetupTransport.Phase.Lost
    // Android 11 and older find nothing over Bluetooth with Location off.
    val locationHint = looking && !locationOn && android.os.Build.VERSION.SDK_INT < android.os.Build.VERSION_CODES.S
    Column(modifier.fillMaxWidth().verticalScroll(rememberScrollState()).padding(24.dp), horizontalAlignment = Alignment.CenterHorizontally) {
        Spacer(Modifier.height(48.dp))
        val (title, hint) = when (phase) {
            is BLESetupTransport.Phase.Connecting -> S.commonConnecting() to null
            is BLESetupTransport.Phase.Off -> S.appShellBluetoothOff() to S.androidBluetoothOffHint()
            is BLESetupTransport.Phase.Unauthorized -> S.appShellBluetoothNeeded() to S.androidBluetoothNeededHint()
            is BLESetupTransport.Phase.ScanFailed -> S.androidBluetoothScanFailed() to null
            else -> S.appShellSetupLooking() to if (locationHint) S.androidLocationOffHint() else if (notFound) S.appShellSetupNotFound() else S.appShellSetupPowerOn()
        }
        val stuck = phase is BLESetupTransport.Phase.Off || phase is BLESetupTransport.Phase.Unauthorized || phase is BLESetupTransport.Phase.ScanFailed
        if (stuck) Spacer(Modifier.height(8.dp)) else CircularProgressIndicator()
        Spacer(Modifier.height(14.dp))
        Text(title.resolve(), style = MaterialTheme.typography.titleMedium, textAlign = TextAlign.Center, modifier = Modifier.semantics { liveRegion = LiveRegionMode.Polite })
        if (hint != null) {
            Spacer(Modifier.height(6.dp))
            Text(hint.resolve(), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, textAlign = TextAlign.Center)
        }
        // A way out of each: Bluetooth on, the app's access, Location on, or once more.
        val action: Pair<UiText, () -> Unit>? = when {
            phase is BLESetupTransport.Phase.Off -> S.androidBluetoothTurnOn() to actions.turnOnBluetooth
            phase is BLESetupTransport.Phase.Unauthorized -> S.androidOpenSettings() to actions.openAppSettings
            phase is BLESetupTransport.Phase.ScanFailed -> S.commonTryAgain() to actions.tryAgain
            locationHint -> S.androidOpenSettings() to actions.openLocationSettings
            else -> null
        }
        if (action != null) {
            Spacer(Modifier.height(16.dp))
            Button(onClick = action.second) { Text(action.first.resolve()) }
        }
        Spacer(Modifier.height(48.dp))
    }
}

/** How long the phone looks without finding a device before the waiting screen says what to check. */
internal const val SETUP_NOT_FOUND_MS = 30_000L

/**
 * Leaving the setup asks first while the device installs ([installStarted]
 * from its /api/state, not yet online: [readyDomain] null) - the install
 * goes on without the phone, and this screen is where it is followed.
 */
internal fun setupLeaveAsks(installStarted: Boolean, readyDomain: String?): Boolean = installStarted && readyDomain == null

/** The wizard's own Back, where its page shows one: clicked, and true; false when there is none. */
private const val WIZARD_BACK = "(function(){var b=document.getElementById('back')||document.getElementById('v-back');" +
    "if(b&&!b.disabled&&b.offsetParent!==null){b.click();return true}return false})()"

/** Location on (below Android 12 a Bluetooth scan needs it), as far as the phone says; on when it can't tell. */
private fun setupLocationOn(context: Context): Boolean {
    val lm = context.getSystemService(Context.LOCATION_SERVICE) as? android.location.LocationManager ?: return true
    return runCatching { androidx.core.location.LocationManagerCompat.isLocationEnabled(lm) }.getOrDefault(true)
}

private fun openSettings(context: Context, action: String, data: android.net.Uri? = null) {
    try {
        context.startActivity(android.content.Intent(action).apply { if (data != null) setData(data) }.addFlags(android.content.Intent.FLAG_ACTIVITY_NEW_TASK))
    } catch (_: Exception) {
        try { context.startActivity(android.content.Intent(android.provider.Settings.ACTION_SETTINGS).addFlags(android.content.Intent.FLAG_ACTIVITY_NEW_TASK)) } catch (_: Exception) {}
    }
}

private fun signal(rssi: Int): UiText = when {
    rssi >= -60 -> S.androidSignalStrong()
    rssi >= -75 -> S.androidSignalGood()
    else -> S.androidSignalWeak()
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

/**
 * The wizard speaks the app's language (docs/i18n.md, "Setup wizard"): its
 * page is loaded with ?lang=<code>, and every /api call carries it too,
 * since only the path travels over Bluetooth - no header. The language in
 * effect when the call is made; a path that already has one keeps it. The
 * page itself keeps the language it was loaded in. As BLESetupLang in
 * BluetoothSetupView.swift.
 */
internal fun withLang(path: String, code: String = LanguageSettings.wireCode): String {
    if (!(path == "/api" || path.startsWith("/api/") || path.startsWith("/api?"))) return path
    val query = path.substringAfter('?', "")
    if (query.split('&').any { it.startsWith("lang=") }) return path
    return path + (if ('?' in path) "&" else "?") + "lang=" + URLEncoder.encode(code, "UTF-8")
}

/**
 * The wizard's page and the app's language (docs/i18n.md, "Setup wizard"),
 * as BLESetupTransport.appLanguageChanged in BluetoothSetupView.swift. On
 * the main thread.
 */
internal class SetupPage(private val transport: BLESetupTransport) {
    /** The web view showing the page, to ask it to re-word itself. */
    var view: WebView? = null

    /** The page has finished loading (its script runs), not just been asked for. */
    var loaded = false

    /** The language of the page on screen: its address's lang=. */
    var lang: String? = null

    private val _waitingLang = MutableStateFlow<String?>(null)

    /** The app's language, waiting for the moment an older wizard's page can be reloaded in it; null when nothing waits. */
    val waitingLang: StateFlow<String?> = _waitingLang

    /**
     * The app's language changed while the wizard's page is up. A page not
     * loaded yet holds nothing typed: it loads again in the new language. A
     * loaded page is asked to re-word itself (setup_wizard.py's
     * window.otcSetupLang): it reloads on the same step, keeping what was
     * entered there but passwords, and asks first when some are typed. An
     * older image's wizard has no such call and keeps what was typed or
     * picked before the install only in the page: it is reloaded once the
     * device is installing, when it shows only what the device reports and
     * a reload loses nothing (a line over it says so until then). With no
     * page up yet, the page loads in the language of that moment.
     */
    fun appLanguageChanged(code: String) {
        _waitingLang.value = null
        val view = view ?: return
        if (!loaded) {
            view.loadUrl(BLESetupLang.pageUrl(code))
            return
        }
        view.evaluateJavascript(BLESetupLang.switchScript(code)) { result ->
            // Changed again meanwhile: the newer one counts.
            if (LanguageSettings.wireCode != code) return@evaluateJavascript
            // The page goes to it by itself, or is in it already.
            if (BLESetupLang.pageSwitches(result)) return@evaluateJavascript
            if (code == lang) return@evaluateJavascript
            _waitingLang.value = code
            reloadWhenSafe()
        }
    }

    /** The language an older wizard's page waits for, now if a reload loses nothing: the device is installing and the link is up. */
    fun reloadWhenSafe() {
        val code = _waitingLang.value ?: return
        val view = view ?: return
        if (!transport.installStarted.value || !transport.isReady) return
        _waitingLang.value = null
        view.loadUrl(BLESetupLang.pageUrl(code))
    }
}

/** The setup page's language, as BLESetupLang in BluetoothSetupView.swift. */
internal object BLESetupLang {
    /** The wizard's page in [code], as the web view loads it (shouldInterceptRequest serves it). */
    fun pageUrl(code: String): String = "https://device/?lang=" + URLEncoder.encode(code, "UTF-8")

    /**
     * What asks an open page to re-word itself in [code]: the wizard's
     * window.otcSetupLang, which answers true or false; an older image's
     * wizard has none. [quote] makes the code a JavaScript string
     * (JSONObject.quote; JVM tests, which have no org.json, stand in for it).
     */
    fun switchScript(code: String, quote: (String) -> String = { JSONObject.quote(it) }): String =
        "window.otcSetupLang&&window.otcSetupLang(" + quote(code) + ")"

    /**
     * Whether the page answered [switchScript] with a boolean, which
     * evaluateJavascript hands over as JSON: "true" (it goes to the
     * language) or "false" (nothing to do). Anything else - "null" for
     * undefined or null, a string, nothing - is a wizard without the call.
     */
    fun pageSwitches(result: String?): Boolean = result == "true" || result == "false"

    /**
     * From the wizard's /api/state: the device is installing (or has
     * finished, or failed), or was installed already - its page then shows
     * only the device's state, and a reload loses nothing typed.
     */
    fun installStarted(alreadyInstalled: Boolean, phase: String?): Boolean = alreadyInstalled || (phase ?: "idle") != "idle"

    /** The language a page was loaded in: its address's lang=. */
    fun lang(url: String?): String? {
        val query = url?.substringBefore('#')?.substringAfter('?', "") ?: return null
        val item = query.split('&').firstOrNull { it.startsWith("lang=") } ?: return null
        return URLDecoder.decode(item.removePrefix("lang="), "UTF-8")
    }
}

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
                JSONObject().put("ok", false).put("error", e.message ?: S.appShellSetupSigninUnfinished().now(webView.context))
            }
            webView.evaluateJavascript("window.__otcAnswer($id, ${JSONObject.quote(reply.toString())})", null)
        }
    }

    @JavascriptInterface
    fun request(id: Int, method: String, url: String, body: String) {
        scope.launch(Dispatchers.IO) {
            val reply = try {
                val a = transport.request(method, withLang(pathOf(url)), body.ifEmpty { null })
                JSONObject().put("s", a.status).put("t", a.contentType).put("b", a.body)
            } catch (e: Exception) {
                JSONObject().put("s", 503).put("t", "application/json").put("b", JSONObject().put("error", e.message ?: "failed").toString()) // i18n-ignore: an exception without a message; ours all have one
            }
            withContext(Dispatchers.Main) { webView.evaluateJavascript("window.__otcAnswer($id, ${JSONObject.quote(reply.toString())})", null) }
        }
    }
}

@SuppressLint("SetJavaScriptEnabled")
private fun makeWebView(
    context: Context, transport: BLESetupTransport, page: SetupPage, scope: CoroutineScope,
    chooseFile: (ValueCallback<Array<Uri>>) -> Unit,
): WebView = WebView(context).apply {
    page.view = this
    page.loaded = false
    settings.javaScriptEnabled = true
    settings.domStorageEnabled = true
    setBackgroundColor(0xFF1E1F22.toInt()) // the wizard's own background
    addJavascriptInterface(Bridge(this, transport, scope), "OTCSetup")
    webChromeClient = object : WebChromeClient() {
        override fun onShowFileChooser(view: WebView, callback: ValueCallback<Array<Uri>>, params: FileChooserParams): Boolean {
            chooseFile(callback)
            return true
        }
    }
    webViewClient = object : WebViewClient() {
        // Told when the page has loaded, and in which language
        // (SetupPage.appLanguageChanged).
        override fun onPageStarted(view: WebView, url: String?, favicon: android.graphics.Bitmap?) {
            page.loaded = false
        }

        override fun onPageFinished(view: WebView, url: String?) {
            page.loaded = true
            page.lang = BLESetupLang.lang(url)
        }

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
            val path = withLang(pathOf(request.url.toString()))
            val a = try {
                runBlocking { transport.request(request.method, path, null) }
            } catch (e: Exception) {
                BLESetupTransport.Answer(503, "application/json", JSONObject().put("error", e.message ?: "failed").toString()) // i18n-ignore: an exception without a message; ours all have one
            }
            var body = a.body
            if (a.contentType.startsWith("text/html")) body = body.replaceFirst("<head>", "<head>$FETCH_OVERRIDE")
            val mime = a.contentType.substringBefore(';').trim()
            return WebResourceResponse(mime, "utf-8", a.status, reason(a.status), mapOf("Cache-Control" to "no-store"), ByteArrayInputStream(body.toByteArray()))
        }
    }
    // In the app's language now; a change later is SetupPage.appLanguageChanged.
    loadUrl(BLESetupLang.pageUrl(LanguageSettings.wireCode))
}

private fun reason(status: Int): String = when (status) {
    200 -> "OK"; 202 -> "Accepted"; 400 -> "Bad Request"; 404 -> "Not Found"; 409 -> "Conflict"; 503 -> "Service Unavailable" // i18n-ignore: HTTP reason phrases, protocol values
    else -> "Status $status"
}
