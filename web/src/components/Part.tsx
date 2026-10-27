// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Around a part of the app that loads when first opened (lazyPart.ts): a
// spinner while its chunk comes and, when it didn't, "This part of the app
// couldn't load" with Reload - in the page for a section, over it for a
// layer (with Close, and Back closes it). An error of the part itself goes
// on to the boundary around it (ErrorBoundary.tsx), as before.
import { Component, Suspense } from "react";
import type { ReactNode } from "react";
import { useT } from "../i18n";
import Spinner from "./Spinner";
import { useBackLayer } from "./backStack";
import "./ErrorBoundary.css";
import { PartFailed } from "./lazyPart";
import "./Part.css";

type Kind = "section" | "layer";

/** Around a lazy part (lazyPart.ts): its loading state, and what shows if it didn't load. */
export default function Part({ kind = "section", onClose, children }: { kind?: Kind; onClose?: () => void; children: ReactNode }) {
  return (
    <PartBoundary kind={kind} onClose={onClose}>
      <Suspense fallback={<PartWait kind={kind} />}>{children}</Suspense>
    </PartBoundary>
  );
}

class PartBoundary extends Component<{ kind: Kind; onClose?: () => void; children: ReactNode }, { error: unknown }> {
  state = { error: null as unknown };

  static getDerivedStateFromError(error: unknown) {
    return { error };
  }

  render() {
    const { error } = this.state;
    if (error === null) return this.props.children;
    // Not the chunk: the part itself failed, for the boundary around it.
    if (!(error instanceof PartFailed)) throw error;
    return <PartNotice kind={this.props.kind} onClose={this.props.onClose} />;
  }
}

function PartWait({ kind }: { kind: Kind }) {
  const t = useT();
  if (kind === "layer") {
    return (
      <div className="lzp-layer">
        <Spinner label={t("common.loading")} />
      </div>
    );
  }
  return <div className="page-wait"><Spinner label={t("common.loading")} /></div>;
}

function PartNotice({ kind, onClose }: { kind: Kind; onClose?: () => void }) {
  const t = useT();
  // Over the page, Back closes it, as it would have the part.
  useBackLayer(kind === "layer" && !!onClose, () => onClose?.());
  const notice = (
    <div className={kind === "layer" ? "eb-root lzp-card" : "eb-root"} role="alert">
      <h2 className="eb-title">{t("web.shell.error.part_title")}</h2>
      <p className="eb-text">{t("web.shell.error.part_text")}</p>
      <div className="eb-actions">
        <button type="button" onClick={() => window.location.reload()}>{t("web.shell.error.reload")}</button>
        {kind === "layer" && onClose && (
          <button type="button" className="eb-quiet" onClick={onClose}>{t("common.close")}</button>
        )}
      </div>
    </div>
  );
  return kind === "layer" ? <div className="lzp-layer">{notice}</div> : notice;
}
