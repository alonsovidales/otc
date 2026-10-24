// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.sync

import android.content.SharedPreferences
import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.sync.PhotoFolders.Folder
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.text.Collator
import java.util.Locale

/** Settings > Sync Options > Folders: the folders listed, the queries' clauses, the text and the stored sets. */
class PhotoFoldersTest {
    private val primary = "external_primary"
    private val sd = "1234-abcd"
    private val english = Collator.getInstance(Locale.ENGLISH)

    private fun row(id: Long?, name: String? = "Camera", path: String? = "DCIM/Camera/", volume: String? = primary, video: Boolean = false) =
        PhotoFolders.Row(id, name, path, volume, video)

    private fun group(includeVideos: Boolean, vararg rows: PhotoFolders.Row) = PhotoFolders.group(rows.asSequence(), includeVideos, english)

    @Test fun countsEachTypePerFolder() {
        val folders = group(true, row(1), row(1), row(1, video = true), row(2, "Screenshots", "Pictures/Screenshots/"))
        assertEquals(
            listOf(Folder("1", "Camera", "DCIM/Camera", primary, 2, 1), Folder("2", "Screenshots", "Pictures/Screenshots", primary, 1, 0)),
            folders,
        )
    }

    /** Include videos off: videos aren't counted, and a folder of videos alone has nothing to back up. */
    @Test fun videosOnlyWhileTheyAreSynced() {
        val rows = arrayOf(row(1), row(1, video = true), row(3, "Movies", "Movies/", video = true))
        assertEquals(listOf(Folder("1", "Camera", "DCIM/Camera", primary, 1, 0)), group(false, *rows))
        assertEquals(listOf("1", "3"), group(true, *rows).map { it.id })
    }

    /** A row with no BUCKET_ID is no folder's: nothing can turn it off, so it isn't listed. */
    @Test fun rowsWithoutAFolderAreLeftOut() {
        assertEquals(listOf("5"), group(true, row(null), row(5)).map { it.id })
        assertTrue(group(true, row(null)).isEmpty())
    }

    @Test fun nameAndPathFromTheFirstRowThatHasThem() {
        val f = group(true, row(8, name = "", path = null, volume = null), row(8, "WhatsApp Images", "Android/media/com.whatsapp/WhatsApp/Media/WhatsApp Images/", primary)).single()
        assertEquals(Folder("8", "WhatsApp Images", "Android/media/com.whatsapp/WhatsApp/Media/WhatsApp Images", primary, 2, 0), f)
        // No name at all: the end of its path.
        assertEquals("Trip 2024", group(true, row(7, name = null, path = "Pictures/Trip 2024/")).single().name)
        // Files at the top of a storage: no path.
        assertEquals("", group(true, row(9, name = "0", path = "/")).single().path)
    }

    /** By name as people read it (accents and case don't put a name last), then by path, then storage. */
    @Test fun sortedByNameThenPath() {
        val folders = group(
            true,
            row(1, "Screenshots", "Pictures/Screenshots/"),
            row(2, "Ålbum", "Pictures/Ålbum/"),
            row(3, "Camera", "DCIM/Camera/", primary),
            row(4, "Camera", "DCIM/Camera/", sd),
            row(5, "Camera", "Backup/Camera/", primary),
            row(6, "apple", "Pictures/apple/"),
        )
        assertEquals(listOf("2", "6", "5", "4", "3", "1"), folders.map { it.id })
    }

    @Test fun skipClauseInlinesNumbersOnly() {
        assertEquals("(bucket_id IS NULL OR bucket_id NOT IN (12,-5))", PhotoFolders.skipClause(setOf("12", "-5", "x")))
        assertEquals("(bucket_id IS NULL OR bucket_id NOT IN (12))", PhotoFolders.skipClause(setOf("12", "012")))
        // Nothing turned off, or nothing that is a folder: no clause, the query as before.
        assertNull(PhotoFolders.skipClause(emptySet()))
        assertNull(PhotoFolders.skipClause(setOf("1) OR (1=1", "", "*")))
    }

    @Test fun onlyClauseNamesTheFolders() {
        assertEquals("bucket_id IN (7,-8)", PhotoFolders.onlyClause(setOf("7", PhotoSync.CATCH_UP, "-8")))
        assertNull(PhotoFolders.onlyClause(setOf(PhotoSync.CATCH_UP)))
        assertNull(PhotoFolders.onlyClause(emptySet()))
    }

    @Test fun bucketIdsAreTheNumbers() {
        assertEquals(setOf(12L, -5L), PhotoFolders.bucketIds(setOf("12", "-5", "x", "")))
    }

    @Test fun countLineInEnglish() {
        val f = Folder("1", "Camera", "DCIM/Camera", primary, 1234, 56)
        assertEquals("1,234 photos · 56 videos", PhotoFolders.countLine(f, includeVideos = true).english())
        assertEquals("1,234 photos", PhotoFolders.countLine(f, includeVideos = false).english())
        assertEquals("1 photo · 1 video", PhotoFolders.countLine(f.copy(photos = 1, videos = 1), includeVideos = true).english())
        assertEquals("1 photo", PhotoFolders.countLine(f.copy(photos = 1, videos = 0), includeVideos = true).english())
        assertEquals("2 videos", PhotoFolders.countLine(f.copy(photos = 0, videos = 2), includeVideos = true).english())
        assertEquals("1 video", PhotoFolders.countLine(f.copy(photos = 0, videos = 1), includeVideos = true).english())
    }

