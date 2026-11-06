// SPDX-License-Identifier: AGPL-3.0-or-later

// What the slideshow (Slideshow.tsx) puts on the screen for a photo: the
// photo itself, at the size the screen draws it (slideshowRules.ts fitSize), and
// a blurred, darkened copy filling the screen behind it - both decoded
// before the photo's turn, so it never shows half drawn.
//
// The photo is the viewer's full size (GetFile, kept in fullSizeCache as
// the viewer keeps it), or its big thumbnail where that can't be had (the
// device answered with none, a picture this browser can't decode); one
// with neither is skipped (SlideGone). A request that got no answer, or
// the bridge's answer for the device, is no verdict on the photo: it is
// asked again (any other error rejects as it came).
//
// The full size is decoded once and drawn at the screen's size into a
// JPEG of its own: a 12-megapixel photo decoded whole is 48 MB, a wall
// screen's 2048 x 1536 copy 12 MB, and moving the smaller one is what an
// older device's browser keeps smooth. The copy behind it is drawn tiny
// and blurred (canvas filter) and scaled up by the page.

import { useWS } from "../net/useWS";
import { isBridgeAnswer } from "../net/deviceStatus";
import type { File as MsgFile, RespEnvelope } from "../proto/messages";
import { fullSizeCache } from "./fullSizeCache";
import { safeImage } from "./inlineTypes";
import { fitSize } from "./slideshowRules";

/** A photo ready to show; release() frees what it holds. */
export interface Slide {
  photo: string;
  backdrop: string;
  // The backdrop came blurred (canvas filter); else the page blurs it.
  blurred: boolean;
  release(): void;
}

/** A photo that can't be shown: skipped. */
export class SlideGone extends Error {}

/** No answer for it (the connection, the bridge): asked again later. */
export class SlideAway extends Error {}

const toBlob = (content: Uint8Array | undefined, mime: string): Blob | null => {
  if (!content || content.length === 0) return null;
  return new Blob([content as BlobPart], { type: mime });
};

/** A request whose missing answer (or the bridge's for the device) is
 *  SlideAway: asked again once the device is back. */
export async function askDevice(fill: Parameters<typeof useWS.request>[0]): Promise<RespEnvelope> {
  let resp: RespEnvelope;
  try {
    resp = await useWS.request(fill);
  } catch {
    // No connection, or nothing came.
    throw new SlideAway();
  }
  if (isBridgeAnswer(resp)) throw new SlideAway();
  return resp;
}

async function fullSize(file: MsgFile): Promise<Blob | null> {
  const kept = fullSizeCache.get(file.hash);
  if (kept) return kept;
  const resp = await askDevice((e) => { e.payload = { $case: "reqGetFile", reqGetFile: { path: file.path, hash: "" } }; });
  const got = resp.payload?.$case === "respFile" ? resp.payload.respFile : null;
  // A type no <img> may show from a blob: URL (an SVG: inlineTypes.ts) is
  // left to its thumbnail, as the viewer leaves it.
  if (!got || !safeImage(got.mime || file.mime)) return null;
  const blob = toBlob(got.content, got.mime || file.mime);
  if (blob) fullSizeCache.put(got.hash || file.hash, blob);
  return blob;
}

async function bigThumbnail(file: MsgFile): Promise<Blob | null> {
  const resp = await askDevice((e) => {
    e.payload = { $case: "reqGetThumbnails", reqGetThumbnails: { paths: [file.path], smallThumbnails: false } };
  });
  if (resp.payload?.$case !== "respListOfFiles") return null;
  const f = resp.payload.respListOfFiles.files.find((x) => x.path === file.path);
  return toBlob(f?.content, "image/jpeg");
}

async function decode(blob: Blob): Promise<ImageBitmap | null> {
  try {
    return await createImageBitmap(blob, { imageOrientation: "from-image" });
  } catch {
    return null;
  }
}

function canvasOf(w: number, h: number): HTMLCanvasElement {
  const c = document.createElement("canvas");
  c.width = w;
  c.height = h;
  return c;
}

const jpegOf = (c: HTMLCanvasElement, quality: number): Promise<Blob | null> =>
  new Promise((resolve) => c.toBlob((b) => resolve(b), "image/jpeg", quality));

