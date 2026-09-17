// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useState } from "react";

// Import your generated types (adjust paths/names if needed)
import type {
  Friendships as MsgFriendships,
  Friendship as MsgFriendship,
} from "../proto/messages";
import "./FriendshipsManager.css";
import { FriendShipStatus } from "../proto/messages";
import { useWS } from "../net/useWS";

function bytesToObjectURL(bytes?: Uint8Array, mime = "image/png"): string | undefined {
  if (!bytes || bytes.length === 0) return undefined;
  try {
    const blob = new Blob([bytes], { type: mime });
    return URL.createObjectURL(blob);
  } catch {
    return undefined;
  }
}

const statusLabel = (s: FriendShipStatus): string => {
  switch (s) {
    case FriendShipStatus.Accepted: return "Accepted";
    case FriendShipStatus.Blocked:  return "Blocked";
    default:                        return "Pending";
  }
};

/**
 * Buttons allowed when *we are the receiver* (friendship.sent === false)
 * - Pending -> Accept / Block
 * - Accepted -> Set Pending / Block
 * - Blocked -> Accept / Set Pending
 */
function ActionButtons({
  f,
  onChange,
  disabled,
}: {
  f: MsgFriendship;
  onChange: (next: FriendShipStatus) => void;
  disabled?: boolean;
}) {
  if (f.sent) return null; // we sent it; no actions until other side responds

  const btn = (label: string, next: FriendShipStatus) => (
    <button
      className="fr-btn"
      onClick={() => onChange(next)}
      disabled={disabled}
    >
      {label}
    </button>
  );

  switch (f.status) {
    case FriendShipStatus.Pending:
      return (
        <div className="fr-actions">
          {btn("Accept", FriendShipStatus.Accepted)}
          {btn("Block", FriendShipStatus.Blocked)}
        </div>
      );
    case FriendShipStatus.Accepted:
      return (
        <div className="fr-actions">
          {btn("Set Pending", FriendShipStatus.Pending)}
          {btn("Block", FriendShipStatus.Blocked)}
        </div>
      );
    case FriendShipStatus.Blocked:
      return (
        <div className="fr-actions">
          {btn("Accept", FriendShipStatus.Accepted)}
          {btn("Set Pending", FriendShipStatus.Pending)}
        </div>
      );
    default:
      return null;
  }
}

export default function FriendshipsManager() {
  // Friendship request (by domain)
  const [targetDomain, setTargetDomain] = useState("");
  const [sendingReq, setSendingReq] = useState(false);

  // Friendships list
  const [friends, setFriends] = useState<MsgFriendships | null>(null);
  const [loadingFriends, setLoadingFriends] = useState(false);

  // Toast/message
  const [message, setMessage] = useState<string | null>(null);
  const showMsg = (m: string) => {
    setMessage(m);
    setTimeout(() => setMessage(null), 2500);
  };

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
    const domain = targetDomain.trim();
    if (!domain) return;
    setSendingReq(true);
    try {
      const resp = await useWS.request((e) => {
        (e as any).payload = {
          $case: "reqFriendshipRequest",
          reqFriendshipRequest: { domain },
        };
      });
      if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
        showMsg("Friend request sent ✅");
        setTargetDomain("");
        await reloadFriendships();
      } else {
        showMsg(resp.payload?.$case === "respAck"
          ? resp.payload.respAck.errorMsg || "Request failed"
          : "Unexpected response");
      }
    } catch (err) {
      console.error("Friend request error:", err);
      showMsg("Error sending request");
    } finally {
      setSendingReq(false);
    }
  };

  const changeStatus = async (f: MsgFriendship, status: FriendShipStatus) => {
    console.log('Change firendship', f);
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
        showMsg("Status updated ✅");
      } else {
        showMsg(resp.payload?.$case === "respAck"
          ? resp.payload.respAck.errorMsg || "Update failed"
          : "Unexpected response");
      }
    } catch (err) {
      console.error("Change status error:", err);
      showMsg("Error updating status");
    }
  };

  return (
    <div className="friends-wrap">
      {message && <div className="toast">{message}</div>}

      {/* Issue #84: "Your Profile" editing used to live here (this was the
          only place an authenticated owner could actually reach it, since
          ProfileCard's own editable form never got rendered with
          authenticated=true anywhere) - moved to the top of Settings
          instead, so this screen is just friend management now. */}

      {/* New friendship request */}
      <section className="card">
        <h2>Add a friend</h2>
        <div className="add-friend">
          <input
            type="text"
            placeholder="friend-domain.example"
            value={targetDomain}
            onChange={(e) => setTargetDomain(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") void sendFriendRequest();
            }}
          />
          <button onClick={sendFriendRequest} disabled={sendingReq || !targetDomain.trim()}>
            {sendingReq ? "Sending…" : "Send request"}
          </button>
        </div>
      </section>

      {/* Friendships list */}
      <section className="card">
        <div className="list-head">
          <h2>Friend requests</h2>
          <button className="refresh" onClick={reloadFriendships} disabled={loadingFriends}>
            {loadingFriends ? "Loading…" : "Refresh"}
          </button>
        </div>

        {!friends || friends.friendships.length === 0 ? (
          <div className="empty">No friendships yet.</div>
        ) : (
          <ul className="friends-list">
            {friends.friendships.map((f) => {
              const avatar = bytesToObjectURL(f.originProfile?.image);
              const name = f.originProfile?.name || "(no name)";
              const domain = f.originProfile?.domain || "(no domain)";
              return (
                <li key={domain} className="friend-item">
                  <div className="friend-left">
                    <div className="avatar">
                      {avatar ? <img src={avatar} alt="" /> : <div className="ph" />}
                    </div>
                    <div className="meta">
                      <div className="name">{name}</div>
                      <div className="domain">{domain}</div>
                    </div>
                  </div>
                  <div className="friend-right">
                    <span className={`status pill s-${f.status}`}>
                      {statusLabel(f.status)} {f.sent ? "(sent)" : ""}
                    </span>
                    <ActionButtons
                      f={f}
                      onChange={(next) => changeStatus(f, next)}
                    />
                  </div>
                </li>
              );
            })}
          </ul>
        )}
      </section>
    </div>
  );
}

