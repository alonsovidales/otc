// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useMemo, useState } from "react";
import { useWS } from "../net/useWS";
import { encryptForConnection, clearPersistedToken } from "../net/pwCrypto";
import { pushSupported, isPushSubscribed, enablePush, disablePush, unregisterPushOnSignOut } from "../net/webPush";
import UsersPanel from "./UsersPanel";
import ProfileCard from "./ProfileCard";
import UpdatePanel from "./UpdatePanel";
import SharedLinksPanel from "./SharedLinksPanel";
import TailscalePanel from "./TailscalePanel";
import LogsPanel from "./LogsPanel";
import BridgePanel from "./BridgePanel";
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

  // Domain form

  // Password form
  const [oldKey, setOldKey] = useState("");
  const [newKey, setNewKey] = useState("");
  const [confirmKey, setConfirmKey] = useState("");
  const [savingKey, setSavingKey] = useState(false);

  // Push notifications (issue #43)
  const [pushSubscribed, setPushSubscribed] = useState(false);
  const [pushBusy, setPushBusy] = useState(false);

  // Face recognition / People search (issue #52) - on by default. Turning
  // it on only affects photos uploaded from that point on; it never scans
  // whatever's already in the library.
  const [faceRecognitionEnabled, setFaceRecognitionEnabled] = useState(false);
  const [imageTaggingEnabled, setImageTaggingEnabled] = useState(true);
  const [imageTaggingBusy, setImageTaggingBusy] = useState(false);
  const [faceRecognitionBusy, setFaceRecognitionBusy] = useState(false);
  // Issue #153: the space friends' posts may take (GB in the field, MB on
  // the device), and what they take now.
  const [socialLimitGB, setSocialLimitGB] = useState("");
  const [socialUsedBytes, setSocialUsedBytes] = useState<number | null>(null);
  const [socialLimitBusy, setSocialLimitBusy] = useState(false);
  const [socialLimitNote, setSocialLimitNote] = useState<{ kind: "ok" | "error"; text: string } | null>(null);
  const saveSocialLimit = async () => {
    const gb = Number(socialLimitGB.replace(",", "."));
    if (!Number.isFinite(gb) || gb < 0.1) {
      setSocialLimitNote({ kind: "error", text: "Enter a size of at least 0.1 GB." });
      return;
    }
    setSocialLimitBusy(true);
    setSocialLimitNote(null);
    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqSetSocialStorageLimit", reqSetSocialStorageLimit: { mb: Math.round(gb * 1024) } };
      });
      if (resp.error) setSocialLimitNote({ kind: "error", text: resp.errorMessage || "Could not save the limit." });
      else setSocialLimitNote({ kind: "ok", text: "Saved. Older posts over the limit are being removed." });
    } catch (e: any) {
      setSocialLimitNote({ kind: "error", text: e?.message ?? String(e) });
    } finally {
      setSocialLimitBusy(false);
    }
  };

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

  // Issue #181: the tagging model can be turned off like face recognition.
  const toggleImageTagging = async () => {
    setImageTaggingBusy(true);
    setStatus(null);
    const next = !imageTaggingEnabled;
    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqSetImageTaggingEnabled", reqSetImageTaggingEnabled: { enabled: next } };
      });
      if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
        setImageTaggingEnabled(next);
      } else {
        setStatus({ kind: "error", text: resp.errorMessage || (resp.payload?.$case === "respAck" ? resp.payload.respAck.errorMsg : "Could not update this setting.") });
      }
    } catch (err: any) {
      setStatus({ kind: "error", text: err?.message ?? String(err) });
    } finally {
      setImageTaggingBusy(false);
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
          setFaceRecognitionEnabled(!!s.faceRecognitionEnabled);
          setImageTaggingEnabled(!!s.imageTaggingEnabled);
          const limitMb = s.socialStorageLimitMb || 5120;
          setSocialLimitGB(String(Math.round((limitMb / 1024) * 10) / 10));
          setSocialUsedBytes(Number(s.socialStorageUsedBytes ?? 0));
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

  const pwMismatch = newKey.length > 0 && confirmKey.length > 0 && newKey !== confirmKey;
  const canSaveKey = useMemo(() => {
    if (!oldKey || !newKey || !confirmKey) return false;
    if (pwMismatch) return false;
    if (oldKey === newKey) return false;
    return true;
  }, [oldKey, newKey, confirmKey, pwMismatch]);

  // ---------- Actions ----------

  const changePassword = async () => {
    if (!canSaveKey || savingKey) return;
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

      <UsersPanel />

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

        <button className="sf-btn" disabled={!canSaveKey || savingKey} onClick={() => void changePassword()}>
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

      {/* Issue #145: joining the bridge after a setup without it. */}
      <BridgePanel />

      {/* Issue #80: Tailscale Funnel as an alternative to the bridge.
          Primary-only too - Funnel publishes the whole machine. */}
      <TailscalePanel />

      {/* Live device logs; copy or send them for support. */}
      <LogsPanel />

      <section className="sf-section">
        <h3>Face Recognition</h3>
        <p className="sf-hint">
          Detect faces in newly uploaded photos so you can search by person, like other photo
          apps. Off unless you turn it on (here or during setup), and faces never leave the
          device. It looks at everyone in your photos, not just you. It only affects photos
          uploaded while it is on — use Reprocess Media to scan the ones you already have.
        </p>
        <button className="sf-btn" disabled={faceRecognitionBusy} onClick={() => void toggleFaceRecognition()}>
          {faceRecognitionBusy ? "Working…" : faceRecognitionEnabled ? "Disable Face Recognition" : "Enable Face Recognition"}
        </button>
      </section>

      <section className="sf-section">
        <h3>Image Tagging</h3>
        <p className="sf-hint">
          Recognise what newly uploaded photos and videos show (a beach, a dog, a birthday cake)
          so you can search for it. It runs on this device and nothing leaves it. Turning it off
          saves processing time; places from a photo's own location data are still searchable.
          It only affects what is uploaded while it is off.
        </p>
        <button className="sf-btn" disabled={imageTaggingBusy} onClick={() => void toggleImageTagging()}>
          {imageTaggingBusy ? "Working…" : imageTaggingEnabled ? "Disable Image Tagging" : "Enable Image Tagging"}
        </button>
      </section>

      <section className="sf-section">
        <h3>Friends' posts storage</h3>
        <p className="sf-hint">
          Photos and videos from your friends' posts are kept on this device, so your feed works
          even when they're offline. When they take more than this, the oldest friends' posts are
          removed. Your own posts and files are never touched.
          {socialUsedBytes !== null && <> Using {(socialUsedBytes / 1024 ** 3).toFixed(2)} GB now.</>}
        </p>
        <div className="sf-row">
          <label htmlFor="sf-social-limit">Limit (GB)</label>
          <div className="sf-secret-row">
            <input id="sf-social-limit" className="sf-input" type="number" min="0.1" step="0.5" inputMode="decimal"
              value={socialLimitGB} onChange={e => setSocialLimitGB(e.target.value)} />
            <button className="sf-btn small" type="button" disabled={socialLimitBusy} onClick={() => void saveSocialLimit()}>
              {socialLimitBusy ? "Saving…" : "Save"}
            </button>
          </div>
        </div>
        {socialLimitNote && <p className={`sf-note ${socialLimitNote.kind === "ok" ? "success" : "error"}`}>{socialLimitNote.text}</p>}
      </section>

      {/* Issue #94: in-place updates. Renders nothing on a non-primary
          instance - see UpdatePanel. */}
      <UpdatePanel />

      {/* Issue #180: share links, and the space their copies take. */}
      <SharedLinksPanel />

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
            // Issue #131: and forget this browser's push subscription,
            // so the device stops pushing to a browser no longer signed
            // in. Before the tokens go, since it needs the session.
            try {
              await unregisterPushOnSignOut();
            } catch (err) {
              console.error("Could not unregister push on sign out:", err);
            }
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
    </div>
  );
}

