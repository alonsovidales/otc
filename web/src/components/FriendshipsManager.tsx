// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useMemo, useState } from "react";

// Import your generated types (adjust paths/names if needed)
import type {
  Friendships as MsgFriendships,
  Friendship as MsgFriendship,
} from "../proto/messages";
import "./FriendshipsManager.css";
import { FriendShipStatus } from "../proto/messages";
import { useWS } from "../net/useWS";
import { friendDomain, FRIEND_TLD } from "../net/friendDomain";
import { useT, type TFunction } from "../i18n";
import { asIs, say, type UiText } from "./uiText";

function bytesToObjectURL(bytes?: Uint8Array, mime = "image/png"): string | undefined {
  if (!bytes || bytes.length === 0) return undefined;
  try {
    const blob = new Blob([bytes], { type: mime });
    return URL.createObjectURL(blob);
  } catch {
    return undefined;
  }
}

// The pill beside a friend; "(sent)" when the request was ours.
const statusLabel = (t: TFunction, s: FriendShipStatus, sent: boolean): string => {
  switch (s) {
    case FriendShipStatus.Accepted: return sent ? t("web.social.friends.status_accepted_sent") : t("web.social.friends.status_accepted");
    case FriendShipStatus.Blocked:  return sent ? t("web.social.friends.status_blocked_sent") : t("web.social.friends.status_blocked");
    default:                        return sent ? t("web.social.friends.status_pending_sent") : t("web.social.friends.status_pending");
  }
};

/**
 * Buttons allowed when *we are the receiver* (friendship.sent === false)
 * - Pending -> Accept / Block / Delete
 * - Accepted -> Set Pending / Block
 * - Blocked -> Accept / Set Pending
 * When *we sent it* the only thing to do is withdraw it while it is still
 * pending (issue #25) - the other side decides everything else.
 * Issue #174: an accepted or blocked friend, whoever sent the request, can
 * also be removed (onRemove opens the confirm panel with its options).
 */
function ActionButtons({
  f,
  onChange,
  onDelete,
  onRemove,
  disabled,
}: {
  f: MsgFriendship;
  onChange: (next: FriendShipStatus) => void;
  onDelete: () => void;
  onRemove: () => void;
  disabled?: boolean;
}) {
  const t = useT();
  const btn = (label: string, next: FriendShipStatus) => (
    <button
      className="fr-btn"
      onClick={() => onChange(next)}
      disabled={disabled}
    >
      {label}
    </button>
  );
  // Issue #25: same request to the device either way; the label says
  // which side of it the owner is on.
  const del = (
    <button className="fr-btn danger" onClick={onDelete} disabled={disabled}>
      {f.sent ? t("web.social.friends.cancel_request") : t("common.delete")}
    </button>
  );

  const remove = (
    <button className="fr-btn danger" onClick={onRemove} disabled={disabled}>
      {t("web.social.friends.remove")}
    </button>
  );

  if (f.sent) {
    return f.status === FriendShipStatus.Pending
      ? <div className="fr-actions">{del}</div>
      : <div className="fr-actions">{remove}</div>;
  }

  switch (f.status) {
    case FriendShipStatus.Pending:
      return (
        <div className="fr-actions">
          {btn(t("common.accept"), FriendShipStatus.Accepted)}
          {btn(t("web.social.friends.block"), FriendShipStatus.Blocked)}
          {del}
        </div>
      );
    case FriendShipStatus.Accepted:
      return (
        <div className="fr-actions">
          {btn(t("web.social.friends.set_pending"), FriendShipStatus.Pending)}
          {btn(t("web.social.friends.block"), FriendShipStatus.Blocked)}
          {remove}
        </div>
      );
    case FriendShipStatus.Blocked:
      return (
        <div className="fr-actions">
          {btn(t("common.accept"), FriendShipStatus.Accepted)}
          {btn(t("web.social.friends.set_pending"), FriendShipStatus.Pending)}
          {remove}
        </div>
      );
    default:
      return null;
  }
}

