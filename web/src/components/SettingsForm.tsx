// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useWS } from "../net/useWS";
import { encryptForConnection } from "../net/pwCrypto";
import { signOut } from "../net/signOut";
import { pushSupported, isPushSubscribed, enablePush, disablePush } from "../net/webPush";
import UsersPanel from "./UsersPanel";
import ProfileCard from "./ProfileCard";
import UpdatePanel from "./UpdatePanel";
import SharedLinksPanel from "./SharedLinksPanel";
import TailscalePanel from "./TailscalePanel";
import LogsPanel from "./LogsPanel";
import BridgePanel from "./BridgePanel";
import LanguagePanel from "./settings/LanguagePanel";
import WifiPanel from "./settings/WifiPanel";
import MirrorDiskPanel from "./settings/MirrorDiskPanel";
import BackgroundWorkPanel from "./settings/BackgroundWorkPanel";
import { setFaceRecognition, useFaceRecognition } from "./faceRecognition";
import { setPlacesOn, usePlacesOn } from "./places";
import { localDay, setMemoriesOn, showAllMemories } from "./memories";
import {
  announcement, applySmartSearchSettings, followSmartSearch, modelView, refreshSmartSearch, setSmartSearchOn,
  useSmartSearchModel, useSmartSearchOn, type Announcement, type ModelView, type SmartModel,
} from "./smartSearch";
import { ConfirmDialog } from "./ConfirmDialog";
import { useT, type TFunction } from "../i18n";
import { asIs, percentText, say, unitText, type UiText } from "./uiText";
import type {
  ReqEnvelope,
  RespEnvelope,
  Settings as PbSettings,
} from "../proto/messages";
import { MemoryPeriod } from "../proto/messages";
import { useDemo } from "./useDemo";
import "./SettingsForm.css";
import { refuseOnDemo } from "../net/demo";

// Rounded, clamped to [0, 100] - total can be 0 right when a run has just
// started and the very first status poll hasn't landed yet.
function reprocessPercent(s: { total: number; processed: number }): number {
  if (s.total <= 0) return 0;
  return Math.max(0, Math.min(100, Math.round((s.processed / s.total) * 100)));
}

// "1.25 GB", as (bytes / 1024^3).toFixed(2) wrote it, in the language's
// format: rounded by toFixed first (Intl rounds the shortest decimal form,
// toFixed the exact value), so Intl only lays out the final digits.
const usedGB = (t: TFunction, bytes: number) => unitText(t, Number((bytes / 1024 ** 3).toFixed(2)), "gigabyte", 2);

