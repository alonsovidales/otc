// SPDX-License-Identifier: AGPL-3.0-or-later

//import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from "react-router-dom";
import './index.css'
import App from './App.tsx'
import TipLayer from './components/TipLayer.tsx'
import { I18nProvider } from './i18n'

createRoot(document.getElementById('root')!).render(
  <I18nProvider>
    <BrowserRouter>
      <App />
      <TipLayer />
    </BrowserRouter>
  </I18nProvider>
)