export default function FriendshipsManager() {
  const t = useT();
  // Friendship request (by domain)
  const [targetDomain, setTargetDomain] = useState("");
  const [sendingReq, setSendingReq] = useState(false);

  // Friendships list
  const [friends, setFriends] = useState<MsgFriendships | null>(null);
  const [loadingFriends, setLoadingFriends] = useState(false);

  // Issue #25: domain of the request whose delete is awaiting a second
  // click - the row swaps its buttons for a confirm/keep pair meanwhile.
  const [confirmingDelete, setConfirmingDelete] = useState<string | null>(null);

  // Issue #174: domain of the friend whose removal is being confirmed, and
  // the two options of that panel (both off unless the owner ticks them).
  const [confirmingRemove, setConfirmingRemove] = useState<string | null>(null);
  const [deleteTheirData, setDeleteTheirData] = useState(false);
  const [askThemToDeleteMine, setAskThemToDeleteMine] = useState(false);
  const openRemove = (domain: string) => {
    setDeleteTheirData(false);
    setAskThemToDeleteMine(false);
    setConfirmingRemove(domain);
  };

  // Toast/message: ours, or the device's own words.
  const [message, setMessage] = useState<UiText | null>(null);
  const showMsg = (m: UiText | null) => {
    setMessage(() => m);
    setTimeout(() => setMessage(null), 2500);
  };
  // The device's words when it gave some, else ours.
  const orSay = (words: string | undefined, fallback: Parameters<typeof say>[0]) => (words ? asIs(words) : say(fallback));

  // Initial load
  useEffect(() => {
    void reloadFriendships();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const reloadFriendships = async () => {
    setLoadingFriends(true);
    try {
      const resp = await useWS.request((e) => {
        (e as any).payload = { $case: "reqFriendshipsList", reqFriendshipsList: {} };
      });
      if (resp.payload?.$case === "respFriendships") {
        setFriends(resp.payload.respFriendships);
      }
    } catch (err) {
      console.error("Friendships load error:", err);
    } finally {
      setLoadingFriends(false);
    }
  };

  const sendFriendRequest = async () => {
    if (!targetDomain.trim()) return;
    const domain = friendDomain(targetDomain);
    if (!domain) {
      showMsg(say("web.social.friends.invalid_name"));
      return;
    }
    setSendingReq(true);
    try {
      const resp = await useWS.request((e) => {
        (e as any).payload = {
          $case: "reqFriendshipRequest",
          reqFriendshipRequest: { domain },
        };
      });
      if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
        // Issue #140: "accepted" - they already had this device as a friend
        // and re-linked it, nothing left to accept.
        showMsg(say(resp.payload.respAck.code === "accepted" ? "web.social.friends.friends_again" : "web.social.friends.request_sent"));
        setTargetDomain("");
        await reloadFriendships();
      } else {
        showMsg(resp.payload?.$case === "respAck"
          ? orSay(resp.payload.respAck.errorMsg, "web.social.friends.request_failed")
          : resp.error ? orSay(resp.errorMessage, "web.social.friends.request_failed") : say("web.social.friends.unexpected"));
      }
    } catch (err) {
      console.error("Friend request error:", err);
      showMsg(say("web.social.friends.send_error"));
    } finally {
      setSendingReq(false);
    }
  };

  const changeStatus = async (f: MsgFriendship, status: FriendShipStatus) => {
    // Not the whole Friendship: from an older device it carries the friend-auth secret.
    console.log('Change friendship', f.originProfile?.domain, status);
    try {
      const resp = await useWS.request((e) => {
        (e as any).payload = {
          $case: "reqChangeFriendStatus",
          reqChangeFriendStatus: {
            domain: f.originProfile?.domain,
            status,
          },
        };
      });
      if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
        await reloadFriendships();
        showMsg(say("web.social.friends.status_updated"));
      } else {
        showMsg(resp.payload?.$case === "respAck"
          ? orSay(resp.payload.respAck.errorMsg, "web.social.friends.update_failed")
          : say("web.social.friends.unexpected"));
      }
    } catch (err) {
      console.error("Change status error:", err);
      showMsg(say("web.social.friends.update_error"));
    }
  };

  // Issue #25: removes the request here and, best effort, on the other
  // device - the device handles that side, this just asks.
  const deleteFriendship = async (f: MsgFriendship) => {
    setConfirmingDelete(null);
    try {
      const resp = await useWS.request((e) => {
        (e as any).payload = {
          $case: "reqDeleteFriendship",
          reqDeleteFriendship: { domain: f.originProfile?.domain },
        };
      });
      if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
        await reloadFriendships();
        showMsg(say(f.sent ? "web.social.friends.request_cancelled" : "web.social.friends.request_deleted"));
      } else {
        // The device's words as they are; none shows nothing (as before).
        showMsg(resp.error ? (resp.errorMessage ? asIs(resp.errorMessage) : null) : say("web.social.friends.delete_failed"));
      }
    } catch (err) {
      console.error("Delete friendship error:", err);
      showMsg(say("web.social.friends.delete_error"));
    }
  };

  // Issue #174: removes an accepted/blocked friend. deleteTheirData drops
  // everything of theirs here; askThemToDeleteMine asks their device to drop
  // what this one shared, and the friendship stays "leaving" until it does.
  // Both false (the "Remove now" of a leaving friend) just drops the relation.
  const removeFriend = async (f: MsgFriendship, theirData: boolean, askThem: boolean) => {
    setConfirmingRemove(null);
    try {
      const resp = await useWS.request((e) => {
        (e as any).payload = {
          $case: "reqDeleteFriendship",
          reqDeleteFriendship: {
            domain: f.originProfile?.domain,
            deleteTheirData: theirData,
            askThemToDeleteMine: askThem,
          },
        };
      });
      if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
        await reloadFriendships();
        showMsg(say(askThem ? "web.social.friends.asked_delete" : "web.social.friends.removed"));
      } else {
        showMsg(resp.payload?.$case === "respAck"
          ? orSay(resp.payload.respAck.errorMsg, "web.social.friends.remove_failed")
          : resp.error ? orSay(resp.errorMessage, "web.social.friends.remove_failed") : say("web.social.friends.unexpected"));
      }
    } catch (err) {
      console.error("Remove friend error:", err);
      showMsg(say("web.social.friends.remove_error"));
    }
  };

  const isWaiting = (f: MsgFriendship) => !f.sent && f.status === FriendShipStatus.Pending;
  const waiting = friends?.friendships.filter(isWaiting) ?? [];
  const others = friends?.friendships.filter((f) => !isWaiting(f)) ?? [];
  // One avatar URL per friend per loaded list, freed when the list is
  // reloaded or the panel closes - not a new, never-freed one for every
  // row on every render.
  const avatars = useMemo(
    () => new Map((friends?.friendships ?? []).map(f => [f, bytesToObjectURL(f.originProfile?.image)])),
    [friends]
  );
  useEffect(() => () => avatars.forEach(u => { if (u) URL.revokeObjectURL(u); }), [avatars]);

  const renderRow = (f: MsgFriendship) => {
    const avatar = avatars.get(f);
    const name = f.originProfile?.name || t("web.social.friends.no_name");
    // The row's key and what the confirmations remember (not the shown
    // text, which follows the language).
    const domain = f.originProfile?.domain || "";
    const shownDomain = domain || t("web.social.friends.no_domain");
    return (
      <li key={domain} className="friend-item">
        <div className="friend-left">
          <div className="avatar">
            {avatar ? <img src={avatar} alt="" /> : <div className="ph" />}
          </div>
          <div className="meta">
            <div className="name">{name}</div>
            <div className="domain">{shownDomain}</div>
          </div>
        </div>
        <div className="friend-right">
          {f.leaving ? (
            <span className="status pill s-leaving">{t("web.social.friends.leaving")}</span>
          ) : (
            // The space after a request not ours, as it always had.
            <span className={`status pill s-${f.status}`}>
              {statusLabel(t, f.status, f.sent)}{f.sent ? "" : " "}
            </span>
          )}
          {f.leaving ? (
            // Issue #174: waiting for their device to delete what this one
            // shared; nothing syncs meanwhile. "Remove now" stops waiting.
            <div className="fr-actions fr-confirm">
              <span>{t("web.social.friends.waiting_delete")}</span>
              <button className="fr-btn danger" onClick={() => {
                if (window.confirm(t("web.social.friends.confirm_remove_now", { name }))) void removeFriend(f, false, false);
              }}>{t("web.social.friends.remove_now")}</button>
            </div>
          ) : confirmingRemove === domain ? (
            <div className="fr-actions fr-confirm fr-remove">
              <label className="fr-check">
                <input
                  type="checkbox"
                  checked={deleteTheirData}
                  onChange={(e) => setDeleteTheirData(e.target.checked)}
                />
                <span>
                  {t("web.social.friends.delete_theirs", { name })}
                  <small className="fr-hint">{t("web.social.friends.delete_theirs_hint")}</small>
                </span>
              </label>
              <label className="fr-check">
                <input
                  type="checkbox"
                  checked={askThemToDeleteMine}
                  onChange={(e) => setAskThemToDeleteMine(e.target.checked)}
                />
                <span>
                  {t("web.social.friends.ask_delete_mine", { name })}
                  <small className="fr-hint">{t("web.social.friends.ask_delete_mine_hint")}</small>
                </span>
              </label>
              <div className="fr-actions">
                <button
                  className="fr-btn danger"
                  onClick={() => removeFriend(f, deleteTheirData, askThemToDeleteMine)}
                >
                  {t("web.social.friends.remove")}
                </button>
                <button className="fr-btn" onClick={() => setConfirmingRemove(null)}>{t("web.social.friends.keep")}</button>
              </div>
            </div>
          ) : confirmingDelete === domain ? (
            <div className="fr-actions fr-confirm">
              <span>{f.sent ? t("web.social.friends.confirm_cancel") : t("web.social.friends.confirm_delete")}</span>
              <button className="fr-btn danger" onClick={() => deleteFriendship(f)}>{t("web.social.friends.yes")}</button>
              <button className="fr-btn" onClick={() => setConfirmingDelete(null)}>{t("web.social.friends.keep")}</button>
            </div>
          ) : (
            <ActionButtons
              f={f}
              onChange={(next) => changeStatus(f, next)}
              onDelete={() => setConfirmingDelete(domain)}
              onRemove={() => openRemove(domain)}
            />
          )}
        </div>
      </li>
    );
  };

  return (
    <div className="friends-wrap">
      {message && <div className="toast">{message(t)}</div>}

      {/* Requests waiting for an answer come first: a clicked
          friend-request alert lands here to answer it. */}
      {waiting.length > 0 && (
        <section className="card">
          <h2>{t("web.social.friends.waiting_title")}</h2>
          <ul className="friends-list">{waiting.map(renderRow)}</ul>
        </section>
      )}

      {/* Issue #84: "Your Profile" editing used to live here (this was the
          only place an authenticated owner could actually reach it, since
          ProfileCard's own editable form never got rendered with
          authenticated=true anywhere) - moved to the top of Settings
          instead, so this screen is just friend management now. */}

      {/* New friendship request */}
      <section className="card">
        <h2>{t("web.social.friends.add_title")}</h2>
        <div className="add-friend">
          {/* Issue #142: the name only; the suffix is fixed. */}
          <label className="friend-domain-field">
            <input
              type="text"
              placeholder={t("web.social.friends.name_placeholder")}
              aria-label={t("web.social.friends.name_label", { suffix: FRIEND_TLD })}
              autoCapitalize="none"
              autoCorrect="off"
              spellCheck={false}
              value={targetDomain}
              onChange={(e) => setTargetDomain(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") void sendFriendRequest();
              }}
            />
            <span className="friend-domain-suffix">.{FRIEND_TLD}</span>
          </label>
          <button onClick={sendFriendRequest} disabled={sendingReq || !targetDomain.trim()}>
            {sendingReq ? t("web.social.friends.sending") : t("web.social.friends.send")}
          </button>
        </div>
      </section>

      {/* Friendships list */}
      <section className="card">
        <div className="list-head">
          <h2>{t("web.social.friends.title")}</h2>
          <button className="refresh" onClick={reloadFriendships} disabled={loadingFriends}>
            {loadingFriends ? t("common.loading") : t("common.refresh")}
          </button>
        </div>

        {others.length === 0 ? (
          <div className="empty">{waiting.length ? t("web.social.friends.no_others") : t("web.social.friends.none")}</div>
        ) : (
          <ul className="friends-list">{others.map(renderRow)}</ul>
        )}
      </section>
    </div>
  );
}

