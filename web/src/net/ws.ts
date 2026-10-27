// SPDX-License-Identifier: AGPL-3.0-or-later

import { ReqEnvelope, RespEnvelope } from "../proto/messages";
import { noteResponse } from "./deviceStatus";
import { demoRequestReply, noteDemoReply } from "./demo";
import { noteLanguageConnection, noteLanguageReply, requestLanguage } from "../i18n/choice";
import { t } from "../i18n/runtime";

type RespListener = (env: RespEnvelope) => void;

/** A request no answer can come to: the socket isn't open, it closed
 *  before the answer, or nothing answered in time. Its message is in the
 *  language on screen. */
export class NoAnswer extends Error {}

// How long a request waits for its answer. A socket whose other end went
// away without closing it (a laptop that slept, a phone that changed
// networks) never fires onclose, and every request on it used to wait for
// ever: a spinner that never ended, a Cancel stuck on "Stopping…".
const cAnswerMs = 60_000;
// A piece of a file either way, or work on a whole file or folder before
// the answer: 4 MB over a slow home upload takes a minute or more. The
// clock waits while this browser's own bytes are still leaving (an
// upload's piece), so a slow upload is never cut.
const cTransferMs = 5 * 60_000;
// The opening of a socket that never opens (an address that drops what is
// sent to it).
const cConnectMs = 15_000;
// No frame at all for this long, while only small requests the device
// answers at once wait (the status polls, a sign-in), and one of them has
// waited this long: the socket is dead, and is dropped so the next
// request opens a new one. Never while a bigger answer may be on its way:
// a socket carries one message at a time, and 4 MB on a slow line keeps
// every other answer behind it for longer than this.
const cSilenceMs = 20_000;
// How often the waiting requests are looked at.
const cWatchMs = 5_000;

// Answered with whole pieces of files, or only once the device has done
// the work on a whole file, folder or archive: cTransferMs.
const cTransfers = new Set([
  "reqReadFile", "reqGetFile", "reqUploadFile", "reqBeginUpload", "reqUploadChunk", "reqFinishUpload",
  "reqGetPublicationMedia", "reqGetSharedGalleryItem", "reqDownloadSharedLink", "reqGetMediaRange",
  "reqShareFilesLink", "reqCreateSharedGallery", "reqPreviewSharedGallery", "reqGetFolderUsage",
  "reqLinkFile", "reqGetLogs", "reqSendLogs", "reqCheckUpdate", "reqSetupTailscale",
]);
// Work the device finishes before it answers, however long it takes (a
// post's videos cut and shrunk, an update, the disks set up, a network
// joined, a folder of thousands of files deleted one by one, a user's
// database made or dropped): no clock of their own - a dead socket still
// fails them, found by the requests around them.
const cOpenEnded = new Set([
  "reqNewSocialPublication", "reqApplyUpdate", "reqSetupStorage", "reqStartReprocess", "reqRestartDevice",
  "reqEnableBridge", "reqDisableBridge", "reqSetWifi", "reqJoinWifi",
  "reqDelFile", "reqCreateUser", "reqDeleteUser",
]);
// Small answers the device gives at once: only while every waiting
// request is one of these does silence say the socket is dead.
const cQuick = new Set([
  "reqGetStatus", "reqGetNotificationCount", "reqGetPubKey", "reqAuth", "reqAuthWithToken",
  "reqIssueSessionToken", "reqGetReprocessStatus", "reqGetMediaUrl", "reqGetSettings",
  "reqMarkNotificationsAcknowledged", "reqLikePublication", "reqLikeComment", "reqSetLanguage",
  "reqGetBridgeAccess", "reqGetTailscaleStatus", "reqGetWifiStatus", "reqGetSharedGalleryJob",
  "reqGetSharedGalleryStream", "reqGetVapidPublicKey", "reqGetInstanceRole",
]);

/** What a request may wait for its answer: 0 for no limit. */
export function answerTimeoutMs(kind: string): number {
  if (cOpenEnded.has(kind)) return 0;
  return cTransfers.has(kind) ? cTransferMs : cAnswerMs;
}

type Waiter = {
  resolve: (env: RespEnvelope) => void;
  reject: (err: Error) => void;
  kind: string;
  sentAt: number;
  timeoutMs: number;
  // How many frames the socket had brought when this went: one more since
  // is a sign of life, whatever the clock says.
  framesAtSend: number;
};

