// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Settings › Wi-Fi: the network the device uses. Its owner changes it here
// (another network, or the same one with its new password) when the
// router's name or password changed. The device's own Wi-Fi, never this
// computer's.
//
// Shown to the owner of the primary instance of a device that changes its
// own Wi-Fi (GetWifiStatus managed: NetworkManager and the root runner, not
// in a container); nothing anywhere else. The device hands the change to
// its root runner, which goes back to the network it was on when the new
// one doesn't work, so the connection may drop for about a minute: the
// panel then asks GetWifiStatus until the change it asked for has ended,
// and shows the device's words for how it went (with a failure, where the
// device ended up). The rules are wifiFlow.ts, shared with the phones'
// WifiSection (iOS) and WifiSection() (Android): change them together.
//
// While the device can't be reached the whole app gives way to
// DeviceUnreachable, and this panel goes with it: the change under way is
// kept at module level, so Settings coming back picks the wait up again
// and shows how it went.

import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { useWS } from "../../net/useWS";
import { encryptForConnection } from "../../net/pwCrypto";
import { refuseOnDemo } from "../../net/demo";
import type { RespEnvelope, WifiJoin, WifiNetwork, WifiStatus } from "../../proto/messages";
import { useT, type TFunction } from "../../i18n";
import Spinner from "../Spinner";
import { asIs, percentText, say, type PlainKey, type UiText } from "../uiText";
import {
  cLookTimeoutMs, confirmKey, joinable, judgeJoin, judgePoll, judgeStatus, keepsPicker,
  checkPassword, hiddenFor, kindOf, listedNetworks, nextPhase, pollDelay, resumeID, sentPassword, signalBars,
  validateSsid, words,
  type NameProblem, type PasswordProblem, type StatusVerdict,
} from "./wifiFlow";
import "../UpdatePanel.css";
import "./WifiPanel.css";

// The network chosen in the list: one listed (secured or open), or one
// typed by hand (Other Network…: hidden, its name in the name field).
type Chosen = { ssid: string; secured: boolean; hidden: boolean };

// idle; sending (sealing the password, then JoinWifi); waiting for the
// change to end; slow once that has taken cSlowAfterMs.
type Phase = "idle" | "sending" | "waiting" | "slow";

const problemKeys: Record<NameProblem | PasswordProblem, PlainKey> = {
  name_needed: "web.settings.wifi.name_needed",
  name_too_long: "web.settings.wifi.name_too_long",
  // Half a surrogate pair, which no field really holds; such a listed
  // network can't be chosen (joinable).
  name_invalid: "web.settings.wifi.failed",
  password_needed: "web.settings.wifi.password_needed",
  password_invalid: "web.settings.wifi.password_invalid",
};

// A device before release 140 answered unknown_payload: not asked again
// until the next sign-in (the phones' rule).
let unsupported = false;
let watchingSignIns = false;
function rememberUnsupported() {
  unsupported = true;
  if (watchingSignIns) return;
  watchingSignIns = true;
  useWS.onSignedIn(() => { unsupported = false; });
}

// The change this page asked for, until its result has been shown.
// chosen is what to show again after a wrong password (null: a change
// picked up after a reload, whose network this page never had).
let ongoing: { id: string; ssid: string; since: number; chosen: Chosen | null } | null = null;

const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

function askStatus(): Promise<RespEnvelope> {
  return useWS.request((e) => {
    e.payload = { $case: "reqGetWifiStatus", reqGetWifiStatus: {} };
  });
}

// One look while the device may be switching networks: a request that
// gets no answer in cLookTimeoutMs counts as no answer.
function lookOnce(): Promise<RespEnvelope> {
  return Promise.race([
    askStatus(),
    sleep(cLookTimeoutMs).then((): RespEnvelope => { throw new Error("no answer"); }),
  ]);
}

const messageOf = (e: unknown) => (e instanceof Error ? e.message : String(e));

function LockIcon({ label }: { label: string }) {
  return (
    <svg className="wf-lock" width="14" height="14" viewBox="0 0 24 24" fill="none" role="img" aria-label={label} data-tip={label}>
      <rect x="5" y="11" width="14" height="10" rx="2" stroke="currentColor" strokeWidth="2" />
      <path d="M8 11V8a4 4 0 0 1 8 0v3" stroke="currentColor" strokeWidth="2" strokeLinecap="round" />
    </svg>
  );
}

