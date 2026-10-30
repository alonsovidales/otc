// SPDX-License-Identifier: AGPL-3.0-or-later
//
// A share link's page (issue #104): /#download=<uuid>_<secret> (?download=
// in the links from before), the whole page while the address names one.
// Images' and Files' Download ZIP open it in a tab of their own
// (zipTab.ts). It fetches the archive in parts of at most 4 MB (issue #166:
// the device never holds the whole archive for one reply), asks first above
// bigFile.ts' limit (F11 of the 2026-10 review: it is put together in this
// page's memory), and hands it to the browser to save - then says it is
// done, keeps a Download button that saves it again, and closes itself only
// when nothing happened meanwhile (zipDownload.ts: a browser asking where
// to save, or whether this site may download, keeps the tab and its
// question).
import { useEffect, useRef, useState } from "react";
import { useT } from "../i18n";
import { useWS } from "../net/useWS";
import type { ReqEnvelope, RespEnvelope } from "../proto/messages";
import { asIs, percentText, say, type UiText } from "./uiText";
import Spinner from "./Spinner";
import BigFileDialog from "./BigFileDialog";
import { isTooBigForMemory } from "./bigFile";
import { cCloseAfterMs, mayClose, receivedPercent, watchHandOff } from "./zipDownload";
import "./ZipLinkPage.css";

const cZipName = "shared.zip";
const cPartBytes = 4 << 20;

type State =
  | { kind: "preparing" }
  | { kind: "downloading"; percent: number | null }
  // Handed to the browser: its blob: URL, for the Download button.
  | { kind: "saved"; url: string }
  | { kind: "cancelled" }
  | { kind: "failed"; text: UiText };

// The browser's own download of a blob: URL, as a click on a link would.
function save(url: string) {
  const a = document.createElement("a");
  a.href = url;
  a.download = cZipName;
  document.body.appendChild(a);
  a.click();
  a.remove();
}

export default function ZipLinkPage({ link }: { link: string }) {
  const t = useT();
  const [state, setState] = useState<State>({ kind: "preparing" });
  const [bigAsk, setBigAsk] = useState<{ bytes: number; answer: (go: boolean) => void } | null>(null);
  // Once per link, also where React runs an effect twice (StrictMode): a
  // second run would fetch and save the archive again (issue #104 saw
  // three copies of one ZIP from re-renders).
  const startedRef = useRef<string | null>(null);
  const stopWatchRef = useRef<(() => void) | null>(null);

  useEffect(() => {
    if (startedRef.current === link) return;
    startedRef.current = link;
    void (async () => {
      const [uuid, secret] = link.split("_").filter(Boolean);
      if (!uuid || !secret) {
        setState({ kind: "failed", text: say("web.shell.link.malformed") });
        return;
      }
      try {
        const parts: Uint8Array[] = [];
        let offset = 0;
        let total = -1;
        while (total < 0 || offset < total) {
          const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
            e.payload = {
              $case: "reqDownloadSharedLink",
              reqDownloadSharedLink: { uuid, secret, offset: BigInt(offset), length: cPartBytes },
            };
          });
          if (resp.payload?.$case !== "respFileChunk") {
            // The device's own words, in the language the request carried.
            setState({ kind: "failed", text: resp.errorMessage ? asIs(resp.errorMessage) : say("web.shell.link.invalid") });
            return;
          }
          const chunk = resp.payload.respFileChunk;
          total = Number(chunk.size);
          if (chunk.data.length === 0 && offset < total) {
            setState({ kind: "failed", text: say("web.shell.link.stopped") });
            return;
          }
          parts.push(chunk.data);
          offset += chunk.data.length;
          setState({ kind: "downloading", percent: receivedPercent(offset, total) });
          if (parts.length === 1 && offset < total && isTooBigForMemory(total)) {
            const go = await new Promise<boolean>((answer) => setBigAsk({ bytes: total, answer }));
            setBigAsk(null);
            if (!go) {
              setState({ kind: "cancelled" });
              return;
            }
          }
        }
        if (total === 0) {
          setState({ kind: "failed", text: say("web.shell.link.empty") });
          return;
        }
        const url = URL.createObjectURL(new Blob(parts as BlobPart[], { type: "application/zip" }));
        // Watched from before the click: a dialog it opens takes the focus.
        stopWatchRef.current = watchHandOff(window, document, cCloseAfterMs, (quiet) => {
          // Images and Files open this tab for the ZIP: it has nothing more
          // to do. A tab someone opened by hand stays (the browser refuses),
          // saying it is done.
          if (mayClose(quiet, document.visibilityState === "hidden", document.hasFocus())) window.close();
        });
        save(url);
        setState({ kind: "saved", url });
      } catch (err: unknown) {
        // Reported on the page rather than with alert(), which blocks the
        // whole tab until dismissed - and this page has nothing else.
        const message = err instanceof Error ? err.message : "";
        setState({ kind: "failed", text: message ? asIs(message) : say("web.shell.link.failed") });
      }
    })();
  }, [link]);

  // The file stays in memory while the page shows it (its Download button),
  // and goes with the page, or with another link pasted in its place.
  const savedURL = state.kind === "saved" ? state.url : null;
  useEffect(() => {
    if (!savedURL) return;
    return () => URL.revokeObjectURL(savedURL);
  }, [savedURL]);
  useEffect(() => () => stopWatchRef.current?.(), []);

  let body;
  switch (state.kind) {
    case "failed":
      body = <p className="zl-note zl-error" role="alert">{state.text(t)}</p>;
      break;
    case "cancelled":
      body = <p className="zl-note" role="status">{t("web.shell.link.cancelled")}</p>;
      break;
    case "saved":
      body = (
        <div className="zl-done">
          <span className="zl-mark" aria-hidden="true"><DoneGlyph /></span>
          <p className="zl-note" role="status">{t("web.shell.link.done")}</p>
          {/* The same file again: for a browser that didn't save it (it
              refused, or asked and was told no), a click it always lets
              save. */}
          <a className="zl-again" href={state.url} download={cZipName}>
            <DownloadGlyph />
            <span>{t("common.download")}</span>
          </a>
        </div>
      );
      break;
    default: {
      const percent = state.kind === "downloading" ? state.percent : null;
      // The wait is the longest of the lot - the device builds the archive
      // and sends it over the relay before a byte arrives - so a spinner,
      // not a line of text that could as well mean "stuck".
      body = (
        <Spinner label={percent === null
          ? t("web.shell.link.preparing")
          : t("web.shell.link.progress", { percent: percentText(t, percent) })} />
      );
    }
  }

  return (
    <div className="download-view">
      {body}
      {bigAsk && (
        <BigFileDialog bytes={bigAsk.bytes} kind="save" onGo={() => bigAsk.answer(true)} onCancel={() => bigAsk.answer(false)} />
      )}
    </div>
  );
}

const DownloadGlyph = () => (
  <svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true" focusable="false">
    <path d="M12 4v11M7.5 10.5 12 15l4.5-4.5M5 19.5h14" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
  </svg>
);
const DoneGlyph = () => (
  <svg viewBox="0 0 24 24" width="28" height="28" aria-hidden="true" focusable="false">
    <path d="M5 12.5 9.5 17 19 7.5" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
  </svg>
);
