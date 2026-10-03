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
import "./LogsPanel.css";

type Source = "app" | "update";
const cMaxChars = 1 << 20; // keep about the last megabyte on screen
const cWarning = "Logs can include file and folder names, search words, Wi-Fi network names and your device's addresses. Check them before you share them.";

export default function LogsPanel() {
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
      <h3>Logs</h3>
      <p className="sf-hint">What the device is doing, live - useful when something goes wrong. You can copy them, or send them to us so we can help.</p>
      <button className="sf-btn" onClick={() => setOpen(true)}>Show logs</button>
      {open && <LogsViewer onClose={() => setOpen(false)} />}
    </section>
  );
}

function LogsViewer({ onClose }: { onClose: () => void }) {
  const [source, setSource] = useState<Source>("app");
  const [text, setText] = useState("");
  const [paused, setPaused] = useState(false);
  const [error, setError] = useState<string | null>(null);
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
            (e as any).payload = { $case: "reqGetLogs", reqGetLogs: { source, offset, maxBytes: first ? 65536 : 262144, waitSeconds: first ? 0 : 25 } };
          });
          if (!alive) return;
          if (resp.payload?.$case !== "respLogs") throw new Error(resp.errorMessage || "Could not read the logs");
          const l = resp.payload.respLogs;
          const rotated = offset > 0 && Number(l.size) < offset;
          offset = Number(l.nextOffset);
          if (l.text || rotated) {
            setText(t => {
              const next = t + (rotated ? "\n— log rotated —\n" : "") + l.text;
              return next.length > cMaxChars ? next.slice(next.length - cMaxChars) : next;
            });
          }
          setError(null);
        } catch (e: any) {
          if (!alive) return;
          setError(e?.message ?? String(e));
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
      <div className="lg-dialog" role="dialog" aria-label="Logs" onClick={e => e.stopPropagation()}>
        <div className="lg-head">
          <h3>Logs</h3>
          <div className="lg-tabs" role="tablist">
            <button role="tab" aria-selected={source === "app"} className={source === "app" ? "on" : ""} onClick={() => setSource("app")}>Device</button>
            <button role="tab" aria-selected={source === "update"} className={source === "update" ? "on" : ""} onClick={() => setSource("update")}>Updates</button>
          </div>
          <button className="lg-close" onClick={onClose} aria-label="Close">×</button>
        </div>
        <p className="lg-warning">{cWarning}</p>
        <pre className="lg-text" ref={boxRef} onScroll={onScroll}>{text || "Waiting for the log…"}</pre>
        {error && <p className="lg-error">{error} - trying again…</p>}
        <div className="lg-actions">
          <span className={`lg-live${paused ? "" : " on"}`}>{paused ? "Paused" : "Live"}</span>
          <button className="sf-btn" onClick={() => setPaused(p => !p)}>{paused ? "Resume" : "Pause"}</button>
          <span className="lg-grow" />
          <button className="sf-btn" onClick={() => void copy()} disabled={!text}>{copied ? "Copied" : "Copy"}</button>
          <button className="sf-btn" onClick={download} disabled={!text}>Download</button>
          <button className="sf-btn" onClick={() => setSending(true)}>Send to us…</button>
        </div>
        {sending && <SendLogs onDone={() => setSending(false)} />}
      </div>
    </div>
  );
}

function SendLogs({ onDone }: { onDone: () => void }) {
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<{ ok: boolean; text: string } | null>(null);
  const send = useCallback(async () => {
    setBusy(true);
    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqSendLogs", reqSendLogs: { note } };
      });
      if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) setResult({ ok: true, text: "Sent - we'll answer at your account's email." });
      else setResult({ ok: false, text: resp.errorMessage || "Could not send them." });
    } catch (e: any) {
      setResult({ ok: false, text: e?.message ?? String(e) });
    } finally {
      setBusy(false);
    }
  }, [note]);
  return (
    <div className="lg-send">
      <p>Sends the last part of your device's logs, your note and your account's email to info@off-the.cloud, so we can help. {cWarning}</p>
      {!result?.ok && <>
        <textarea className="sf-input" rows={3} placeholder="What went wrong? (optional)" value={note} onChange={e => setNote(e.target.value)} maxLength={2000} />
        <div className="lg-actions">
          <span className="lg-grow" />
          <button className="sf-btn" onClick={onDone} disabled={busy}>Cancel</button>
          <button className="sf-btn" onClick={() => void send()} disabled={busy}>{busy ? "Sending…" : "Send"}</button>
        </div>
      </>}
      {result && <p className={result.ok ? "lg-ok" : "lg-error"}>{result.text}</p>}
      {result?.ok && <div className="lg-actions"><span className="lg-grow" /><button className="sf-btn" onClick={onDone}>Done</button></div>}
    </div>
  );
}

const sleep = (ms: number) => new Promise(r => setTimeout(r, ms));
