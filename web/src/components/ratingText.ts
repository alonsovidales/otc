// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Rating's words (rating.ts): what a refused change says, and what a tile
// says of its stars and comment to a screen reader.

import type { TFunction } from "../i18n";
import type { RatingRefusal, RatingShow, RatingSort } from "./rating";
import { asIs, say, type UiText } from "./uiText";

/** A change the device didn't take: a device from before Rating
 *  (unknown_payload) needs an update; otherwise its own words, else ours. */
export function ratingRefusalText(r: RatingRefusal): UiText {
  if (r.code === "unknown_payload") return say("web.images.rating.needs_update");
  return r.said ? asIs(r.said) : say("web.images.rating.failed");
}

/** A rated tile's description ("4 stars, with a comment"); undefined for
 *  a photo without stars or comment. */
export function ratingDescription(t: TFunction, stars: number, commented: boolean): string | undefined {
  if (stars > 0) {
    const s = t("web.images.rating.stars", { count: stars });
    return commented ? t("web.images.rating.tile_both", { stars: s }) : s;
  }
  return commented ? t("web.images.rating.tile_comment") : undefined;
}

/** The Show menu's choices, in its order. */
export function showLabel(t: TFunction, show: RatingShow): string {
  switch (show) {
    case "5": return t("web.images.rating.show_5");
    case "4": return t("web.images.rating.show_4");
    case "3": return t("web.images.rating.show_3");
    case "2": return t("web.images.rating.show_2");
    case "rated": return t("web.images.rating.show_rated");
    case "unrated": return t("web.images.rating.show_unrated");
    case "comment": return t("web.images.rating.show_comment");
    default: return t("web.images.rating.show_all");
  }
}

/** The Sort by menu's choices. */
export const sortLabel = (t: TFunction, sort: RatingSort): string =>
  sort === "rating" ? t("web.images.rating.sort_rating") : t("web.images.rating.sort_date");
