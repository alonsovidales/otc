// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.wallpaper

import kotlin.math.max
import kotlin.math.min
import kotlin.math.roundToInt
import kotlin.random.Random

// A collection as the phone's wallpaper (the owner's request, 2026-10-28):
// the rules, free of Android, tested in WallpaperRulesTest.kt. Wallpaper.kt
// does the rest: the box's choices kept on the phone, the copies in the
// app's private storage, WorkManager's runs and WallpaperManager.

/** Where the collection's photos show: WallpaperManager's FLAG_LOCK (2), FLAG_SYSTEM (1), or both. */
enum class WallpaperTarget(val stored: String, val flags: Int) {
    LOCK("lock", 2), HOME("home", 1), BOTH("both", 3);

    /** The lock screen shows the photos: the notice says anyone who sees it sees them. */
    val showsOnLock: Boolean get() = this != HOME

    companion object {
        /** The box's first choice. */
        val DEFAULT = BOTH
        fun parse(s: String?): WallpaperTarget? = entries.firstOrNull { it.stored == s }
    }
}

/** How often the photo changes: a WorkManager periodic run (15 minutes is its shortest). */
enum class WallpaperInterval(val stored: String, val minutes: Long) {
    MIN_15("15m", 15), HOUR("1h", 60), DAY("1d", 24 * 60);

    companion object {
        /** The box's first choice. */
        val DEFAULT = HOUR
        fun parse(s: String?): WallpaperInterval? = entries.firstOrNull { it.stored == s }
    }
}

/**
 * The collection shown as wallpaper: its id and name (kept in step with a
 * rename), the box's two choices, and the device it belongs to (the
 * endpoint signed in to when it started) - its copies are that device's
 * photos, so signing in to another one stops it.
 */
data class WallpaperSettings(
    val groupId: String, val name: String, val target: WallpaperTarget, val interval: WallpaperInterval, val endpoint: String,
) {
    /** As kept on the phone: one value per key (otc_wallpaper's preferences). */
    fun toMap(): Map<String, String> = mapOf(
        KEY_GROUP to groupId, KEY_NAME to name, KEY_TARGET to target.stored, KEY_INTERVAL to interval.stored, KEY_ENDPOINT to endpoint,
    )

    companion object {
        const val KEY_GROUP = "group_id"
        const val KEY_NAME = "name"
        const val KEY_TARGET = "target"
        const val KEY_INTERVAL = "interval"
        const val KEY_ENDPOINT = "endpoint"
        val KEYS = listOf(KEY_GROUP, KEY_NAME, KEY_TARGET, KEY_INTERVAL, KEY_ENDPOINT)

        /** Read back: null when no collection is kept, or what is kept isn't whole (nothing is shown then). */
        fun fromMap(m: Map<String, String?>): WallpaperSettings? {
            val id = m[KEY_GROUP]?.takeIf { it.isNotEmpty() } ?: return null
            val target = WallpaperTarget.parse(m[KEY_TARGET]) ?: return null
            val interval = WallpaperInterval.parse(m[KEY_INTERVAL]) ?: return null
            val endpoint = m[KEY_ENDPOINT] ?: return null
            return WallpaperSettings(id, m[KEY_NAME] ?: "", target, interval, endpoint)
        }
    }
}

/** A photo of the collection: its content's hash (a copy's name) and the path it is fetched by. */
data class WallpaperPhoto(val hash: String, val path: String)

object WallpaperPlan {
    /** The most photos of a collection copied to the phone; a random this many of a bigger one. */
    const val CAP = 100

    /** How many photos the copies will be, for a collection of [photos] photos: what the notice counts. */
    fun copies(photos: Int, cap: Int = CAP): Int = min(max(photos, 0), cap)

    /** What a refresh does: the copies to [keep], the photos to fetch ([add]) and the copies to delete ([remove]). */
    data class Plan(val keep: Set<String>, val add: List<WallpaperPhoto>, val remove: Set<String>)

