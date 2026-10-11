// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The language on screen, for components: I18nProvider (at the root,
// main.tsx) puts the runtime's snapshot here, so a change re-renders every
// component that reads it - and only those.

import { createContext } from "react";
import { i18nSnapshot, type I18nSnapshot } from "./runtime";

export const I18nContext = createContext<I18nSnapshot>(i18nSnapshot());