// A timer that never keeps Node running (the scripts' tests load this
// module); browsers have no unref.
function unref<T>(timer: T): T {
  (timer as { unref?: () => void }).unref?.();
  return timer;
}

export class WSClient {
  private ws?: WebSocket;
  private nextId = 1;
  // Holds both callbacks (not just resolve) so a dropped connection can
  // actually reject whatever's still waiting - see drop below. A request
  // in flight when the socket closes used to just hang forever (the
  // Promise executor only ever captured `resolve`, nothing ever called
  // `reject`), reproduced live as a Cancel button stuck reading
  // "Stopping…" until a manual reload, because the server happened to
  // restart between the request going out and its reply coming back.
  private waiters = new Map<number, Waiter>();
  private listeners: Set<RespListener> = new Set();
  public connected = false;
  // Two components mounting at once (e.g. the app-level fresh-device check
  // alongside a component's own first request) both used to call connect()
  // concurrently. The old readyState-based guard only stopped the second
  // caller from opening a *second* socket — it didn't make that caller
  // actually wait for the first one to finish, so it returned as if
  // already connected while the real socket was still CONNECTING, and its
  // immediately-following request() failed with "WS not connected". Every
  // caller now awaits this same in-flight promise instead.
  private connecting?: Promise<void>;
  private failConnect?: (err: unknown) => void;
  // Signs of life: the last frame received, and the last time this
  // browser's own queued bytes went down (an upload's piece leaving).
  private lastFrameAt = 0;
  private frames = 0;
  private lastDrainAt = 0;
  private lastBuffered = 0;
  private watchTimer?: ReturnType<typeof setInterval>;

  onMessage(fn: RespListener) {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  connect(url: string): Promise<void> {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) return Promise.resolve();
    if (this.connecting) return this.connecting;

    this.connecting = new Promise<void>((resolve, reject) => {
      console.log('WS to endpoint:', url);
      const ws = new WebSocket(url);
      ws.binaryType = "arraybuffer";
      const opening = unref(setTimeout(() => {
        if (this.ws === ws && !this.connected) this.drop(ws, t("web.shell.connection.no_answer"));
      }, cConnectMs));
      this.failConnect = (err) => { clearTimeout(opening); reject(err); };

      ws.onopen = () => {
        clearTimeout(opening);
        if (this.ws !== ws) return;
        this.connected = true;
        this.connecting = undefined;
        this.failConnect = undefined;
        this.lastFrameAt = Date.now();
        noteLanguageConnection();
        resolve();
      };
      ws.onerror = (e) => {
        if (this.ws !== ws) return;
        this.connecting = undefined;
        this.failConnect?.(e);
        this.failConnect = undefined;
      };
      ws.onclose = () => {
        if (this.ws !== ws) return;
        this.drop(ws, t("web.shell.connection.closed"));
      };
      ws.onmessage = (ev) => {
        if (this.ws !== ws) return;
        this.lastFrameAt = Date.now();
        this.frames++;
        try {
          const env = RespEnvelope.decode(new Uint8Array(ev.data as ArrayBuffer));
          // Issue #56: every response passes through here, which makes
          // this the one place that sees the bridge's "I couldn't reach
          // the device" verdict no matter which part of the app asked -
          // and equally, sees the first successful reply that means it's
          // back.
          noteResponse(
            env.payload?.$case === "respAck" ? env.payload.respAck.code : undefined,
            env.payload?.$case === "respAck" ? env.payload.respAck.errorMsg : env.errorMessage,
            env.error,
          );
          // The user's language as the device has it (Status, Settings):
          // whichever part of the app asked, this browser follows it.
          noteLanguageReply(env);
          // A demo device says so in its PubKey and Status (net/demo.ts).
          noteDemoReply(env);
          const cont = this.waiters.get(env.id);
          if (cont) { this.waiters.delete(env.id); cont.resolve(env); }
          this.listeners.forEach(fn => fn(env));
        } catch (err) {
          console.error("WS decode error", err);
        }
      };

      this.ws = ws;
    });
    return this.connecting;
  }

