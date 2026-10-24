// SPDX-License-Identifier: AGPL-3.0-or-later

// A new device's first-run setup (issue #39): the owner's name and
// password, then its disks and its WiFi. App shows it, as the SignIn tab,
// only while the device says it is new; everyone else signs in from the
// top bar (components/TopSignIn.tsx), and both send the password through
// the same signInWithPassword - this one with setUp, the top bar's never
// to a new device.

import { useState, type ReactNode } from 'react'

import './SignIn.css'
import { useWS } from "../net/useWS";
import { signInWithPassword } from "../net/signIn";
import type { StorageDevice, WifiNetwork } from "../proto/messages";
import { useT, type TFunction } from "../i18n";
import { asIs, percentText, say, unitText, type UiText } from "../components/uiText";
import LanguagePicker from "../components/LanguagePicker";

type Step = "credentials" | "storage" | "wifi";

// A disk's size, in the language's own number format ("500.0 GB").
function formatSize(t: TFunction, bytes: bigint): string {
  const gb = Number(bytes) / (1000 * 1000 * 1000);
  return gb >= 1 ? unitText(t, gb, "gigabyte", 1) : unitText(t, Number(bytes) / (1000 * 1000), "megabyte", 0);
}

// A step's heading, with the language in the card's top-right corner above
// it (SignIn.css): a new device's setup comes before any Settings, so it is
// chosen here (sent to the device as soon as its password is set).
function StepHeading({ children }: { children: ReactNode }) {
  return (
    <div className="setup-head">
      <h3>{children}</h3>
      <LanguagePicker className="setup-lang lpk-phone" />
    </div>
  );
}

