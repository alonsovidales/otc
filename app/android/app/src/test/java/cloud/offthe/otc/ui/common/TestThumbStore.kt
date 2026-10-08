// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import kotlinx.coroutines.runBlocking
import java.io.File
import java.security.MessageDigest
import java.util.concurrent.Executor
import kotlin.coroutines.EmptyCoroutineContext

/** A device content hash (64 lowercase hex digits) for a test's name. */
fun hx(name: String): String = MessageDigest.getInstance("SHA-256").digest(name.toByteArray()).joinToString("") { "%02x".format(it) }

/** A test's thumbnail bytes "decode" unless empty or all zeros (what a power cut leaves). */
fun testDecodable(b: ByteArray): Boolean = b.isNotEmpty() && b.any { it != 0.toByte() }

const val TEST_ENDPOINT = "wss://pit.off-the.cloud/ws"

/** Runs file work at once, on the caller's thread: tests see its outcome right away. */
val DIRECT = Executor { it.run() }

/**
 * The grids' thumbnail store for tests: a real ThumbStoreCore in [dir]
 * (`cache/` and `state/`; a restart is a new TestThumbStore on the same
 * folder), bound at launch to [endpoint], file work done at once.
 */
class TestThumbStore(
    val dir: File,
    omits: Boolean = false,
    endpoint: String = TEST_ENDPOINT,
    nowMs: () -> Long = System::currentTimeMillis,
    val core: ThumbStoreCore = ThumbStoreCore(
        cacheRoot = File(dir, "cache"), stateDir = File(dir, "state"), savedEndpoint = { endpoint },
        decodable = ::testDecodable, io = EmptyCoroutineContext, background = DIRECT, nowMs = nowMs,
    ),
) : GridThumbStore by core {
    init {
        if (omits) runBlocking { core.notePage(true, core.scope()) }
    }

    /** The bound scope's disk cache. */
    val cache: ThumbDiskCache get() = core.cacheNow()!!
}
