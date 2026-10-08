// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import java.io.BufferedWriter
import java.io.File
import java.io.FileOutputStream
import java.io.OutputStreamWriter
import java.io.RandomAccessFile
import java.util.concurrent.Executor
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicLong

/**
 * Which thumbnail a cache entry holds (File.thumbnail_small): the grids'
 * small one, the big one (the device had no small one yet), or UNKNOWN - an
 * answer from a device before release 113, which doesn't say (asked for
 * small, it is the small one or the big one).
 */
enum class ThumbKind(val code: Char) {
    SMALL('S'), BIG('B'), UNKNOWN('U');

    companion object {
        fun ofCode(c: Char): ThumbKind? = entries.firstOrNull { it.code == c }

        /** What an answer's entry says it holds: File.thumbnail_small, null when unset (before release 113). */
        fun of(says: Boolean?): ThumbKind = when (says) {
            true -> SMALL
            false -> BIG
            null -> UNKNOWN
        }
    }
}

/**
 * The grids' thumbnails on this phone, by content hash, up to [limitBytes]:
 * past it, the least recently used go until the rest take 90% of it. Pure
 * JVM (unit tested), thread safe. The same rules as the iOS app's
 * ThumbDiskCache.swift.
 *
 * One entry per hash (the device's: 64 lowercase hex digits, nothing else
 * names a file here), of at most [MAX_ENTRY_BYTES], holding its bytes and
 * its [ThumbKind]; a small one replaces a big or unknown one, a big one an
 * unknown one, and nothing a small one ([replaces]). An entry can be marked
 * still big ([noteStillBig]): its small one was asked for and the big one
 * came again.
 *
 * On disk, in [dir]: each entry's bytes in `<xx>/<hash>` (xx: its first two
 * digits, so no directory holds more than a few hundred files), and
 * `journal`, the index - a header (`otc-thumbs <version>`) and one line per
 * change: `P <hash> <kind> <size>` (kept), `A <hash>` (used), `D <hash>`
 * (dropped), `S <hash> <epoch seconds>` (still big). Opening replays it:
 * the order of the lines is the LRU order, so nothing is stat'ed per entry.
 * Once it holds far more lines than entries it is written over from the
 * index - on [background], not under the lock.
 *
 * Writes are atomic: the bytes go to a temporary file renamed over the
 * entry's, and the journal line after it. Whatever a killed process, a full
 * disk or a power cut leaves is tolerated: a journal line cut short is
 * skipped, a file the journal doesn't know (or a temporary one) is deleted
 * at open, an entry whose file is gone or has the wrong size (the system
 * clearing cache space) is dropped when read - and one whose bytes don't
 * decode is dropped by its caller (ThumbStoreCore). A journal of another
 * [version] (the small size changed) or unreadable starts the cache over.
 * Uses are written out every [FLUSH_EVERY_LINES] lines or
 * [FLUSH_EVERY_MS], and on [flush] (the app going to the background).
 *
 * Slow file work never runs under the lock: evicted files, and the folders
 * a clear or a new version sets aside, are deleted on [background].
 */
