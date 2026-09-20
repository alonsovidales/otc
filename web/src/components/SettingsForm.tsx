// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useMemo, useState } from "react";
import { useWS } from "../net/useWS";
import { encryptForConnection, clearPersistedToken } from "../net/pwCrypto";
import { pushSupported, isPushSubscribed, enablePush, disablePush } from "../net/webPush";
import UsersPanel from "./UsersPanel";
import ProfileCard from "./ProfileCard";
import UpdatePanel from "./UpdatePanel";
import type {
  ReqEnvelope,
  RespEnvelope,
  Settings as PbSettings,
} from "../proto/messages";
import "./SettingsForm.css";

// Rounded, clamped to [0, 100] - total can be 0 right when a run has just
// started and the very first status poll hasn't landed yet.
function reprocessPercent(s: { total: number; processed: number }): number {
  if (s.total <= 0) return 0;
  return Math.max(0, Math.min(100, Math.round((s.processed / s.total) * 100)));
}

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

  // Push notifications (issue #43)
  const [pushSubscribed, setPushSubscribed] = useState(false);
  const [pushBusy, setPushBusy] = useState(false);

  // Face recognition / People search (issue #52) - off by default. Turning
  // it on only affects photos uploaded from that point on; it never scans
  // whatever's already in the library.
  const [faceRecognitionEnabled, setFaceRecognitionEnabled] = useState(false);
  const [faceRecognitionBusy, setFaceRecognitionBusy] = useState(false);

  const toggleFaceRecognition = async () => {
    setFaceRecognitionBusy(true);
    setStatus(null);
    const next = !faceRecognitionEnabled;
    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqSetFaceRecognitionEnabled", reqSetFaceRecognitionEnabled: { enabled: next } };
      });
      if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
        setFaceRecognitionEnabled(next);
      } else {
        setStatus({ kind: "error", text: resp.payload?.$case === "respAck" ? resp.payload.respAck.errorMsg : "Could not update this setting." });
      }
    } catch (err: any) {
      setStatus({ kind: "error", text: err?.message ?? String(err) });
    } finally {
      setFaceRecognitionBusy(false);
    }
  };

  // Issue #73: full-library reprocess - re-runs tagging/face detection
  // against every already-uploaded photo/video (e.g. after a detection
  // fix), stripping existing tags/people first so they're recalculated
  // clean rather than piling on top of possibly-wrong old data. Runs
  // entirely server-side; this just starts it and polls for progress,
  // same pattern as StatusWidget polling GetStatus.
  const [reprocessStatus, setReprocessStatus] = useState<{ status: string; total: number; processed: number } | null>(null);
  // "resume": continue a stopped run from where it left off. "restart":
  // wipe everything and start fresh - always available for a first run,
  // and also offered *instead of* resume when a run was stopped, so the
  // owner isn't stuck only ever continuing (issue #73 follow-up).
  const [reprocessConfirming, setReprocessConfirming] = useState<"resume" | "restart" | null>(null);
  const [reprocessBusy, setReprocessBusy] = useState(false);
  const [reprocessCancelling, setReprocessCancelling] = useState(false);

  const loadReprocessStatus = useCallback(async () => {
    const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
      (e as any).payload = { $case: "reqGetReprocessStatus", reqGetReprocessStatus: {} };
    });
    if (resp.payload?.$case === "respReprocessStatus") {
      setReprocessStatus(resp.payload.respReprocessStatus);
    }
  }, []);

  useEffect(() => {
    loadReprocessStatus();
  }, [loadReprocessStatus]);

  // Only actually polls while a run is active - a completed/failed/idle
  // status doesn't change on its own, no point re-fetching it every tick.
  useEffect(() => {
    if (reprocessStatus?.status !== "running") return;
    const t = setInterval(loadReprocessStatus, 1500);
    return () => clearInterval(t);
  }, [reprocessStatus?.status, loadReprocessStatus]);

  const startReprocess = async (forceRestart: boolean) => {
    setReprocessConfirming(null);
    setReprocessBusy(true);
    setStatus(null);
    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqStartReprocess", reqStartReprocess: { forceRestart } };
      });
      if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
        await loadReprocessStatus();
      } else {
        setStatus({ kind: "error", text: resp.payload?.$case === "respAck" ? resp.payload.respAck.errorMsg : "Could not start reprocessing." });
      }
    } catch (err: any) {
      setStatus({ kind: "error", text: err?.message ?? String(err) });
    } finally {
      setReprocessBusy(false);
    }
  };

  // Cancelling doesn't wait for the worker to actually wind down (it
  // notices between files - see files_manager.CancelReprocess) - just
  // requests it and refreshes status, same as starting.
  const stopReprocess = async () => {
    setReprocessCancelling(true);
    setStatus(null);
    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqStopReprocess", reqStopReprocess: {} };
      });
      if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
        await loadReprocessStatus();
      } else {
        setStatus({ kind: "error", text: resp.payload?.$case === "respAck" ? resp.payload.respAck.errorMsg : "Could not stop reprocessing." });
      }
    } catch (err: any) {
      setStatus({ kind: "error", text: err?.message ?? String(err) });
    } finally {
      setReprocessCancelling(false);
    }
  };

  // Status
  const [status, setStatus] = useState<{ kind: "info"|"success"|"error"; text: string } | null>(null);

  useEffect(() => {
    if (pushSupported()) void isPushSubscribed().then(setPushSubscribed);
  }, []);

  const togglePush = async () => {
    setPushBusy(true);
    setStatus(null);
    try {
      if (pushSubscribed) {
        await disablePush();
        setPushSubscribed(false);
      } else {
        const ok = await enablePush();
        setPushSubscribed(ok);
        if (!ok) setStatus({ kind: "error", text: "Notification permission was denied." });
      }
    } catch (err: any) {
      setStatus({ kind: "error", text: err?.message ?? String(err) });
    } finally {
      setPushBusy(false);
    }
  };

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
          setFaceRecognitionEnabled(!!s.faceRecognitionEnabled);
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
        // Issue #46/#101: this connection's session survives a password
        // change untouched (ChangeKey only re-wraps the same vault secret
        // under the new password — see session.ChangeKey), so rather than
        // stashing the new password the way this used to, just rotate a
        // fresh token in, which is what the old savePersistedKey(newKey)
        // was really trying to achieve: don't leave the next reload
        // holding something stale.
        await useWS.refreshSessionToken();
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

      {/* Issue #84: first thing in Settings, not tucked away behind a
          top-level "Profile" tab (which used to show this only while
          signed out - an authenticated owner had no way at all to reach
          the editable form, since that tab showed FriendshipsManager
          instead whenever authenticated=true). SettingsForm only ever
          renders once authenticated (see App.tsx), so this is always the
          editable branch, never ProfileCard's own read-only visitor view. */}
      <section className="sf-section">
        <h3>Profile</h3>
        <ProfileCard authenticated={true} />
      </section>

      {/* Issue #94: in-place updates. Renders nothing on a non-primary
          instance - see UpdatePanel. */}
      <UpdatePanel />

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

      {pushSupported() && (
        <section className="sf-section">
          <h3>Notifications</h3>
          <p className="sf-hint">
            Get a push notification in this browser when a friend posts. Self-hosted — this device
            sends it directly, no third-party notification service involved.
          </p>
          <button className="sf-btn" disabled={pushBusy} onClick={() => void togglePush()}>
            {pushBusy ? "Working…" : pushSubscribed ? "Disable Notifications" : "Enable Notifications"}
          </button>
        </section>
      )}

      <section className="sf-section">
        <h3>People</h3>
        <p className="sf-hint">
          Detect faces in newly uploaded photos so you can search by person, like other photo
          apps. Off by default. Turning this on only affects photos uploaded from now on — it
          never scans photos you already have, even after you enable it.
        </p>
        <button className="sf-btn" disabled={faceRecognitionBusy} onClick={() => void toggleFaceRecognition()}>
          {faceRecognitionBusy ? "Working…" : faceRecognitionEnabled ? "Disable Face Recognition" : "Enable Face Recognition"}
        </button>
      </section>

      <section className="sf-section">
        <h3>Reprocess Media</h3>
        <p className="sf-hint">
          Re-run tagging and face detection on every photo and video already in your library —
          useful after a detection fix or model update. This clears existing tags and recognized
          people first and rebuilds them from scratch.
        </p>
        {reprocessStatus?.status === "running" ? (
          <div className="sf-progress">
            <div className="sf-progress-track">
              <div
                className="sf-progress-fill"
                style={{ width: `${reprocessPercent(reprocessStatus)}%` }}
              />
            </div>
            <div className="sf-progress-row">
              <div className="sf-hint">
                Reprocessing… {reprocessPercent(reprocessStatus)}% ({reprocessStatus.processed} / {reprocessStatus.total})
              </div>
              <button className="sf-btn small sf-danger" disabled={reprocessCancelling} onClick={() => void stopReprocess()}>
                {reprocessCancelling ? "Stopping…" : "Cancel"}
              </button>
            </div>
          </div>
        ) : reprocessStatus?.status === "stopped" ? (
          <>
            <div className="sf-hint">Stopped at {reprocessPercent(reprocessStatus)}% ({reprocessStatus.processed} / {reprocessStatus.total}).</div>
            <div className="sf-btn-row">
              <button className="sf-btn" disabled={reprocessBusy} onClick={() => setReprocessConfirming("resume")}>
                {reprocessBusy ? "Starting…" : "Resume Reprocessing"}
              </button>
              <button className="sf-btn sf-btn-secondary" disabled={reprocessBusy} onClick={() => setReprocessConfirming("restart")}>
                Start Over
              </button>
            </div>
          </>
        ) : (
          <>
            <button className="sf-btn" disabled={reprocessBusy} onClick={() => setReprocessConfirming("restart")}>
              {reprocessBusy ? "Starting…" : "Reprocess All Media"}
            </button>
            {reprocessStatus?.status === "completed" && (
              <div className="sf-hint">Last run completed — {reprocessStatus.processed} file(s) processed.</div>
            )}
            {reprocessStatus?.status === "failed" && (
              <div className="sf-note error">Last run failed after {reprocessStatus.processed} file(s) — try again.</div>
            )}
          </>
        )}
      </section>

      {reprocessConfirming && (
        <div className="sf-modal" onClick={() => setReprocessConfirming(null)}>
          <div className="sf-modal-inner" onClick={e => e.stopPropagation()}>
            <p>
              {reprocessConfirming === "resume"
                ? "Resume reprocessing where it left off? It can take a while."
                : "Reprocess every photo and video in your library? This deletes all existing tags and recognized people and rebuilds them from scratch. It can take a while and can't be undone."}
            </p>
            <div className="sf-modal-actions">
              <button className="sf-btn small" onClick={() => setReprocessConfirming(null)}>Cancel</button>
              <button className="sf-btn small sf-danger" onClick={() => void startReprocess(reprocessConfirming === "restart")}>
                {reprocessConfirming === "resume" ? "Resume" : "Reprocess"}
              </button>
            </div>
          </div>
        </div>
      )}

      <section className="sf-section">
        <h3>Session</h3>
        <p className="sf-hint">
          This browser stays signed in across reloads. Sign out if you're on a shared or public
          computer.
        </p>
        <button
          className="sf-btn"
          onClick={async () => {
            // Issue #101: tell the device to drop every token descending
            // from this login first, so a copy of one sitting in another
            // tab's storage stops working right now rather than whenever
            // its TTL happens to run out. Best-effort — clearing this
            // browser's own storage and reloading happens either way.
            try {
              await useWS.request((e: Partial<ReqEnvelope>) => {
                (e as any).payload = { $case: "reqRevokeSessionToken", reqRevokeSessionToken: {} };
              });
            } catch (err) {
              console.error("Could not revoke session tokens on sign out:", err);
            }
            clearPersistedToken();
            window.location.reload();
          }}
        >
          Sign Out
        </button>
      </section>

      <UsersPanel />
    </div>
  );
}

