// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.wallpaper

import cloud.offthe.otc.proto.GetFile
import cloud.offthe.otc.proto.ListImageGroups
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.SearchPhotos
import kotlinx.coroutines.CancellationException
import kotlin.random.Random

/** One request to the device, answered (it throws when nothing answered; tests answer in its place). */
typealias WallpaperRequest = suspend (build: (ReqEnvelope.Builder) -> Unit) -> RespEnvelope

/**
 * The copies of one collection on the phone (Wallpaper.kt: a folder of the
 * app's private storage, each copy named by its photo's hash; tests: a
 * stand-in). [save] makes a copy sized for the screen from what the device
 * sent (GetFile, as the viewer shows a photo): false when it can't.
 */
interface WallpaperCopies {
    fun hashes(): Set<String>
    fun save(hash: String, bytes: ByteArray): Boolean
    fun delete(hash: String)
}

/**
 * A collection's copies brought in step with it: the device lists the
 * collection (ListImageGroups: still there, and its name now), then its
 * photos (SearchPhotos by the collection, photos only, without
 * thumbnails), and WallpaperPlan says what to keep, fetch and delete. Each
 * photo to add is fetched as the viewer fetches a photo shown full screen
 * (GetFile) and stored sized for the screen (WallpaperCopies.save) - never
 * the original - one at a time, saying how far it got. A list that didn't
 * come whole deletes nothing.
 */
class WallpaperFetch(
    private val send: WallpaperRequest,
    private val copies: WallpaperCopies,
    private val random: Random = Random.Default,
    private val cap: Int = WallpaperPlan.CAP,
) {
    sealed interface Outcome {
        /** In step: [stored] copies now, the collection called [name]. */
        data class Done(val name: String, val stored: Int) : Outcome
        /** The collection was deleted: the wallpaper stops and its copies go. */
        data object Gone : Outcome
        /** Something didn't answer: what is stored stays, and it is tried again later. */
        data object Failed : Outcome
    }

    /**
     * Brings collection [groupId]'s copies in step: [progress] (done, total)
     * as each copy is stored; [stored] after each one saved (Wallpaper sets
     * the first one at once).
     */
    suspend fun run(groupId: String, progress: (Int, Int) -> Unit = { _, _ -> }, stored: (String) -> Unit = {}): Outcome {
        val name = try {
            val r = send { it.setReqListImageGroups(ListImageGroups.newBuilder().setSmallThumbnails(true)) }
            if (r.payloadCase != RespEnvelope.PayloadCase.RESP_IMAGE_GROUPS) return Outcome.Failed
            r.respImageGroups.groupsList.firstOrNull { it.id == groupId }?.name ?: return Outcome.Gone
        } catch (e: CancellationException) {
            throw e
        } catch (_: Exception) { return Outcome.Failed }
        val photos = listPhotos(send, groupId) ?: return Outcome.Failed
        val plan = WallpaperPlan.plan(photos, copies.hashes(), cap, random)
        for (h in plan.remove) copies.delete(h)
        val total = plan.keep.size + plan.add.size
        var done = plan.keep.size
        progress(done, total)
        for (p in plan.add) {
            val r = try {
                send { it.setReqGetFile(GetFile.newBuilder().setPath(p.path)) }
            } catch (e: CancellationException) {
                throw e
            } catch (_: Exception) { return Outcome.Failed }
            // The device's refusal (the file went since it was listed): the
            // next refresh lists the collection again.
            if (r.payloadCase == RespEnvelope.PayloadCase.RESP_FILE && copies.save(p.hash, r.respFile.content.toByteArray())) {
                done++
                stored(p.hash)
            } else if (r.payloadCase != RespEnvelope.PayloadCase.RESP_FILE && !r.error) return Outcome.Failed
            progress(done, total)
        }
        return Outcome.Done(name, copies.hashes().size)
    }

    companion object {
        /** SearchPhotos' page without thumbnails: its rows are small. */
        const val PAGE = 200

        // Pages at most: 100,000 photos; a list that hasn't ended by then isn't trusted.
        private const val MAX_PAGES = 500

        /**
         * The photos of collection [groupId], each once - no videos: a
         * collection's search brings them whatever include_videos says, so
         * they are left out by their type here. Null when the list didn't
         * come whole: a page failed, or the device kept starting the search
         * over.
         */
        suspend fun listPhotos(send: WallpaperRequest, groupId: String): List<WallpaperPhoto>? {
            val seen = LinkedHashMap<String, WallpaperPhoto>()
            var token = ""
            var have = 0
            var stale = 0
            repeat(MAX_PAGES) {
                val r = try {
                    send {
                        it.setReqSearchPhotos(
                            SearchPhotos.newBuilder().setGroupId(groupId).setToken(token).setHave(have).setLimit(PAGE)
                                .setSmallThumbnails(true).setOmitThumbnails(true),
                        )
                    }
                } catch (e: CancellationException) {
                    throw e
                } catch (_: Exception) { return null }
                if (r.payloadCase != RespEnvelope.PayloadCase.RESP_LIST_OF_FILES) return null
                val lof = r.respListOfFiles
                val before = seen.size
                for (f in lof.filesList) {
                    if (!f.mime.startsWith("image/") || !isHash(f.hash) || f.path.isEmpty()) continue
                    seen.putIfAbsent(f.hash, WallpaperPhoto(f.hash, f.path))
                }
                have += lof.filesCount
                token = lof.token
                if (token.isEmpty()) return seen.values.toList()
                // Pages that bring nothing new, again and again: the device
                // starting the search over each time.
                stale = if (seen.size == before) stale + 1 else 0
                if (stale >= 3) return null
            }
            return null
        }

        /**
         * How many photos collection [groupId] has, for the notice's count:
         * its list, as the copies are chosen from it (only the photos' rows,
         * nothing downloaded). Not its date buckets: a collection's count
         * them with its videos, include_videos or not, and so does its
         * SearchPhotos - the videos are left out here. Null when the list
         * didn't come.
         */
        suspend fun countPhotos(send: WallpaperRequest, groupId: String): Int? = listPhotos(send, groupId)?.size
    }
}