    @Test fun rowValueCountsTheFoldersThereNow() {
        val folders = listOf(Folder("1", "Camera", "DCIM/Camera", primary, 3, 0), Folder("2", "Screenshots", "Pictures/Screenshots", primary, 1, 0), Folder("3", "WhatsApp Images", "WhatsApp", primary, 9, 0))
        assertEquals("All", PhotoFolders.rowValue(folders, emptySet()).english())
        // A folder turned off that is gone isn't counted (and stays off).
        assertEquals("All", PhotoFolders.rowValue(folders, setOf("99")).english())
        assertEquals("1 off", PhotoFolders.rowValue(folders, setOf("2", "99")).english())
        assertEquals("2 off", PhotoFolders.rowValue(folders, setOf("1", "3")).english())
    }

    @Test fun labelsSayWhereAFolderIs() {
        val describe = { v: String -> if (v == sd) "SD card" else "Internal storage" }
        val camera = Folder("1", "Camera", "DCIM/Camera", primary, 1, 0)
        val (name, where) = PhotoFolders.labels(camera, describe)
        assertEquals("Camera", name)
        assertEquals("DCIM/Camera", where!!.english())
        // Another storage, named.
        val (sdName, sdWhere) = PhotoFolders.labels(camera.copy(volume = sd), describe)
        assertEquals("Camera", sdName)
        assertEquals("SD card · ⁨DCIM/Camera⁩", sdWhere!!.english())
        // The top of a storage: the storage's own name, no line.
        assertEquals("SD card" to null, PhotoFolders.labels(camera.copy(name = "0", path = "", volume = sd), describe))
    }

    /** The skip list and the one-off passes as kept: none until there is one, and a copy when read. */
    @Test fun setsAreStoredOnlyWhenNotEmpty() {
        val p = MemoryPrefs()
        assertTrue(PhotoFolders.storedSet(p, "skippedFolders").isEmpty())
        PhotoFolders.storeSet(p.edit(), "skippedFolders", emptySet()).apply()
        assertFalse(p.contains("skippedFolders"))
        PhotoFolders.storeSet(p.edit(), "skippedFolders", setOf("12", "-5")).apply()
        val read = PhotoFolders.storedSet(p, "skippedFolders")
        assertEquals(setOf("12", "-5"), read)
        // Changing what was read leaves the stored one alone.
        (read as MutableSet<String>).add("7")
        assertEquals(setOf("12", "-5"), p.getStringSet("skippedFolders", null))
        // Turned back on, the last one: no key again.
        PhotoFolders.storeSet(p.edit(), "skippedFolders", emptySet()).apply()
        assertFalse(p.contains("skippedFolders"))
    }
}

/** SharedPreferences in memory, handing back the very sets it holds, as Android's do. */
class MemoryPrefs : SharedPreferences {
    private val values = HashMap<String, Any?>()

    override fun getAll(): Map<String, *> = values.toMap()
    override fun getString(key: String, defValue: String?): String? = values[key] as String? ?: defValue
    @Suppress("UNCHECKED_CAST")
    override fun getStringSet(key: String, defValues: Set<String>?): Set<String>? = values[key] as Set<String>? ?: defValues
    override fun getInt(key: String, defValue: Int): Int = values[key] as Int? ?: defValue
    override fun getLong(key: String, defValue: Long): Long = values[key] as Long? ?: defValue
    override fun getFloat(key: String, defValue: Float): Float = values[key] as Float? ?: defValue
    override fun getBoolean(key: String, defValue: Boolean): Boolean = values[key] as Boolean? ?: defValue
    override fun contains(key: String): Boolean = key in values
    override fun registerOnSharedPreferenceChangeListener(listener: SharedPreferences.OnSharedPreferenceChangeListener) {}
    override fun unregisterOnSharedPreferenceChangeListener(listener: SharedPreferences.OnSharedPreferenceChangeListener) {}

    override fun edit(): SharedPreferences.Editor = object : SharedPreferences.Editor {
        private val changes = HashMap<String, Any?>()
        private val removed = HashSet<String>()
        private var clear = false
        private fun put(key: String, value: Any?): SharedPreferences.Editor { changes[key] = value; return this }
        override fun putString(key: String, value: String?) = put(key, value)
        // A copy, as Android keeps.
        override fun putStringSet(key: String, values: Set<String>?) = put(key, values?.let { HashSet(it) })
        override fun putInt(key: String, value: Int) = put(key, value)
        override fun putLong(key: String, value: Long) = put(key, value)
        override fun putFloat(key: String, value: Float) = put(key, value)
        override fun putBoolean(key: String, value: Boolean) = put(key, value)
        override fun remove(key: String): SharedPreferences.Editor { removed += key; return this }
        override fun clear(): SharedPreferences.Editor { clear = true; return this }
        override fun commit(): Boolean { apply(); return true }
        override fun apply() {
            if (clear) values.clear()
            for (k in removed) values.remove(k)
            values.putAll(changes)
        }
    }
}
