// SPDX-License-Identifier: AGPL-3.0-or-later

// Issue #78, follow-up: notifications used to be a header-level bell with a
// dropdown panel; the user asked for it to be a section of its own instead,
// living in TopTabs like Social/Profile/etc. (rightmost, on the web) rather
// than floating over the header. This is that section's full-page content;
// useNotificationCount below is the small shared bit App.tsx also needs, to
// badge the TopTabs entry itself with the unread count.
import { useCallback, useEffect, useMemo, useState } from "react";
import { useWS } from "../net/useWS";
import type { ReqEnvelope, RespEnvelope, Notification as PbNotification } from "../proto/messages";
import { NotificationType } from "../proto/messages";
import "./NotificationsPage.css";

const POLL_MS = 5000;

function bytesToURL(bytes?: Uint8Array, mime = "image/jpeg") {
  if (!bytes || bytes.length === 0) return null;
  return URL.createObjectURL(new Blob([bytes], { type: mime }));
}

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
// specifics stay local" spirit as push/push.go's own notification text.
function describe(n: PbNotification): string {
  switch (n.type) {
    case NotificationType.NotificationError: return "";
    case NotificationType.NotificationLikePublication: return "liked your post";
    case NotificationType.NotificationLikeComment: return "liked your comment";
    case NotificationType.NotificationNewComment: return "commented on your post";
    case NotificationType.NotificationFriendRequest: return "sent you a friend request";
    case NotificationType.NotificationFriendAccepted: return "accepted your friend request";
    default: return "";
  }
}

// Shared with App.tsx, which needs the unread count to badge the
// Notifications entry in TopTabs itself - polls independently of whether
// this page is actually mounted (same setInterval-in-a-useEffect shape as
// StatusWidget's own polling elsewhere in this app).
export function useNotificationCount(authenticated: boolean): [number, () => void] {
  const [count, setCount] = useState(0);

  const fetchCount = useCallback(async () => {
    if (!useWS.connected()) return;
    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqGetNotificationCount", reqGetNotificationCount: {} };
      });
      if (resp.payload?.$case === "respNotificationCount") {
        setCount(resp.payload.respNotificationCount.unacknowledgedCount);
      }
    } catch { /* next poll will retry */ }
  }, []);

  useEffect(() => {
    if (!authenticated) { setCount(0); return; }
    fetchCount();
    const t = setInterval(fetchCount, POLL_MS);
    return () => clearInterval(t);
  }, [authenticated, fetchCount]);

  // Lets NotificationsPage zero this the instant it marks everything read,
  // rather than waiting for the next poll tick to notice.
  return [count, () => setCount(0)];
}

export default function NotificationsPage({
  onOpenPost,
  onOpenFriendRequests,
  onAcknowledged,
}: {
  onOpenPost: (pubUuid: string, commentUuid: string | null) => void;
  onOpenFriendRequests: () => void;
  // Lets App.tsx zero the TopTabs badge the instant this page marks
  // everything read, rather than waiting for that separate poll to catch up.
  onAcknowledged: () => void;
}) {
  const [notifications, setNotifications] = useState<PbNotification[] | null>(null);
  // Issue #64: an error row's full list of errors shows on hover; a click
  // pins it open, for touch screens and for copying the text.
  const [pinnedDetails, setPinnedDetails] = useState<Record<string, boolean>>({});

  // Issue #78 follow-up: an avatar on the left (who did this) and, when
  // the notification points at a post/comment, a small thumbnail on the
  // right (what it's about) - object URLs, revoked whenever the
  // notification list is replaced or this page unmounts.
  const images = useMemo(() => {
    const map = new Map<string, { avatar: string | null; thumb: string | null }>();
    for (const n of notifications ?? []) {
      map.set(n.uuid, {
        avatar: bytesToURL(n.actorImage as unknown as Uint8Array),
        thumb: bytesToURL(n.thumbnail as unknown as Uint8Array),
      });
    }
    return map;
  }, [notifications]);
  useEffect(() => () => {
    for (const { avatar, thumb } of images.values()) {
      if (avatar) URL.revokeObjectURL(avatar);
      if (thumb) URL.revokeObjectURL(thumb);
    }
  }, [images]);

  // Issue #78: "when the user opens the section all the notifications will
  // change to acknowledged" - fetch the list, then immediately mark
  // everything read.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqListNotifications", reqListNotifications: { limit: 50 } };
      });
      if (cancelled) return;
      if (resp.payload?.$case === "respNotifications") {
        setNotifications(resp.payload.respNotifications.notifications);
      }
      onAcknowledged();
      await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqMarkNotificationsAcknowledged", reqMarkNotificationsAcknowledged: {} };
      });
    })();
    return () => { cancelled = true; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const onClickNotification = (n: PbNotification) => {
    if (n.type === NotificationType.NotificationError) {
      setPinnedDetails((p) => ({ ...p, [n.uuid]: !p[n.uuid] }));
      return;
    }
    if (n.type === NotificationType.NotificationFriendRequest || n.type === NotificationType.NotificationFriendAccepted) {
      onOpenFriendRequests();
    } else if (n.pubUuid) {
      onOpenPost(n.pubUuid, n.commentUuid || null);
    }
  };

  return (
    <div className="np-page">
      {notifications == null ? (
        <div className="np-empty">Loading…</div>
      ) : notifications.length === 0 ? (
        <div className="np-empty">Nothing yet</div>
      ) : (
        <ul className="np-list">
          {notifications.map(n => {
            const { avatar, thumb } = images.get(n.uuid) ?? { avatar: null, thumb: null };
            const isError = n.type === NotificationType.NotificationError;
            return (
              <li
                key={n.uuid}
                className={`np-item${n.acknowledged ? "" : " np-unacknowledged"}${isError ? " np-error" : ""}${pinnedDetails[n.uuid] ? " np-pinned" : ""}`}
                onClick={() => onClickNotification(n)}
                title={isError ? undefined : undefined}
              >
                {isError ? (
                  <div className="np-avatar np-avatar-placeholder np-error-icon" aria-hidden="true">
                    <svg width="20" height="20" viewBox="0 0 24 24">
                      <path d="M12 3 2 21h20L12 3z" fill="none" stroke="currentColor" strokeWidth="2" strokeLinejoin="round" />
                      <path d="M12 10v5" stroke="currentColor" strokeWidth="2" strokeLinecap="round" />
                      <circle cx="12" cy="18" r="1.2" fill="currentColor" />
                    </svg>
                  </div>
                ) : avatar ? (
                  <img src={avatar} className="np-avatar" alt="" />
                ) : (
                  <div className="np-avatar np-avatar-placeholder">👤</div>
                )}
                <span className="np-item-text">
                  {isError ? (
                    <>
                      {/* Issue #64: one line in the list; the whole list of
                          errors on hover (or pinned by a click). */}
                      <strong>{n.title}</strong>
                      {n.occurrences > 1 && <span className="np-more"> +{n.occurrences - 1} more</span>}
                    </>
                  ) : (
                    <><strong>{n.actorName || n.actorDomain}</strong> {describe(n)}</>
                  )}
                  <span className="np-item-when"> · {formatWhen(n.dt)}</span>
                  {isError && n.details && (
                    <pre className="np-popover">{n.details}</pre>
                  )}
                </span>
                {thumb && <img src={thumb} className="np-thumb" alt="" />}
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
