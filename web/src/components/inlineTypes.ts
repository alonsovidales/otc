// SPDX-License-Identifier: AGPL-3.0-or-later

// Security advisory (web app, HTML/SVG in the app's origin): a blob: URL is
// of this app's own origin, so an HTML or SVG file opened from one - in a
// tab, or by "Open image in new tab" on a picture shown from one - ran with
// full access to the app (its session token, the window that opened it).
// Only types that can't run script get a blob: URL of their own type and
// are opened or shown; everything else is downloaded (application/
// octet-stream), never rendered. Files decides by the device's reading of
// the content; Social by the type a post's item carries (a friend's device
// says what it is).
export const INLINE_EXT = /\.(pdf|txt|md|csv|log|json|jpe?g|png|gif|webp|bmp|heic|heif|avif|mp4|mov|m4v|webm|mkv|mp3|m4a|aac|wav|ogg|oga|flac)$/i;
export const INLINE_MIME = /^(application\/pdf|text\/plain|text\/csv|text\/markdown|application\/json|image\/(jpeg|png|gif|webp|bmp|heic|heif|avif)|video\/[\w.+-]+|audio\/[\w.+-]+)(;.*)?$/i;

/** A file name a browser tab may show (by its extension). */
export function canOpenInline(name: string): boolean { return INLINE_EXT.test(name); }

/** A type a blob: URL may carry for showing or opening. */
export function safeToOpen(mime: string): boolean { return INLINE_MIME.test(mime || ""); }

/** A photo type an <img> may show from a blob: URL. */
export function safeImage(mime: string): boolean { return /^image\//i.test(mime || "") && safeToOpen(mime); }

/** A video type a <video> may play from a blob: URL. */
export function safeVideo(mime: string): boolean { return /^video\//i.test(mime || "") && safeToOpen(mime); }
