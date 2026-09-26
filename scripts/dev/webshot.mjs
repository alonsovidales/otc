// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Screenshots of the web app on a real device, without a browser extension:
// drives a headless Chrome over the DevTools protocol (Node 22+'s built-in
// WebSocket, no npm packages), signs in, opens the Notifications tab, and
// optionally hovers then clicks one element, saving a PNG after each step.
//
//   ssh -f -N -L 18080:127.0.0.1:8080 otc@pit.otc
//   node scripts/dev/webshot.mjs http://127.0.0.1:18080/ '<password>' /tmp/shot '.np-item.np-error'
//
// Through a loopback tunnel on purpose: macOS's local-network permission
// makes a headless Chrome launched from a terminal answer
// ERR_ADDRESS_UNREACHABLE for a LAN address, and Chrome does not resolve
// .otc names. Steps are plain Runtime.evaluate snippets - copy the file and
// change them for another page.
import { spawn } from "node:child_process";
import { writeFileSync, mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const [url, password, out, hoverSel] = process.argv.slice(2);
const chrome = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";
const profile = mkdtempSync(join(tmpdir(), "otc-chrome-"));
const port = 9333;
const proc = spawn(chrome, [
  "--headless=new", `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`,
  "--window-size=1400,900", "--no-first-run", "--no-default-browser-check", "about:blank",
], { stdio: "ignore" });
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function targets() {
  for (let i = 0; i < 40; i++) {
    try {
      const r = await fetch(`http://127.0.0.1:${port}/json`);
      const list = await r.json();
      const page = list.find((t) => t.type === "page");
      if (page) return page;
    } catch {}
    await sleep(250);
  }
  throw new Error("chrome did not come up");
}

const page = await targets();
const ws = new WebSocket(page.webSocketDebuggerUrl);
await new Promise((r) => (ws.onopen = r));
let id = 0;
const pending = new Map();
ws.onmessage = (ev) => {
  const m = JSON.parse(ev.data);
  if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); }
};
const send = (method, params = {}) => new Promise((resolve) => {
  const i = ++id;
  pending.set(i, resolve);
  ws.send(JSON.stringify({ id: i, method, params }));
});
const evaluate = async (expression) => {
  const r = await send("Runtime.evaluate", { expression, awaitPromise: true, returnByValue: true });
  return r.result?.result?.value;
};
const shot = async (name) => {
  const r = await send("Page.captureScreenshot", { format: "png" });
  writeFileSync(`${out}-${name}.png`, Buffer.from(r.result.data, "base64"));
  console.log("saved", `${out}-${name}.png`);
};

await send("Page.enable");
await send("Runtime.enable");
await send("Emulation.setDeviceMetricsOverride", { width: 1400, height: 900, deviceScaleFactor: 1, mobile: false });
await send("Page.navigate", { url });
await sleep(3000);

// An anonymous visitor sees the public profile with a "Sign In" button;
// click it to get the password form.
console.log(await evaluate(`(() => {
  const b = [...document.querySelectorAll("button, a")].find((e) => /sign in/i.test(e.textContent));
  if (!b) return "no sign-in button";
  b.click();
  return "clicked sign in";
})()`));
await sleep(1500);

// Sign in: React-controlled password input, so set through the native
// setter and fire an input event, then submit the form.
await evaluate(`(() => {
  const input = document.querySelector('input[type="password"]');
  if (!input) return "no password input";
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value").set;
  setter.call(input, ${JSON.stringify(password)});
  input.dispatchEvent(new Event("input", { bubbles: true }));
  input.form.requestSubmit();
  return "submitted";
})()`);
await sleep(4000);
await shot("signed-in");

// The notifications tab: whatever tab-like element mentions notifications.
const clicked = await evaluate(`(() => {
  const els = [...document.querySelectorAll("button, a, [role=tab]")];
  const el = els.find((e) => /notif/i.test(e.textContent + " " + (e.getAttribute("aria-label") || "") + " " + (e.getAttribute("title") || "")));
  if (!el) return "no notifications tab among " + els.length;
  el.click();
  return "clicked: " + (el.getAttribute("aria-label") || el.textContent).trim();
})()`);
console.log(clicked);
await sleep(3000);
await shot("notifications");

if (hoverSel) {
  const rect = await evaluate(`(() => {
    const el = document.querySelector(${JSON.stringify(hoverSel)});
    if (!el) return null;
    const r = el.getBoundingClientRect();
    return { x: r.x + r.width / 2, y: r.y + r.height / 2 };
  })()`);
  console.log("hover target", rect);
  if (rect) {
    await send("Input.dispatchMouseEvent", { type: "mouseMoved", x: rect.x, y: rect.y });
    await sleep(800);
    await shot("hover");
    await send("Input.dispatchMouseEvent", { type: "mousePressed", x: rect.x, y: rect.y, button: "left", clickCount: 1 });
    await send("Input.dispatchMouseEvent", { type: "mouseReleased", x: rect.x, y: rect.y, button: "left", clickCount: 1 });
    await sleep(500);
    await send("Input.dispatchMouseEvent", { type: "mouseMoved", x: 5, y: 5 });
    await sleep(500);
    await shot("pinned");
  }
}

ws.close();
proc.kill();
