// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Settings > Logs: the device's own log and the update log, live. The
// device holds each GetLogs open until new lines are written (wait_seconds),
// and the next one goes out as soon as an answer arrives - a stream made of
// plain requests, which the bridge relays as it is. Copy, download, or send
// them to the project (SendLogs, through the bridge). Main instance only.
// Mirrors LogsView.swift and LogsView.kt.
import { useCallback, useEffect, useRef, useState } from "react";
import { useWS } from "../net/useWS";
import type { ReqEnvelope, RespEnvelope } from "../proto/messages";
import { useT } from "../i18n";
import { asIs, say, type UiText } from "./uiText";
import "./LogsPanel.css";

type Source = "app" | "update";
const cMaxChars = 1 << 20; // keep about the last megabyte on screen
// Part of the log text itself, copied and downloaded with it: English, like the log lines.
const cRotated = "\n— log rotated —\n"; // i18n-ignore: inside the log text, which stays English

export default function LogsPanel() {
  const t = useT();
  const [isPrimary, setIsPrimary] = useState(false);
  const [open, setOpen] = useState(false);
  useEffect(() => {
    (async () => {
      try {
        const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
          (e as any).payload = { $case: "reqGetInstanceRole", reqGetInstanceRole: {} };
        });
        setIsPrimary(resp.payload?.$case === "respInstanceRole" && resp.payload.respInstanceRole.isPrimary);
      } catch { setIsPrimary(false); }
    })();
  }, []);
  if (!isPrimary) return null;
  return (
    <section className="sf-section">
      <h3>{t("web.settings.logs.title")}</h3>
      <p className="sf-hint">{t("web.settings.logs.hint")}</p>
      <button className="sf-btn" onClick={() => setOpen(true)}>{t("web.settings.logs.show")}</button>
      {open && <LogsViewer onClose={() => setOpen(false)} />}
    </section>
  );
}