// 1 to 4 bars; the percent beside it is what is read out.
function SignalIcon({ signal }: { signal: number }) {
  const bars = signalBars(signal);
  return (
    <svg className="wf-bars" width="16" height="14" viewBox="0 0 16 14" aria-hidden="true">
      {[0, 1, 2, 3].map((i) => (
        <rect key={i} x={i * 4} y={10 - i * 3} width="3" height={4 + i * 3} rx="1" className={i < bars ? "on" : ""} />
      ))}
    </svg>
  );
}

function WifiIcon() {
  return (
    <svg className="wf-icon" width="16" height="16" viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <path d="M2 9a15 15 0 0 1 20 0M5.5 12.5a10 10 0 0 1 13 0M9 16a5 5 0 0 1 6 0" stroke="currentColor" strokeWidth="2" strokeLinecap="round" />
      <circle cx="12" cy="19.5" r="1.5" fill="currentColor" />
    </svg>
  );
}

function CableIcon() {
  return (
    <svg className="wf-icon" width="16" height="16" viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <rect x="6" y="3" width="12" height="10" rx="2" stroke="currentColor" strokeWidth="2" />
      <path d="M10 6v3M14 6v3M12 13v8" stroke="currentColor" strokeWidth="2" strokeLinecap="round" />
    </svg>
  );
}

function NetworkRow({ n, checked, disabled, onChoose, t }: {
  n: WifiNetwork; checked: boolean; disabled: boolean; onChoose: (c: Chosen) => void; t: TFunction;
}) {
  const ok = joinable(n);
  return (
    <label className={`wf-net${ok ? "" : " wf-off"}${checked ? " wf-on" : ""}`}>
      <input
        type="radio"
        name="wf-net"
        checked={checked}
        disabled={disabled || !ok}
        onChange={() => onChoose({ ssid: n.ssid, secured: n.secured, hidden: false })}
      />
      <bdi className="wf-ssid" title={n.ssid}>{n.ssid}</bdi>
      {n.current && <span className="wf-badge">{t("web.settings.wifi.current_badge")}</span>}
      <span className="wf-meta">
        {!ok && <span className="wf-tag">{t("web.settings.wifi.unsupported")}</span>}
        {n.secured ? (
          <LockIcon label={t("web.settings.wifi.secured")} />
        ) : (
          <span className="wf-tag">{t("web.settings.wifi.open_network")}</span>
        )}
        <SignalIcon signal={n.signal} />
        <span className="wf-signal">{percentText(t, n.signal)}</span>
      </span>
    </label>
  );
}