function SignIn({ isPrimary, container, onDone }: { isPrimary: boolean; container: boolean; onDone: () => void }) {
  const t = useT();
  // Issue #39: a device with no owner secret yet is treated as fresh out
  // of the box — instead of a plain password box (which would just
  // silently adopt whatever's typed as the permanent password with no
  // owner name or storage set up at all), walk through a short setup
  // instead: owner name + password, then how to use any attached disks.
  //
  // Issue #85: an additional user's instance (issue #82) shares the
  // primary's physical machine, so its storage and WiFi are already set up
  // and are not this user's to change - their setup is just a name and a
  // password (`isPrimary` false; true when the device couldn't say, so it
  // gets the full wizard rather than silently skipping real setup steps).
  // A device in a container (`container`) skips them too: the server it
  // runs on looks after its disks and network.
  const [step, setStep] = useState<Step>("credentials");

  const [ownerName, setOwnerName] = useState("");
  const [setupPassword, setSetupPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  // The notes of the steps (an error, the WiFi join under way), made in the
  // language on screen when shown (UiText).
  const [error, setError] = useState<UiText | null>(null);
  const [submitting, setSubmitting] = useState(false);

  const [devices, setDevices] = useState<StorageDevice[]>([]);
  const [selected, setSelected] = useState<string[]>([]);
  const [storageNote, setStorageNote] = useState<UiText | null>(null);

  const [networks, setNetworks] = useState<WifiNetwork[]>([]);
  const [chosenSsid, setChosenSsid] = useState("");
  const [wifiPassword, setWifiPassword] = useState("");
  const [wifiNote, setWifiNote] = useState<UiText | null>(null);

  const submitCredentials = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);

    // The same 8 the setup wizard asks for.
    if (setupPassword.length < 8) {
      setError(() => say("web.shell.setup.password_short"));
      return;
    }
    if (setupPassword !== confirmPassword) {
      setError(() => say("web.shell.setup.password_mismatch"));
      return;
    }

    setSubmitting(true);
    try {
      // The device takes the first password it is sent as its own: the
      // one sign-in allowed to reach a new device.
      const result = await signInWithPassword(setupPassword, { setUp: true });
      if (!result.ok) {
        // A new device's own setup never meets "new-device" (setUp).
        const note = result.reason === "wrong" || result.reason === "new-device" ? say("web.shell.setup.failed") : result.message;
        setError(() => note);
        return;
      }

      if (ownerName.trim()) {
        const resp = await useWS.request((e) => {
          (e as any).payload = {
            $case: "reqSetProfile",
            reqSetProfile: { name: ownerName.trim(), text: "", domain: "" },
          };
        });
        if (resp.payload?.$case === "respAck" && !resp.payload.respAck.ok) {
          // The device is set up and usable either way — the owner can
          // always set/fix their name later from Settings — so this isn't
          // worth blocking on, just worth surfacing.
          console.error("Could not save owner name:", resp.payload.respAck.errorMsg);
        }
      }

      // Issue #85: that's the whole of setup for an additional user - the
      // disks and the network belong to the machine, which the primary
      // already configured (and which the device refuses to let a child
      // instance touch anyway - see the guards on ReqSetupStorage/
      // ReqSetWifi). Asking would be offering a choice that isn't theirs
      // to make and wouldn't be honoured. The same for a device in a
      // container: its server's disks and network are the server's.
      if (!isPrimary || container) {
        onDone();
        return;
      }

      const devResp = await useWS.request((e) => {
        (e as any).payload = { $case: "reqListStorageDevices", reqListStorageDevices: {} };
      });
      if (devResp.payload?.$case === "respStorageDevices") {
        setDevices(devResp.payload.respStorageDevices.devices);
      }
      setStep("storage");
    } finally {
      setSubmitting(false);
    }
  };

  const toggleDevice = (path: string) => {
    setSelected((cur) => {
      if (cur.includes(path)) return cur.filter((p) => p !== path);
      if (cur.length >= 2) return cur; // at most 2 — see issue #39
      return [...cur, path];
    });
  };

  const submitStorage = async (e: React.FormEvent) => {
    e.preventDefault();
    setSubmitting(true);
    try {
      const resp = await useWS.request((e) => {
        (e as any).payload = { $case: "reqSetupStorage", reqSetupStorage: { devicePaths: selected } };
      });
      if (resp.error) {
        // Not fatal to finishing setup — storage can always be configured
        // later from Settings.
        setStorageNote(() => (resp.errorMessage ? asIs(resp.errorMessage) : say("web.shell.setup.storage_failed")));
      }
      // Building the array (if 1 or 2 disks were picked) happens in the
      // background from here — see raid_watch.py — so this only confirms
      // the request was saved, not that storage is ready yet.

      const netResp = await useWS.request((e) => {
        (e as any).payload = { $case: "reqListWifiNetworks", reqListWifiNetworks: {} };
      });
      if (netResp.payload?.$case === "respWifiNetworks") {
        setNetworks(netResp.payload.respWifiNetworks.networks);
      }
      setStep("wifi");
    } finally {
      setSubmitting(false);
    }
  };

  // Issue #38: this step only matters if you reached the wizard over the
  // device's own temporary "Off The Cloud" access point — joining a real
  // network here drops that AP, taking the browser's own connection to it
  // down along with it. A device that already has a working connection
  // (ethernet, or WiFi pre-set some other way) doesn't need this at all,
  // hence the skip option.
  const submitWifi = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!chosenSsid) return;
    setSubmitting(true);
    setWifiNote(null);
    try {
      const resp = await useWS.request((e) => {
        (e as any).payload = { $case: "reqSetWifi", reqSetWifi: { ssid: chosenSsid, password: wifiPassword } };
      });
      if (resp.error) {
        setWifiNote(() => (resp.errorMessage ? asIs(resp.errorMessage) : say("web.shell.setup.wifi_failed")));
        return;
      }
      const ssid = chosenSsid;
      setWifiNote(() => (tr: TFunction) => tr("web.shell.setup.joining", { ssid }));
    } finally {
      setSubmitting(false);
    }
  };

  if (step === "wifi") {
    return (
      <section className="sf-section setup-section">
        <StepHeading>{t("web.shell.setup.wifi")}</StepHeading>
        <p className="sf-hint">{t("web.shell.setup.wifi_intro")}</p>
        <form onSubmit={submitWifi}>
          {networks.map((n) => (
            <label key={n.ssid} className="sf-row" style={{ gridTemplateColumns: "24px 1fr", cursor: "pointer" }}>
              <input
                type="radio"
                name="wifi-ssid"
                checked={chosenSsid === n.ssid}
                onChange={() => setChosenSsid(n.ssid)}
              />
              <span>{n.secured ? "🔒 " : ""}{t("web.shell.setup.wifi_network", { ssid: n.ssid, signal: percentText(t, n.signal) })}</span>
            </label>
          ))}
          {networks.length === 0 && <p className="sf-hint">{t("web.shell.setup.no_networks")}</p>}
          {chosenSsid && (
            <div className="sf-row">
              <label htmlFor="sf-wifi-password">{t("common.password")}</label>
              <input
                id="sf-wifi-password"
                className="sf-input"
                type="password"
                value={wifiPassword}
                onChange={(e) => setWifiPassword(e.target.value)}
              />
            </div>
          )}
          {wifiNote && <p className="sf-note success">{wifiNote(t)}</p>}
          <div style={{ display: "flex", gap: 8 }}>
            <button className="sf-btn" disabled={!chosenSsid || submitting}>
              {submitting ? t("common.connecting") : t("common.connect")}
            </button>
            <button className="sf-btn" type="button" onClick={onDone}>{t("web.shell.setup.skip")}</button>
          </div>
        </form>
      </section>
    );
  }

  if (step === "storage") {
    return (
      <section className="sf-section setup-section">
        <StepHeading>{t("web.shell.setup.storage")}</StepHeading>
        <p className="sf-hint">
          {devices.length === 0 ? t("web.shell.setup.no_disks") : t("web.shell.setup.choose_disks")}
        </p>
        <form onSubmit={submitStorage}>
          {devices.map((d) => (
            <label key={d.path} className="sf-row" style={{ gridTemplateColumns: "24px 1fr", cursor: "pointer" }}>
              <input
                type="checkbox"
                checked={selected.includes(d.path)}
                onChange={() => toggleDevice(d.path)}
              />
              <span>
                {d.model
                  ? t("web.shell.setup.disk_model", { path: d.path, size: formatSize(t, d.sizeBytes), model: d.model })
                  : t("web.shell.setup.disk", { path: d.path, size: formatSize(t, d.sizeBytes) })}
              </span>
            </label>
          ))}
          {storageNote && <p className="sf-note error">{storageNote(t)}</p>}
          <button className="sf-btn" disabled={submitting}>
            {submitting ? t("web.shell.setup.saving") : t("web.shell.setup.next_wifi")}
          </button>
        </form>
      </section>
    );
  }

  return (
    <section className="sf-section setup-section">
      <StepHeading>{t("web.shell.setup.welcome")}</StepHeading>
      {/* Issue #85: an additional user isn't setting up a device - the
          machine is already running, someone else set it up, and saying
          otherwise invites them to go looking for disks and WiFi that
          aren't theirs. */}
      <p className="sf-hint">
        {isPrimary ? t("web.shell.setup.intro_device") : t("web.shell.setup.intro_account")}
      </p>
      <form onSubmit={submitCredentials}>
        <div className="sf-row">
          <h3>{t("web.shell.setup.name")}</h3>
          <input
            className="sf-input"
            type="text"
            value={ownerName}
            onChange={(e) => setOwnerName(e.target.value)}
            placeholder={t("web.shell.setup.name_example")}
          />
        </div>
        <div className="sf-row">
          <h3>{t("common.password")}</h3>
          <input
            className="sf-input"
            type="password"
            autoComplete="new-password"
            value={setupPassword}
            onChange={(e) => setSetupPassword(e.target.value)}
          />
        </div>
        <div className="sf-row">
          <h3>{t("web.shell.setup.confirm_password")}</h3>
          <input
            className="sf-input"
            type="password"
            autoComplete="new-password"
            value={confirmPassword}
            onChange={(e) => setConfirmPassword(e.target.value)}
          />
        </div>
        {error && <p className="sf-note error">{error(t)}</p>}
        <button className="sf-btn" disabled={submitting}>
          {submitting ? t("web.shell.setup.setting_up") : isPrimary && !container ? t("web.shell.setup.next_storage") : t("web.shell.setup.finish")}
        </button>
      </form>
    </section>
  )
}

export default SignIn
