// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Where a viewer's stars and comment go (RatingPanel.tsx): a bar under the
// photo where there is room, or - on a phone, upright or on its side, and
// any window too short for both - a sheet the viewer opens from its Rate
// button, so the comment box never sits over the photo uninvited.

import { useEffect, useState } from "react";

const cSheetQuery = "(max-width: 599px), (max-height: 559px)";

const matches = () => typeof window !== "undefined" && typeof window.matchMedia === "function" && window.matchMedia(cSheetQuery).matches;

/** True while the stars and comment are a sheet the viewer opens. */
export function useRatingSheet(): boolean {
  const [sheet, setSheet] = useState(matches);
  useEffect(() => {
    if (typeof window.matchMedia !== "function") return;
    const mq = window.matchMedia(cSheetQuery);
    const on = () => setSheet(mq.matches);
    on();
    mq.addEventListener("change", on);
    return () => mq.removeEventListener("change", on);
  }, []);
  return sheet;
}
