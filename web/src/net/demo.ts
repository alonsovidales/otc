// SPDX-License-Identifier: AGPL-3.0-or-later
//
// A public demo device (docs/demo-device.md): anyone may sign in with its
// published password and look around, and it refuses every change
// (error_code "demo", with its own words). It says so before sign-in, with
// the password (PubKey.demo, demo_password), and after (Status.demo). A
// page is served by one device, so once seen the flag stays for the page's
// life. The page then offers Try the demo, shows a banner and hides what
// would change something.
//
// React-free: ws.ts feeds it every reply (noteDemoReply); components read
// it with useDemo (components/useDemo.ts).

import type { RespEnvelope } from "../proto/messages";

let demo = false;
let password = "";
const listeners = new Set<() => void>();

/** Called by ws.ts with every reply the device sends. */
export function noteDemoReply(env: RespEnvelope) {
  const p = env.payload;
  if (p?.$case === "respPubKey" && p.respPubKey.demo) set(p.respPubKey.demoPassword);
  else if (p?.$case === "respStatus" && p.respStatus.demo) set(password);
}

function set(pw: string) {
  if (demo && pw === password) return;
  demo = true;
  password = pw || password;
  listeners.forEach((fn) => fn());
}

/** Whether the device serving this page is a demo device. */
export function isDemo(): boolean {
  return demo;
}

/** The demo device's published password ("" until told, or none). */
export function demoPassword(): string {
  return password;
}

/** For the tests: as before the first reply. */
export function forgetDemo() {
  demo = false;
  password = "";
}

export function subscribeDemo(fn: () => void): () => void {
  listeners.add(fn);
  return () => { listeners.delete(fn); };
}
