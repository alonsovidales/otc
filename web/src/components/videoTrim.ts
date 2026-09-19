// SPDX-License-Identifier: AGPL-3.0-or-later

// Issue #108: the trim vocabulary shared between the editor
// (VideoTrimmer.tsx) and the composer that opens it
// (NewPostPicker.tsx). Its own module rather than an extra export on the
// component, so neither file exports a mix of components and helpers.

export type TrimRange = { start: number; end: number };

export const formatTimecode = (secs: number) => {
  if (!isFinite(secs) || secs < 0) secs = 0;
  const m = Math.floor(secs / 60);
  const s = Math.floor(secs % 60);
  const tenths = Math.floor((secs * 10) % 10);
  return `${m}:${String(s).padStart(2, "0")}.${tenths}`;
};