  // The socket is done with - it closed, or it is dead (see watch): every
  // request still waiting on it fails at once, with `text`, and the next
  // request opens a new one. Its handlers go, so a close that comes later
  // (a dead socket's can take minutes) can't touch the socket after it.
  // Snapshot-then-clear so a reject handler that itself starts a new
  // request doesn't touch the map while it's still being iterated.
  private drop(ws: WebSocket, text: string) {
    if (this.ws === ws) {
      this.ws = undefined;
      this.connected = false;
      this.connecting = undefined;
      this.failConnect?.(new NoAnswer(text));
      this.failConnect = undefined;
    }
    ws.onopen = ws.onmessage = ws.onerror = ws.onclose = null;
    try { ws.close(); } catch { /* already closed */ }
    this.stopWatch();
    const pending = this.waiters;
    this.waiters = new Map();
    pending.forEach(({ reject }) => reject(new NoAnswer(text)));
  }

  close() { this.ws?.close(); }

  request(build: (e: Partial<ReqEnvelope>) => void): Promise<RespEnvelope> {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) {
      return Promise.reject(new NoAnswer(t("web.shell.connection.not_connected")));
    }
    const id = this.nextId++;
    const draft: Partial<ReqEnvelope> = { id };
    build(draft);
    // A demo device changes nothing: answered here (net/demo.ts).
    const demoReply = demoRequestReply(draft, t("web.shell.demo.refused"));
    if (demoReply) return Promise.resolve(demoReply);
    // docs/i18n.md: every request says which language this browser shows,
    // so the device answers in it. Set here, after build and over whatever
    // it put in the envelope - the one place, so no request goes without.
    draft.lang = requestLanguage();
    const req = ReqEnvelope.fromPartial(draft);
    const bytes = ReqEnvelope.encode(req).finish();
    this.ws.send(bytes);
    const kind = (draft.payload as { $case?: string } | undefined)?.$case ?? "";
    return new Promise<RespEnvelope>((resolve, reject) => {
      this.waiters.set(id, { resolve, reject, kind, sentAt: Date.now(), timeoutMs: answerTimeoutMs(kind), framesAtSend: this.frames });
      this.startWatch();
    });
  }

  private startWatch() {
    if (this.watchTimer != null) return;
    this.lastBuffered = this.ws?.bufferedAmount ?? 0;
    this.watchTimer = unref(setInterval(() => this.watch(), cWatchMs));
  }

  private stopWatch() {
    if (this.watchTimer != null) clearInterval(this.watchTimer);
    this.watchTimer = undefined;
  }

  // Every cWatchMs while anything waits: requests past their time fail,
  // and a socket that is plainly dead is dropped (see cSilenceMs).
  private watch(now = Date.now()) {
    const ws = this.ws;
    if (!ws || !this.connected || this.waiters.size === 0) { this.stopWatch(); return; }
    // This browser's bytes still leaving: whatever waits is behind them,
    // and its clock waits too.
    const buffered = ws.bufferedAmount ?? 0;
    if (buffered < this.lastBuffered) this.lastDrainAt = now;
    this.lastBuffered = buffered;
    const sending = buffered > 0;
    const since = (w: Waiter) => Math.max(w.sentAt, this.lastDrainAt);

    // While a transfer is within its time, a bigger answer may be on the
    // way and the rest wait behind it: only transfers can run out then.
    const waiting = [...this.waiters.entries()];
    const transferOn = waiting.some(([, w]) => w.timeoutMs === cTransferMs && now - since(w) < w.timeoutMs);
    const late = waiting.filter(([, w]) =>
      w.timeoutMs > 0 && now - since(w) >= w.timeoutMs && (!transferOn || w.timeoutMs === cTransferMs));
    if (late.length) {
      // Nothing at all came since the first of them went: the socket is
      // dead. Otherwise the device is there, only slow to answer these.
      const first = Math.min(...late.map(([, w]) => w.framesAtSend));
      if (this.frames === first && !sending) {
        this.drop(ws, t("web.shell.connection.no_answer"));
        return;
      }
      for (const [id, w] of late) {
        this.waiters.delete(id);
        w.reject(new NoAnswer(t("web.shell.connection.no_answer")));
      }
    }

    const quiet = now - this.lastFrameAt >= cSilenceMs;
    const rest = [...this.waiters.values()];
    if (quiet && !sending && rest.length && rest.every((w) => cQuick.has(w.kind)) && rest.some((w) => now - w.sentAt >= cSilenceMs)) {
      this.drop(ws, t("web.shell.connection.no_answer"));
      return;
    }
    if (this.waiters.size === 0) this.stopWatch();
  }

  /** Tests: one look at the waiting requests, as the timer takes it. */
  watchNowForTest(now: number) { this.watch(now); }
}

export const wsClient = new WSClient();
