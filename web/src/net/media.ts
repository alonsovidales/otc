// SPDX-License-Identifier: AGPL-3.0-or-later

// Issue #110: getting a URL a <video> element can stream from, instead of
// downloading a whole video over the socket before it can start.
//
// The device decides whether streaming is worth it at all - below its own
// size threshold it answers with no URL, because a small clip already
// arrives in one socket round trip and streaming would add another one
// (this call) before any bytes moved. So a null result here is a normal
// answer meaning "fetch it the old way", not a failure.
import { useWS } from "./useWS";
import type { RespEnvelope } from "../proto/messages";

// Where /media/<token> lives. Normally nowhere: the page is served by the
// device itself (on the LAN) or by the bridge on that device's subdomain,
// so a relative URL is already right and works in both without this code
// knowing which. The exception is the native app, which loads this web
// app from its own bundle and tells it which device to talk to - there
// the URL has to be resolved against that device instead.
const mediaBase = (): string => {
  const endpoint = window.__OTC_CONFIG?.endpoint;
  if (!endpoint) return "";
  try {
    const url = new URL(endpoint);
    url.protocol = url.protocol === "wss:" ? "https:" : "http:";
    return url.origin;
  } catch {
    return "";
  }
};

export type MediaRef =
  | { path: string }
  | { pubUuid: string; hash: string };

/**
 * Asks the device for a streamable URL for one file. Returns null when
 * the device says it isn't worth streaming, or when anything at all goes
 * wrong - every caller has the old whole-file fetch to fall back on, and
 * falling back quietly is better than failing to show a video.
 */
export async function requestStreamURL(ref: MediaRef): Promise<string | null> {
  try {
    const resp: RespEnvelope = await useWS.request(e => {
      (e as any).payload = { $case: "reqGetMediaUrl", reqGetMediaUrl: ref };
    });
    if (resp.payload?.$case !== "respMediaUrl") return null;
    const url = resp.payload.respMediaUrl.url;
    if (!url) return null;

    return mediaBase() + url;
  } catch {
    return null;
  }
}

// Streaming only ever applies to video. An image has to be complete
// before it can be shown at all, so there's nothing to gain - and the
// device converts HEIC to JPEG on its way out through the normal fetch
// (issue #44), which a raw byte-range stream would bypass and hand the
// browser something it can't display.
export const canStream = (mime?: string) => !!mime && mime.startsWith("video/");
