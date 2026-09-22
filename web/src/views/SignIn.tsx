// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useState } from 'react'

import './SignIn.css'
import { useWS } from "../net/useWS";
import { getDeviceSetupInfo } from "../net/pwCrypto";
import type { StorageDevice, WifiNetwork } from "../proto/messages";

type Step = "checking" | "login" | "credentials" | "storage" | "wifi";

function formatSize(bytes: bigint): string {
  const gb = Number(bytes) / (1000 * 1000 * 1000);
  return gb >= 1 ? `${gb.toFixed(1)} GB` : `${(Number(bytes) / (1000 * 1000)).toFixed(0)} MB`;
}

function SignIn({ onAuth, onDone }: { onAuth: (key: string) => Promise<boolean>; onDone: () => void }) {
  // Issue #39: a device with no owner secret yet is treated as fresh out
  // of the box — instead of a plain password box (which would just
  // silently adopt whatever's typed as the permanent password with no
  // owner name or storage set up at all), walk through a short setup
  // instead: owner name + password, then how to use any attached disks.
  const [step, setStep] = useState<Step>("checking");
  // Issue #85: an additional user's instance (issue #82) shares the
  // primary's physical machine, so its storage and WiFi are already set up
  // and are not this user's to change - their setup is just a name and a
  // password. Defaults to true so a device that somehow can't answer gets
  // the full wizard rather than silently skipping real setup steps.
  const [isPrimary, setIsPrimary] = useState(true);

  const [key, setKey] = useState("");
  const [ownerName, setOwnerName] = useState("");
  const [setupPassword, setSetupPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);

  const [devices, setDevices] = useState<StorageDevice[]>([]);
  const [selected, setSelected] = useState<string[]>([]);
  const [storageNote, setStorageNote] = useState("");

  const [networks, setNetworks] = useState<WifiNetwork[]>([]);
  const [chosenSsid, setChosenSsid] = useState("");
  const [wifiPassword, setWifiPassword] = useState("");
  const [wifiNote, setWifiNote] = useState("");

  useEffect(() => {
    (async () => {
      try {
        const info = await getDeviceSetupInfo(useWS.request);
        setIsPrimary(info.isPrimary);
        setStep(info.isNewDevice ? "credentials" : "login");
      } catch (e) {
        console.error("Could not check device state:", e);
        // Fall back to the normal sign-in form rather than ever blocking
        // an existing owner from logging in over an uncertain check.
        setStep("login");
      }
    })();
  }, []);

  if (step === "checking") {
    return null;
  }

  if (step === "login") {
    return (
      <section className="sf-section" style={{ width: 400, margin: "auto" }}>
        <form onSubmit={async (e) => {
          e.preventDefault();
          setError("");
          try {
            if (await onAuth(key)) onDone();
            else setError("Incorrect password.");
          } catch (err: any) {
            // Issue #93: a disabled account's own auth attempt throws
            // here (see encryptForConnection - the bridge answers its
            // reqGetPubKey with a RespAck error instead of a real key)
            // rather than resolving false like a plain wrong password
            // does above - this used to just vanish as an unhandled
            // rejection, leaving the form looking like it had done
            // nothing at all.
            setError(err?.message || "Could not sign in.");
          }
        }}>
          <div className="sf-row">
          <h3>Password</h3>
            {/* Focused as soon as the form appears: pressing Sign In and
                then having to click into the one field on the page was a
                wasted click every single time. */}
            <input id="sf-old" className="sf-input" type="password" autoFocus onChange={(e)=>setKey(e.target.value)} />
          </div>
          <button className="sf-btn">
            Log In
          </button>
          {error && <p className="sf-note error">{error}</p>}
        </form>
      </section>
    )
  }

  const submitCredentials = async (e: React.FormEvent) => {
    e.preventDefault();
    setError("");

    if (setupPassword.length < 4) {
      setError("Choose a password with at least 4 characters.");
      return;
    }
    if (setupPassword !== confirmPassword) {
      setError("Passwords don't match.");
      return;
    }

    setSubmitting(true);
    try {
      const ok = await onAuth(setupPassword);
      if (!ok) {
        setError("Could not set up the device — please try again.");
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
      // to make and wouldn't be honoured.
      if (!isPrimary) {
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
        setStorageNote(resp.errorMessage || "Could not save storage configuration.");
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
    setWifiNote("");
    try {
      const resp = await useWS.request((e) => {
        (e as any).payload = { $case: "reqSetWifi", reqSetWifi: { ssid: chosenSsid, password: wifiPassword } };
      });
      if (resp.error) {
        setWifiNote(resp.errorMessage || "Could not save the WiFi request.");
        return;
      }
      setWifiNote(`Joining "${chosenSsid}"… if this device was reached over its own setup network, reconnect to your normal WiFi and find it there.`);
    } finally {
      setSubmitting(false);
    }
  };

  if (step === "wifi") {
    return (
      <section className="sf-section setup-section" style={{ width: 420, margin: "auto" }}>
        <h3>WiFi</h3>
        <p className="sf-hint">
          If you're set up over this device's own temporary "Off The Cloud" network, pick your
          real WiFi below. Already connected some other way (ethernet, or WiFi set up earlier)?
          Just skip this.
        </p>
        <form onSubmit={submitWifi}>
          {networks.map((n) => (
            <label key={n.ssid} className="sf-row" style={{ gridTemplateColumns: "24px 1fr", cursor: "pointer" }}>
              <input
                type="radio"
                name="wifi-ssid"
                checked={chosenSsid === n.ssid}
                onChange={() => setChosenSsid(n.ssid)}
              />
              <span>{n.secured ? "🔒 " : ""}{n.ssid} ({n.signal}%)</span>
            </label>
          ))}
          {networks.length === 0 && <p className="sf-hint">No networks found — move the device closer to your router, or skip for now.</p>}
          {chosenSsid && (
            <div className="sf-row">
              <label htmlFor="sf-wifi-password">Password</label>
              <input
                id="sf-wifi-password"
                className="sf-input"
                type="password"
                value={wifiPassword}
                onChange={(e) => setWifiPassword(e.target.value)}
              />
            </div>
          )}
          {wifiNote && <p className="sf-note success">{wifiNote}</p>}
          <div style={{ display: "flex", gap: 8 }}>
            <button className="sf-btn" disabled={!chosenSsid || submitting}>
              {submitting ? "Connecting…" : "Connect"}
            </button>
            <button className="sf-btn" type="button" onClick={onDone}>Skip — I'm already connected</button>
          </div>
        </form>
      </section>
    );
  }

  if (step === "storage") {
    return (
      <section className="sf-section setup-section" style={{ width: 420, margin: "auto" }}>
        <h3>Storage</h3>
        <p className="sf-hint">
          {devices.length === 0
            ? "No extra disks were detected — your files will be stored on the boot disk."
            : "Choose up to 2 disks. Pick 2 to mirror them (RAID1, recommended — your files survive one disk failing). Pick 1 to use it alone, with no redundancy. Pick none to use the boot disk instead."}
        </p>
        <form onSubmit={submitStorage}>
          {devices.map((d) => (
            <label key={d.path} className="sf-row" style={{ gridTemplateColumns: "24px 1fr", cursor: "pointer" }}>
              <input
                type="checkbox"
                checked={selected.includes(d.path)}
                onChange={() => toggleDevice(d.path)}
              />
              <span>{d.path} — {formatSize(d.sizeBytes)}{d.model ? ` (${d.model})` : ""}</span>
            </label>
          ))}
          {storageNote && <p className="sf-note error">{storageNote}</p>}
          <button className="sf-btn" disabled={submitting}>
            {submitting ? "Saving…" : "Next: WiFi"}
          </button>
        </form>
      </section>
    );
  }

  return (
    <section className="sf-section setup-section" style={{ width: 420, margin: "auto" }}>
      <h3>Welcome to Off The Cloud</h3>
      {/* Issue #85: an additional user isn't setting up a device - the
          machine is already running, someone else set it up, and saying
          otherwise invites them to go looking for disks and WiFi that
          aren't theirs. */}
      <p className="sf-hint">
        {isPrimary
          ? "This device isn't set up yet. Choose an owner name and a password — this password protects your files and social profile, so keep it somewhere safe."
          : "This account isn't set up yet. Choose your name and a password — this password protects your files and social profile, so keep it somewhere safe."}
      </p>
      <form onSubmit={submitCredentials}>
        <div className="sf-row">
          <h3>Your name</h3>
          <input
            className="sf-input"
            type="text"
            value={ownerName}
            onChange={(e) => setOwnerName(e.target.value)}
            placeholder="e.g. Alex"
          />
        </div>
        <div className="sf-row">
          <h3>Password</h3>
          <input
            className="sf-input"
            type="password"
            value={setupPassword}
            onChange={(e) => setSetupPassword(e.target.value)}
          />
        </div>
        <div className="sf-row">
          <h3>Confirm password</h3>
          <input
            className="sf-input"
            type="password"
            value={confirmPassword}
            onChange={(e) => setConfirmPassword(e.target.value)}
          />
        </div>
        {error && <p className="sf-note error">{error}</p>}
        <button className="sf-btn" disabled={submitting}>
          {submitting ? "Setting up…" : isPrimary ? "Next: Storage" : "Finish setup"}
        </button>
      </form>
    </section>
  )
}

export default SignIn
