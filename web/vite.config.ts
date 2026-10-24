// SPDX-License-Identifier: AGPL-3.0-or-later

import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  base: './',
  // `vite preview` only at the one address the web app knows as the
  // developer's (src/net/endpoint.ts): on another port it would look for
  // a device behind itself.
  preview: { port: 4173, strictPort: true },
})
