// SPDX-License-Identifier: AGPL-3.0-or-later

// STUB - replaced by the real date scrubber.
export type ScrubBucket = { month: string; count: number };

type Props = {
  // The whole timeline under the current filter, newest first ("YYYY-MM").
  buckets: ScrubBucket[];
  // The month of the photos at the top of the screen now, for the marker.
  currentMonth: string | null;
  // While dragging: the month under the pointer; null when the drag is
  // cancelled without a jump.
  onPreview: (bucket: ScrubBucket | null) => void;
  // Released on a month: show the photos from there.
  onJump: (month: string) => void;
};

export default function DateScrubber(_: Props) {
  return null;
}
