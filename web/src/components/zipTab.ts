import type { TFunction } from "../i18n";

// A tab for a ZIP still being made, opened at the click: a browser refuses
// one opened more than a few seconds after it, and making the archive can
// take longer. It goes to the link once there is one.
export function openWaitingTab(t: TFunction): Window | null {
  const tab = window.open("", "_blank");
  if (!tab) return null;
  tab.opener = null;
  try {
    tab.document.title = t("web.images.share.preparing_zip");
    tab.document.body.style.cssText =
      "margin:0;min-height:100vh;display:grid;place-items:center;background:#242424;color:#eaf2ef;font:16px system-ui,sans-serif";
    tab.document.body.textContent = t("web.images.share.zip_tab");
  } catch {
    // A blank tab still does the job.
  }
  return tab;
}