// onShowTour: Getting started's button, the first-run tour again (Tour.tsx).
export default function SettingsForm({ onShowTour }: { onShowTour?: () => void }) {
  const t = useT();
  const demo = useDemo();
  // Loaded settings

  // Domain form

  // Password form
  const [oldKey, setOldKey] = useState("");
  const [newKey, setNewKey] = useState("");
  const [confirmKey, setConfirmKey] = useState("");
  const [savingKey, setSavingKey] = useState(false);

  // Session
  const [signingOut, setSigningOut] = useState(false);

  // Push notifications (issue #43)
  const [pushSubscribed, setPushSubscribed] = useState(false);
  const [pushBusy, setPushBusy] = useState(false);

  // Face recognition / People search (issue #52) - off by default. Turning
  // it on only affects photos uploaded from that point on; it never scans
  // whatever's already in the library. Shared with the menu and the
  // search (faceRecognition.ts), which show People only while it is on.
  const faceRecognitionEnabled = useFaceRecognition() === true;
  const [imageTaggingEnabled, setImageTaggingEnabled] = useState(true);
  const [imageTaggingBusy, setImageTaggingBusy] = useState(false);
  const [faceRecognitionBusy, setFaceRecognitionBusy] = useState(false);
  // Places' switch (places.ts), on a device that has Places: its Settings
  // answer says places_enabled either way.
  const placesOn = usePlacesOn() === true;
  const [placesKnown, setPlacesKnown] = useState(false);
  const [placesBusy, setPlacesBusy] = useState(false);
  // On this day's switch (memories.ts), on a device that has it, and how
  // many things the user hid from it (GetMemories' answer).
  const [memoriesOn, setMemoriesOnHere] = useState<boolean | null>(null);
  const [memoriesBusy, setMemoriesBusy] = useState(false);
  const [memoriesHidden, setMemoriesHidden] = useState(0);
  // Smart search's switch (smartSearch.ts), on a device that has it: on at
  // once, off after the confirmation (what the device learned goes). Its
  // model's download, which turning it on starts where the model isn't
  // there yet (release 152), shows under the hint; the device is asked
  // again every 3 s while it goes on and this shows.
  const smartOn = useSmartSearchOn() === true;
  const smartModel = useSmartSearchModel();
  const [smartKnown, setSmartKnown] = useState(false);
  const [smartBusy, setSmartBusy] = useState(false);
  const [smartOffAsk, setSmartOffAsk] = useState(false);
  const followSmart = smartKnown && smartOn && smartModel.downloading;
  useEffect(() => (followSmart ? followSmartSearch() : undefined), [followSmart]);
  // Issue #153: the space friends' posts may take (GB in the field, MB on
  // the device), and what they take now.
  const [socialLimitGB, setSocialLimitGB] = useState("");
  const [socialUsedBytes, setSocialUsedBytes] = useState<number | null>(null);
  const [socialLimitBusy, setSocialLimitBusy] = useState(false);
  const [socialLimitNote, setSocialLimitNote] = useState<{ kind: "ok" | "error"; text: UiText } | null>(null);
  const saveSocialLimit = async () => {
    if (refuseOnDemo()) return; // a demo device (net/demo.ts)
    const gb = Number(socialLimitGB.replace(",", "."));
    if (!Number.isFinite(gb) || gb < 0.1) {
      setSocialLimitNote({ kind: "error", text: say("web.settings.social.too_small") });
      return;
    }
    setSocialLimitBusy(true);
    setSocialLimitNote(null);
    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqSetSocialStorageLimit", reqSetSocialStorageLimit: { mb: Math.round(gb * 1024) } };
      });
      if (resp.error) setSocialLimitNote({ kind: "error", text: resp.errorMessage ? asIs(resp.errorMessage) : say("web.settings.social.failed") });
      else setSocialLimitNote({ kind: "ok", text: say("web.settings.social.saved") });
    } catch (e: any) {
      setSocialLimitNote({ kind: "error", text: asIs(e?.message ?? String(e)) });
    } finally {
      setSocialLimitBusy(false);
    }
  };

  const toggleFaceRecognition = async () => {

    if (refuseOnDemo()) return; // a demo device (net/demo.ts)
    setFaceRecognitionBusy(true);
    setStatus(null);
    const next = !faceRecognitionEnabled;
    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqSetFaceRecognitionEnabled", reqSetFaceRecognitionEnabled: { enabled: next } };
      });
      if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
        setFaceRecognition(next);
      } else {
        setStatus({ kind: "error", text: resp.payload?.$case === "respAck" ? asIs(resp.payload.respAck.errorMsg) : say("web.settings.general.update_failed") });
      }
    } catch (err: any) {
      setStatus({ kind: "error", text: asIs(err?.message ?? String(err)) });
    } finally {
      setFaceRecognitionBusy(false);
    }
  };

  // On this day: hidden things are counted by GetMemories (the device keeps
  // today's answer, so this costs it nothing new).
  const countHiddenMemories = useCallback(async () => {
    try {
      const resp: RespEnvelope = await useWS.request((e) => {
        e.payload = { $case: "reqGetMemories", reqGetMemories: { date: localDay(), period: MemoryPeriod.MemoryDay, timeZone: "", utcOffsetMinutes: -new Date().getTimezoneOffset() } };
      });
      if (resp.payload?.$case === "respMemories") setMemoriesHidden(resp.payload.respMemories.hiddenCount);
    } catch {
      // The button just doesn't show.
    }
  }, []);

  const toggleMemories = async () => {
    if (refuseOnDemo()) return; // a demo device (net/demo.ts)
    setMemoriesBusy(true);
    setStatus(null);
    const next = !memoriesOn;
    try {
      const resp: RespEnvelope = await useWS.request((e) => {
        e.payload = { $case: "reqSetMemoriesEnabled", reqSetMemoriesEnabled: { enabled: next } };
      });
      if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
        setMemoriesOnHere(next);
        setMemoriesOn(next);
        if (next) void countHiddenMemories();
      } else {
        const said = resp.errorMessage || (resp.payload?.$case === "respAck" ? resp.payload.respAck.errorMsg : "");
        setStatus({ kind: "error", text: said ? asIs(said) : say("web.settings.general.update_failed") });
      }
    } catch (err) {
      setStatus({ kind: "error", text: asIs(err instanceof Error ? err.message : String(err)) });
    } finally {
      setMemoriesBusy(false);
    }
  };

  // Smart search: the device's answer to its switch - null once done, else
  // its words (or ours when it gave none). Done, the model's state is asked
  // again: turned on, its download may have started; off, stopped.
  const sendSmartSearch = async (enabled: boolean): Promise<UiText | null> => {
    try {
      const resp: RespEnvelope = await useWS.request((e) => {
        e.payload = { $case: "reqSetSmartSearchEnabled", reqSetSmartSearchEnabled: { enabled } };
      });
      if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
        setSmartSearchOn(enabled);
        await refreshSmartSearch();
        return null;
      }
      const said = resp.errorMessage || (resp.payload?.$case === "respAck" ? resp.payload.respAck.errorMsg : "");
      return said ? asIs(said) : say("web.settings.general.update_failed");
    } catch (err) {
      return asIs(err instanceof Error ? err.message : String(err));
    }
  };
  const toggleSmartSearch = async () => {
    if (refuseOnDemo()) return; // a demo device (net/demo.ts)
    setStatus(null);
    if (smartOn) { setSmartOffAsk(true); return; }
    setSmartBusy(true);
    const said = await sendSmartSearch(true);
    setSmartBusy(false);
    if (said) setStatus({ kind: "error", text: said });
  };

  const showHiddenMemories = async () => {
    if (refuseOnDemo()) return;
    setMemoriesBusy(true);
    setStatus(null);
    const said = await showAllMemories();
    setMemoriesBusy(false);
    if (said === null) {
      setMemoriesHidden(0);
      setStatus({ kind: "success", text: say("web.settings.memories.shown_again") });
    } else {
      setStatus({ kind: "error", text: said ? asIs(said) : say("web.settings.general.update_failed") });
    }
  };

  // Places: the device keeps where each photo was taken only while it is
  // on; turning it off deletes those locations before it answers.
  const togglePlaces = async () => {
    if (refuseOnDemo()) return; // a demo device (net/demo.ts)
    setPlacesBusy(true);
    setStatus(null);
    const next = !placesOn;
    try {
      const resp: RespEnvelope = await useWS.request((e) => {
        e.payload = { $case: "reqSetPlacesEnabled", reqSetPlacesEnabled: { enabled: next } };
      });
      if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
        setPlacesOn(next);
      } else {
        const said = resp.errorMessage || (resp.payload?.$case === "respAck" ? resp.payload.respAck.errorMsg : "");
        setStatus({ kind: "error", text: said ? asIs(said) : say("web.settings.general.update_failed") });
      }
    } catch (err) {
      setStatus({ kind: "error", text: asIs(err instanceof Error ? err.message : String(err)) });
    } finally {
      setPlacesBusy(false);
    }
  };

  // Issue #181: the tagging model can be turned off like face recognition.
  const toggleImageTagging = async () => {
    if (refuseOnDemo()) return; // a demo device (net/demo.ts)
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
        setStatus({ kind: "error", text: resp.errorMessage ? asIs(resp.errorMessage) : (resp.payload?.$case === "respAck" ? asIs(resp.payload.respAck.errorMsg) : say("web.settings.general.update_failed")) });
      }
    } catch (err: any) {
      setStatus({ kind: "error", text: asIs(err?.message ?? String(err)) });
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
    const timer = setInterval(loadReprocessStatus, 1500);
    return () => clearInterval(timer);
  }, [reprocessStatus?.status, loadReprocessStatus]);

  const startReprocess = async (forceRestart: boolean) => {

    if (refuseOnDemo()) return; // a demo device (net/demo.ts)
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
        setStatus({ kind: "error", text: resp.payload?.$case === "respAck" ? asIs(resp.payload.respAck.errorMsg) : say("web.settings.reprocess.start_failed") });
      }
    } catch (err: any) {
      setStatus({ kind: "error", text: asIs(err?.message ?? String(err)) });
    } finally {
      setReprocessBusy(false);
    }
  };

  // Cancelling doesn't wait for the worker to actually wind down (it
  // notices between files - see files_manager.CancelReprocess) - just
  // requests it and refreshes status, same as starting.
  const stopReprocess = async () => {
    if (refuseOnDemo()) return; // a demo device (net/demo.ts)
    setReprocessCancelling(true);
    setStatus(null);
    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqStopReprocess", reqStopReprocess: {} };
      });
      if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
        await loadReprocessStatus();
      } else {
        setStatus({ kind: "error", text: resp.payload?.$case === "respAck" ? asIs(resp.payload.respAck.errorMsg) : say("web.settings.reprocess.stop_failed") });
      }
    } catch (err: any) {
      setStatus({ kind: "error", text: asIs(err?.message ?? String(err)) });
    } finally {
      setReprocessCancelling(false);
    }
  };

  // Status
  // Kept as UiText: made when shown, so it follows a language change.
  const [status, setStatus] = useState<{ kind: "info"|"success"|"error"; text: UiText } | null>(null);
  // Anything but an error goes by itself.
  useEffect(() => {
    if (!status || status.kind === "error") return;
    const timer = window.setTimeout(() => setStatus(null), 5000);
    return () => window.clearTimeout(timer);
  }, [status]);

  useEffect(() => {
    if (pushSupported()) void isPushSubscribed().then(setPushSubscribed);
  }, []);

  const togglePush = async () => {

    if (refuseOnDemo()) return; // a demo device (net/demo.ts)
    setPushBusy(true);
    setStatus(null);
    try {
      if (pushSubscribed) {
        await disablePush();
        setPushSubscribed(false);
      } else {
        const ok = await enablePush();
        setPushSubscribed(ok);
        if (!ok) setStatus({ kind: "error", text: say("web.settings.notifications.denied") });
      }
    } catch (err: any) {
      setStatus({ kind: "error", text: asIs(err?.message ?? String(err)) });
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
          setFaceRecognition(!!s.faceRecognitionEnabled);
          setImageTaggingEnabled(!!s.imageTaggingEnabled);
          // Absent: a device before Places, with no switch.
          setPlacesKnown(s.placesEnabled !== undefined);
          if (s.placesEnabled !== undefined) setPlacesOn(s.placesEnabled);
          // Absent: a device before On this day.
          setMemoriesOnHere(s.memoriesEnabled ?? null);
          // Absent: a device before Smart search, with no switch (and
          // its model: a device before release 152, which had it).
          setSmartKnown(s.smartSearchEnabled !== undefined);
          if (s.smartSearchEnabled !== undefined) applySmartSearchSettings(s);
          if (s.memoriesEnabled) void countHiddenMemories();
          const limitMb = s.socialStorageLimitMb || 5120;
          setSocialLimitGB(String(Math.round((limitMb / 1024) * 10) / 10));
          setSocialUsedBytes(Number(s.socialStorageUsedBytes ?? 0));
        } else if (resp.payload?.$case === "respAck") {
          const msg = resp.payload.respAck.errorMsg;
          setStatus({ kind: "error", text: msg ? asIs(msg) : say("web.settings.general.load_failed") });
        }
      } catch (err: any) {
        setStatus({ kind: "error", text: asIs(err?.message ?? String(err)) });
      }
    })();
  }, [countHiddenMemories]);

  // ---------- Validation ----------

  const pwMismatch = newKey.length > 0 && confirmKey.length > 0 && newKey !== confirmKey;
  // The same 8 the setup wizard and the device ask for: the password is
  // the only credential for a device reachable through the bridge.
  const pwTooShort = newKey.length > 0 && newKey.length < 8;
  const canSaveKey = useMemo(() => {
    if (!oldKey || !newKey || !confirmKey) return false;
    if (pwTooShort) return false;
    if (pwMismatch) return false;
    if (oldKey === newKey) return false;
    return true;
  }, [oldKey, newKey, confirmKey, pwMismatch, pwTooShort]);

  // ---------- Actions ----------

  const changePassword = async () => {

    if (refuseOnDemo()) return; // a demo device (net/demo.ts)
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
        // holding something stale. Nor the next reconnect.
        useWS.passwordChanged(newKey);
        await useWS.refreshSessionToken();
        setOldKey(""); setNewKey(""); setConfirmKey("");
        setStatus({ kind: "success", text: say("web.settings.password.changed") });
      } else if (resp.payload?.$case === "respAck") {
        const msg = resp.payload.respAck.errorMsg;
        setStatus({ kind: "error", text: msg ? asIs(msg) : say("web.settings.password.failed") });
      } else {
        setStatus({ kind: "error", text: say("web.settings.password.unexpected") });
      }
    } catch (err: any) {
      setStatus({ kind: "error", text: asIs(err?.message ?? String(err)) });
    } finally {
      setSavingKey(false);
    }
  };

  return (
    <div className="sf-wrap">
      {/* The result of a change (password, notifications, faces, tagging,
          reprocess), fixed at the foot of the window where it is seen
          whatever was scrolled to - it used to be at the top of Settings,
          above the screen. Always on the page, so it is announced: an
          error at once (alert) and staying until closed or the next
          change; the rest politely, and gone after a while. */}
      <div className="sf-toast-wrap" role="alert">
        {status?.kind === "error" && (
          <div className="sf-status error">
            <span>{status.text(t)}</span>
            <button type="button" className="sf-status-close" onClick={() => setStatus(null)} aria-label={t("common.close")}>✕</button>
          </div>
        )}
      </div>
      <div className="sf-toast-wrap" role="status">
        {status && status.kind !== "error" && <div className={`sf-status ${status.kind}`}><span>{status.text(t)}</span></div>}
      </div>

      {/* Issue #84: first thing in Settings, not tucked away behind a
          top-level "Profile" tab (which used to show this only while
          signed out - an authenticated owner had no way at all to reach
          the editable form, since that tab showed FriendshipsManager
          instead whenever authenticated=true). SettingsForm only ever
          renders once authenticated (see App.tsx), so this is always the
          editable branch, never ProfileCard's own read-only visitor view. */}
      <section className="sf-section">
        <h3>{t("web.settings.profile.title")}</h3>
        <ProfileCard authenticated={true} />
      </section>

      {/* Storage: a disk the mirror could take, erased once confirmed
          (nothing shows otherwise). */}
      <MirrorDiskPanel />

      {/* What the device works through by itself (Status.background_tasks:
          new uploads' analysis, Smart search's descriptions, ...), with
          how far it is, from the Status the panel above polls; nothing
          shows while there is none. */}
      <BackgroundWorkPanel />

      <LanguagePanel />

      {onShowTour && (
        <section className="sf-section">
          <h3>{t("web.tour.settings.title")}</h3>
          <p className="sf-hint">{t("web.tour.settings.hint")}</p>
          {/* The tour's last step rings it (tourSteps.ts). */}
          <button type="button" className="sf-btn" data-tour="show-tour" onClick={onShowTour}>{t("web.tour.show")}</button>
        </section>
      )}

      <UsersPanel />

      <section className="sf-section">
        <h3>{t("web.settings.password.title")}</h3>
        <div className="sf-row">
          <label htmlFor="sf-old">{t("web.settings.password.old")}</label>
          <input
            id="sf-old"
            className="sf-input"
            type="password"
            value={oldKey}
            onChange={(e) => setOldKey(e.target.value)}
          />
        </div>
        <div className="sf-row">
          <label htmlFor="sf-new">{t("web.settings.password.new")}</label>
          <input
            id="sf-new"
            className="sf-input"
            type="password"
            value={newKey}
            onChange={(e) => setNewKey(e.target.value)}
          />
        </div>
        <div className="sf-row">
          <label htmlFor="sf-conf">{t("web.settings.password.confirm")}</label>
          <input
            id="sf-conf"
            className="sf-input"
            type="password"
            value={confirmKey}
            onChange={(e) => setConfirmKey(e.target.value)}
          />
        </div>
        {pwTooShort && <div className="sf-note error">{t("web.settings.password.too_short")}</div>}
        {pwMismatch && <div className="sf-note error">{t("web.settings.password.mismatch")}</div>}

        <button className="sf-btn" disabled={!canSaveKey || savingKey} onClick={() => void changePassword()}>
          {savingKey ? t("web.settings.general.saving") : t("web.settings.password.change")}
        </button>
      </section>

      {pushSupported() && (
        <section className="sf-section">
          <h3>{t("web.settings.notifications.title")}</h3>
          <p className="sf-hint">{t("web.settings.notifications.hint")}</p>
          <button className="sf-btn" disabled={pushBusy} onClick={() => void togglePush()}>
            {pushBusy ? t("web.settings.general.working") : pushSubscribed ? t("web.settings.notifications.disable") : t("web.settings.notifications.enable")}
          </button>
        </section>
      )}

      {/* Issue #145: joining the bridge after a setup without it. */}
      <BridgePanel />

      {/* The device's own Wi-Fi: another network, or its new password.
          The owner of the primary instance, on a device that changes its
          own Wi-Fi (the panel hides itself everywhere else). */}
      <WifiPanel />

      {/* Issue #80: Tailscale Funnel as an alternative to the bridge.
          Primary-only too - Funnel publishes the whole machine. */}
      <TailscalePanel />

      {/* Live device logs; copy or send them for support. Not on a demo
          device, which doesn't show them. */}
      {!demo && <LogsPanel />}

      <section className="sf-section">
        <h3>{t("web.settings.faces.title")}</h3>
        <p className="sf-hint">{t("web.settings.faces.hint")}</p>
        <button className="sf-btn" disabled={faceRecognitionBusy} onClick={() => void toggleFaceRecognition()}>
          {faceRecognitionBusy ? t("web.settings.general.working") : faceRecognitionEnabled ? t("web.settings.faces.disable") : t("web.settings.faces.enable")}
        </button>
      </section>

      {placesKnown && (
        <section className="sf-section">
          <h3>{t("web.settings.places.title")}</h3>
          <p className="sf-hint">{t("web.settings.places.hint")}</p>
          <button className="sf-btn" disabled={placesBusy} onClick={() => void togglePlaces()}>
            {placesBusy ? t("web.settings.general.working") : placesOn ? t("web.settings.places.disable") : t("web.settings.places.enable")}
          </button>
        </section>
      )}

      {memoriesOn !== null && (
        <section className="sf-section">
          <h3>{t("web.settings.memories.title")}</h3>
          <p className="sf-hint">{t("web.settings.memories.hint")}</p>
          <div className="sf-btn-row sf-mem-btns">
            <button className="sf-btn" disabled={memoriesBusy} onClick={() => void toggleMemories()}>
              {memoriesBusy ? t("web.settings.general.working") : memoriesOn ? t("web.settings.memories.disable") : t("web.settings.memories.enable")}
            </button>
            {memoriesOn && memoriesHidden > 0 && (
              <button className="sf-btn sf-btn-secondary" disabled={memoriesBusy} onClick={() => void showHiddenMemories()}>
                {t("web.settings.memories.show_hidden", { count: memoriesHidden })}
              </button>
            )}
          </div>
        </section>
      )}

      {smartKnown && (
        <section className="sf-section" aria-labelledby="sf-smart-title">
          <h3 id="sf-smart-title">
            {t("web.settings.smart_search.title")}
            <span className="sf-beta">{t("web.settings.smart_search.beta")}</span>
          </h3>
          <p className="sf-hint">{t("web.settings.smart_search.hint")}</p>
          <SmartSearchModelNote on={smartOn} model={smartModel} labelId="sf-smart-title" />
          <button className="sf-btn" disabled={smartBusy} onClick={() => void toggleSmartSearch()}>
            {smartBusy ? t("web.settings.general.working") : smartOn ? t("web.settings.smart_search.disable") : t("web.settings.smart_search.enable")}
          </button>
        </section>
      )}
      {smartOffAsk && (
        <ConfirmDialog
          title={t("web.settings.smart_search.off_title")}
          text={t("web.settings.smart_search.off_text")}
          confirm={t("web.settings.smart_search.off_confirm")}
          busyText={t("web.settings.general.working")}
          onConfirm={async () => {
            const said = await sendSmartSearch(false);
            if (!said) setSmartOffAsk(false);
            return said;
          }}
          onCancel={() => setSmartOffAsk(false)}
        />
      )}

      <section className="sf-section">
        <h3>{t("web.settings.tagging.title")}</h3>
        <p className="sf-hint">{t("web.settings.tagging.hint")}</p>
        <button className="sf-btn" disabled={imageTaggingBusy} onClick={() => void toggleImageTagging()}>
          {imageTaggingBusy ? t("web.settings.general.working") : imageTaggingEnabled ? t("web.settings.tagging.disable") : t("web.settings.tagging.enable")}
        </button>
      </section>

      <section className="sf-section">
        <h3>{t("web.settings.social.title")}</h3>
        <p className="sf-hint">
          {t("web.settings.social.hint")}
          {socialUsedBytes !== null && <> {t("web.settings.social.using", { size: usedGB(t, socialUsedBytes) })}</>}
        </p>
        <div className="sf-row">
          <label htmlFor="sf-social-limit">{t("web.settings.social.limit")}</label>
          <div className="sf-secret-row">
            <input id="sf-social-limit" className="sf-input" type="number" min="0.1" step="0.5" inputMode="decimal"
              value={socialLimitGB} onChange={e => setSocialLimitGB(e.target.value)} />
            <button className="sf-btn small" type="button" disabled={socialLimitBusy} onClick={() => void saveSocialLimit()}>
              {socialLimitBusy ? t("web.settings.general.saving") : t("common.save")}
            </button>
          </div>
        </div>
        {socialLimitNote && <p className={`sf-note ${socialLimitNote.kind === "ok" ? "success" : "error"}`}>{socialLimitNote.text(t)}</p>}
      </section>

      {/* Issue #94: in-place updates. Renders nothing on a non-primary
          instance - see UpdatePanel. */}
      <UpdatePanel />

      {/* Issue #180: share links, and the space their copies take. */}
      <SharedLinksPanel />

      <section className="sf-section">
        <h3>{t("web.settings.reprocess.title")}</h3>
        <p className="sf-hint">{t("web.settings.reprocess.hint")}</p>
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
                {t("web.settings.reprocess.progress", {
                  percent: percentText(t, reprocessPercent(reprocessStatus)),
                  processed: reprocessStatus.processed,
                  total: reprocessStatus.total,
                })}
              </div>
              <button className="sf-btn small sf-danger" disabled={reprocessCancelling} onClick={() => void stopReprocess()}>
                {reprocessCancelling ? t("web.settings.reprocess.stopping") : t("web.settings.reprocess.cancel")}
              </button>
            </div>
          </div>
        ) : reprocessStatus?.status === "stopped" ? (
          <>
            <div className="sf-hint">
              {t("web.settings.reprocess.stopped", {
                percent: percentText(t, reprocessPercent(reprocessStatus)),
                processed: reprocessStatus.processed,
                total: reprocessStatus.total,
              })}
            </div>
            <div className="sf-btn-row">
              <button className="sf-btn" disabled={reprocessBusy} onClick={() => { if (!refuseOnDemo()) setReprocessConfirming("resume"); }}>
                {reprocessBusy ? t("web.settings.general.starting") : t("web.settings.reprocess.resume_all")}
              </button>
              <button className="sf-btn sf-btn-secondary" disabled={reprocessBusy} onClick={() => { if (!refuseOnDemo()) setReprocessConfirming("restart"); }}>
                {t("web.settings.reprocess.start_over")}
              </button>
            </div>
          </>
        ) : (
          <>
            <button className="sf-btn" disabled={reprocessBusy} onClick={() => { if (!refuseOnDemo()) setReprocessConfirming("restart"); }}>
              {reprocessBusy ? t("web.settings.general.starting") : t("web.settings.reprocess.all")}
            </button>
            {reprocessStatus?.status === "completed" && (
              <div className="sf-hint">{t("web.settings.reprocess.completed", { count: reprocessStatus.processed })}</div>
            )}
            {reprocessStatus?.status === "failed" && (
              <div className="sf-note error">{t("web.settings.reprocess.failed", { count: reprocessStatus.processed })}</div>
            )}
          </>
        )}
      </section>

      {reprocessConfirming && (
        <div className="sf-modal" onClick={() => setReprocessConfirming(null)}>
          <div className="sf-modal-inner" onClick={e => e.stopPropagation()}>
            <p>
              {reprocessConfirming === "resume"
                ? t("web.settings.reprocess.confirm_resume")
                : t("web.settings.reprocess.confirm_restart")}
            </p>
            <div className="sf-modal-actions">
              <button className="sf-btn small" onClick={() => setReprocessConfirming(null)}>{t("common.cancel")}</button>
              <button className="sf-btn small sf-danger" onClick={() => void startReprocess(reprocessConfirming === "restart")}>
                {reprocessConfirming === "resume" ? t("web.settings.reprocess.resume") : t("web.settings.reprocess.confirm")}
              </button>
            </div>
          </div>
        </div>
      )}

      <section className="sf-section">
        <h3>{t("web.settings.session.title")}</h3>
        <p className="sf-hint">{t("web.settings.session.hint")}</p>
        {/* The same as the account menu's Sign out (net/signOut.ts):
            the device forgets this browser's push subscription and
            tokens, then the page reloads signed out. aria-disabled
            rather than disabled while it runs, so the focus stays on the
            button and a screen reader hears its new label. */}
        <button
          className="sf-btn"
          aria-disabled={signingOut || undefined}
          onClick={() => {
            if (signingOut) return;
            setSigningOut(true);
            void signOut();
          }}
        >
          {signingOut ? t("web.settings.session.signing_out") : t("web.settings.session.sign_out")}
        </button>
      </section>
    </div>
  );
}


