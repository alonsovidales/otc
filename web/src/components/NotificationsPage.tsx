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
import { FriendShipStatus, NotificationType } from "../proto/messages";
import "./NotificationsPage.css";
import { Trans, fmtDate, useT, type RichKey, type TFunction } from "../i18n";
import { refuseOnDemo } from "../net/demo";
import { pollDevice } from "./devicePoll";
import { asIs, say, type UiText } from "./uiText";
import Spinner from "./Spinner";
import { ErrorBoundary } from "./ErrorBoundary";

const POLL_MS = 5000;

function bytesToURL(bytes?: Uint8Array, mime = "image/jpeg") {
  if (!bytes || bytes.length === 0) return null;
  return URL.createObjectURL(new Blob([bytes], { type: mime }));
}

function formatWhen(t: TFunction, d?: Date): string {
  if (!d) return "";
  const diffMs = Date.now() - d.getTime();
  const mins = Math.floor(diffMs / 60000);
  if (mins < 1) return t("web.social.time.just_now");
  if (mins < 60) return t("web.social.time.minutes", { minutes: mins });
  const hours = Math.floor(mins / 60);
  if (hours < 24) return t("web.social.time.hours", { hours });
  const days = Math.floor(hours / 24);
  if (days < 7) return t("web.social.time.days", { days });
  return fmtDate(t.lang, d, { month: "short", day: "numeric" });
}

// A fixed sentence per type, actor name filled in - same "generic body,
// specifics stay local" spirit as push/push.go's own notification text.
type ActorKey = Extract<RichKey, `web.social.alerts.${string}`>;
function describe(n: PbNotification): ActorKey | null {
  switch (n.type) {
    case NotificationType.NotificationLikePublication: return "web.social.alerts.liked_post";
    case NotificationType.NotificationLikeComment: return "web.social.alerts.liked_comment";
    case NotificationType.NotificationNewComment: return "web.social.alerts.commented";
    case NotificationType.NotificationFriendRequest: return "web.social.alerts.friend_request";
    case NotificationType.NotificationFriendAccepted: return "web.social.alerts.friend_accepted";
    default: return null;
  }
}

// Shared with App.tsx, which needs the unread count to badge the
// Notifications entry in TopTabs itself - polls independently of whether
// this page is actually mounted (devicePoll.ts: a closed socket is opened
// again, and a failed turn waits longer before the next).
export function useNotificationCount(authenticated: boolean): [number, () => void] {
  const [count, setCount] = useState(0);

  const fetchCount = useCallback(async (): Promise<boolean> => {
    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        e.payload = { $case: "reqGetNotificationCount", reqGetNotificationCount: {} };
      });
      if (resp.payload?.$case !== "respNotificationCount") return false;
      setCount(resp.payload.respNotificationCount.unacknowledgedCount);
      return true;
    } catch {
      return false; // the next turn tries again
    }
  }, []);

  useEffect(() => {
    if (!authenticated) { setCount(0); return; }
    return pollDevice(fetchCount, POLL_MS);
  }, [authenticated, fetchCount]);

  // Lets NotificationsPage zero this the instant it marks everything read,
  // rather than waiting for the next poll tick to notice.
  return [count, () => setCount(0)];
}

type NotificationsProps = {
  onOpenPost: (pubUuid: string, commentUuid: string | null) => void;
  onOpenFriendRequests: () => void;
  // Issue #183: an update notification opens Settings, where Update is.
  onOpenSettings?: () => void;
  // Lets App.tsx zero the TopTabs badge the instant this page marks
  // everything read, rather than waiting for that separate poll to catch up.
  onAcknowledged: () => void;
};

// Alerts, inside its section's error boundary (ErrorBoundary.tsx).
export default function NotificationsPage(props: NotificationsProps) {
  return <ErrorBoundary where="tab"><Alerts {...props} /></ErrorBoundary>;
}

