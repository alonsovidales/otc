import { useEffect, useMemo, useState } from "react";
import { useWS } from "../net/useWS";
import { encryptForConnection } from "../net/pwCrypto";
import type {
  ReqEnvelope,
  RespEnvelope,
  Settings as PbSettings,
} from "../proto/messages";
import "./SettingsForm.css";

export default function SettingsForm() {
  // Loaded settings
  const [currentDomain, setCurrentDomain] = useState("");
  const [currentBridgeSecret, setCurrentBridgeSecret] = useState("");

  // Domain form
  const [newDomain, setNewDomain] = useState("");
  const [savingDomain, setSavingDomain] = useState(false);

  // Bridge shared secret form (issue #40)
  const [newSecret, setNewSecret] = useState("");
  const [savingSecret, setSavingSecret] = useState(false);

  // Password form
  const [oldKey, setOldKey] = useState("");
  const [newKey, setNewKey] = useState("");
  const [confirmKey, setConfirmKey] = useState("");
  const [savingKey, setSavingKey] = useState(false);

  // Status
  const [status, setStatus] = useState<{ kind: "info"|"success"|"error"; text: string } | null>(null);

  // ---------- Load settings once ----------
  useEffect(() => {
    (async () => {
      try {
        const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
          (e as any).payload = { $case: "reqGetSettings", reqGetSettings: {} };
        });

        if (resp.payload?.$case === "respSettings") {
          const s: PbSettings = resp.payload.respSettings;
          setCurrentDomain(s.domain || "");
          setCurrentBridgeSecret(s.bridgeSecret || "");
        } else if (resp.payload?.$case === "respAck") {
          const msg = resp.payload.respAck.errorMsg || "Failed to load settings.";
          setStatus({ kind: "error", text: msg });
        }
      } catch (err: any) {
        setStatus({ kind: "error", text: err?.message ?? String(err) });
      }
    })();
  }, []);

  // ---------- Validation ----------
  const canSaveDomain = useMemo(() => {
    const nd = newDomain.trim();
    return !!nd && nd !== currentDomain;
  }, [newDomain, currentDomain]);

  const canSaveSecret = useMemo(() => {
    const ns = newSecret.trim();
    return !!ns && ns !== currentBridgeSecret;
  }, [newSecret, currentBridgeSecret]);

  const pwMismatch = newKey.length > 0 && confirmKey.length > 0 && newKey !== confirmKey;
  const canSaveKey = useMemo(() => {
    if (!oldKey || !newKey || !confirmKey) return false;
    if (pwMismatch) return false;
    if (oldKey === newKey) return false;
    return true;
  }, [oldKey, newKey, confirmKey, pwMismatch]);

  // ---------- Actions ----------
  const saveDomain = async () => {
    if (!canSaveDomain || savingDomain || savingKey) return;
    setSavingDomain(true);
    setStatus(null);
    try {
      const payload =
        { $case: "reqSetSettings", reqSetSettings: { domain: newDomain.trim() } } as any;

      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = payload;
      });

      if (resp.payload?.$case === "respAck") {
        if (resp.payload.respAck.ok) {
          setCurrentDomain(newDomain.trim());
          setNewDomain("");
          setStatus({ kind: "success", text: "Domain updated." });
        } else {
          const msg = resp.payload.respAck.errorMsg || "Update failed.";
          setStatus({ kind: "error", text: msg });
        }
      } else if (resp.payload?.$case === "respSettings") {
        // In case server returns the updated settings
        setCurrentDomain(resp.payload.respSettings.domain || "");
        setNewDomain("");
        setStatus({ kind: "success", text: "Domain updated." });
      } else {
        setStatus({ kind: "error", text: "Unexpected response." });
      }
    } catch (err: any) {
      setStatus({ kind: "error", text: err?.message ?? String(err) });
    } finally {
      setSavingDomain(false);
    }
  };

  // Issue #40: update the bridge shared secret, independent of the domain
  // form above (they used to be coupled server-side — see the proto
  // comment on SetBridgeSecret).
  const saveSecret = async () => {
    if (!canSaveSecret || savingSecret || savingDomain || savingKey) return;
    setSavingSecret(true);
    setStatus(null);
    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqSetBridgeSecret", reqSetBridgeSecret: { secret: newSecret.trim() } };
      });
      if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
        setCurrentBridgeSecret(newSecret.trim());
        setNewSecret("");
        setStatus({ kind: "success", text: "Bridge shared secret updated." });
      } else if (resp.payload?.$case === "respAck") {
        setStatus({ kind: "error", text: resp.payload.respAck.errorMsg || "Update failed." });
      } else {
        setStatus({ kind: "error", text: "Unexpected response." });
      }
    } catch (err: any) {
      setStatus({ kind: "error", text: err?.message ?? String(err) });
    } finally {
      setSavingSecret(false);
    }
  };

  const copyCurrentSecret = async () => {
    if (!currentBridgeSecret) return;
    try {
      await navigator.clipboard?.writeText?.(currentBridgeSecret);
      setStatus({ kind: "info", text: "Current secret copied to clipboard." });
    } catch {
      /* clipboard access denied — nothing to do */
    }
  };

  const changePassword = async () => {
    if (!canSaveKey || savingKey || savingDomain) return;
    setSavingKey(true);
    setStatus(null);
    try {
      const encryptedOldKey = await encryptForConnection(useWS.request, oldKey);
      const encryptedNewKey = await encryptForConnection(useWS.request, newKey);
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = {
          $case: "reqChangeKey",
          reqChangeKey: {
            oldKey: encryptedOldKey,
            newKey: encryptedNewKey,
          },
        };
      });

      if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
        setOldKey(""); setNewKey(""); setConfirmKey("");
        setStatus({ kind: "success", text: "Password changed." });
      } else if (resp.payload?.$case === "respAck") {
        const msg = resp.payload.respAck.errorMsg || "Change failed.";
        setStatus({ kind: "error", text: msg });
      } else {
        setStatus({ kind: "error", text: "Unexpected response." });
      }
    } catch (err: any) {
      setStatus({ kind: "error", text: err?.message ?? String(err) });
    } finally {
      setSavingKey(false);
    }
  };

  return (
    <div className="sf-wrap">
      {status && (
        <div className={`sf-status ${status.kind}`}>
          {status.text}
        </div>
      )}

      <section className="sf-section">
        <h3>Update Domain</h3>
        <div className="sf-row">
          <label htmlFor="sf-domain">New domain</label>
          <input
            id="sf-domain"
            className="sf-input"
            placeholder={currentDomain}
            value={newDomain}
            onChange={(e) => setNewDomain(e.target.value)}
            autoCapitalize="none"
            autoCorrect="off"
          />
        </div>
        <button className="sf-btn" disabled={!canSaveDomain || savingDomain || savingKey} onClick={() => void saveDomain()}>
          {savingDomain ? "Saving…" : "Save Domain"}
        </button>
      </section>

      <section className="sf-section">
        <h3>Bridge Shared Secret</h3>
        <p className="sf-hint">
          This must match what the bridge has on record for this device — the bridge only
          accepts a secret it already knows, it can't learn a new one from here. To rotate it:
          on the bridge's admin panel, delete and re-add this device's domain (it'll hand you a
          new secret), then paste that value below.
        </p>
        <div className="sf-row">
          <label htmlFor="sf-current-secret">Current secret</label>
          <div className="sf-secret-row">
            <input id="sf-current-secret" className="sf-input" value={currentBridgeSecret} readOnly />
            <button className="sf-btn small" type="button" onClick={() => void copyCurrentSecret()}>Copy</button>
          </div>
        </div>
        <div className="sf-row">
          <label htmlFor="sf-new-secret">Secret from the bridge</label>
          <input
            id="sf-new-secret"
            className="sf-input sf-secret-input"
            value={newSecret}
            onChange={(e) => setNewSecret(e.target.value)}
            placeholder="Paste the secret the bridge gave you"
            autoCapitalize="none"
            autoCorrect="off"
          />
        </div>
        <button className="sf-btn" disabled={!canSaveSecret || savingSecret || savingDomain || savingKey} onClick={() => void saveSecret()}>
          {savingSecret ? "Saving…" : "Save Secret"}
        </button>
      </section>

      <section className="sf-section">
        <h3>Change Password</h3>
        <div className="sf-row">
          <label htmlFor="sf-old">Old password</label>
          <input
            id="sf-old"
            className="sf-input"
            type="password"
            value={oldKey}
            onChange={(e) => setOldKey(e.target.value)}
          />
        </div>
        <div className="sf-row">
          <label htmlFor="sf-new">New password</label>
          <input
            id="sf-new"
            className="sf-input"
            type="password"
            value={newKey}
            onChange={(e) => setNewKey(e.target.value)}
          />
        </div>
        <div className="sf-row">
          <label htmlFor="sf-conf">Confirm new password</label>
          <input
            id="sf-conf"
            className="sf-input"
            type="password"
            value={confirmKey}
            onChange={(e) => setConfirmKey(e.target.value)}
          />
        </div>
        {pwMismatch && <div className="sf-note error">New passwords do not match.</div>}

        <button className="sf-btn" disabled={!canSaveKey || savingKey || savingDomain} onClick={() => void changePassword()}>
          {savingKey ? "Saving…" : "Change Password"}
        </button>
      </section>
    </div>
  );
}

