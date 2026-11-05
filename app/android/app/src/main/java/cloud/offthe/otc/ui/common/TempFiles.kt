// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import java.io.File
import java.util.UUID

/**
 * The app's temporary copies, in folders of their own under the cache
 * (cacheDir/tmp/<kind>/), as iOS's TempFiles.swift: the viewer's whole-file
 * video, Files' opened files, Share's picture, the composer's trims, the
 * feed's fallback videos and the setup page's picture made a JPEG (SetupPicture). Each is deleted when done with; what a process
 * ended before that left is removed at the next start ([sweepAtStart]).
 * They used to sit at the cache's top level for good - a 2 GB video played
 * three times without a stream URL left 6 GB.
 */
object TempFiles {
    enum class Kind(val dir: String) { VIEWER("viewer"), PREVIEW("preview"), SHARE("share"), TRIM("trim"), FEED("feed"), SETUP("setup") }

    private const val ROOT = "tmp"
    private const val OLD = "tmp-old-"

    /**
     * What the cache's top level keeps across starts; every other plain file
     * there is a copy older versions left (the viewer's "<uuid>.mp4", Files'
     * "<name>", Share's "<uuid>.jpg", "save-…", "otc-trim-…", the logs
     * shared). Folders (the thumbnails', the full-size photos', the web
     * view's) are never touched.
     */
    val keptAtTop = setOf("social_feed_cache.pb")

    fun dir(cacheDir: File, kind: Kind): File = File(File(cacheDir, ROOT), kind.dir).also { it.mkdirs() }

    /** A new file of [kind] ending in [ext]. */
    fun file(cacheDir: File, kind: Kind, ext: String): File = File(dir(cacheDir, kind), "${UUID.randomUUID()}.${ext.ifEmpty { "bin" }}")

    /** A new file of [kind] called [name], in a folder of its own: the app it opens in shows the name. */
    fun named(cacheDir: File, kind: Kind, name: String): File =
        File(File(dir(cacheDir, kind), UUID.randomUUID().toString()).also { it.mkdirs() }, name.ifEmpty { "file" })

    /** Every copy of [kind]: one at a time for what is handed to another app (Share, Files' open). */
    fun clear(cacheDir: File, kind: Kind) {
        File(File(cacheDir, ROOT), kind.dir).deleteRecursively()
    }

    /** Deletes [f], and the folder of its own [named] made for it. */
    fun delete(f: File?) {
        f ?: return
        f.delete()
        val parent = f.parentFile ?: return
        if (Kind.entries.none { it.dir == parent.name } && parent.parentFile?.let { p -> Kind.entries.any { it.dir == p.name } } == true) parent.delete()
    }

    /**
     * Process start: nothing of this process is in [cacheDir]'s copies yet.
     * The copies' folder is moved aside at once (a rename: fast, so this
     * start's copies land in a new one), then it and what older versions
     * left at the top level are deleted on [background].
     */
    fun sweepAtStart(cacheDir: File, background: (() -> Unit) -> Unit) {
        val root = File(cacheDir, ROOT)
        if (root.exists()) root.renameTo(File(cacheDir, OLD + System.nanoTime()))
        background { sweep(cacheDir) }
    }

    /** What [sweepAtStart] deletes: the moved-aside folders, and the top level's leftovers (tests). */
    internal fun sweep(cacheDir: File) {
        cacheDir.listFiles()?.forEach { f ->
            when {
                f.isDirectory && f.name.startsWith(OLD) -> f.deleteRecursively()
                f.isFile && f.name !in keptAtTop -> f.delete()
            }
        }
    }
}
