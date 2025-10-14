import { useEffect, useMemo, useState } from "react";

// Import your generated types (adjust paths/names if needed)
import type {
  Profile as MsgProfile,
  Friendships as MsgFriendships,
  Friendship as MsgFriendship,
} from "../proto/messages";
import "./FriendshipsManager.css";
import { FriendShipStatus } from "../proto/messages";
import { useWS } from "../net/useWS";

const emptyProfile: MsgProfile = {
  name: "",
  text: "",
  domain: "",
  image: undefined,
};

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
  // Profile
  const [profile, setProfile] = useState<MsgProfile>(emptyProfile);
  const [savingProfile, setSavingProfile] = useState(false);
  const imgURL = useMemo(() => bytesToObjectURL(profile.image), [profile.image]);

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
    (async () => {
      try {
        // Load profile
        const resp = await useWS.request((e) => {
          (e as any).payload = { $case: "reqGetProfile", reqGetProfile: {} };
        });
        if (resp.payload?.$case === "respProfile") {
          setProfile(resp.payload.respProfile);
        }

        // Load friendships
        await reloadFriendships();
      } catch (err) {
        console.error("Initial load error:", err);
      }
    })();
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

  const handleProfileChange = (field: keyof MsgProfile, value: string | Uint8Array | undefined) => {
    setProfile((p) => ({ ...p, [field]: value as any }));
  };

  const handleImageFile = async (file: File) => {
    const arr = new Uint8Array(await file.arrayBuffer());
    handleProfileChange("image", arr);
  };

  const saveProfile = async () => {
    setSavingProfile(true);
    try {
      // In this proto, req_set_profile is a Profile payload directly.
      const resp = await useWS.request((e) => {
        (e as any).payload = { $case: "reqSetProfile", reqSetProfile: profile };
      });
      if (resp.payload?.$case === "respAck") {
        if (resp.payload.respAck.ok) {
          showMsg("Profile updated ✅");
        } else {
          showMsg(resp.payload.respAck.errorMsg || "Profile update failed");
        }
      } else {
        showMsg("Unexpected response while saving profile");
      }
    } catch (err) {
      console.error("saveProfile error:", err);
      showMsg("Error saving profile");
    } finally {
      setSavingProfile(false);
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

      {/* Profile editor/view */}
      <section className="card profile-card">
        <h2>Your Profile</h2>
        <div className="profile-row">
          <div className="profile-photo">
            {imgURL ? (
              <img src={imgURL} alt="profile" />
            ) : (
              <div className="ph">No image</div>
            )}
            <label className={`upload`}>
              <input
                type="file"
                accept="image/*"
                onChange={(e) => {
                  const f = e.target.files?.[0];
                  if (f) void handleImageFile(f);
                  e.currentTarget.value = "";
                }}
              />
              Change photo
            </label>
          </div>

          <div className="profile-form">
            <label>
              Name
              <input
                type="text"
                value={profile.name}
                onChange={(e) => handleProfileChange("name", e.target.value)}
              />
            </label>
            <label>
              Bio
              <textarea
                rows={3}
                value={profile.text}
                onChange={(e) => handleProfileChange("text", e.target.value)}
              />
            </label>

            <div className="profile-actions">
              <button
                onClick={saveProfile}
                disabled={savingProfile}
              >
                {savingProfile ? "Saving…" : "Save Profile"}
              </button>
            </div>
          </div>
        </div>
      </section>

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

