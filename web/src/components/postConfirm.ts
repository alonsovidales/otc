// SPDX-License-Identifier: AGPL-3.0-or-later
//
// A post whose answer never came (the composer, NewPostPicker.tsx). The
// device finishes a post before it answers - a video is cut and shrunk
// first, minutes on a Pi - so the socket closing meanwhile, or the bridge
// giving up waiting for the device, leaves the post made or on its way with
// nothing on the page saying so; Publish again then gave every friend the
// post twice. What publishing came to is told apart here (publishOutcome),
// and a post that may have been made is looked for among the newest posts
// (findSentPost): the composer closes as posted once it is there.
//
// Tested by web/scripts/post-confirm-test.mjs.
import type { RespEnvelope, SocialPublication } from "../proto/messages";
import { isBridgeAnswer } from "../net/deviceStatus";
import { NoAnswer } from "../net/ws";

/** What a publish came to: posted (the device's answer has the post),
 *  refused (the device's own answer, with its words), unknown (it went and
 *  no answer came: the socket closed, nothing in time, or the bridge
 *  answering for a device it lost on the way - it may be posted) or unsent
 *  (it never left this browser: no socket, the sign-in before it failed). */
export type PublishOutcome = "posted" | "refused" | "unknown" | "unsent";

/** The outcome of an answer, or of the error the request threw. */
export function publishOutcome(answer: { resp: RespEnvelope } | { error: unknown }): PublishOutcome {
  if ("error" in answer) {
    const e = answer.error;
    // Anything else thrown is no answer either, and nothing says it
    // didn't go: as one that went.
    return e instanceof NoAnswer && !e.sent ? "unsent" : "unknown";
  }
  const resp = answer.resp;
  if (resp.payload?.$case === "respNewSocial" && resp.payload.respNewSocial.uuid) return "posted";
  return isBridgeAnswer(resp) ? "unknown" : "refused";
}

/** What was sent: the caption, how many photos and videos, when (this
 *  browser's clock), and the posts already there as it went (`before`:
 *  their uuids, those the feed showed) - none of those is it. */
export type SentPost = { text: string; files: number; sentAt: number; before?: readonly string[] };

/** How far the device's clock may be from this browser's (a post is dated
 *  by the device as it is made, which is after it was sent). */
export const cClockSlackMs = 15 * 60_000;

/** The post among the newest that is the one sent: one of the owner's own,
 *  not one that was there before it went, with the same caption and no
 *  more photos and videos than were sent (the feed leaves out an item whose
 *  thumbnail is missing), dated no earlier than cClockSlackMs before it was
 *  sent. Without `before` an earlier post of the same words made in those
 *  minutes was taken for it, and the composer closed as posted though the
 *  device never made the new one. */
export function findSentPost(pubs: readonly SocialPublication[], sent: SentPost): SocialPublication | null {
  const before = new Set(sent.before ?? []);
  return pubs.find((p) =>
    p.own && !before.has(p.uuid) && p.text === sent.text && p.files.length <= sent.files &&
    (!p.dateTime || p.dateTime.getTime() >= sent.sentAt - cClockSlackMs)) ?? null;
}

/** How many of the newest posts each look asks for: the post, made a
 *  moment ago, is among the first unless friends posted meanwhile. */
export const cLookPosts = 4;

/** When the next look for it goes (after `looks` of them): soon at first,
 *  then every half minute while the composer stays open - a long video
 *  can take its device many minutes. */
export function nextLookMs(looks: number): number {
  return [3_000, 7_000, 15_000][looks] ?? 30_000;
}
