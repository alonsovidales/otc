import type { TabKey } from "../components/TopTabs";

// Issue #53: a reload used to always drop the user back on the default tab
// (Profile, or Social right after sign-in) regardless of what they were
// actually looking at — this persists the current tab so a reload restores
// the exact view instead. Session-local to this browser (localStorage),
// same mechanism issue #46 already uses to persist the login itself.
const cTabStorageKey = "otc_last_tab";

const cValidTabs: readonly TabKey[] = ["Profile", "Social", "SignIn", "AdminPannel", "PhotoGallery", "Settings"];

export function saveLastTab(tab: TabKey) {
  try {
    localStorage.setItem(cTabStorageKey, tab);
  } catch {
    // Storage can be unavailable (private browsing, quota) — reload just
    // won't restore the view in that case, not worth surfacing an error.
  }
}

export function loadLastTab(): TabKey | null {
  try {
    const stored = localStorage.getItem(cTabStorageKey);
    return (cValidTabs as string[]).includes(stored ?? "") ? (stored as TabKey) : null;
  } catch {
    return null;
  }
}
