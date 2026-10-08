// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.withTimeoutOrNull

/**
 * The moments a load that failed is worth asking for again at once, not at
 * the end of its wait - the web's usePageRetry wakes on the device's first
 * good answer and on the browser's "online": signed in again
 * (OTCConnection), a network came up (NetworkWatch), the app came back to
 * the foreground (RootView). A counter that each of them bumps.
 */
object Wake {
    private val _count = MutableStateFlow(0)
    val count: StateFlow<Int> = _count

    fun fire() = _count.update { it + 1 }
}

/** delay([ms]), cut short by a [Wake]. */
suspend fun sleepOrWake(ms: Long) {
    val seen = Wake.count.value
    withTimeoutOrNull(ms) { Wake.count.first { it != seen } }
}

/**
 * How long a load that failed [failures] times in a row waits before it is
 * asked again: 1 s doubling to 10 s, the web's usePageRetry - kept short so
 * a screen fills soon after the device is back.
 */
fun retryWaitMs(failures: Int, firstMs: Long = 1_000, maxMs: Long = 10_000): Long {
    if (failures <= 1) return firstMs
    var wait = firstMs
    repeat(failures - 1) { wait = minOf(wait * 2, maxMs) }
    return wait
}
