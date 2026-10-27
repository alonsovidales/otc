// SPDX-License-Identifier: AGPL-3.0-or-later
//
// "Reconnecting…" (UX 6 of the 2026-10 review): while the socket is down
// and the app is opening it again (the polls and a wake go through
// request(), which reconnects - net/ws.ts, devicePoll.ts), the page used
// to go on showing what it had as if it were current. A small pill under
// the top bar says so instead, for as long as it lasts. Not for a drop a
// reconnect mends at once (a laptop waking): only after cShowAfterMs down.
// App leaves it out over the device-unreachable screen, which says more,
// and while nobody is signed in. Its status region stays in the page,
// empty while there's nothing to say, so the words are announced.
import { useEffect, useState, useSyncExternalStore } from "react";
import { wsClient } from "../net/ws";
import { useT } from "../i18n";
import Spinner from "./Spinner";
import "./ReconnectingBanner.css";

const cShowAfterMs = 1500;

export default function ReconnectingBanner({ active }: { active: boolean }) {
  const t = useT();
  const down = useSyncExternalStore(wsClient.onDownChange, wsClient.isDown);
  const [late, setLate] = useState(false);
  useEffect(() => {
    if (!down || !active) { setLate(false); return; }
    const timer = window.setTimeout(() => setLate(true), cShowAfterMs);
    return () => window.clearTimeout(timer);
  }, [down, active]);
  return (
    <div className="rc-wrap" role="status">
      {active && down && late && (
        <div className="rc-pill">
          <Spinner />
          <span>{t("web.shell.connection.reconnecting")}</span>
        </div>
      )}
    </div>
  );
}
