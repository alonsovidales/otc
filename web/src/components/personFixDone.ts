// SPDX-License-Identifier: AGPL-3.0-or-later
//
// What Not this person and Someone else… (personFix.tsx) did, and the note
// Images shows for it - apart from the dialogs, so both can be imported
// on their own.
import type { TFunction } from "../i18n";
import type { Person } from "../proto/messages";

/** What was done: the photos (content hashes) that left `from`, for no
 *  one (removed) or for `to` (moved). A person not in the list (yet) is
 *  undefined, and worded as an unnamed one. */
export type PersonFixDone = {
  hashes: ReadonlySet<string>;
  from?: Person;
  to?: Person;
  moved: boolean;
};

/** Every photo once: the content hashes a selection holds. */
export const uniqueHashes = (items: readonly { hash?: string }[]): string[] =>
  [...new Set(items.map((f) => f.hash ?? "").filter((h) => h !== ""))];

/** A person's name, "" for one without (or not in the list). */
export const named = (p?: Person) => (p?.name.trim() ? p.name.trim() : "");

/** The note at the foot of Images once it is done. */
export function personFixText(t: TFunction, done: PersonFixDone): string {
  const count = done.hashes.size;
  if (done.moved) {
    const name = named(done.to);
    return name ? t("web.images.person.moved", { count, name }) : t("web.images.person.moved_unnamed", { count });
  }
  const name = named(done.from);
  return name ? t("web.images.person.removed", { count, name }) : t("web.images.person.removed_unnamed", { count });
}
