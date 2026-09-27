// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Issue #142: friends are always <name>.off-the.cloud - the device refuses
// any other domain (social.isAllowedFriendDomain) - so the add-friend box
// takes only the name, with the suffix shown fixed next to it. Same rule
// as FriendshipsView.swift / FriendshipsView.kt.

export const FRIEND_TLD = "off-the.cloud";

const NAME = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;

/** The friend's full domain from what was typed or pasted (a bare name, the
 *  full domain or a link to it), or null when it isn't a device name. */
export function friendDomain(input: string): string | null {
  let s = input.trim().toLowerCase();
  s = s.replace(/^[a-z]+:\/\//, "").split(/[/?#]/)[0];
  if (s.endsWith("." + FRIEND_TLD)) s = s.slice(0, -(FRIEND_TLD.length + 1));
  return NAME.test(s) ? `${s}.${FRIEND_TLD}` : null;
}
