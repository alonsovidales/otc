// SPDX-License-Identifier: AGPL-3.0-or-later

// Issue #94: updating the device from Settings, without anyone rebuilding
// or reinstalling anything.
//
// Shown only on the primary instance, the same rule the Users panel
// follows: an additional user (issue #82) is a separate process sharing
// one machine, and the binary and schema it runs on are not theirs to
// replace. The device enforces that too - this only decides what to draw.
import { useCallback, useEffect, useRef, useState } from "react";
import { useWS } from "../net/useWS";
import { isDeviceStatusCode } from "../net/deviceStatus";
import type { RespEnvelope } from "../proto/messages";
import { Trans, dateTimeFields, fmtDate, useT, type TFunction } from "../i18n";
import Spinner from "./Spinner";
import { asIs, say, type UiText } from "./uiText";
import "./UpdatePanel.css";
import { refuseOnDemo } from "../net/demo";

// Issue #183: releases have kinds and "major.minor" labels; one without a
// label (from before them) shows as a build. The description is the
// release's own summary, which stays English (docs/i18n.md).
type Release = { version: number; description: string; kind: string; label: string };
type UpdateInfo = {
  currentVersion: number;
  latestVersion: number;
  currentLabel: string;
  latestLabel: string;
  pending: Release[];
  state: string;
  // The runner's status line, in the device's words - or ours right after
  // the update was asked for, until the device reports its own.
  message: UiText;
  lastUpdated: string;
  checkError: string;
};

// "1.1 (build 85)", or "build 79" for a release from before labels. Build
// numbers are versions: written as they are, never grouped.
function versionText(t: TFunction, label: string, build: number) {
  if (!build) return "—";
  return label
    ? t("web.settings.update.version_label", { label, build: String(build) })
    : t("web.settings.update.version_build", { build: String(build) });
}

// While an update runs the device rebuilds and restarts itself, so the
// socket drops partway through - polling is how the panel picks the story
// back up once it reconnects.
const cPollMs = 5000;

// The runner writes an ISO timestamp; show it in the page's language (the
// browser's own format when it is that language), and fall back to the raw
// value rather than printing "Invalid Date".
const formatWhen = (t: TFunction, iso: string) => {
  const when = new Date(iso);
  return isNaN(when.getTime()) ? iso : fmtDate(t.lang, when, dateTimeFields);
};

// Settings > Restart device. "asking" until the device answers, then
// "restarting" until it answers again, "back" once it has, or "slow" when
// it hasn't within cRestartGiveUpMs.
type RestartPhase = "" | "asking" | "restarting" | "back" | "slow";
const cRestartFirstLookMs = 20_000;
const cRestartPollMs = 5000;
// Until the restart really begins the device still answers: an answer
// counts once a look has failed since, or after this long.
const cRestartSureMs = 90_000;
const cRestartGiveUpMs = 5 * 60_000;
const cRestartLookTimeoutMs = 10_000;

const sleep = (ms: number) => new Promise<void>(r => setTimeout(r, ms));

