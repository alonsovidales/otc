// SPDX-License-Identifier: AGPL-3.0-or-later

import { defineConfig, type Plugin } from 'vite'
import react from '@vitejs/plugin-react'

// index.html's Content-Security-Policy is for the built app the device
// serves. The dev server (npm run dev) injects an inline script of its own
// (React's refresh preamble) and talks to a device at another address
// (VITE_DEVICE), which it would refuse: there it is left out.
const devWithoutCSP = (): Plugin => ({
  name: 'otc-dev-without-csp',
  apply: 'serve',
  transformIndexHtml: (html) => html.replace(/\s*<meta http-equiv="Content-Security-Policy"[^>]*>/, ''),
})

// https://vite.dev/config/
export default defineConfig({
  plugins: [react(), devWithoutCSP()],
  base: './',
  // node-forge is loaded only on a plain-HTTP page, to seal the password
  // (net/pwCrypto.ts): a chunk named for it, not "index-*.js" like the
  // page's entry, which deviceBuild.ts tells builds by.
  build: {
    rollupOptions: {
      output: {
        manualChunks: (id) => (id.includes('/node_modules/node-forge/') ? 'forge' : undefined),
      },
    },
  },
  // `vite preview` only at the one address the web app knows as the
  // developer's (src/net/endpoint.ts): on another port it would look for
  // a device behind itself.
  preview: { port: 4173, strictPort: true },
})
