// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import android.content.ActivityNotFoundException
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Handler
import android.os.Looper
import android.webkit.MimeTypeMap
import androidx.core.content.FileProvider
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.androidNoAppForLink
import java.io.File

// The Android counterparts of iOS's ActivityView (share sheet), QuickLook
// (preview) and UIApplication.open(url).
object Share {
    fun link(context: Context, url: String) {
        val i = Intent(Intent.ACTION_SEND).apply { type = "text/plain"; putExtra(Intent.EXTRA_TEXT, url) }
        context.startActivity(Intent.createChooser(i, null).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
    }

    /**
     * Opens [url] in the app that handles it. A phone with none (no browser
     * on a work or child profile, no maps app on a phone without Google's)
     * says so in a toast: startActivity threw, and took the app down.
     * false then.
     */
    fun openInBrowser(context: Context, url: String): Boolean {
        if (tryOpen(context, url)) return true
        noApp(context)
        return false
    }

    /**
     * A place on a map: the phone's maps app (geo:), else OpenStreetMap in
     * the browser ([mapLinks]); neither, a toast.
     */
    fun openMap(context: Context, latitude: Double, longitude: Double) {
        if (mapLinks(latitude, longitude).any { tryOpen(context, it) }) return
        noApp(context)
    }

    /** Pure: where a place opens, in order - the maps app, then OpenStreetMap's page. */
    fun mapLinks(latitude: Double, longitude: Double): List<String> = listOf(
        "geo:$latitude,$longitude?q=$latitude,$longitude",
        "https://www.openstreetmap.org/?mlat=$latitude&mlon=$longitude#map=16/$latitude/$longitude",
    )

    private fun tryOpen(context: Context, url: String): Boolean = try {
        context.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(url)).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
        true
    } catch (e: ActivityNotFoundException) {
        false
    } catch (e: SecurityException) {
        false
    }

    // From any thread: a toast needs the main one.
    private fun noApp(context: Context) {
        val app = context.applicationContext
        Handler(Looper.getMainLooper()).post { android.widget.Toast.makeText(app, S.androidNoAppForLink().now(), android.widget.Toast.LENGTH_LONG).show() }
    }

    fun uriFor(context: Context, file: File): Uri =
        FileProvider.getUriForFile(context, "cloud.offthe.otc.fileprovider", file)

    fun file(context: Context, file: File, mime: String? = null) {
        val uri = uriFor(context, file)
        val i = Intent(Intent.ACTION_SEND).apply {
            type = mime ?: mimeOf(file.name)
            putExtra(Intent.EXTRA_STREAM, uri)
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        }
        context.startActivity(Intent.createChooser(i, null).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
    }

    /** Opens a fetched file in whatever app handles its type - the QuickLook counterpart. */
    fun preview(context: Context, file: File, mime: String? = null) {
        val uri = uriFor(context, file)
        val i = Intent(Intent.ACTION_VIEW).apply {
            setDataAndType(uri, mime ?: mimeOf(file.name))
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_ACTIVITY_NEW_TASK)
        }
        context.startActivity(Intent.createChooser(i, null).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
    }

    fun mimeOf(name: String): String {
        val ext = name.substringAfterLast('.', "").lowercase()
        return MimeTypeMap.getSingleton().getMimeTypeFromExtension(ext) ?: "application/octet-stream"
    }
}