const cSaidMs = 5000;

// Smart search's model under the section's hint (smartSearch.ts' modelView):
// while off, that turning it on downloads it (it is on by default since
// release 152, so nothing here says the user turned it on); the download - a
// bar and its percent, or the device's words while a try failed (it tries
// again by itself). Nothing once it is there. A screen reader hears only
// when the download starts, runs into a problem and is done (announcement),
// not every percent.
function SmartSearchModelNote({ on, model, labelId }: { on: boolean; model: SmartModel; labelId: string }) {
  const t = useT();
  const view = modelView(on, model);
  const [said, setSaid] = useState<Announcement | null>(null);
  const shown = useRef<ModelView | null>(null);
  useEffect(() => {
    const now = modelView(on, model);
    const next = announcement(shown.current, now);
    shown.current = now;
    if (next) setSaid(next);
  }, [on, model]);
  // Said, then emptied: no old words left for a screen reader to come
  // across later, and the same words said again are heard again.
  useEffect(() => {
    if (!said) return;
    const timer = setTimeout(() => setSaid(null), cSaidMs);
    return () => clearTimeout(timer);
  }, [said]);
  const live = !said ? "" : said.kind === "problem" ? said.text : t("web.settings.smart_search.downloading", { percent: said.percent });
  return (
    <>
      {view.kind === "note" && <p className="sf-hint">{t("web.settings.smart_search.download_note")}</p>}
      {view.kind === "downloading" && (
        <div className="sf-progress sf-smart-dl">
          <div className="sf-progress-track" role="progressbar" aria-labelledby={labelId} aria-describedby="sf-smart-dl-text"
            aria-valuemin={0} aria-valuemax={100} aria-valuenow={view.percent}>
            <div className="sf-progress-fill" style={{ width: `${view.percent}%` }} />
          </div>
          <p id="sf-smart-dl-text" className="sf-hint">
            {view.problem || t("web.settings.smart_search.downloading", { percent: view.percent })}
          </p>
        </div>
      )}
      <span className="sf-sr" role="status">{live}</span>
    </>
  );
}
