// SPDX-License-Identifier: AGPL-3.0-or-later
//
// docs/promo: serves the real web app (web/) with net/useWS.ts swapped for
// mock-device.ts - a pretend device with the sample library - on
// http://127.0.0.1:5288/, plus docs/promo itself under /promo/ (tools/desk.html,
// the photos). Needs web/'s node_modules (npm install --prefix web).
//   node docs/promo/tools/serve.mjs
import { readFileSync, existsSync } from "node:fs";
import { dirname, extname, join, normalize, resolve, sep } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";
const here = dirname(fileURLToPath(import.meta.url));
const promo = resolve(here, "..");
const WEB = resolve(here, "../../../web");
const { createServer } = await import(join(WEB, "node_modules/vite/dist/node/index.js"));
const react = (await import(join(WEB, "node_modules/@vitejs/plugin-react/dist/index.js"))).default;
const types = { ".html": "text/html", ".jpg": "image/jpeg", ".png": "image/png", ".svg": "image/svg+xml", ".css": "text/css", ".js": "text/javascript" };
const promoFiles = {
  name: "promo-files",
  configureServer(server) {
    server.middlewares.use((req, res, next) => {
      if (!req.url.startsWith("/promo/")) return next();
      const p = normalize(join(promo, decodeURIComponent(req.url.slice(7).split("?")[0])));
      if (!p.startsWith(promo + sep) || !existsSync(p)) { res.statusCode = 404; return res.end(); }
      res.setHeader("Content-Type", types[extname(p)] ?? "application/octet-stream");
      res.end(readFileSync(p));
    });
  },
};
const server = await createServer({
  configFile: false,
  root: WEB,
  cacheDir: join(tmpdir(), "otc-promo-vite"),
  plugins: [react(), promoFiles],
  resolve: { alias: [{ find: /^(\.{1,2}\/)+(net\/)?useWS$/, replacement: join(here, "mock-device.ts") }] },
  server: { port: 5288, strictPort: true, host: "127.0.0.1", fs: { allow: [WEB, promo] }, hmr: false },
  logLevel: "info",
});
await server.listen();
server.printUrls();
