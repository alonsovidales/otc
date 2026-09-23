// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import android.content.Context
import android.content.Intent
import android.net.Uri
import android.webkit.MimeTypeMap
import androidx.core.content.FileProvider
import java.io.File

// The Android counterparts of iOS's ActivityView (share sheet), QuickLook
// (preview) and UIApplication.open(url).
object Share {
    fun link(context: Context, url: String) {
        val i = Intent(Intent.ACTION_SEND).apply { type = "text/plain"; putExtra(Intent.EXTRA_TEXT, url) }
        context.startActivity(Intent.createChooser(i, null).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
    }

    fun openInBrowser(context: Context, url: String) {
        context.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(url)).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
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
