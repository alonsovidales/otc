// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import cloud.offthe.otc.net.WSClient
import cloud.offthe.otc.proto.RespEnvelope

/**
 * Why a screen's load failed, as far as the phone can tell - what the
 * screen says while it keeps asking: the phone has no network, the bridge
 * says the device isn't connected to it, nothing answered in time, or
 * anything else (an error answer, a dropped connection).
 */
enum class LoadProblem { OFFLINE, UNREACHABLE, SLOW, FAILED }

/** The bridge's Ack.code for a device it can't reach (deviceStatus.ts on the web). */
const val DEVICE_UNREACHABLE = "device_unreachable"

/**
 * [resp]: the answer that came instead of the one asked for (null: none
 * came), [error]: what was thrown instead of an answer. [online]: the phone
 * has a network (NetworkWatch.online); [statusCode]: the bridge's last word
 * on the device at sign-in (OTCConnection.statusCode).
 */
fun loadProblem(resp: RespEnvelope?, error: Throwable?, online: Boolean, statusCode: String?): LoadProblem = when {
    !online -> LoadProblem.OFFLINE
    resp != null && resp.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && resp.respAck.code == DEVICE_UNREACHABLE -> LoadProblem.UNREACHABLE
    error is WSClient.RequestTimeout -> LoadProblem.SLOW
    // Signing in got the bridge's verdict: OTCConnection threw for it.
    error != null && statusCode == DEVICE_UNREACHABLE -> LoadProblem.UNREACHABLE
    else -> LoadProblem.FAILED
}

/** Images' first page failed: the title, then why ([photosProblemText]), then Try again. */
const val PHOTOS_PROBLEM_TITLE = "Couldn't load your photos"

/** Under [PHOTOS_PROBLEM_TITLE]: why, in plain words, and that they come by themselves. */
fun photosProblemText(p: LoadProblem): String = loadProblemText(p, "Your photos")

/** Why [what] ("Your photos", "The posts") isn't here yet, and that it comes by itself once it can. */
fun loadProblemText(p: LoadProblem, what: String): String = when (p) {
    LoadProblem.OFFLINE -> "This phone is offline. $what will appear once it's back online."
    LoadProblem.UNREACHABLE -> "Your device isn't reachable right now. $what will appear as soon as it answers."
    LoadProblem.SLOW -> "Your device took too long to answer. $what will appear as soon as it does."
    LoadProblem.FAILED -> "$what will appear as soon as your device answers."
}

/**
 * The phone is back online while a load that failed offline is asked for
 * again: "This phone is offline" no longer says why - the neutral line
 * (FAILED's) until the answer comes or the next failure says why.
 */
fun LoadProblem.backOnline(online: Boolean): LoadProblem = if (this == LoadProblem.OFFLINE && online) LoadProblem.FAILED else this

/** A failed load's button, and while it is asked again. */
const val TRY_AGAIN = "Try again"
const val TRYING = "Trying…"

/** A later page failed: the row at the end of the grid, before its Try again (the web's wording). */
const val MORE_PHOTOS_PROBLEM = "Couldn't load more photos."

/** Social's next page failed: the end of the feed, before its Try again. */
const val MORE_POSTS_PROBLEM = "Couldn't load more posts."

/** The first page has been on its way a while (People's and Collections' wording). */
const val PHOTOS_SLOW = "Still waiting for your device…"

/** The grey tiles, to TalkBack (the web's aria-busy grid). */
const val PHOTOS_LOADING = "Loading photos"

/** The viewer: a video that couldn't be fetched or played, over its poster, before its Try again. */
const val VIDEO_UNPLAYABLE = "Couldn't play this video."