// Whether this browser's canvas blurs (Chromium and Firefox do; Safari
// before 18 keeps the property but draws as if it weren't there).
let canvasBlur: boolean | null = null;
function canvasBlurs(): boolean {
  if (canvasBlur !== null) return canvasBlur;
  try {
    const c = canvasOf(8, 1);
    const ctx = c.getContext("2d", { willReadFrequently: true });
    if (!ctx || !("filter" in ctx)) return (canvasBlur = false);
    ctx.fillStyle = "#000";
    ctx.fillRect(0, 0, 8, 1);
    ctx.filter = "blur(2px)";
    ctx.fillStyle = "#fff";
    ctx.fillRect(3, 0, 2, 1);
    // Blurred, the white spreads into the pixel beside it.
    canvasBlur = ctx.getImageData(2, 0, 1, 1).data[0] > 10;
  } catch {
    canvasBlur = false;
  }
  return canvasBlur;
}

// The copy behind the photo: tiny, cover-fit, blurred and darkened.
async function backdropOf(src: CanvasImageSource, w: number, h: number): Promise<{ blob: Blob | null; blurred: boolean }> {
  const bw = 96;
  const bh = Math.max(1, Math.round((bw * window.innerHeight) / Math.max(1, window.innerWidth)));
  const c = canvasOf(bw, bh);
  const ctx = c.getContext("2d");
  if (!ctx) return { blob: null, blurred: false };
  const cover = Math.max(bw / w, bh / h);
  const dw = w * cover;
  const dh = h * cover;
  const blurred = canvasBlurs();
  if (blurred) ctx.filter = "blur(4px) brightness(0.55) saturate(1.15)";
  // Drawn a little past the edges, so the blur has picture to take in.
  ctx.drawImage(src, (bw - dw) / 2 - 4, (bh - dh) / 2 - 4, dw + 8, dh + 8);
  return { blob: await jpegOf(c, 0.8), blurred };
}

// A URL whose picture is decoded: the <img> that shows it draws at once.
async function decoded(url: string): Promise<void> {
  const img = new Image();
  img.src = url;
  await img.decode();
}

/** Loads file for a boxW x boxH screen of dpr. Rejects with SlideGone
 *  (skip it) or SlideAway (ask again later). */
export async function loadSlide(file: MsgFile, boxW: number, boxH: number, dpr: number): Promise<Slide> {
  const urls: string[] = [];
  const url = (b: Blob) => {
    const u = URL.createObjectURL(b);
    urls.push(u);
    return u;
  };
  const release = () => { for (const u of urls.splice(0)) URL.revokeObjectURL(u); };
  try {
    let blob = await fullSize(file);
    let bitmap = blob ? await decode(blob) : null;
    if (!bitmap) {
      // None, or one this browser can't decode (an SVG, a RAW): the big
      // thumbnail, the picture as the device shows it.
      blob = await bigThumbnail(file);
      bitmap = blob ? await decode(blob) : null;
    }
    if (!blob || !bitmap) throw new SlideGone();
    let photo: Blob = blob;
    try {
      const want = fitSize(bitmap.width, bitmap.height, boxW, boxH, dpr);
      if (want.w < bitmap.width) {
        const c = canvasOf(want.w, want.h);
        const ctx = c.getContext("2d");
        if (ctx) {
          ctx.imageSmoothingEnabled = true;
          ctx.imageSmoothingQuality = "high";
          ctx.drawImage(bitmap, 0, 0, want.w, want.h);
          photo = (await jpegOf(c, 0.92)) ?? blob;
        }
      } else if (blob.type !== "image/jpeg" && blob.type !== "image/png" && blob.type !== "image/webp") {
        // A HEIC or an AVIF the browser decodes, as it decodes it.
        const c = canvasOf(bitmap.width, bitmap.height);
        c.getContext("2d")?.drawImage(bitmap, 0, 0);
        photo = (await jpegOf(c, 0.92)) ?? blob;
      }
      // The grid's small thumbnail when the page brought it, else the photo.
      const small = toBlob(file.content, "image/jpeg");
      const smallBitmap = small ? await decode(small) : null;
      const back = smallBitmap
        ? await backdropOf(smallBitmap, smallBitmap.width, smallBitmap.height)
        : await backdropOf(bitmap, bitmap.width, bitmap.height);
      smallBitmap?.close();
      const photoUrl = url(photo);
      const backUrl = url(back.blob ?? photo);
      await Promise.all([decoded(photoUrl), decoded(backUrl)]);
      return { photo: photoUrl, backdrop: backUrl, blurred: back.blurred && !!back.blob, release };
    } finally {
      bitmap.close();
    }
  } catch (err) {
    release();
    if (err instanceof SlideAway || err instanceof SlideGone) throw err;
    // A decode that failed after all: the photo can't be shown.
    throw new SlideGone();
  }
}
