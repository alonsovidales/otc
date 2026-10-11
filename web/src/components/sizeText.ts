// SPDX-License-Identifier: AGPL-3.0-or-later
//
// A size in bytes as Files, the share dialog, Settings > Shared Links and
// a shared gallery's page write it: B, KB, MB or GB (of 1024 each), with a
// fixed number of digits after the point and no grouping ("1023.5 KB").
// The unit's symbol comes from the catalog (French writes "Ko"), the
// number is formatted for the language (uiText.ts plainNumber), so English
// reads exactly as the hand-made toFixed() it replaces.

import type { TFunction } from "../i18n";
import { plainNumber } from "./uiText";

/** Digits after the point for KB, MB and GB. */
export type SizeDigits = readonly [kb: number, mb: number, gb: number];

export function sizeText(t: TFunction, bytes: number, digits: SizeDigits): string {
  const fixed = (v: number, d: number) =>
    plainNumber(t, v, { minimumFractionDigits: d, maximumFractionDigits: d, useGrouping: false });
  if (bytes >= 1 << 30) return t("web.files.size.gb", { size: fixed(bytes / (1 << 30), digits[2]) });
  if (bytes >= 1 << 20) return t("web.files.size.mb", { size: fixed(bytes / (1 << 20), digits[1]) });
  if (bytes >= 1 << 10) return t("web.files.size.kb", { size: fixed(bytes / (1 << 10), digits[0]) });
  return t("web.files.size.b", { size: plainNumber(t, bytes, { useGrouping: false }) });
}
