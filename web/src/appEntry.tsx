// SPDX-License-Identifier: AGPL-3.0-or-later

// The app, for every page but a shared gallery's (main.tsx).
//import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from "react-router-dom";
import App from './App.tsx'
import TipLayer from './components/TipLayer.tsx'
import { ErrorBoundary } from './components/ErrorBoundary.tsx'
import { I18nProvider } from './i18n'

// Each section has its own boundary (ErrorBoundary.tsx): this one is for
// the rest, the top bar and the menu included.
createRoot(document.getElementById('root')!).render(
  <I18nProvider>
    <BrowserRouter>
      <ErrorBoundary where="app">
        <App />
      </ErrorBoundary>
      <TipLayer />
    </BrowserRouter>
  </I18nProvider>
)
