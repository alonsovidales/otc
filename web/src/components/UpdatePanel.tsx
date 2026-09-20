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
import type { RespEnvelope } from "../proto/messages";
import Spinner from "./Spinner";
import "./UpdatePanel.css";

type Release = { version: number; description: string };
type UpdateInfo = {
  currentVersion: number;
  latestVersion: number;
  pending: Release[];
  state: string;
  message: string;
  lastUpdated: string;
  checkError: string;
};

// While an update runs the device rebuilds and restarts itself, so the
// socket drops partway through - polling is how the panel picks the story
// back up once it reconnects.
const cPollMs = 5000;

// The runner writes an ISO timestamp; show it in the reader's own terms,
// and fall back to the raw value rather than printing "Invalid Date".
const formatWhen = (iso: string) => {
  const when = new Date(iso);
  return isNaN(when.getTime()) ? iso : when.toLocaleString();
};

export default function UpdatePanel() {
  const [isPrimary, setIsPrimary] = useState<boolean | null>(null);
  const [info, setInfo] = useState<UpdateInfo | null>(null);
  const [checking, setChecking] = useState(false);
  const [starting, setStarting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

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
          pending: u.pending.map(p => ({ version: p.version, description: p.summary })),
          state: u.state,
          message: u.message,
          lastUpdated: u.lastUpdated,
          checkError: u.checkError,
        });
        setError(null);
      } else if (resp.error) {
        setError(resp.errorMessage || "Could not check for updates.");
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
    setStarting(true);
    setError(null);
    try {
      const resp: RespEnvelope = await useWS.request(e => {
        (e as any).payload = { $case: "reqApplyUpdate", reqApplyUpdate: {} };
      });
      if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
        setInfo(prev => (prev ? { ...prev, state: "running", message: "Starting" } : prev));
      } else {
        setError(resp.errorMessage || "Could not start the update.");
      }
    } catch (e: any) {
      setError(e?.message ?? String(e));
    } finally {
      setStarting(false);
    }
  };

  if (isPrimary === false) return null;

  const running = info?.state === "running";
  const hasUpdate = !!info && info.pending.length > 0;

  return (
    <section className="sf-section">
      <h3>Device version</h3>

      {!info ? (
        <div className="up-line"><Spinner label="Checking for updates…" /></div>
      ) : (
        <>
          <div className="up-line">
            <span className="up-version">
              Version {info.currentVersion || "—"}
              {hasUpdate && <> → <strong>{info.latestVersion}</strong></>}
            </span>
            {!hasUpdate && !running && !info.checkError && (
              <span className="up-ok">Up to date</span>
            )}
          </div>

          {hasUpdate && (
            <ul className="up-releases">
              {info.pending.map(r => (
                <li key={r.version}>
                  <strong>{r.version}</strong> {r.description}
                </li>
              ))}
            </ul>
          )}

          {running && (
            <div className="up-line">
              <Spinner label={info.message || "Updating…"} />
            </div>
          )}

          {info.state === "failed" && (
            // With the date: a failure sits in the status file until
            // another run replaces it, so one from weeks ago would
            // otherwise read as something that just happened.
            <p className="up-error">
              Update failed{info.lastUpdated ? ` on ${formatWhen(info.lastUpdated)}` : ""}: {info.message}
            </p>
          )}

          {info.checkError && (
            <p className="up-note">
              Couldn't reach the update server just now, so this may be out of date.
            </p>
          )}

          <div className="up-actions">
            <button className="sf-btn" onClick={() => void check()} disabled={checking || running}>
              {checking ? "Checking…" : "Check again"}
            </button>
            {hasUpdate && (
              <button className="sf-btn primary" onClick={() => void apply()} disabled={starting || running}>
                {starting ? "Starting…" : `Update to ${info.latestVersion}`}
              </button>
            )}
          </div>

          {(hasUpdate || running) && (
            <p className="up-note">
              The device rebuilds itself and restarts, which takes a few minutes and
              drops this connection on the way. Your photos and settings are left
              alone.
            </p>
          )}
        </>
      )}

      {error && <p className="up-error">{error}</p>}
    </section>
  );
}
