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

  // Bridge shared secret (issue #40)
  const [regeneratingSecret, setRegeneratingSecret] = useState(false);

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

  // Issue #40 follow-up: the device asks the bridge itself for a fresh
  // secret (authenticated by the current one) rather than inventing one
  // locally — a self-generated secret would just be rejected by the
  // bridge, which only ever accepts one it already has on record.
  const regenerateSecret = async () => {
    if (regeneratingSecret || savingDomain || savingKey) return;
    setRegeneratingSecret(true);
    setStatus(null);
    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqRegenerateBridgeSecret", reqRegenerateBridgeSecret: {} };
      });
      if (resp.payload?.$case === "respSettings") {
        setCurrentBridgeSecret(resp.payload.respSettings.bridgeSecret || "");
        setStatus({ kind: "success", text: "Bridge shared secret regenerated." });
      } else if (resp.payload?.$case === "respAck") {
        setStatus({ kind: "error", text: resp.payload.respAck.errorMsg || "Regenerate failed." });
      } else if (resp.error) {
        setStatus({ kind: "error", text: resp.errorMessage || "Regenerate failed." });
      } else {
        setStatus({ kind: "error", text: "Unexpected response." });
      }
    } catch (err: any) {
      setStatus({ kind: "error", text: err?.message ?? String(err) });
    } finally {
      setRegeneratingSecret(false);
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
          This is what pairs this device with the bridge relay. Regenerating asks the bridge
          for a new one on the spot — no need to visit its admin panel.
        </p>
        <div className="sf-row">
          <label htmlFor="sf-current-secret">Secret</label>
          <div className="sf-secret-row">
            <input id="sf-current-secret" className="sf-input" value={currentBridgeSecret} readOnly />
            <button className="sf-btn small" type="button" onClick={() => void copyCurrentSecret()}>Copy</button>
          </div>
        </div>
        <button className="sf-btn" disabled={regeneratingSecret || savingDomain || savingKey} onClick={() => void regenerateSecret()}>
          {regeneratingSecret ? "Regenerating…" : "Regenerate"}
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

