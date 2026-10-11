// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Telling the device about a change made in this browser: SetLanguage
// {language, expected}, answered with an Ack (choice.ts reads it). Through
// useWS, so it waits for a sign-in under way like any other request.

import { useWS } from "../net/useWS";
import type { ReqEnvelope, RespEnvelope } from "../proto/messages";
import { languageChoice, setLanguageResult, type SendResult } from "./choice";

// A request the device never answers (the socket stays open) fails here,
// and the change waits for the next connection.
const cSendTimeoutMs = 15_000;

function withTimeout<T>(p: Promise<T>, ms: number): Promise<T | null> {
  return new Promise((resolve, reject) => {
    const timer = window.setTimeout(() => resolve(null), ms);
    p.then(
      (v) => { window.clearTimeout(timer); resolve(v); },
      (err) => { window.clearTimeout(timer); reject(err); },
    );
  });
}

async function sendSetLanguage(language: string, expected: string | undefined): Promise<SendResult> {
  const resp: RespEnvelope | null = await withTimeout(useWS.request((e: Partial<ReqEnvelope>) => {
    e.payload = { $case: "reqSetLanguage", reqSetLanguage: expected === undefined ? { language } : { language, expected } };
  }), cSendTimeoutMs);
  return resp ? setLanguageResult(resp) : "failed";
}

// After "changed": the Settings reply carries the device's value, which
// ws.ts hands to choice.ts like any other.
function rereadSettings() {
  useWS.request((e: Partial<ReqEnvelope>) => {
    e.payload = { $case: "reqGetSettings", reqGetSettings: {} };
  }).catch((err) => console.error("Could not read the device's language again:", err));
}

languageChoice.connect(sendSetLanguage, rereadSettings);
