// SPDX-License-Identifier: AGPL-3.0-or-later

// The toasts at the foot of Images and Files (ToastView.tsx): one at a
// time, the newest replacing the one before. Its words are made when shown
// (UiText), so a message on screen follows a change of language.
import { useCallback, useEffect, useRef, useState } from "react";
import type { UiText } from "./uiText";

export type Toast = {
  id: number;
  text: UiText;
  error?: boolean;
  // Stays until replaced (work in progress), with a spinner.
  busy?: boolean;
  action?: { label: UiText; run: () => void };
  // Stays until used or dismissed: its action is the only way to what it
  // offers.
  stay?: boolean;
};

export function useToasts() {
  const [toast, setToast] = useState<Toast | null>(null);
  const toastIdRef = useRef(0);
  const showToast = useCallback((next: Omit<Toast, "id">) => {
    const id = ++toastIdRef.current;
    setToast({ ...next, id });
    return id;
  }, []);
  const dismissToast = useCallback((id: number) => setToast((cur) => (cur?.id === id ? null : cur)), []);
  // Gone by itself after a while, longer for one with an action or an
  // error; never while busy or when it stays.
  useEffect(() => {
    if (!toast || toast.busy || toast.stay) return;
    const timer = window.setTimeout(() => dismissToast(toast.id), toast.action ? 6000 : toast.error ? 5000 : 3000);
    return () => window.clearTimeout(timer);
  }, [toast, dismissToast]);
  const toastError = useCallback((text: UiText) => { showToast({ text, error: true }); }, [showToast]);
  return { toast, showToast, dismissToast, toastError };
}