    /**
     * The copies in step with the collection ([photos], as listed now; a
     * hash listed twice is one photo) for the copies [stored]: those still
     * in it stay, those no longer in it go, and photos not copied yet are
     * added, at random, up to [cap] in all. Over the cap (it was lowered),
     * a random few of the kept go too.
     */
    fun plan(photos: List<WallpaperPhoto>, stored: Set<String>, cap: Int = CAP, random: Random = Random.Default): Plan {
        val listed = photos.mapTo(HashSet()) { it.hash }
        val still = stored.filterTo(LinkedHashSet()) { it in listed }
        val keep = if (still.size > cap) still.shuffled(random).take(cap).toSet() else still
        val remove = stored.filterTo(LinkedHashSet()) { it !in keep }
        val candidates = photos.filter { it.hash !in stored }.distinctBy { it.hash }
        val add = candidates.shuffled(random).take(max(0, cap - keep.size))
        return Plan(keep, add, remove)
    }
}

object WallpaperShuffle {
    /**
     * The next photo to show of the copies [all], and the round's shown
     * photos after it: one at random among those not shown yet this round
     * ([shown]); once every one has been, a new round starts - never with
     * the photo on screen now ([last]) unless it is the only one. Null with
     * no copies.
     */
    fun next(all: List<String>, shown: Set<String>, last: String?, random: Random = Random.Default): Pair<String, Set<String>>? {
        val copies = all.distinct()
        if (copies.isEmpty()) return null
        val seen = shown.filterTo(LinkedHashSet()) { it in copies }
        var left = copies.filter { it !in seen }
        if (left.isEmpty()) {
            seen.clear()
            left = copies.filter { it != last }.ifEmpty { copies }
        }
        val pick = left[random.nextInt(left.size)]
        return pick to (seen + pick)
    }

    /** The round's shown photos as kept: hashes joined with commas. */
    fun format(shown: Set<String>): String = shown.joinToString(",")

    fun parse(s: String?): Set<String> = s?.split(',')?.map { it.trim() }?.filter { isHash(it) }?.toSet() ?: emptySet()
}

/** A content hash as the device sends them (hex): the only names a copy's file can have. */
fun isHash(s: String): Boolean = s.length in 16..128 && s.all { it in '0'..'9' || it in 'a'..'f' || it in 'A'..'F' }

/**
 * Part of a photo [srcW] x [srcH] to show on a screen [dstW] x [dstH]: the
 * centre, at the screen's shape, as large as the photo allows ([left],
 * [top], [width], [height]), and the size it ends up ([outW] x [outH]) - the
 * screen's, or smaller when the photo is (never made bigger).
 */
data class WallpaperCrop(val left: Int, val top: Int, val width: Int, val height: Int, val outW: Int, val outH: Int) {
    companion object {
        fun of(srcW: Int, srcH: Int, dstW: Int, dstH: Int): WallpaperCrop? {
            if (srcW <= 0 || srcH <= 0 || dstW <= 0 || dstH <= 0) return null
            // The screen's shape inside the photo: its full width, or its full height.
            val wide = srcW.toLong() * dstH > srcH.toLong() * dstW
            val w = if (wide) max(1, (srcH.toDouble() * dstW / dstH).roundToInt()).coerceAtMost(srcW) else srcW
            val h = if (wide) srcH else max(1, (srcW.toDouble() * dstH / dstW).roundToInt()).coerceAtMost(srcH)
            val scale = min(1.0, dstW.toDouble() / w)
            return WallpaperCrop((srcW - w) / 2, (srcH - h) / 2, w, h, max(1, (w * scale).roundToInt()), max(1, (h * scale).roundToInt()))
        }
    }
}

/** How much a photo [srcW] x [srcH] may be decoded smaller (a power of two) and still cover [dstW] x [dstH]. */
fun wallpaperSample(srcW: Int, srcH: Int, dstW: Int, dstH: Int): Int {
    if (srcW <= 0 || srcH <= 0 || dstW <= 0 || dstH <= 0) return 1
    var s = 1
    while (srcW / (s * 2) >= dstW && srcH / (s * 2) >= dstH) s *= 2
    return s
}
