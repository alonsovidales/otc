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
// Every screen looks as on any device. A change tried on a demo device is
// refused here, with a short notice (refuseOnDemo, components/
// DemoBanner.tsx's DemoRefusedNotice), before any dialog or request; likes
// stay on the page alone. ws.ts refuses whatever change request still gets
// that far (demoRequestReply).
//
// React-free: ws.ts feeds it every reply (noteDemoReply); components read
// it with useDemo (components/useDemo.ts).

import type { ReqEnvelope, RespEnvelope } from "../proto/messages";

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

const refusalListeners = new Set<() => void>();

/**
 * On a demo device: shows the notice that changes are turned off and
 * answers true, for the caller to stop there. False anywhere else.
 */
export function refuseOnDemo(): boolean {
  if (!demo) return false;
  refusalListeners.forEach((fn) => fn());
  return true;
}

export function subscribeDemoRefusals(fn: () => void): () => void {
  refusalListeners.add(fn);
  return () => { refusalListeners.delete(fn); };
}

// The requests that change something, as the web app sends them. A demo
// device refuses them all (websocket/demo.go); ws.ts answers them here
// instead of sending them.
const cChanges = new Set([
  "reqDelFile", "reqUploadFile", "reqBeginUpload", "reqUploadChunk", "reqFinishUpload", "reqLinkFile",
  "reqSetUploadOnly", "reqSetOutOfImages", "reqRenamePerson", "reqDeletePerson", "reqMergePeople",
  "reqStartReprocess", "reqStopReprocess", "reqCreateImageGroup", "reqAddToImageGroup", "reqRenameImageGroup",
  "reqDeleteImageGroup", "reqNewSocialPublication", "reqNewSocialComment", "reqDelSocialPublication",
  "reqDelSocialComment", "reqFriendshipRequest", "reqChangeFriendStatus", "reqDeleteFriendship",
  "reqShareFilesLink", "reqCreateSharedGallery", "reqDeleteSharedLink", "reqChangeKey", "reqSetSettings",
  "reqSetProfile", "reqSetFaceRecognitionEnabled", "reqSetImageTaggingEnabled", "reqSetSocialStorageLimit",
  "reqCreateUser", "reqDeleteUser", "reqSetUserActive", "reqApplyUpdate", "reqRestartDevice", "reqSetupTailscale",
  "reqBridgeSignIn", "reqEnableBridge", "reqDisableBridge", "reqSendLogs", "reqSetupStorage", "reqSetWifi",
]);
const cLikes = new Set(["reqLikePublication", "reqLikeComment"]);

/**
 * What ws.ts answers, on a demo device, for a request that changes
 * something, without sending it: a like is taken (it stays on the page),
 * anything else is refused as the device would (error_code "demo"), in the
 * words given. Undefined for every other request, and off a demo device.
 */
export function demoRequestReply(req: Partial<ReqEnvelope>, refused: string): RespEnvelope | undefined {
  const kind = (req.payload as { $case?: string } | undefined)?.$case;
  if (!demo || !kind) return undefined;
  if (cLikes.has(kind)) {
    return { id: req.id ?? 0, error: false, errorMessage: "", errorCode: "", payload: { $case: "respAck", respAck: { ok: true, errorMsg: "", code: "" } } } as RespEnvelope;
  }
  if (!cChanges.has(kind)) return undefined;
  return {
    id: req.id ?? 0, error: true, errorMessage: refused, errorCode: "demo",
    payload: { $case: "respAck", respAck: { ok: false, errorMsg: refused, code: "demo" } },
  } as RespEnvelope;
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
