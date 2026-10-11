// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The user's language choice (docs/i18n.md, "The stored choice"). It lives
// on the device, per user, and every app follows it; this browser keeps a
// local copy (localStorage otc_language: "" for Automatic, or a code), which
// Sign Out keeps, and a pending flag while a change of its own hasn't
// reached the device yet. The language this browser shows (effective) is,
// in order:
//
//   1. the device's value when it has one ("" -> 3),
//   2. else (an older device, or not asked yet) the local copy ("" -> 3),
//   3. the browser's languages, matched by base language (match.ts),
//   4. English.
//
// The local copy follows the device's value whenever one is seen, so 1 and
// 2 read the same field. A code this build doesn't ship shows as English
// and is still kept as the choice.
//
// For cPendingWindowMs after a change is sent, a different value the device
// reports is taken for a reply that left it before the change landed and
// is ignored - also after the device's ok, since the device answers each
// request on its own and a Status read just before the change can arrive
// after it. A device too old to keep it (unknown_payload) leaves the change
// pending: nothing more is sent until the next connection, when the device
// may have been updated.
//
// React-free, and with no import from net/: ws.ts imports this module (the
// lang line, the device's replies), so it must never import anything that
// imports ws.ts. deviceSync.ts plugs the SetLanguage request in.

import type { RespEnvelope } from "../proto/messages";
import { languages, sourceLanguage, type Language, type LanguageCode } from "./messages";
import { isLanguageCode, matchLanguage } from "./match";

/** localStorage: the local copy of the choice. */
export const cLanguageKey = "otc_language";
/** localStorage: {"at": ms, "expected"?: code} while a change is pending. */
export const cPendingKey = "otc_language_pending";
/**
 * How long a pending change hides a different value the device reports: a
 * Status poll answered just before the change landed still carries the old
 * one.
 */
export const cPendingWindowMs = 30_000;

/** How the device answered a SetLanguage. */
export type SendResult = "ok" | "changed" | "unknown_payload" | "failed";
/** What became of a change made here, for the Language panel. */
export type ChooseOutcome = "ok" | "changed" | "too_old" | "failed";
/** Sends SetLanguage{language, expected} (expected left out when undefined). */
export type Sender = (language: string, expected: string | undefined) => Promise<SendResult>;

/** Storage that never throws: a failure reads as nothing stored. */
export interface ChoiceStorage {
  get(key: string): string | null;
  set(key: string, value: string): void;
  remove(key: string): void;
}

export interface ChoiceEnv {
  storage: ChoiceStorage;
  now(): number;
  /** The system's languages in order of preference (navigator.languages). */
  systemLanguages(): readonly string[];
  /** This build's languages (messages.ts). */
  languages: readonly Language[];
}

export interface ChoiceSnapshot {
  /** "" (Automatic) or a code, possibly one this build doesn't ship. */
  readonly choice: string;
  /** The language shown and sent as ReqEnvelope.lang. */
  readonly effective: LanguageCode;
  /** What Automatic resolves to in this browser. */
  readonly automatic: LanguageCode;
  /** The system's languages it was resolved from (formatters use them too). */
  readonly system: readonly string[];
  /** A change made here hasn't reached the device yet. */
  readonly pending: boolean;
  /** A SetLanguage is on its way. */
  readonly sending: boolean;
  /** The device answered unknown_payload: until the next connection the change stays here, pending. */
  readonly tooOld: boolean;
}

interface Pending {
  readonly at: number;
  /** The device's value this change was made over; undefined when none was seen. */
  readonly expected?: string;
}

const isChoice = (s: unknown): s is string => s === "" || isLanguageCode(s);

/** How a SetLanguage reply reads (codes before prose: docs/i18n.md). */
export function setLanguageResult(resp: RespEnvelope): SendResult {
  const ack = resp.payload?.$case === "respAck" ? resp.payload.respAck : undefined;
  if (ack?.ok && !resp.error) return "ok";
  if (ack?.code === "changed") return "changed";
  const code = resp.errorCode || ack?.code || "";
  if (code === "unknown_payload") return "unknown_payload";
  // A device from before error codes said only this.
  if (code === "" && /unknown payload/i.test(resp.errorMessage || ack?.errorMsg || "")) return "unknown_payload";
  return "failed";
}

