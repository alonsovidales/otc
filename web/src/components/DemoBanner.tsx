// SPDX-License-Identifier: AGPL-3.0-or-later
//
// A public demo device (net/demo.ts): signed in, a banner above every page
// says so, with a link to setting up a device of one's own; signed out, a
// note with Try the demo, which signs in with the demo's published
// password.
import { useState } from "react";
import { useT } from "../i18n";
import { demoPassword } from "../net/demo";
import { signInWithPassword } from "../net/signIn";
import type { UiText } from "./uiText";
import "./DemoBanner.css";

const cOwnDeviceUrl = "https://off-the.cloud/";

export function DemoBanner() {
  const t = useT();
  return (
    <div className="demo-banner" role="note">
      <span className="demo-text">{t("web.shell.demo.banner")}</span>
      <a className="demo-link" href={cOwnDeviceUrl} target="_blank" rel="noopener noreferrer">
        {t("web.shell.demo.get_own")}
      </a>
    </div>
  );
}

export function DemoWelcome({ onSignedIn }: { onSignedIn: () => void }) {
  const t = useT();
  const [busy, setBusy] = useState(false);
  const [refusal, setRefusal] = useState<UiText | null>(null);
  const tryIt = async () => {
    setBusy(true);
    setRefusal(null);
    try {
      const result = await signInWithPassword(demoPassword());
      if (result.ok) onSignedIn();
      else if (result.reason !== "new-device") setRefusal(() => result.message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <section className="demo-welcome">
      <p className="demo-text">{t("web.shell.demo.banner")}</p>
      <button type="button" className="demo-try" onClick={() => void tryIt()} disabled={busy || !demoPassword()} aria-busy={busy}>
        {t("web.shell.demo.try")}
      </button>
      {refusal && <p className="demo-refusal" role="alert">{refusal(t)}</p>}
    </section>
  );
}