function LogsViewer({ onClose }: { onClose: () => void }) {
  const t = useT();
  const [source, setSource] = useState<Source>("app");
  const [text, setText] = useState("");
  const [paused, setPaused] = useState(false);
  const [error, setError] = useState<UiText | null>(null);
  const [copied, setCopied] = useState(false);
  const [sending, setSending] = useState(false);
  const boxRef = useRef<HTMLPreElement>(null);
  const atBottom = useRef(true);
  const pausedRef = useRef(paused);
  pausedRef.current = paused;

  // One loop per source; a new source (or closing) ends the old one, and
  // whatever it still receives is dropped.
  useEffect(() => {
    let alive = true;
    let offset = -1;
    setText("");
    setError(null);
    (async () => {
      while (alive) {
        if (pausedRef.current) { await sleep(500); continue; }
        try {
          const first = offset < 0;
          const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
            (e as any).payload = { $case: "reqGetLogs", reqGetLogs: { source, offset: BigInt(offset), maxBytes: first ? 65536 : 262144, waitSeconds: first ? 0 : 25 } };
          });
          if (!alive) return;
          if (resp.payload?.$case !== "respLogs") {
            // The device's words, or ours when it has none; tried again like a failed request.
            setError(() => (resp.errorMessage ? asIs(resp.errorMessage) : say("web.settings.logs.read_failed")));
            await sleep(5000);
            continue;
          }
          const l = resp.payload.respLogs;
          const rotated = offset > 0 && Number(l.size) < offset;
          offset = Number(l.nextOffset);
          if (l.text || rotated) {
            setText(prev => {
              const next = prev + (rotated ? cRotated : "") + l.text;
              return next.length > cMaxChars ? next.slice(next.length - cMaxChars) : next;
            });
          }
          setError(null);
        } catch (e: any) {
          if (!alive) return;
          setError(() => asIs(e?.message ?? String(e)));
          await sleep(5000);
        }
      }
    })();
    return () => { alive = false; };
  }, [source]);

  // Follow the end while the reader is there.
  useEffect(() => {
    const el = boxRef.current;
    if (el && atBottom.current) el.scrollTop = el.scrollHeight;
  }, [text]);
  const onScroll = () => {
    const el = boxRef.current;
    if (el) atBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
  };

  const copy = async () => {
    try { await navigator.clipboard.writeText(text); setCopied(true); setTimeout(() => setCopied(false), 1500); } catch { /* select and copy by hand */ }
  };
  const download = () => {
    const url = URL.createObjectURL(new Blob([text], { type: "text/plain" }));
    const a = document.createElement("a");
    a.href = url; a.download = source === "app" ? "otc.log" : "otc-update.log";
    document.body.appendChild(a); a.click(); a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  };

  return (
    <div className="lg-backdrop" onClick={onClose}>
      <div className="lg-dialog" role="dialog" aria-label={t("web.settings.logs.title")} onClick={e => e.stopPropagation()}>
        <div className="lg-head">
          <h3>{t("web.settings.logs.title")}</h3>
          <div className="lg-tabs" role="tablist">
            <button role="tab" aria-selected={source === "app"} className={source === "app" ? "on" : ""} onClick={() => setSource("app")}>{t("web.settings.logs.tab_device")}</button>
            <button role="tab" aria-selected={source === "update"} className={source === "update" ? "on" : ""} onClick={() => setSource("update")}>{t("web.settings.logs.tab_updates")}</button>
          </div>
          <button className="lg-close" onClick={onClose} aria-label={t("common.close")}>×</button>
        </div>
        <p className="lg-warning">{t("web.settings.logs.warning")}</p>
        <pre className="lg-text" ref={boxRef} onScroll={onScroll}>{text || t("web.settings.logs.waiting")}</pre>
        {error && <p className="lg-error">{t("web.settings.logs.retrying", { error: error(t) })}</p>}
        <div className="lg-actions">
          <span className={`lg-live${paused ? "" : " on"}`}>{paused ? t("web.settings.logs.paused") : t("web.settings.logs.live")}</span>
          <button className="sf-btn" onClick={() => setPaused(p => !p)}>{paused ? t("common.resume") : t("common.pause")}</button>
          <span className="lg-grow" />
          <button className="sf-btn" onClick={() => void copy()} disabled={!text}>{copied ? t("web.settings.logs.copied") : t("web.settings.logs.copy")}</button>
          <button className="sf-btn" onClick={download} disabled={!text}>{t("common.download")}</button>
          <button className="sf-btn" onClick={() => setSending(true)}>{t("web.settings.logs.send_to_us")}</button>
        </div>
        {sending && <SendLogs onDone={() => setSending(false)} />}
      </div>
    </div>
  );
}

function SendLogs({ onDone }: { onDone: () => void }) {
  const t = useT();
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<{ ok: boolean; text: UiText } | null>(null);
  const send = useCallback(async () => {
    setBusy(true);
    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqSendLogs", reqSendLogs: { note } };
      });
      if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) setResult({ ok: true, text: say("web.settings.logs.sent") });
      else setResult({ ok: false, text: resp.errorMessage ? asIs(resp.errorMessage) : say("web.settings.logs.send_failed") });
    } catch (e: any) {
      setResult({ ok: false, text: asIs(e?.message ?? String(e)) });
    } finally {
      setBusy(false);
    }
  }, [note]);
  return (
    <div className="lg-send">
      <p>{t("web.settings.logs.send_hint")} {t("web.settings.logs.warning")}</p>
      {!result?.ok && <>
        <textarea className="sf-input" rows={3} placeholder={t("web.settings.logs.note")} value={note} onChange={e => setNote(e.target.value)} maxLength={2000} />
        <div className="lg-actions">
          <span className="lg-grow" />
          <button className="sf-btn" onClick={onDone} disabled={busy}>{t("common.cancel")}</button>
          <button className="sf-btn" onClick={() => void send()} disabled={busy}>{busy ? t("web.settings.logs.sending") : t("common.send")}</button>
        </div>
      </>}
      {result && <p className={result.ok ? "lg-ok" : "lg-error"}>{result.text(t)}</p>}
      {result?.ok && <div className="lg-actions"><span className="lg-grow" /><button className="sf-btn" onClick={onDone}>{t("common.done")}</button></div>}
    </div>
  );
}

const sleep = (ms: number) => new Promise(r => setTimeout(r, ms));