export default function UpdatePanel() {
  const t = useT();
  const [isPrimary, setIsPrimary] = useState<boolean | null>(null);
  const [info, setInfo] = useState<UpdateInfo | null>(null);
  const [checking, setChecking] = useState(false);
  const [starting, setStarting] = useState(false);
  const [error, setError] = useState<UiText | null>(null);
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const [restartPhase, setRestartPhase] = useState<RestartPhase>("");
  const [restartError, setRestartError] = useState<UiText | null>(null);
  // Bumped on unmount, so a wait for the device stops with the panel.
  const restartGen = useRef(0);
  useEffect(() => () => { restartGen.current++; }, []);

  useEffect(() => {
    void (async () => {
      try {
        const resp: RespEnvelope = await useWS.request(e => {
          (e as any).payload = { $case: "reqGetInstanceRole", reqGetInstanceRole: {} };
        });
        setIsPrimary(resp.payload?.$case === "respInstanceRole" && resp.payload.respInstanceRole.isPrimary);
      } catch {
        setIsPrimary(false);
      }
    })();
  }, []);

  const check = useCallback(async () => {
    setChecking(true);
    try {
      const resp: RespEnvelope = await useWS.request(e => {
        (e as any).payload = { $case: "reqCheckUpdate", reqCheckUpdate: {} };
      });
      if (resp.payload?.$case === "respUpdateInfo") {
        const u = resp.payload.respUpdateInfo;
        setInfo({
          currentVersion: u.currentVersion,
          latestVersion: u.latestVersion,
          currentLabel: u.currentLabel,
          latestLabel: u.latestLabel,
          pending: u.pending.map(p => ({ version: p.version, description: p.summary, kind: p.kind, label: p.label })),
          state: u.state,
          message: asIs(u.message),
          lastUpdated: u.lastUpdated,
          checkError: u.checkError,
        });
        setError(null);
      } else if (resp.error) {
        setError(() => (resp.errorMessage ? asIs(resp.errorMessage) : say("web.settings.update.check_failed")));
      }
    } catch (e: any) {
      // A dropped socket mid-update is expected, not a failure to report:
      // the device is restarting into the version it just installed.
      setError(null);
      console.error("update check failed:", e);
    } finally {
      setChecking(false);
    }
  }, []);

  useEffect(() => {
    if (isPrimary) void check();
  }, [isPrimary, check]);

  // Poll only while something is actually happening.
  useEffect(() => {
    const running = info?.state === "running";
    if (running && !pollRef.current) {
      pollRef.current = setInterval(() => void check(), cPollMs);
    } else if (!running && pollRef.current) {
      clearInterval(pollRef.current);
      pollRef.current = null;
    }
    return () => {
      if (pollRef.current) { clearInterval(pollRef.current); pollRef.current = null; }
    };
  }, [info?.state, check]);

  const apply = async () => {

    if (refuseOnDemo()) return; // a demo device (net/demo.ts)
    setStarting(true);
    setError(null);
    try {
      const resp: RespEnvelope = await useWS.request(e => {
        (e as any).payload = { $case: "reqApplyUpdate", reqApplyUpdate: {} };
      });
      if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
        setInfo(prev => (prev ? { ...prev, state: "running", message: say("web.settings.update.starting_status") } : prev));
      } else {
        setError(() => (resp.errorMessage ? asIs(resp.errorMessage) : say("web.settings.update.start_failed")));
      }
    } catch (e: any) {
      setError(() => asIs(e?.message ?? String(e)));
    } finally {
      setStarting(false);
    }
  };

  // Waits for the device to answer again after a restart. Through the
  // bridge a device that is away still gets an answer - the bridge's
  // error - which isn't the device, so only the device's own answer counts.
  const waitUntilBack = async (gen: number) => {
    const asked = Date.now();
    let failed = false;
    await sleep(cRestartFirstLookMs);
    while (restartGen.current === gen && Date.now() - asked < cRestartGiveUpMs) {
      let answered = false;
      try {
        const resp: RespEnvelope = await Promise.race([
          useWS.request(e => {
            (e as any).payload = { $case: "reqGetInstanceRole", reqGetInstanceRole: {} };
          }),
          sleep(cRestartLookTimeoutMs).then(() => { throw new Error("no answer"); }),
        ]);
        // The device's own answer - or its own refusal, when this tab
        // must sign in again (a session token doesn't outlive a restart).
        const bridgeSaid = resp.payload?.$case === "respAck" && isDeviceStatusCode(resp.payload.respAck.code);
        answered = resp.payload?.$case === "respInstanceRole" || (resp.error && !bridgeSaid);
      } catch {
        answered = false;
      }
      if (!answered) failed = true;
      if (restartGen.current !== gen) return;
      if (answered && (failed || Date.now() - asked >= cRestartSureMs)) {
        setRestartPhase("back");
        void check();
        return;
      }
      await sleep(cRestartPollMs);
    }
    if (restartGen.current === gen) setRestartPhase("slow");
  };

  const restart = async () => {

    if (refuseOnDemo()) return; // a demo device (net/demo.ts)
    if (!window.confirm(t("web.settings.restart.confirm"))) return;
    const gen = ++restartGen.current;
    setRestartPhase("asking");
    setRestartError(null);
    try {
      const resp: RespEnvelope = await useWS.request(e => {
        (e as any).payload = { $case: "reqRestartDevice", reqRestartDevice: {} };
      });
      if (resp.payload?.$case !== "respRestartingDevice") {
        setRestartPhase("");
        setRestartError(() => (resp.error && resp.errorCode === "unknown_payload"
          ? say("web.settings.restart.needs_update")
          : (resp.errorMessage ? asIs(resp.errorMessage) : say("web.settings.restart.failed"))));
        return;
      }
    } catch (e: any) {
      setRestartPhase("");
      setRestartError(() => asIs(e?.message ?? String(e)));
      return;
    }
    setRestartPhase("restarting");
    void waitUntilBack(gen);
  };

  if (isPrimary === false) return null;

  const running = info?.state === "running";
  const hasUpdate = !!info && info.pending.length > 0;

  return (
    <section className="sf-section">
      <h3>{t("web.settings.update.title")}</h3>

      {!info ? (
        <div className="up-line"><Spinner label={t("web.settings.update.checking_updates")} /></div>
      ) : (
        <>
          <div className="up-line">
            <span className="up-version">
              {hasUpdate ? (
                <Trans
                  k="web.settings.update.version_to"
                  args={{ version: versionText(t, info.currentLabel, info.currentVersion), latest: versionText(t, info.latestLabel, info.latestVersion) }}
                  parts={{ b: (s) => <strong>{s}</strong> }}
                />
              ) : t("web.settings.update.version", { version: versionText(t, info.currentLabel, info.currentVersion) })}
            </span>
            {!hasUpdate && !running && !info.checkError && (
              <span className="up-ok">{t("web.settings.update.up_to_date")}</span>
            )}
          </div>

          {hasUpdate && (
            <ul className="up-releases">
              {info.pending.map(r => (
                <li key={r.version}>
                  <strong>{r.label || t("web.settings.update.version_build", { build: String(r.version) })}</strong>
                  {(r.kind === "major" || r.kind === "critical") && <span className={`up-kind ${r.kind}`}>{r.kind === "critical" ? t("web.settings.update.critical") : t("web.settings.update.major")}</span>}
                  {" "}{r.description}
                </li>
              ))}
            </ul>
          )}

          {running && (
            <div className="up-line">
              <Spinner label={info.message(t) || t("common.updating")} />
            </div>
          )}

          {info.state === "failed" && (
            // With the date: a failure sits in the status file until
            // another run replaces it, so one from weeks ago would
            // otherwise read as something that just happened.
            <p className="up-error">
              {info.lastUpdated
                ? t("web.settings.update.failed_on", { date: formatWhen(t, info.lastUpdated), reason: info.message(t) })
                : t("web.settings.update.failed", { reason: info.message(t) })}
            </p>
          )}

          {info.checkError && (
            <p className="up-note">{t("web.settings.update.server_unreachable")}</p>
          )}

          <div className="up-actions">
            <button className="sf-btn" onClick={() => void check()} disabled={checking || running}>
              {checking ? t("web.settings.update.checking") : t("web.settings.update.check_again")}
            </button>
            {hasUpdate && (
              <button className="sf-btn primary" onClick={() => void apply()} disabled={starting || running}>
                {starting ? t("web.settings.general.starting") : t("web.settings.update.update_to", { version: `${info.latestLabel || info.latestVersion}` })}
              </button>
            )}
          </div>

          {(hasUpdate || running) && (
            <p className="up-note">{t("web.settings.update.restarts_note")}</p>
          )}
        </>
      )}

      {error && <p className="up-error">{error(t)}</p>}

      {/* Settings > Restart device: the whole machine, owner of the main
          instance only (the device refuses the others too). */}
      {isPrimary && (
        <div className="up-restart">
          <div className="up-actions">
            <button className="sf-btn sf-danger" onClick={() => void restart()}
              disabled={restartPhase === "asking" || restartPhase === "restarting" || info?.state === "running"}>
              {restartPhase === "asking" || restartPhase === "restarting" ? t("web.settings.restart.restarting") : t("web.settings.restart.button")}
            </button>
          </div>
          {restartPhase === "restarting" && (
            <div className="up-line"><Spinner label={t("web.settings.restart.wait")} /></div>
          )}
          {restartPhase === "back" && <p className="up-note">{t("web.settings.restart.back")}</p>}
          {restartPhase === "slow" && <p className="up-warn">{t("web.settings.restart.slow")}</p>}
          {restartError && <p className="up-error">{restartError(t)}</p>}
        </div>
      )}
    </section>
  );
}
