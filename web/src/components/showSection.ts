// SPDX-License-Identifier: AGPL-3.0-or-later

// Another section of the app from inside one (an empty feed's way to
// Friends, a broken page's way to Images). App owns which section shows and
// hands the way to change it only to the menu (Sidebar's onSelect): this
// presses the menu's own item, as a click would - found by its section
// (data-tour, which the tour's ring uses too) in the menu beside the page,
// else in the closed drawer (a phone), whose picks close it. Where the menu
// isn't there (the whole app gave way to an error), the section is
// remembered as the last one open (net/uiState.ts) and the page reloads
// into it.
import type { TabKey } from "./nav";
import { saveLastTab } from "../net/uiState";

/** The menu's item for `tab`, pressed; false when there is none to press. */
export function pressMenuItem(tab: TabKey): boolean {
  const sel = `button.sb-item[data-tour="${CSS.escape(tab)}"]`;
  const item = document.querySelector<HTMLButtonElement>(`nav.sb-dock ${sel}`)
    ?? document.querySelector<HTMLButtonElement>(`nav.sb-drawer ${sel}`);
  if (!item) return false;
  item.click();
  return true;
}

/** Shows `tab`: through the menu, else by reloading into it. */
export function showSection(tab: TabKey) {
  if (pressMenuItem(tab)) return;
  saveLastTab(tab);
  window.location.reload();
}