export default function WifiPanel() {
  const t = useT();
  const [status, setStatus] = useState<WifiStatus | null>(null);
  const [hidden, setHidden] = useState(unsupported);
  const [picking, setPicking] = useState(false);
  const [networks, setNetworks] = useState<WifiNetwork[] | null>(null);
  const networksRef = useRef<WifiNetwork[] | null>(null);
  networksRef.current = networks;
  const [scanning, setScanning] = useState(false);
  const [chosen, setChosen] = useState<Chosen | null>(null);
  const chosenRef = useRef<Chosen | null>(null);
  chosenRef.current = chosen;
  const [typedName, setTypedName] = useState("");
  // The Wi-Fi password, only until it is sealed (or another network is
  // chosen, or the list is closed); never stored anywhere else.
  const [password, setPassword] = useState("");
  const [phase, setPhase] = useState<Phase>("idle");
  const phaseRef = useRef<Phase>("idle");
  phaseRef.current = phase;
  const [joiningSsid, setJoiningSsid] = useState("");
  // ok: the change worked (shown as a success).
  const [note, setNote] = useState<{ text: UiText; ok: boolean } | null>(null);
  const [error, setError] = useState<UiText | null>(null);
  const [fieldError, setFieldError] = useState<UiText | null>(null);
  // Bumped by each new wait (and list): one whose number has changed, or
  // whose panel has gone (alive), stops.
  const waitGen = useRef(0);
  const scanGen = useRef(0);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => { alive.current = false; };
  }, []);

  const scan = useCallback(async () => {
    const gen = ++scanGen.current;
    setScanning(true);
    try {
      const resp: RespEnvelope = await useWS.request((e) => {
        e.payload = { $case: "reqListWifiNetworks", reqListWifiNetworks: {} };
      });
      if (!alive.current || gen !== scanGen.current) return;
      if (resp.payload?.$case === "respWifiNetworks") {
        const list = listedNetworks(resp.payload.respWifiNetworks.networks);
        setNetworks(list);
        // A listed network that is gone from the list is chosen no more.
        const c = chosenRef.current;
        if (c && !c.hidden && !list.some((n) => n.ssid === c.ssid && joinable(n))) {
          setChosen(null);
          setPassword("");
        }
      } else {
        const w = words(resp);
        setError(() => (w ? asIs(w) : say("web.settings.wifi.scan_failed")));
      }
    } catch (e) {
      if (!alive.current || gen !== scanGen.current) return;
      setError(() => asIs(messageOf(e)));
    } finally {
      if (alive.current && gen === scanGen.current) setScanning(false);
    }
  }, []);

  // A change ended (the device said how): its words, and the list closed -
  // or, after a wrong password or name, still open on the same network for
  // another try.
  const finish = useCallback((join: WifiJoin, tried: Chosen | null) => {
    ongoing = null;
    waitGen.current++;
    setPhase("idle");
    if (join.state === "connected") {
      setNote(join.message ? { text: asIs(join.message), ok: true } : null);
      setError(null);
      setPicking(false);
      setChosen(null);
      setNetworks(null);
      return;
    }
    setNote(null);
    setError(() => (join.message ? asIs(join.message) : say("web.settings.wifi.failed")));
    if (tried && keepsPicker(join.code)) {
      setPicking(true);
      setChosen(tried);
      if (tried.hidden) setTypedName(tried.ssid);
      setPassword("");
      // Settings came back after the device was away: list again.
      if (networksRef.current === null) void scan();
      return;
    }
    setPicking(false);
    setChosen(null);
  }, [scan]);

  // The device couldn't say how the change went (it restarted since, or
  // another change replaced it): the line above shows where it is.
  const lost = useCallback(() => {
    ongoing = null;
    waitGen.current++;
    setPhase("idle");
    setPicking(false);
    setChosen(null);
    setError(null);
    setNote({ text: say("web.settings.wifi.unknown_result"), ok: false });
  }, []);

  // Asks GetWifiStatus until the change id has ended (the restart's
  // waitUntilBack in UpdatePanel, with the device's own word for the end).
  const waitFor = useCallback(async (id: string, since: number) => {
    const gen = ++waitGen.current;
    const current = () => alive.current && gen === waitGen.current;
    while (current()) {
      const elapsed = Date.now() - since;
      setPhase(nextPhase(elapsed));
      await sleep(pollDelay(elapsed));
      if (!current()) return;
      let resp: RespEnvelope;
      try {
        resp = await lookOnce();
      } catch {
        continue; // no answer: the device is switching networks
      }
      if (!current()) return;
      const v = judgePoll(resp, id);
      if (v.kind === "ignore") continue;
      setStatus(v.status);
      if (v.kind === "waiting") continue;
      if (v.kind === "unknown") { lost(); return; }
      finish(v.join, ongoing?.id === id ? ongoing.chosen : null);
      return;
    }
  }, [finish, lost]);

  // What the device says now: the line at the top, and a change under way
  // or just ended - this page's (Settings came back), or one asked for
  // before a reload, picked up while it runs.
  const take = useCallback((s: WifiStatus) => {
    setStatus(s);
    const join = s.lastJoin;
    if (ongoing) {
      const ours = ongoing;
      if (!join || join.id !== ours.id) { lost(); return; }
      setJoiningSsid(ours.ssid);
      if (join.state === "joining") {
        if (phaseRef.current !== "waiting" && phaseRef.current !== "slow") void waitFor(ours.id, ours.since);
        return;
      }
      finish(join, ours.chosen);
      return;
    }
    const resume = resumeID(s);
    if (join && resume && phaseRef.current === "idle") {
      ongoing = { id: resume, ssid: join.ssid, since: Date.now(), chosen: null };
      setJoiningSsid(join.ssid);
      setNote(null);
      setError(null);
      void waitFor(resume, ongoing.since);
    }
  }, [finish, lost, waitFor]);

  const apply = useCallback((v: StatusVerdict, adopt: boolean) => {
    if (v.kind === "unsupported") rememberUnsupported();
    if (v.kind !== "show") { setHidden(true); return; }
    setHidden(false);
    if (adopt) take(v.status);
    else setStatus(v.status);
  }, [take]);

  // The first look: the owner of the primary instance only, then the
  // device's own answer.
  useEffect(() => {
    if (unsupported) return;
    void (async () => {
      let primary = false;
      try {
        const resp: RespEnvelope = await useWS.request((e) => {
          e.payload = { $case: "reqGetInstanceRole", reqGetInstanceRole: {} };
        });
        primary = resp.payload?.$case === "respInstanceRole" && resp.payload.respInstanceRole.isPrimary;
      } catch {
        // Not known now: what is shown stays as it is (nothing, so far).
        return;
      }
      if (!alive.current) return;
      if (!primary) { setHidden(true); return; }
      let resp: RespEnvelope;
      try {
        resp = await askStatus();
      } catch {
        return;
      }
      if (alive.current) apply(judgeStatus(resp), true);
    })();
  }, [apply]);

  // Coming back to this tab (the router's page in another one, say): the
  // device's network may have changed meanwhile. Not while a change runs:
  // the wait is already asking.
  const shown = status !== null && !hidden;
  useEffect(() => {
    if (!shown) return;
    const onFocus = () => {
      if (phaseRef.current !== "idle") return;
      void askStatus().then((resp) => {
        if (alive.current && phaseRef.current === "idle") apply(judgeStatus(resp), false);
      }, () => {
        // No answer: the line stays as it was.
      });
    };
    window.addEventListener("focus", onFocus);
    return () => window.removeEventListener("focus", onFocus);
  }, [shown, apply]);

  const openPicker = () => {
    if (refuseOnDemo()) return; // a demo device (net/demo.ts)
    setPicking(true);
    setNote(null);
    setError(null);
    setFieldError(null);
    setChosen(null);
    setTypedName("");
    setPassword("");
    void scan();
  };

  const closePicker = () => {
    scanGen.current++;
    setScanning(false);
    setPicking(false);
    setChosen(null);
    setTypedName("");
    setPassword("");
    setFieldError(null);
    setError(null);
    setNetworks(null);
  };

  const choose = (c: Chosen) => {
    setChosen(c);
    setPassword("");
    setFieldError(null);
    setError(null);
    setNote(null);
    if (c.hidden) setTypedName("");
  };

  const connect = async (ev?: FormEvent) => {
    ev?.preventDefault();
    if (!chosen || !status || phaseRef.current !== "idle") return;
    if (refuseOnDemo()) return;
    setNote(null);
    setError(null);
    setFieldError(null);
    const ssid = chosen.hidden ? typedName : chosen.ssid;
    const nameProblem = validateSsid(ssid);
    if (nameProblem) { setFieldError(() => say(problemKeys[nameProblem])); return; }
    // An open listed network takes no password; one typed by hand may.
    const kind = kindOf(chosen);
    const passwordProblem = checkPassword(password, kind);
    if (passwordProblem) { setFieldError(() => say(problemKeys[passwordProblem])); return; }
    const plain = sentPassword(password, kind);
    if (!window.confirm(t(confirmKey(status), { ssid }))) return;

    const tried: Chosen = { ...chosen, ssid };
    setPhase("sending");
    phaseRef.current = "sending";
    // Sealed for this connection, like a user's password (ReqCreateUser):
    // it never crosses the bridge or the home network readable.
    let sealed = new Uint8Array(0);
    if (plain) {
      try {
        sealed = await encryptForConnection(useWS.request, plain);
      } catch (e) {
        if (!alive.current) return;
        setPhase("idle");
        setError(() => asIs(messageOf(e)));
        return;
      }
    }
    if (!alive.current) return;
    setPassword("");

    let resp: RespEnvelope;
    try {
      resp = await useWS.request((e) => {
        e.payload = { $case: "reqJoinWifi", reqJoinWifi: { ssid, password: sealed, hidden: hiddenFor(kind) } };
      });
    } catch (e) {
      if (!alive.current) return;
      setPhase("idle");
      phaseRef.current = "idle";
      setError(() => asIs(messageOf(e)));
      // It may have gone out before the connection dropped: the device
      // then reports the change, and the wait picks it up.
      void askStatus().then((r) => { if (alive.current) apply(judgeStatus(r), true); }, () => {});
      return;
    }
    if (!alive.current) return;
    const v = judgeJoin(resp);
    switch (v.kind) {
      case "joining":
        ongoing = { id: v.id, ssid, since: Date.now(), chosen: tried };
        setJoiningSsid(ssid);
        setPhase("waiting");
        void waitFor(v.id, ongoing.since);
        return;
      case "done":
        finish(v.join, tried);
        return;
      case "unknown":
        setStatus(v.status);
        lost();
        return;
      case "unsupported":
        rememberUnsupported();
        setPhase("idle");
        setHidden(true);
        return;
      case "not_managed":
        setPhase("idle");
        setError(() => (v.message ? asIs(v.message) : say("web.settings.wifi.failed")));
        void askStatus().then((r) => { if (alive.current) apply(judgeStatus(r), false); }, () => {});
        return;
      case "refused":
        setPhase("idle");
        phaseRef.current = "idle";
        setError(() => (v.message ? asIs(v.message) : say("web.settings.wifi.failed")));
        // A change already under way (busy: this one, sent again) is
        // waited for.
        void askStatus().then((r) => { if (alive.current) apply(judgeStatus(r), true); }, () => {});
        return;
    }
  };

  if (hidden || !status) return null;

  const busy = phase !== "idle";
  const withField = !!chosen && (chosen.secured || chosen.hidden);

  return (
    <section className="sf-section wf-section">
      <h3 id="wf-title">{t("web.settings.wifi.title")}</h3>
      <p className="sf-hint">{t("web.settings.wifi.intro")}</p>

      <ul className="wf-now">
        <li>
          <WifiIcon />
          <span>
            {status.ssid
              ? t("web.settings.wifi.on_wifi", { ssid: status.ssid, signal: percentText(t, status.signal) })
              : t("web.settings.wifi.no_wifi")}
          </span>
        </li>
        {status.wired && (
          <li>
            <CableIcon />
            <span>{t("web.settings.wifi.on_cable")}</span>
          </li>
        )}
      </ul>

      {!picking && (
        <div className="up-actions">
          <button className="sf-btn" onClick={openPicker} disabled={busy}>{t("web.settings.wifi.change")}</button>
        </div>
      )}

      {picking && (
        <div className="wf-picker">
          {scanning && <div className="up-line"><Spinner label={t("web.settings.wifi.scanning")} /></div>}
          {!scanning && networks?.length === 0 && <p className="sf-hint wf-empty">{t("web.settings.wifi.no_networks")}</p>}

          <div className="wf-list" role="radiogroup" aria-labelledby="wf-title">
            {(networks ?? []).map((n) => (
              <NetworkRow
                key={n.ssid}
                n={n}
                t={t}
                disabled={busy}
                checked={!!chosen && !chosen.hidden && chosen.ssid === n.ssid}
                onChoose={choose}
              />
            ))}
            <label className={`wf-net wf-other${chosen?.hidden ? " wf-on" : ""}`}>
              <input
                type="radio"
                name="wf-net"
                checked={!!chosen?.hidden}
                disabled={busy}
                onChange={() => choose({ ssid: "", secured: false, hidden: true })}
              />
              <span className="wf-ssid">{t("web.settings.wifi.other")}</span>
            </label>
          </div>

          {chosen && (
            // Not the device's sign-in: nothing here is for the browser's
            // password manager to fill in or keep (autocomplete off, and a
            // field name other than "password").
            <form id="wf-form" className="wf-form" autoComplete="off" noValidate onSubmit={(e) => void connect(e)}>
              {chosen.hidden && (
                <div className="sf-row">
                  <label htmlFor="wf-name">{t("web.settings.wifi.name")}</label>
                  <input
                    id="wf-name"
                    name="wf-network-name"
                    className="sf-input"
                    type="text"
                    value={typedName}
                    disabled={busy}
                    autoComplete="off"
                    autoCapitalize="off"
                    autoCorrect="off"
                    spellCheck={false}
                    onChange={(e) => setTypedName(e.target.value)}
                  />
                </div>
              )}
              {withField && (
                <div className="sf-row">
                  <label htmlFor="wf-key">{t("common.password")}</label>
                  <input
                    id="wf-key"
                    name="wf-network-key"
                    className="sf-input"
                    type="password"
                    value={password}
                    disabled={busy}
                    autoComplete="off"
                    autoCapitalize="off"
                    autoCorrect="off"
                    spellCheck={false}
                    data-1p-ignore=""
                    data-lpignore="true"
                    placeholder={chosen.hidden ? t("web.settings.wifi.password_optional") : undefined}
                    onChange={(e) => setPassword(e.target.value)}
                  />
                </div>
              )}
              {fieldError && <p className="up-error" role="alert">{fieldError(t)}</p>}
            </form>
          )}

          <div className="up-actions">
            {chosen && (
              <button type="submit" form="wf-form" className="sf-btn" disabled={busy}>
                {phase === "sending" ? t("common.connecting") : t("common.connect")}
              </button>
            )}
            <button type="button" className="sf-btn sf-btn-secondary" onClick={() => void scan()} disabled={busy || scanning}>
              {t("web.settings.wifi.scan")}
            </button>
            <button type="button" className="sf-btn sf-btn-secondary" onClick={closePicker} disabled={busy}>
              {t("common.cancel")}
            </button>
          </div>
        </div>
      )}

      {phase === "waiting" && (
        <div className="up-line wf-wait">
          <Spinner label={t("web.settings.wifi.joining", { ssid: joiningSsid })} />
        </div>
      )}
      {phase === "slow" && <p className="up-warn">{t("web.settings.wifi.slow")}</p>}
      {note && <p className={`up-note${note.ok ? " wf-done" : ""}`}>{note.text(t)}</p>}
      {error && <p className="up-error" role="alert">{error(t)}</p>}
    </section>
  );
}
