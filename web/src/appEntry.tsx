// SPDX-License-Identifier: AGPL-3.0-or-later

// The app, for every page but a shared gallery's (main.tsx). No router:
// the app has one address, and its history entries are backStack.ts's.
//import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import App from './App.tsx'
import TipLayer from './components/TipLayer.tsx'
import { ErrorBoundary } from './components/ErrorBoundary.tsx'
import { I18nProvider } from './i18n'

// Each section has its own boundary (ErrorBoundary.tsx): this one is for
// the rest, the top bar and the menu included.
createRoot(document.getElementById('root')!).render(
  <I18nProvider>
    <ErrorBoundary where="app">
      <App />
    </ErrorBoundary>
    <TipLayer />
  </I18nProvider>
)