class ThumbDiskCache(
    val dir: File,
    limitBytes: Long,
    private val version: Int = 1,
    // Where deletes and journal rewrites run (tests: at once, or a thread of theirs).
    private val background: Executor = BACKGROUND,
    private val nowMs: () -> Long = System::currentTimeMillis,
) {
    private class Node(val size: Long, val kind: ThumbKind) {
        // When its small one was asked for and the big one came (epoch s; 0: never).
        var stillBigAt = 0L
    }

    // Every entry by hash, least recently used first (insertion order: a use
    // moves an entry to the end by taking it out and putting it back).
    private val index = LinkedHashMap<String, Node>()
    private var used = 0L
    private var limit = limitBytes
    private var writer: BufferedWriter? = null
    private var journalLines = 0
    private var unflushed = 0
    private var lastFlushMs = 0L
    private var closed = false
    // Bumped by clear and close: a journal rewrite started before lands nowhere.
    private var epoch = 0
    private var compacting = false
    // While a rewrite runs: the lines recorded since its snapshot.
    private var pending: ArrayList<String>? = null
    // A rewrite that failed (a full disk) is tried again only once the journal doubled.
    private var compactRetryAt = 0
    private val tmpSeq = AtomicLong()

    private val journal get() = File(dir, JOURNAL)

    init {
        synchronized(this) { open() }
    }

    /** The bytes the entries take. */
    val usedBytes: Long
        @Synchronized get() = used

    val limitBytes: Long
        @Synchronized get() = limit

    val count: Int
        @Synchronized get() = index.size

    /** The kind [hash] is kept as, without reading it or counting it as used; null: not kept. */
    @Synchronized fun kindOf(hash: String): ThumbKind? = index[hash]?.kind

    /** [kindOf] for several at once. */
    @Synchronized fun kinds(hashes: Collection<String>): Map<String, ThumbKind> {
        val out = HashMap<String, ThumbKind>()
        for (h in hashes) index[h]?.let { out[h] = it.kind }
        return out
    }

    /** Of [hashes], those marked still big ([noteStillBig]) less than [withinMs] ago. */
    @Synchronized fun stillBig(hashes: Collection<String>, withinMs: Long): Set<String> {
        val now = nowMs()
        val out = HashSet<String>()
        for (h in hashes) index[h]?.let { if (it.stillBigAt > 0 && now - it.stillBigAt * 1_000 < withinMs) out += h }
        return out
    }

    /** [hash]'s bytes and kind, now the most recently used; null when not kept (or no longer whole). */
    fun get(hash: String): Pair<ByteArray, ThumbKind>? {
        if (!isHash(hash)) return null
        val node = synchronized(this) {
            if (closed) return null
            val n = index[hash] ?: return null
            touch(hash, n)
            maybeFlush()
            compactIfLong()
            n
        }
        val bytes = try { fileOf(hash).readBytes() } catch (_: Exception) { null }
        if (bytes == null || bytes.size.toLong() != node.size) {
            // Gone or not whole: dropped, unless it was replaced meanwhile.
            synchronized(this) { if (!closed && index[hash] === node) { drop(hash, node); flushQuietly() } }
            return null
        }
        return bytes to node.kind
    }

    /**
     * Keeps [bytes] as [hash]'s thumbnail of [kind], unless what is kept
     * already is as good ([replaces]): that one is then counted as used.
     * The kind kept afterwards; null when nothing is (not a device hash,
     * over [MAX_ENTRY_BYTES], or a write failed with nothing kept before).
     */
    fun put(hash: String, bytes: ByteArray, kind: ThumbKind): ThumbKind? {
        if (!isHash(hash) || bytes.isEmpty() || bytes.size > MAX_ENTRY_BYTES) return null
        synchronized(this) {
            if (closed) return null
            index[hash]?.let { cur -> if (!replaces(kind, cur.kind)) { touch(hash, cur); maybeFlush(); return cur.kind } }
        }
        val sub = File(dir, hash.take(2))
        val tmp = File(sub, "$hash.${tmpSeq.incrementAndGet()}$TMP")
        try {
            sub.mkdirs()
            FileOutputStream(tmp).use { it.write(bytes) }
        } catch (_: Exception) {
            tmp.delete()
            return synchronized(this) { index[hash]?.kind }
        }
        synchronized(this) {
            val cur = index[hash]
            if (closed || (cur != null && !replaces(kind, cur.kind))) {
                tmp.delete()
                cur?.let { touch(hash, it) }
                return cur?.kind
            }
            if (!tmp.renameTo(fileOf(hash))) {
                tmp.delete()
                return cur?.kind
            }
            if (cur != null) { index.remove(hash); used -= cur.size }
            val node = Node(bytes.size.toLong(), kind)
            index[hash] = node
            used += node.size
            record("P $hash ${kind.code} ${node.size}")
            evictToLimit(keep = hash)
            compactIfLong()
            flushQuietly()
            return kind
        }
    }

    /** [hash]'s small one was asked for and its big one came again: kept so, across launches, until it is replaced. */
    @Synchronized fun noteStillBig(hash: String) {
        if (closed) return
        val n = index[hash] ?: return
        n.stillBigAt = nowMs() / 1_000
        record("S $hash ${n.stillBigAt}")
        flushQuietly()
    }

    /** Forgets [hash] (its bytes didn't decode). */
    @Synchronized fun remove(hash: String) {
        if (closed) return
        index[hash]?.let { drop(hash, it); flushQuietly() }
    }

    /** A new size limit: past it, the least recently used go until the rest take 90% of it. */
    @Synchronized fun setLimit(bytes: Long) {
        limit = bytes
        if (closed) return
        evictToLimit(keep = null)
        compactIfLong()
        flushQuietly()
    }

    /** Everything goes (Settings' Clear thumbnail cache): set aside at once, deleted in the background. */
    @Synchronized fun clear() {
        if (closed) return
        epoch++
        compacting = false
        pending = null
        closeWriter()
        setAside()
        index.clear()
        used = 0
        rewriteJournal()
    }

    /** Written out and let go: nothing more is kept or read through this instance. */
    @Synchronized fun close() {
        if (closed) return
        closed = true
        epoch++
        closeWriter()
    }

    /** Pending journal lines (uses) written out. */
    @Synchronized fun flush() { flushQuietly() }

    // ---- inside, under the lock ------------------------------------------------------

    private fun fileOf(hash: String) = File(File(dir, hash.take(2)), hash)

    // The journal (and its temporary copies) and the entries' directories:
    // what a clear or a wipe takes. Anything else in dir stays.
    private fun isOurs(f: File) = f.name == JOURNAL || f.name.startsWith("$JOURNAL.") || (f.isDirectory && f.name.length == 2)

    private fun touch(hash: String, n: Node) {
        index.remove(hash)
        index[hash] = n
        record("A $hash")
    }

    private fun drop(hash: String, n: Node) {
        index.remove(hash)
        used -= n.size
        fileOf(hash).delete()
        record("D $hash")
    }

    // Past the limit: the least recently used leave the index here, down to
    // 90% of it in one pass; their files are deleted in the background.
    private fun evictToLimit(keep: String?) {
        if (used <= limit) return
        val target = limit / 10 * 9
        val victims = ArrayList<String>()
        val it = index.entries.iterator()
        while (used > target && it.hasNext()) {
            val (hash, n) = it.next()
            if (hash == keep) continue
            it.remove()
            used -= n.size
            record("D $hash")
            victims += hash
        }
        deleteLater(victims)
    }

    // A file is deleted only if its hash wasn't kept again meanwhile (a put
    // renames and indexes under the lock, so this can't take the new file).
    private fun deleteLater(hashes: List<String>) {
        if (hashes.isEmpty()) return
        background.execute {
            for (h in hashes) synchronized(this) { if (!index.containsKey(h)) fileOf(h).delete() }
        }
    }

    // The journal and the entries' folders moved into a folder of their own
    // beside dir (a rename each), deleted in the background.
    private fun setAside() {
        val trash = File(dir.absoluteFile.parentFile, "${dir.name}.trash-${System.nanoTime()}")
        val trashing = trash.mkdirs()
        for (f in dir.listFiles().orEmpty()) {
            if (isOurs(f) && !(trashing && f.renameTo(File(trash, f.name)))) f.deleteRecursively()
        }
        if (trashing) background.execute { trash.deleteRecursively() }
    }

    private fun record(line: String) {
        pending?.add(line)
        val w = writer ?: return
        try {
            w.write(line)
            w.write('\n'.code)
            journalLines++
            unflushed++
        } catch (_: Exception) {
            // A full disk: the index in memory stays right; what the journal
            // missed is made good at the next open (unknown files go, missing
            // ones are dropped when read).
            closeWriter()
        }
    }

    // Uses are buffered: written out every so many lines or seconds.
    private fun maybeFlush() {
        if (unflushed >= FLUSH_EVERY_LINES || nowMs() - lastFlushMs >= FLUSH_EVERY_MS) flushQuietly()
    }

    private fun flushQuietly() {
        try { writer?.flush() } catch (_: Exception) { closeWriter() }
        unflushed = 0
        lastFlushMs = nowMs()
    }

    private fun closeWriter() {
        try { writer?.close() } catch (_: Exception) {}
        writer = null
    }

    // Far more lines than entries (uses add one each): written over in the background.
    private fun compactIfLong() {
        if (journalLines > COMPACT_MIN_LINES && journalLines > 2 * index.size && journalLines >= compactRetryAt) startCompaction()
    }

    private fun startCompaction() {
        if (compacting || closed) return
        flushQuietly()
        val hashes = ArrayList<String>(index.size)
        val nodes = ArrayList<Node>(index.size)
        val stamps = LongArray(index.size)
        for ((h, n) in index) { stamps[hashes.size] = n.stillBigAt; hashes += h; nodes += n }
        compacting = true
        pending = ArrayList()
        val ep = epoch
        background.execute { finishCompaction(hashes, nodes, stamps, ep) }
    }

    // Off the lock: the snapshot written to a temporary journal; under it,
    // what was recorded meanwhile appended and the temporary one renamed over.
    private fun finishCompaction(hashes: List<String>, nodes: List<Node>, stamps: LongArray, ep: Int) {
        val tmp = File(dir, COMPACT_TMP)
        var lines = 0
        var ok = try {
            BufferedWriter(OutputStreamWriter(FileOutputStream(tmp), Charsets.UTF_8), 1 shl 16).use { w ->
                w.write("$HEADER $version\n")
                for (i in hashes.indices) {
                    w.write("P ${hashes[i]} ${nodes[i].kind.code} ${nodes[i].size}\n")
                    lines++
                    if (stamps[i] > 0) { w.write("S ${hashes[i]} ${stamps[i]}\n"); lines++ }
                }
            }
            true
        } catch (_: Exception) { false }
        synchronized(this) {
            val since = pending ?: emptyList<String>()
            pending = null
            compacting = false
            if (ep != epoch || closed) { tmp.delete(); return }
            if (ok) ok = try {
                FileOutputStream(tmp, true).bufferedWriter(Charsets.UTF_8).use { w -> for (l in since) { w.write(l); w.write("\n") } }
                tmp.renameTo(journal)
            } catch (_: Exception) { false }
            if (ok) {
                // The old writer's lines are in `since`: it goes with its file.
                closeWriter()
                openWriter()
                journalLines = lines + since.size
                compactRetryAt = 0
            } else {
                tmp.delete()
                compactRetryAt = journalLines * 2
            }
        }
    }

    /** The journal written anew from the index (LRU order) - only ever small: a new, cleared or wiped cache. */
    private fun rewriteJournal() {
        closeWriter()
        val tmp = File(dir, "$JOURNAL$TMP")
        try {
            dir.mkdirs()
            BufferedWriter(OutputStreamWriter(FileOutputStream(tmp), Charsets.UTF_8)).use { w ->
                w.write("$HEADER $version\n")
                for ((h, n) in index) w.write("P $h ${n.kind.code} ${n.size}\n")
            }
            if (!tmp.renameTo(journal)) throw java.io.IOException("rename")
            journalLines = index.size
        } catch (_: Exception) {
            tmp.delete()
            // A full disk: no journal at all rather than a stale one, and
            // nothing appended to a file without its header - the next open
            // starts the cache over. Until then the index in memory serves.
            journal.delete()
            return
        }
        openWriter()
    }

    private fun openWriter() {
        writer = try {
            BufferedWriter(OutputStreamWriter(FileOutputStream(journal, true), Charsets.UTF_8), 8 shl 10)
        } catch (_: Exception) { null }
    }

    private fun open() {
        dir.mkdirs()
        lastFlushMs = nowMs()
        val r = replay()
        if (r == null) {
            // None (a new cache), another version's or unreadable: start over.
            setAside()
            index.clear()
            used = 0
            rewriteJournal()
            return
        }
        val changed = sweep()
        openWriter()
        // A last line cut short ends here: what follows starts a line of its own.
        if (!r.endsWhole) try { writer?.write('\n'.code) } catch (_: Exception) { closeWriter() }
        evictToLimit(keep = null)
        if (r.bad > 0 || changed) startCompaction() else compactIfLong()
        flushQuietly()
    }

    private class Replayed(val bad: Int, val endsWhole: Boolean)

    /**
     * The journal read into the index, line by line: how many lines made no
     * sense (skipped; a last line cut short - no newline - is one); null
     * when there is no journal or it is another version's.
     */
    private fun replay(): Replayed? {
        val f = journal
        if (!f.isFile) return null
        return try {
            val endsWhole = RandomAccessFile(f, "r").use { r -> r.length() > 0L && r.run { seek(length() - 1); read() } == '\n'.code }
            var bad = 0
            var lines = 0
            var header = true
            var last: String? = null
            f.bufferedReader(Charsets.UTF_8).useLines { seq ->
                for (line in seq) {
                    if (header) {
                        if (line != "$HEADER $version") return null
                        header = false
                        continue
                    }
                    last?.let { if (!apply(it)) bad++ }
                    last = line
                    lines++
                }
            }
            if (header) return null
            last?.let { if (!endsWhole || !apply(it)) bad++ }
            journalLines = lines
            Replayed(bad, endsWhole)
        } catch (_: Exception) {
            index.clear()
            used = 0
            null
        }
    }

    private fun apply(line: String): Boolean {
        val p = line.split(' ')
        val hash = p.getOrNull(1)?.takeIf { isHash(it) } ?: return false
        when (p[0]) {
            "P" -> {
                if (p.size != 4 || p[2].length != 1) return false
                val kind = ThumbKind.ofCode(p[2][0]) ?: return false
                val size = p[3].toLongOrNull()?.takeIf { it in 1..MAX_ENTRY_BYTES } ?: return false
                index.remove(hash)?.let { used -= it.size }
                index[hash] = Node(size, kind)
                used += size
            }
            "A" -> { if (p.size != 2) return false; index.remove(hash)?.let { index[hash] = it } }
            "D" -> { if (p.size != 2) return false; index.remove(hash)?.let { used -= it.size } }
            "S" -> {
                if (p.size != 3) return false
                val t = p[2].toLongOrNull()?.takeIf { it > 0 } ?: return false
                index[hash]?.stillBigAt = t
            }
            else -> return false
        }
        return true
    }

    /**
     * The files against the index: temporary ones and ones it doesn't know
     * are deleted, and entries without a file are dropped. True when the
     * index changed.
     */
    private fun sweep(): Boolean {
        val present = HashSet<String>(index.size * 2)
        for (sub in dir.listFiles().orEmpty()) {
            if (!sub.isDirectory || sub.name.length != 2) continue
            for (name in sub.list().orEmpty()) {
                if (name.take(2) == sub.name && index.containsKey(name)) present += name
                else File(sub, name).deleteRecursively()
            }
        }
        if (present.size == index.size) return false
        val it = index.entries.iterator()
        while (it.hasNext()) {
            val (h, n) = it.next()
            if (h !in present) { it.remove(); used -= n.size }
        }
        return true
    }

    companion object {
        /** The most an entry may take: a thumbnail over it is shown, not kept (as iOS). */
        const val MAX_ENTRY_BYTES = 8 shl 20
        private const val HEADER = "otc-thumbs"
        private const val JOURNAL = "journal"
        private const val TMP = ".tmp"
        private const val COMPACT_TMP = "$JOURNAL.compact$TMP"
        // Fewer lines than this are never worth writing over.
        private const val COMPACT_MIN_LINES = 2_000
        const val FLUSH_EVERY_LINES = 64
        const val FLUSH_EVERY_MS = 10_000L

        /** One thread for every cache's slow file work. */
        val BACKGROUND: Executor = Executors.newSingleThreadExecutor { r -> Thread(r, "thumb-cache").apply { isDaemon = true } }

        /** The device's content hashes: 64 lowercase hex digits (dao.IsContentHash). Nothing else names a file here. */
        fun isHash(h: String): Boolean = h.length == 64 && h.all { it in '0'..'9' || it in 'a'..'f' }

        /**
         * Whether a [new] thumbnail replaces the [old] one kept for the same
         * hash: a small one replaces a big or unknown one, a big one (the
         * device said so) an unknown one, nothing replaces a small one, and
         * the same kind again is the same picture. As the iOS app.
         */
        fun replaces(new: ThumbKind, old: ThumbKind): Boolean = rank(new) > rank(old)

        private fun rank(k: ThumbKind) = when (k) {
            ThumbKind.SMALL -> 2
            ThumbKind.BIG -> 1
            ThumbKind.UNKNOWN -> 0
        }
    }
}