function Alerts({
  onOpenPost,
  onOpenFriendRequests,
  onOpenSettings,
  onAcknowledged,
}: NotificationsProps) {
  const t = useT();
  const [notifications, setNotifications] = useState<PbNotification[] | null>(null);
  // The list couldn't be read: the device's words, or ours, with Try again.
  const [loadError, setLoadError] = useState<UiText | null>(null);
  const [loadTry, setLoadTry] = useState(0);
  // Issue #64: an error row's full list of errors shows on hover; a click
  // pins it open, for touch screens and for copying the text.
  const [pinnedDetails, setPinnedDetails] = useState<Record<string, boolean>>({});
  // Domains whose friend request still waits for an answer (their alert
  // gets an Accept button), and the ones accepted from here.
  const [awaiting, setAwaiting] = useState<Set<string>>(new Set());
  const [acceptedHere, setAcceptedHere] = useState<Set<string>>(new Set());

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
  // everything read. A list that didn't come says so, with Try again (it
  // used to say "Loading…" for good), and marks nothing read.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      setLoadError(null);
      let resp: RespEnvelope;
      try {
        resp = await useWS.request((e: Partial<ReqEnvelope>) => {
          e.payload = { $case: "reqListNotifications", reqListNotifications: { limit: 50 } };
        });
      } catch {
        if (!cancelled) setLoadError(() => say("web.social.alerts.load_failed"));
        return;
      }
      if (cancelled) return;
      if (resp.payload?.$case !== "respNotifications") {
        const words = resp.errorMessage;
        setLoadError(() => (words ? asIs(words) : say("web.social.alerts.load_failed")));
        return;
      }
      const list = resp.payload.respNotifications.notifications;
      setNotifications(list);
      onAcknowledged();
      try {
        if (list.some((n) => n.type === NotificationType.NotificationFriendRequest)) {
          const fr: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
            e.payload = { $case: "reqFriendshipsList", reqFriendshipsList: {} };
          });
          if (!cancelled && fr.payload?.$case === "respFriendships") {
            setAwaiting(new Set(fr.payload.respFriendships.friendships
              .filter((f) => !f.sent && f.status === FriendShipStatus.Pending)
              .map((f) => f.originProfile?.domain ?? "")));
          }
        }
        await useWS.request((e: Partial<ReqEnvelope>) => {
          e.payload = { $case: "reqMarkNotificationsAcknowledged", reqMarkNotificationsAcknowledged: {} };
        });
      } catch {
        // The list shows; the next visit marks it read.
      }
    })();
    return () => { cancelled = true; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [loadTry]);

  const accept = async (domain: string) => {
    if (refuseOnDemo()) return; // a demo device (net/demo.ts)
    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        e.payload = { $case: "reqChangeFriendStatus", reqChangeFriendStatus: { domain, status: FriendShipStatus.Accepted } };
      });
      if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
        setAwaiting((s) => { const n = new Set(s); n.delete(domain); return n; });
        setAcceptedHere((s) => new Set(s).add(domain));
      }
    } catch {
      // No answer: the button stays, to press again.
    }
  };

  const onClickNotification = (n: PbNotification) => {
    if (n.type === NotificationType.NotificationUpdate) {
      onOpenSettings?.();
      return;
    }
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
      {notifications == null && loadError ? (
        <div className="np-empty np-load-error" role="alert">
          <p>{loadError(t)}</p>
          <button type="button" className="np-retry" onClick={() => setLoadTry((n) => n + 1)}>{t("common.try_again")}</button>
        </div>
      ) : notifications == null ? (
        <div className="np-empty"><Spinner label={t("common.loading")} /></div>
      ) : notifications.length === 0 ? (
        <div className="np-empty">{t("web.social.alerts.nothing")}</div>
      ) : (
        <ul className="np-list">
          {notifications.map(n => {
            const { avatar, thumb } = images.get(n.uuid) ?? { avatar: null, thumb: null };
            const isError = n.type === NotificationType.NotificationError;
            const isUpdate = n.type === NotificationType.NotificationUpdate;
            const actorKey = describe(n);
            const actor = n.actorName || n.actorDomain;
            // Where the row goes: an error row (with details) opens and
            // closes its list of errors, an update Settings, the rest the
            // post or the friend requests. A row with nowhere to go is
            // text, not a button.
            const opens = isUpdate || (isError ? !!n.details
              : n.type === NotificationType.NotificationFriendRequest || n.type === NotificationType.NotificationFriendAccepted || !!n.pubUuid);
            const content = (
              <>
                {isUpdate ? (
                  <div className="np-avatar np-avatar-placeholder np-update-icon" aria-hidden="true">
                    <svg width="20" height="20" viewBox="0 0 24 24">
                      <circle cx="12" cy="12" r="9" fill="none" stroke="currentColor" strokeWidth="2" />
                      <path d="M12 7v8M8.5 11.5 12 15l3.5-3.5" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
                    </svg>
                  </div>
                ) : isError ? (
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
                  {/* The device's title and details (an update, an error) as
                      they come: it writes them in the language asked. */}
                  {isUpdate ? (
                    <><strong>{n.title}</strong>{n.details ? <> - {n.details}</> : null}</>
                  ) : isError ? (
                    <>
                      {/* Issue #64: one line in the list; the whole list of
                          errors on hover (or pinned by a click). */}
                      <strong>{n.title}</strong>
                      {n.occurrences > 1 && <span className="np-more"> {t("web.social.alerts.more", { more: n.occurrences - 1 })}</span>}
                    </>
                  ) : actorKey ? (
                    <Trans k={actorKey} args={{ name: actor }} parts={{ b: (s) => <strong>{s}</strong> }} />
                  ) : (
                    <><strong>{actor}</strong> </>
                  )}
                  <span className="np-item-when"> · {formatWhen(t, n.dt)}</span>
                </span>
                {thumb && <img src={thumb} className="np-thumb" alt="" />}
              </>
            );
            return (
              <li
                key={n.uuid}
                className={`np-item${n.acknowledged ? "" : " np-unacknowledged"}${isError ? " np-error" : ""}${pinnedDetails[n.uuid] ? " np-pinned" : ""}`}
              >
                {opens ? (
                  <button
                    type="button"
                    className="np-open"
                    onClick={() => onClickNotification(n)}
                    aria-expanded={isError ? !!pinnedDetails[n.uuid] : undefined}
                  >
                    {content}
                  </button>
                ) : (
                  <div className="np-open">{content}</div>
                )}
                {n.type === NotificationType.NotificationFriendRequest && awaiting.has(n.actorDomain) && (
                  <button className="np-accept" onClick={() => void accept(n.actorDomain)}>{t("common.accept")}</button>
                )}
                {n.type === NotificationType.NotificationFriendRequest && acceptedHere.has(n.actorDomain) && (
                  <span className="np-accepted">{t("web.social.alerts.friends")}</span>
                )}
                {isError && n.details && (
                  <pre className="np-popover">{n.details}</pre>
                )}
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