export class LanguageChoice {
  private readonly env: ChoiceEnv;
  private local: string;
  private pending: Pending | null;
  // The device's value last seen in a Status or Settings reply ("" or a
  // code); undefined until one is (or while the device has none).
  private device: string | undefined;
  private tooOld = false;
  // When the last SetLanguage went out: a different value the device
  // reports within cPendingWindowMs of it is ignored even once nothing is
  // pending (it may predate the change). Cleared by "changed", whose
  // re-read value must be taken.
  private sentAt = Number.NEGATIVE_INFINITY;
  // Connections, counted by noteConnection(), and the one the last
  // SetLanguage went out on: a change that failed is retried once per
  // connection, at its first Status or Settings reply.
  private conn = 0;
  private attemptConn = -1;
  private flight: Promise<ChooseOutcome> | null = null;
  private sender: Sender | null = null;
  private refetch: (() => void) | null = null;
  private readonly listeners = new Set<() => void>();
  private snap: ChoiceSnapshot;

  constructor(env: ChoiceEnv) {
    this.env = env;
    const stored = env.storage.get(cLanguageKey);
    this.local = isChoice(stored) ? stored : "";
    this.pending = this.loadPending();
    this.snap = this.makeSnapshot();
  }

  /** Plugs in the SetLanguage request and a re-read of the device's Settings. */
  connect(sender: Sender, refetch: () => void) {
    this.sender = sender;
    this.refetch = refetch;
  }

  subscribe = (fn: () => void): (() => void) => {
    this.listeners.add(fn);
    return () => { this.listeners.delete(fn); };
  };

  /** The current state; the same object until something changes. */
  snapshot = (): ChoiceSnapshot => this.snap;

  /** The system's languages may have changed (the languagechange event). */
  refresh() {
    this.emit();
  }

  /**
   * A change made in this browser: shown at once, then sent to the device.
   * Resolves with what became of it.
   */
  choose(choice: string): Promise<ChooseOutcome> {
    if (!isChoice(choice)) return Promise.reject(new TypeError("not a language code"));
    this.local = choice;
    this.env.storage.set(cLanguageKey, choice);
    // A change on top of one still pending keeps the value that one was
    // made over: the device hasn't moved from it as far as this app knows.
    this.setPending({ at: this.env.now(), expected: this.pending ? this.pending.expected : this.device });
    this.emit();
    // A device that can't store it yet: pending until the next connection.
    if (this.tooOld) return Promise.resolve("too_old");
    return this.flush();
  }

  /**
   * The device's value from a Status or Settings reply: undefined when the
   * field is absent (a device from before localization).
   */
  noteDevice(value: string | undefined) {
    if (value !== undefined && !isChoice(value)) return;
    if (value !== undefined) this.device = value;
    if (this.pending) {
      if (value !== undefined && value === this.local) {
        // Our own change is there: it landed, whatever became of its answer.
        this.setPending(null);
        this.emit();
        return;
      }
      if (!this.flight && this.attemptConn !== this.conn) {
        // Not tried on this connection yet (it failed on an earlier one, or
        // the page was reloaded meanwhile): this reply means the session
        // is up, so it goes now, and this value waits for its answer.
        void this.flush();
        return;
      }
      if (this.flight || this.env.now() - this.pending.at < cPendingWindowMs) return;
    }
    if (value === undefined) return;
    if (value !== this.local && this.env.now() - this.sentAt < cPendingWindowMs) return;
    this.adopt(value);
  }

  /** A new connection to the device (ws.ts): a device found too old may have been updated. */
  noteConnection() {
    this.conn++;
    if (this.tooOld) {
      this.tooOld = false;
      this.emit();
    }
  }

  /** Sign Out: a change the device never got must not reach whoever signs in next. */
  dropPending() {
    if (!this.pending) return;
    this.setPending(null);
    this.emit();
  }

  private adopt(value: string) {
    const changed = value !== this.local || this.pending !== null;
    this.local = value;
    this.env.storage.set(cLanguageKey, value);
    this.setPending(null);
    if (changed) this.emit();
  }

