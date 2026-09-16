// SPDX-License-Identifier: AGPL-3.0-or-later

// Issue #78: Instagram-style notifications - a bell in the header, a
// dropdown timeline of likes/comments/friend requests, an unread badge,
// and a highlighted bell while anything is unacknowledged. Polls for the
// count the same way StatusWidget/SettingsForm's reprocess status do
// (setInterval inside a useEffect) rather than anything push-based - see
// this repo's own convention for "live-ish but not truly real-time" data.
import { useCallback, useEffect, useRef, useState } from "react";
import { useWS } from "../net/useWS";
import type { ReqEnvelope, RespEnvelope, Notification as PbNotification } from "../proto/messages";
import { NotificationType } from "../proto/messages";
import "./NotificationsBell.css";

const POLL_MS = 5000;

function formatWhen(d?: Date): string {
  if (!d) return "";
  const diffMs = Date.now() - d.getTime();
  const mins = Math.floor(diffMs / 60000);
  if (mins < 1) return "just now";
  if (mins < 60) return `${mins}m`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h`;
  const days = Math.floor(hours / 24);
  if (days < 7) return `${days}d`;
  return d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

// A fixed sentence per type, actor name filled in - same "generic body,
// specifics stay local" spirit as push/push.go's own notification text,
// even though this feed (unlike a push payload) has no reason to be that
// cautious on its own; it's just a clean, consistent line either way.
function describe(n: PbNotification): string {
  switch (n.type) {
    case NotificationType.NotificationLikePublication: return "liked your post";
    case NotificationType.NotificationLikeComment: return "liked your comment";
    case NotificationType.NotificationNewComment: return "commented on your post";
    case NotificationType.NotificationFriendRequest: return "sent you a friend request";
    case NotificationType.NotificationFriendAccepted: return "accepted your friend request";
    default: return "";
  }
}

export default function NotificationsBell({
  authenticated,
  onOpenPost,
  onOpenFriendRequests,
}: {
  authenticated: boolean;
  onOpenPost: (pubUuid: string, commentUuid: string | null) => void;
  onOpenFriendRequests: () => void;
}) {
  const [unacknowledgedCount, setUnacknowledgedCount] = useState(0);
  const [open, setOpen] = useState(false);
  const [notifications, setNotifications] = useState<PbNotification[] | null>(null);
  const rootRef = useRef<HTMLDivElement | null>(null);

  const fetchCount = useCallback(async () => {
    if (!useWS.connected()) return;
    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqGetNotificationCount", reqGetNotificationCount: {} };
      });
      if (resp.payload?.$case === "respNotificationCount") {
        setUnacknowledgedCount(resp.payload.respNotificationCount.unacknowledgedCount);
      }
    } catch { /* next poll will retry */ }
  }, []);

  useEffect(() => {
    if (!authenticated) return;
    fetchCount();
    const t = setInterval(fetchCount, POLL_MS);
    return () => clearInterval(t);
  }, [authenticated, fetchCount]);

  // Issue #78: "when the user opens the section all the notifications
  // will change to acknowledged" - fetch the list, then immediately mark
  // everything read and zero the badge, rather than waiting for the next
  // poll tick to notice.
  const openPanel = useCallback(async () => {
    setOpen(true);
    setNotifications(null);
    const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
      (e as any).payload = { $case: "reqListNotifications", reqListNotifications: { limit: 50 } };
    });
    if (resp.payload?.$case === "respNotifications") {
      setNotifications(resp.payload.respNotifications.notifications);
    }
    setUnacknowledgedCount(0);
    await useWS.request((e: Partial<ReqEnvelope>) => {
      (e as any).payload = { $case: "reqMarkNotificationsAcknowledged", reqMarkNotificationsAcknowledged: {} };
    });
  }, []);

  const toggle = () => { if (open) setOpen(false); else void openPanel(); };

  // Close on an outside click, same convention as the other dropdown-style
  // popovers in this app (e.g. the tag search suggestions list).
  useEffect(() => {
    if (!open) return;
    const onDocClick = (e: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onDocClick);
    return () => document.removeEventListener("mousedown", onDocClick);
  }, [open]);

  const onClickNotification = (n: PbNotification) => {
    setOpen(false);
    if (n.type === NotificationType.NotificationFriendRequest || n.type === NotificationType.NotificationFriendAccepted) {
      onOpenFriendRequests();
    } else if (n.pubUuid) {
      onOpenPost(n.pubUuid, n.commentUuid || null);
    }
  };

  if (!authenticated) return null;

  return (
    <div className="nb-root" ref={rootRef}>
      <button
        className={`nb-bell${unacknowledgedCount > 0 ? " nb-bell-active" : ""}`}
        onClick={toggle}
        title="Notifications"
        aria-label="Notifications"
      >
        <svg width="19" height="19" viewBox="0 0 24 24" aria-hidden="true">
          <path
            d="M12 3a5 5 0 0 0-5 5v2.7c0 1.15-.45 2.25-1.26 3.06L4.5 15h15l-1.24-1.24A4.33 4.33 0 0 1 17 10.7V8a5 5 0 0 0-5-5Z"
            stroke="currentColor" strokeWidth="1.5" fill="none" strokeLinecap="round" strokeLinejoin="round"
          />
          <path d="M9.5 18a2.5 2.5 0 0 0 5 0" stroke="currentColor" strokeWidth="1.5" fill="none" strokeLinecap="round" />
        </svg>
        {unacknowledgedCount > 0 && (
          <span className="nb-badge">{unacknowledgedCount > 99 ? "99+" : unacknowledgedCount}</span>
        )}
      </button>

      {open && (
        <div className="nb-panel">
          <div className="nb-panel-title">Notifications</div>
          {notifications == null ? (
            <div className="nb-empty">Loading…</div>
          ) : notifications.length === 0 ? (
            <div className="nb-empty">Nothing yet</div>
          ) : (
            <ul className="nb-list">
              {notifications.map(n => (
                <li
                  key={n.uuid}
                  className={`nb-item${n.acknowledged ? "" : " nb-unacknowledged"}`}
                  onClick={() => onClickNotification(n)}
                >
                  <span className="nb-item-text">
                    <strong>{n.actorName || n.actorDomain}</strong> {describe(n)}
                  </span>
                  <span className="nb-item-when">{formatWhen(n.dt)}</span>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  );
}
