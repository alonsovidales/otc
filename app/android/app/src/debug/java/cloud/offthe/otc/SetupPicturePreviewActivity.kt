// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc

import android.annotation.SuppressLint
import android.net.Uri
import android.os.Bundle
import android.util.Log
import android.webkit.ConsoleMessage
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebView
import androidx.activity.ComponentActivity
import androidx.activity.result.contract.ActivityResultContracts
import androidx.core.content.FileProvider
import androidx.lifecycle.lifecycleScope
import cloud.offthe.otc.ui.SetupPicture
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.io.File

/**
 * Debug builds only: the setup page's profile picture without a device. A
 * web view with the page's own steps (a file input, FileReader, an Image)
 * gets what Choose File picks through SetupPicture, as the Bluetooth setup
 * hands it over, and says - on screen and in logcat, tag
 * SetupPicturePreview - whether the page could open it.
 * adb shell am start -n cloud.offthe.otc/.SetupPicturePreviewActivity
 * [--es path <a file in the app's cache, instead of the picker>]
 * [--ez raw true: hand it over as picked, as before the fix].
 */
class SetupPicturePreviewActivity : ComponentActivity() {
    private var pending: ValueCallback<Array<Uri>>? = null
    private val pick = registerForActivityResult(ActivityResultContracts.GetContent()) { uri -> handOver(uri) }

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val web = WebView(this)
        web.settings.javaScriptEnabled = true
        web.webChromeClient = object : WebChromeClient() {
            override fun onShowFileChooser(view: WebView, callback: ValueCallback<Array<Uri>>, params: FileChooserParams): Boolean {
                Log.i("SetupPicturePreview", "file chooser asked for")
                pending = callback
                val path = intent.getStringExtra("path")
                if (path == null) {
                    pick.launch("image/*")
                } else {
                    // A file of the cache, granted as the picker's would be.
                    val src = File(File(cacheDir, "tmp/preview-src").also { it.mkdirs() }, File(path).name)
                    File(path).copyTo(src, overwrite = true)
                    handOver(FileProvider.getUriForFile(this@SetupPicturePreviewActivity, "cloud.offthe.otc.fileprovider", src))
                }
                return true
            }

            override fun onConsoleMessage(m: ConsoleMessage): Boolean {
                Log.i("SetupPicturePreview", m.message())
                return true
            }
        }
        web.loadDataWithBaseURL("https://device/", PAGE, "text/html", "utf-8", null)
        setContentView(web)
    }

    private fun handOver(uri: Uri?) {
        val callback = pending ?: return
        pending = null
        if (uri == null) return callback.onReceiveValue(null)
        lifecycleScope.launch {
            val shown = if (intent.getBooleanExtra("raw", false)) uri else withContext(Dispatchers.IO) { SetupPicture.forPage(this@SetupPicturePreviewActivity, uri) }
            Log.i("SetupPicturePreview", "picked ${contentResolver.getType(uri)}, handed over ${contentResolver.getType(shown)} $shown")
            callback.onReceiveValue(arrayOf(shown))
        }
    }

    private companion object {
        const val PAGE = """<!doctype html><meta name=viewport content="width=device-width"><body style="background:#1e1f22;color:#eee;font:20px sans-serif;padding:24px">
<input type=file id=f accept="image/*" style="font-size:24px"><p id=r>Pick a picture</p><img id=i style="max-width:100%">
<script>f.onclick=()=>console.log('SETUPPIC clicked');f.onchange=e=>{const x=e.target.files[0];if(!x)return;const im=new Image();
im.onload=()=>{r.textContent='opened '+im.width+'x'+im.height+' '+x.name+' '+x.type;i.src=im.src;console.log('SETUPPIC '+r.textContent)};
im.onerror=()=>{r.textContent='not opened: '+x.name+' '+x.type;console.log('SETUPPIC '+r.textContent)};
const fr=new FileReader();fr.onload=()=>{im.src=fr.result};fr.readAsDataURL(x)}</script>"""
    }
}
