// SPDX-License-Identifier: AGPL-3.0-or-later

// A page that throws while it renders (a post with no files, issue F1 of
// the 2026-10 review) used to unmount the whole app: a blank window, and
// since the last section open is remembered, a blank window after a reload
// too. Each section is wrapped in one of these (where="tab"), so the top
// bar and the menu stay and only the section says "Something went wrong",
// with Reload and Go to Images; the whole app is wrapped too (where="app",
// appEntry.tsx), and the public gallery page (where="shared", Reload only,
// sharedEntry.tsx).
import { Component, type ErrorInfo, type ReactNode } from "react";
import { useT } from "../i18n";
import { showSection } from "./showSection";
import "./ErrorBoundary.css";

type Where = "tab" | "app" | "shared";

type Props = {
  children: ReactNode;
  where: Where;
  // Images itself: going "to Images" would only show it again.
  noImages?: boolean;
};

export function ErrorBoundary(props: Props) {
  return <Boundary {...props} />;
}

class Boundary extends Component<Props, { failed: boolean }> {
  state = { failed: false };

  static getDerivedStateFromError() {
    return { failed: true };
  }

  componentDidCatch(error: unknown, info: ErrorInfo) {
    console.error("The page stopped working:", error, info.componentStack);
  }

  render() {
    if (!this.state.failed) return this.props.children;
    return <ErrorNotice where={this.props.where} images={!this.props.noImages && this.props.where !== "shared"} />;
  }
}

function ErrorNotice({ where, images }: { where: Where; images: boolean }) {
  const t = useT();
  return (
    <div className={`eb-root eb-${where}`} role="alert">
      <h2 className="eb-title">{t("web.shell.error.title")}</h2>
      <p className="eb-text">{images ? t("web.shell.error.text") : t("web.shell.error.text_reload")}</p>
      <div className="eb-actions">
        <button type="button" onClick={() => window.location.reload()}>{t("web.shell.error.reload")}</button>
        {images && (
          // In a section the menu is still there to go by; with the whole
          // app gone, the page reloads into Images.
          <button type="button" className="eb-quiet" onClick={() => showSection("PhotoGallery")}>{t("web.shell.error.images")}</button>
        )}
      </div>
    </div>
  );
}
