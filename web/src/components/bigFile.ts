// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Something big put together whole in this page's memory before the
// browser saves it - a ZIP link, a file Files reads in pieces to save or
// show in a tab, a shared gallery's originals - can take the tab down with
// it, a phone's first (F11 of the 2026-10 review). Above about 500 MB the
// page asks first (BigFileDialog.tsx). Until the device can hand those to
// the browser from a link, as it does a video it streams.

/** Above this many bytes, a download held in memory asks first. */
export const cBigInMemoryBytes = 500 << 20;

/** Whether a file of `bytes` asks before it is read into memory. */
export const isTooBigForMemory = (bytes: number) => bytes > cBigInMemoryBytes;