  // Sends the pending change, and again for one made while it was on its
  // way, until nothing is pending or the device's answer settles it.
  private flush(): Promise<ChooseOutcome> {
    if (this.flight) return this.flight;
    if (this.tooOld) return Promise.resolve("too_old");
    const sender = this.sender;
    if (!this.pending || !sender) return Promise.resolve(this.pending ? "failed" : "ok");
    this.flight = (async (): Promise<ChooseOutcome> => {
      try {
        for (;;) {
          const p = this.pending;
          if (!p) return "ok";
          const language = this.local;
          this.attemptConn = this.conn;
          // The window runs from each attempt: a retry made long after the
          // change is still answered after polls that predate it.
          this.sentAt = this.env.now();
          this.setPending({ at: this.sentAt, expected: p.expected });
          this.emit();
          let result: SendResult;
          try {
            result = await sender(language, p.expected);
          } catch {
            result = "failed";
          }
          if (result === "ok") {
            this.device = language;
            if (this.pending && this.local !== language) {
              // Picked again while this one was on its way: that one goes
              // next, over the value the device now holds.
              this.setPending({ at: this.env.now(), expected: language });
              continue;
            }
            this.setPending(null);
            return "ok";
          }
          if (result === "changed") {
            // Another app changed it first: its value wins, read again.
            this.setPending(null);
            this.sentAt = Number.NEGATIVE_INFINITY;
            this.refetch?.();
            return "changed";
          }
          if (result === "unknown_payload") {
            // Still pending: sent again at the next connection.
            this.tooOld = true;
            return "too_old";
          }
          // Stays pending: retried at the next connection.
          return "failed";
        }
      } finally {
        this.flight = null;
        this.emit();
      }
    })();
    return this.flight;
  }

  private resolve(choice: string): LanguageCode {
    if (choice !== "") return this.env.languages.find((l) => l.code === choice)?.code ?? sourceLanguage;
    return this.automatic();
  }

  private automatic(): LanguageCode {
    return matchLanguage(this.env.systemLanguages(), this.env.languages) ?? sourceLanguage;
  }

  private makeSnapshot(): ChoiceSnapshot {
    return {
      choice: this.local,
      effective: this.resolve(this.local),
      automatic: this.automatic(),
      system: [...this.env.systemLanguages()],
      pending: this.pending !== null,
      sending: this.flight !== null,
      tooOld: this.tooOld,
    };
  }

  private emit() {
    const next = this.makeSnapshot();
    const s = this.snap;
    if (s.choice === next.choice && s.effective === next.effective && s.automatic === next.automatic
      && s.system.join() === next.system.join()
      && s.pending === next.pending && s.sending === next.sending && s.tooOld === next.tooOld) return;
    this.snap = next;
    this.listeners.forEach((fn) => {
      try {
        fn();
      } catch (err) {
        console.error("i18n: a language listener failed:", err);
      }
    });
  }

  private setPending(p: Pending | null) {
    this.pending = p;
    if (p) this.env.storage.set(cPendingKey, JSON.stringify(p));
    else this.env.storage.remove(cPendingKey);
  }

  private loadPending(): Pending | null {
    const raw = this.env.storage.get(cPendingKey);
    if (!raw) return null;
    try {
      const v: unknown = JSON.parse(raw);
      if (typeof v !== "object" || v === null) return null;
      const { at, expected } = v as { at?: unknown; expected?: unknown };
      if (typeof at !== "number" || !Number.isFinite(at)) return null;
      if (expected !== undefined && !isChoice(expected)) return null;
      return { at: Math.min(at, this.env.now()), expected };
    } catch {
      return null;
    }
  }
}

const browserStorage: ChoiceStorage = {
  get(key) {
    try {
      return window.localStorage.getItem(key);
    } catch {
      return null;
    }
  },
  set(key, value) {
    try {
      window.localStorage.setItem(key, value);
    } catch {
      // Private mode or full: the choice lasts until the page is reloaded.
    }
  },
  remove(key) {
    try {
      window.localStorage.removeItem(key);
    } catch {
      // Nothing could be stored either.
    }
  },
};

/** The browser's languages in order of preference. */
export function browserLanguages(): readonly string[] {
  try {
    if (navigator.languages?.length) return navigator.languages;
    return navigator.language ? [navigator.language] : [];
  } catch {
    return [];
  }
}

/** This page's choice. */
export const languageChoice = new LanguageChoice({
  storage: browserStorage,
  now: () => Date.now(),
  systemLanguages: browserLanguages,
  languages,
});

if (typeof window !== "undefined") window.addEventListener("languagechange", () => languageChoice.refresh());

/** ReqEnvelope.lang: the language this browser shows, on every request (ws.ts). */
export function requestLanguage(): LanguageCode {
  return languageChoice.snapshot().effective;
}

/** Every reply ws.ts decodes: the device's value from Status and Settings. */
export function noteLanguageReply(env: RespEnvelope) {
  try {
    if (env.payload?.$case === "respStatus") languageChoice.noteDevice(env.payload.respStatus.language);
    else if (env.payload?.$case === "respSettings") languageChoice.noteDevice(env.payload.respSettings.language);
  } catch (err) {
    console.error("i18n: could not read the device's language:", err);
  }
}

/** A new socket to the device (ws.ts). */
export function noteLanguageConnection() {
  languageChoice.noteConnection();
}
