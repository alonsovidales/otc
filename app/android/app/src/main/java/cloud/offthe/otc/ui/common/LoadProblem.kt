// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appShellProblemFilesFailed
import cloud.offthe.otc.i18n.appShellProblemFilesOffline
import cloud.offthe.otc.i18n.appShellProblemFilesSlow
import cloud.offthe.otc.i18n.appShellProblemFilesUnreachable
import cloud.offthe.otc.i18n.appShellProblemMorePhotos
import cloud.offthe.otc.i18n.appShellProblemMorePosts
import cloud.offthe.otc.i18n.appShellProblemPhotosFailed
import cloud.offthe.otc.i18n.appShellProblemPhotosLoading
import cloud.offthe.otc.i18n.appShellProblemPhotosOffline
import cloud.offthe.otc.i18n.appShellProblemPhotosSlow
import cloud.offthe.otc.i18n.appShellProblemPhotosTitle
import cloud.offthe.otc.i18n.appShellProblemPhotosUnreachable
import cloud.offthe.otc.i18n.appShellProblemPostsFailed
import cloud.offthe.otc.i18n.appShellProblemPostsOffline
import cloud.offthe.otc.i18n.appShellProblemPostsSlow
import cloud.offthe.otc.i18n.appShellProblemPostsUnreachable
import cloud.offthe.otc.i18n.appShellProblemTrying
import cloud.offthe.otc.i18n.appShellProblemVideo
import cloud.offthe.otc.i18n.appShellProblemWaiting
import cloud.offthe.otc.i18n.commonTryAgain
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

// What the screens say is text resolved where it is shown (UiText): a
// view model keeps it, and a change of language rewords it.

/** Images' first page failed: the title, then why ([photosProblemText]), then Try again. */
val PHOTOS_PROBLEM_TITLE: UiText get() = S.appShellProblemPhotosTitle()

/** Under [PHOTOS_PROBLEM_TITLE]: why, in plain words, and that they come by themselves. */
fun photosProblemText(p: LoadProblem): UiText = loadProblemText(p, LoadSubject.PHOTOS)

/** What a screen is waiting for: each has its own sentences, since a translation can't take "Your photos" or "The posts" as a piece. */
enum class LoadSubject { PHOTOS, FILES, POSTS }

/** Why [what] (your photos, the posts) isn't here yet, and that it comes by itself once it can. */
fun loadProblemText(p: LoadProblem, what: LoadSubject): UiText = when (what) {
    LoadSubject.PHOTOS -> when (p) {
        LoadProblem.OFFLINE -> S.appShellProblemPhotosOffline()
        LoadProblem.UNREACHABLE -> S.appShellProblemPhotosUnreachable()
        LoadProblem.SLOW -> S.appShellProblemPhotosSlow()
        LoadProblem.FAILED -> S.appShellProblemPhotosFailed()
    }
    LoadSubject.FILES -> when (p) {
        LoadProblem.OFFLINE -> S.appShellProblemFilesOffline()
        LoadProblem.UNREACHABLE -> S.appShellProblemFilesUnreachable()
        LoadProblem.SLOW -> S.appShellProblemFilesSlow()
        LoadProblem.FAILED -> S.appShellProblemFilesFailed()
    }
    LoadSubject.POSTS -> when (p) {
        LoadProblem.OFFLINE -> S.appShellProblemPostsOffline()
        LoadProblem.UNREACHABLE -> S.appShellProblemPostsUnreachable()
        LoadProblem.SLOW -> S.appShellProblemPostsSlow()
        LoadProblem.FAILED -> S.appShellProblemPostsFailed()
    }
}

/**
 * The phone is back online while a load that failed offline is asked for
 * again: "This phone is offline" no longer says why - the neutral line
 * (FAILED's) until the answer comes or the next failure says why.
 */
fun LoadProblem.backOnline(online: Boolean): LoadProblem = if (this == LoadProblem.OFFLINE && online) LoadProblem.FAILED else this

/** A failed load's button, and while it is asked again. */
val TRY_AGAIN: UiText get() = S.commonTryAgain()
val TRYING: UiText get() = S.appShellProblemTrying()

/** A later page failed: the row at the end of the grid, before its Try again (the web's wording). */
val MORE_PHOTOS_PROBLEM: UiText get() = S.appShellProblemMorePhotos()

/** Social's next page failed: the end of the feed, before its Try again. */
val MORE_POSTS_PROBLEM: UiText get() = S.appShellProblemMorePosts()

/** The first page has been on its way a while (People's and Collections' wording). */
val PHOTOS_SLOW: UiText get() = S.appShellProblemWaiting()

/** The grey tiles, to TalkBack (the web's aria-busy grid). */
val PHOTOS_LOADING: UiText get() = S.appShellProblemPhotosLoading()

/** The viewer: a video that couldn't be fetched or played, over its poster, before its Try again. */
val VIDEO_UNPLAYABLE: UiText get() = S.appShellProblemVideo()
