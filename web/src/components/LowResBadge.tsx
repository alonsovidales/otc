// SPDX-License-Identifier: AGPL-3.0-or-later
//
// "Low res": shown on a photo opened full-screen while it is still its
// thumbnail - in the viewer Images and Files share (MediaViewer) and in
// Social's. It sits on the photo's own lower-right corner: the image is
// letterboxed (object-fit: contain), so the corner is worked out from its
// natural size and the box it is drawn in. The apps show the same pill.
import { useCallback, useEffect, useState, type RefObject } from "react";
import "./LowResBadge.css";

export default function LowResBadge({ imgRef, loading }: { imgRef: RefObject<HTMLImageElement | null>; loading: boolean }) {
  const [pos, setPos] = useState<{ right: number; bottom: number } | null>(null);
  const place = useCallback(() => {
    const img = imgRef.current;
    if (!img || !img.naturalWidth || !img.clientWidth) return;
    const scale = Math.min(img.clientWidth / img.naturalWidth, img.clientHeight / img.naturalHeight);
    const w = img.naturalWidth * scale, h = img.naturalHeight * scale;
    // Offsets of the drawn photo inside the img box, plus the box's own
    // place inside the badge's positioned parent.
    setPos({
      right: (img.clientWidth - w) / 2 + (img.offsetParent ? (img.offsetParent as HTMLElement).clientWidth - img.offsetLeft - img.clientWidth : 0) + 12,
      bottom: (img.clientHeight - h) / 2 + (img.offsetParent ? (img.offsetParent as HTMLElement).clientHeight - img.offsetTop - img.clientHeight : 0) + 12,
    });
  }, [imgRef]);
  useEffect(() => {
    const img = imgRef.current;
    if (!img) return;
    place();
    img.addEventListener("load", place);
    window.addEventListener("resize", place);
    return () => { img.removeEventListener("load", place); window.removeEventListener("resize", place); };
  }, [imgRef, place]);
  return (
    <span className="lowres-badge" style={pos ? { right: pos.right, bottom: pos.bottom } : undefined} aria-label="Low resolution - the full photo is loading">
      {loading && <span className="lowres-spin" aria-hidden="true" />}Low res
    </span>
  );
}
