// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Keeping the keyboard inside a dialog (ConfirmDialog's Modal, the shared
// gallery's viewer): its own module, so the public /shared page takes it
// without the app's dialog styles.

/** What Tab goes through. */
export const cFocusable = "button, [href], input, textarea, select, video[controls], [tabindex]:not([tabindex='-1'])";

/** Tab and Shift+Tab within `box`: from its last control to its first and
 *  back, and into it from wherever the focus fell (a control that was
 *  removed, or disabled while busy, leaves it on the page). */
export function keepTabInside(box: HTMLElement, e: KeyboardEvent) {
  const all = [...box.querySelectorAll<HTMLElement>(cFocusable)]
    .filter((el) => !(el as HTMLButtonElement).disabled && el.offsetParent !== null);
  if (!all.length) { e.preventDefault(); box.focus(); return; }
  const first = all[0];
  const last = all[all.length - 1];
  const active = document.activeElement;
  if (!box.contains(active)) { e.preventDefault(); (e.shiftKey ? last : first).focus(); }
  else if (e.shiftKey && (active === first || active === box)) { e.preventDefault(); last.focus(); }
  else if (!e.shiftKey && active === last) { e.preventDefault(); first.focus(); }
}
