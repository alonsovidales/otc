// SPDX-License-Identifier: AGPL-3.0-or-later

// Where useToasts' toast shows: fixed at the foot of the page beside the
// menu (PhotoGallery.css's .pg-toast*), announced as it comes.
import { useT } from "../i18n";
import Spinner from "./Spinner";
import type { Toast } from "./useToasts";
import "./PhotoGallery.css";

export default function ToastView({ toast, onDismiss, beforeAction }: {
  toast: Toast | null;
  onDismiss: (id: number) => void;
  // Run before the action (Images: a double click's second click goes
  // nowhere).
  beforeAction?: () => void;
}) {
  const t = useT();
  return (
    <div className="pg-toast-wrap" aria-live="polite">
      {toast && (
        <div key={toast.id} className={`pg-toast${toast.error ? " error" : ""}${toast.action ? " has-action" : ""}`}>
          {toast.busy && <Spinner />}
          <span className="pg-toast-text">{toast.text(t)}</span>
          {toast.action && (
            <button
              type="button"
              className="pg-toast-action"
              onClick={() => { beforeAction?.(); toast.action?.run(); onDismiss(toast.id); }}
            >
              {toast.action.label(t)}
            </button>
          )}
          {toast.stay && (
            <button type="button" className="pg-icon-btn pg-toast-close" onClick={() => onDismiss(toast.id)} aria-label={t("web.images.grid.dismiss")}>
              <svg width={18} height={18} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6"
                strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                <path d="M6.5 6.5l11 11M17.5 6.5l-11 11" />
              </svg>
            </button>
          )}
        </div>
      )}
    </div>
  );
}
