// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.social

import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appSocialPostCommentFailed
import cloud.offthe.otc.i18n.appSocialPostDeleteCommentFailed
import cloud.offthe.otc.i18n.appSocialPostDeleteFailed
import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.Comment
import cloud.offthe.otc.proto.DelSocialPublication
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.RespPublication
import cloud.offthe.otc.proto.SocialPublication
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import java.io.IOException

// The feed's comments and deletes (IOS-14/A14, IOS-15) against a stand-in
// device: a comment the device didn't take says why - its words, or ours -
// so its card can put the text back; one it took asks for that post alone;
// a refused delete says why too. As SocialFeedActionsTests.swift.
class SocialFeedActionsTest {
    private val ok = RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(true)).build()
    // What a demo device answers every change with: both the envelope's error and the Ack's.
    private val demo = RespEnvelope.newBuilder().setError(true).setErrorCode("demo").setErrorMessage("This is a demo device")
        .setRespAck(Ack.newBuilder().setOk(false).setErrorMsg("This is a demo device").setCode("demo")).build()
    private val silentNo = RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(false)).build()

    private class Device(val answer: (ReqEnvelope) -> RespEnvelope) {
        val sent = mutableListOf<ReqEnvelope>()
        val send: FeedSend = { build ->
            val req = ReqEnvelope.newBuilder().also(build).build()
            sent += req
            answer(req)
        }
    }

    @Test fun aCommentTheDeviceTookIsNoFailure() = runBlocking {
        val dev = Device { ok }
        assertNull(commentRefusal(dev.send, "p1", "Nice"))
        val req = dev.sent.single().reqNewSocialComment
        assertEquals("p1", req.pubUuid)
        assertEquals("Nice", req.comment)
    }

    @Test fun aRefusedCommentSaysTheDevicesWordsOrOurs() = runBlocking {
        assertEquals(UiText.Raw("This is a demo device"), commentRefusal(Device { demo }.send, "p1", "Nice"))
        assertEquals(S.appSocialPostCommentFailed(), commentRefusal(Device { silentNo }.send, "p1", "Nice"))
        assertEquals(S.appSocialPostCommentFailed(), commentRefusal(Device { throw IOException("socket closed") }.send, "p1", "Nice"))
        assertEquals("Couldn't post your comment", S.appSocialPostCommentFailed().english())
    }

    @Test fun aCancelledCommentIsNoFailure() {
        try {
            runBlocking { commentRefusal(Device { throw CancellationException("left") }.send, "p1", "Nice") }
            fail("the cancellation went through")
        } catch (_: CancellationException) {
        }
    }

    @Test fun afterACommentThatPostAloneIsAskedFor() = runBlocking {
        val fresh = SocialPublication.newBuilder().setUuid("p2").setText("two")
            .addComments(Comment.newBuilder().setCommentUuid("c1").setComment("Nice")).build()
        val dev = Device { RespEnvelope.newBuilder().setRespPublication(RespPublication.newBuilder().setPublication(fresh)).build() }
        val p = fetchPost(dev.send, "p2")
        assertEquals(fresh, p)
        // One request, ReqGetPublication for that post - not the feed's pages.
        assertEquals(ReqEnvelope.PayloadCase.REQ_GET_PUBLICATION, dev.sent.single().payloadCase)
        assertEquals("p2", dev.sent.single().reqGetPublication.pubUuid)
        // Only that post changes; the pending comment is the device's now.
        val pending = SocialPublication.newBuilder().setUuid("p2").addComments(Comment.newBuilder().setCommentUuid("pending-x")).build()
        val others = listOf(SocialPublication.newBuilder().setUuid("p1").build(), pending, SocialPublication.newBuilder().setUuid("p3").build())
        val after = SocialFeedViewModel.State(posts = others).replacingPost(p!!)
        assertEquals(listOf("p1", "p2", "p3"), after.posts.map { it.uuid })
        assertEquals(listOf("c1"), after.posts[1].commentsList.map { it.commentUuid })
        assertTrue(after.posts[0] === others[0])
        // Nothing came: no post.
        assertNull(fetchPost(Device { silentNo }.send, "p2"))
        assertNull(fetchPost(Device { throw IOException("no answer") }.send, "p2"))
    }

    @Test fun aRefusedDeleteSaysWhy() = runBlocking {
        val build: (ReqEnvelope.Builder) -> Unit = { it.setReqDelSocialPublication(DelSocialPublication.newBuilder().setPubUuid("p1")) }
        assertNull(deleteRefusal(Device { ok }.send, S.appSocialPostDeleteFailed(), build))
        assertEquals(UiText.Raw("This is a demo device"), deleteRefusal(Device { demo }.send, S.appSocialPostDeleteFailed(), build))
        assertEquals(S.appSocialPostDeleteFailed(), deleteRefusal(Device { silentNo }.send, S.appSocialPostDeleteFailed(), build))
        assertEquals(S.appSocialPostDeleteCommentFailed(), deleteRefusal(Device { throw IOException("x") }.send, S.appSocialPostDeleteCommentFailed(), build))
        assertEquals("Couldn't delete the post", S.appSocialPostDeleteFailed().english())
        assertEquals("Couldn't delete the comment", S.appSocialPostDeleteCommentFailed().english())
    }

    @Test fun theVideoFallbackStopsAtFiftyMegabytes() {
        // Not known (an older device's 0): let through, as before.
        assertTrue(fallbackAllowed(0))
        assertTrue(fallbackAllowed(FEED_FALLBACK_MAX_BYTES))
        assertTrue(!fallbackAllowed(FEED_FALLBACK_MAX_BYTES + 1))
        assertEquals(50L * 1024 * 1024, FEED_FALLBACK_MAX_BYTES)
    }
}
