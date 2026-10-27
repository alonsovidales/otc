// SPDX-License-Identifier: AGPL-3.0-or-later

// A shared gallery's public page (issue #180, main.tsx): the gallery and
// what it needs, without the app around it. Its socket goes where App's
// would (net/endpoint.ts), set here as App sets it.
import { createRoot } from 'react-dom/client'
import SharedGalleryView from './components/SharedGalleryView.tsx'
import { ErrorBoundary } from './components/ErrorBoundary.tsx'
import { I18nProvider } from './i18n'
import { useWS } from './net/useWS'
import { pageEndpoint } from './net/endpoint'

const cfg = window.__OTC_CONFIG;
const page = pageEndpoint(window.location, import.meta.env.MODE === "development", import.meta.env.VITE_DEVICE, !!cfg);
// Nobody signs in on this page: nothing to tell when a session ends.
useWS.init(cfg ? cfg.endpoint : page.endpoint, () => {});

createRoot(document.getElementById('root')!).render(
  <I18nProvider>
    <ErrorBoundary where="shared">
      <SharedGalleryView />
    </ErrorBoundary>
  </I18nProvider>
)
