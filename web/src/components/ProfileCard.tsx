import { useEffect, useMemo, useState } from "react";
import { useWS } from "../net/useWS";
import type { ReqEnvelope, RespEnvelope, Profile as PbProfile } from "../proto/messages";
import "./ProfileCard.css";

type Props = {
  authenticated: boolean;
  wsUrl?: string;           // defaults to VITE_WS_URL
  requireAuth?: boolean;    // default true (for private profiles)
};

function bytesToObjectURL(bytes?: Uint8Array, mime = "image/jpeg") {
  if (!bytes || bytes.length === 0) return null;
  const blob = new Blob([bytes], { type: mime });
  return URL.createObjectURL(blob);
}

async function fileToUint8Array(file: File): Promise<Uint8Array> {
  const buf = await file.arrayBuffer();
  return new Uint8Array(buf);
}

export default function ProfileCard({ authenticated }: Props) {
  // Loaded data
  const [name, setName]   = useState("");
  const [text, setText]   = useState("");
  const [imgBytes, setImgBytes] = useState<Uint8Array | null>(null);
  const [imgUrl, setImgUrl] = useState<string | null>(null);

  // UI state
  const [loading, setLoading] = useState(true);
  const [saving, setSaving]   = useState(false);
  const [following, setFollowing] = useState(false);
  const [error, setError]     = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);

  // Load profile on mount
  useEffect(() => {
    let cancelled = false;
    (async () => {
      setLoading(true);
      setError(null);
      setSuccess(null);
      try {
        const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
          (e as any).payload = { $case: "reqGetProfile", reqGetProfile: {} };
        }); // toggle if your profile is public

        if (!cancelled) {
          if (resp.payload?.$case === "respProfile") {
            const p: PbProfile = resp.payload.respProfile;
            setName(p.name ?? "");
            setText(p.text ?? "");

            const bytes = (p.image as unknown as Uint8Array) || undefined;
            setImgBytes(bytes ?? null);

            if (imgUrl) URL.revokeObjectURL(imgUrl);
            const url = bytesToObjectURL(bytes, "image/*");
            setImgUrl(url);
          } else if (resp.payload?.$case === "respAck") {
            setError(resp.payload.respAck.errorMsg || "Failed to load profile.");
          } else {
            setError("Unexpected response.");
          }
        }
      } catch (e: any) {
        if (!cancelled) setError(e?.message ?? String(e));
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
      if (imgUrl) URL.revokeObjectURL(imgUrl);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const onPickImage = async (file?: File | null) => {
    setSuccess(null);
    setError(null);
    if (!file) return;
    try {
      const bytes = await fileToUint8Array(file);
      setImgBytes(bytes);
      if (imgUrl) URL.revokeObjectURL(imgUrl);
      const url = bytesToObjectURL(bytes, file.type || "image/*");
      setImgUrl(url);
    } catch (e: any) {
      setError(e?.message ?? String(e));
    }
  };

  const canFollow = useMemo(() => {
    if (following) return false;
    return true;
  }, [authenticated, saving]);

  const canSave = useMemo(() => {
    if (!authenticated) return false;
    if (saving) return false;
    // Minimal validation: name can be empty if you want; enforce what you need:
    return true;
  }, [authenticated, saving]);

  const followUser = async () => {
    setFollowing(true);
    alert('Not implemented...');
  };

  const saveProfile = async () => {
    if (!canSave) return;
    setSaving(true);
    setError(null);
    setSuccess(null);
    try {
      // In your proto, req_set_profile takes a Profile message directly.
      const payloadProfile = {
        name,
        text,
        image: imgBytes ?? undefined, // bytes | undefined for optional
      };

      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = {
          $case: "reqSetProfile",
          reqSetProfile: payloadProfile,
        };
      });

      if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
        setSuccess("Profile saved.");
      } else if (resp.payload?.$case === "respAck") {
        setError(resp.payload.respAck.errorMsg || "Save failed.");
      } else {
        setError("Unexpected response.");
      }
    } catch (e: any) {
      setError(e?.message ?? String(e));
    } finally {
      setSaving(false);
    }
  };

  // Read-only card when not authenticated
  if (!authenticated) {
    return (
      <div className="pc-card">
        <div className="pc-left">
          {loading ? (
            <div className="pc-skel" />
          ) : imgUrl ? (
            <img className="pc-avatar" src={imgUrl} alt={name || "avatar"} />
          ) : (
            <div className="pc-avatar pc-placeholder">👤</div>
          )}
        </div>
        <div className="pc-right">
          {loading ? (
            <>
              <div className="pc-line" />
              <div className="pc-line short" />
            </>
          ) : (
            <>
              <h3 className="pc-name">{name || "Unnamed user"}</h3>
              <p className="pc-text">{text || "No bio yet."}</p>
              {error && <div className="pc-status error">{error}</div>}
            </>
          )}
          <div className="pc-actions">
            <button className="pc-follow" disabled={!canFollow} onClick={() => void followUser()}>
              {following ? "Following…" : "Follow"}
            </button>
          </div>
        </div>
      </div>
    );
  }

  // Editable form when authenticated
  return (
    <div className="pc-card">
      <div className="pc-left">
        {imgUrl ? (
          <img className="pc-avatar" src={imgUrl} alt={name || "avatar"} />
        ) : (
          <div className="pc-avatar pc-placeholder">👤</div>
        )}
        <label className="pc-btn">
          Change photo
          <input
            type="file"
            accept="image/*"
            hidden
            onChange={(e) => onPickImage(e.target.files?.[0] ?? null)}
          />
        </label>
      </div>

      <div className="pc-right">
        <div className="pc-row">
          <input
            className="pc-input"
            value={name}
            onChange={(e) => { setName(e.target.value); setSuccess(null); setError(null); }}
            placeholder="Your name"
          />
        </div>
        <div className="pc-row">
          <textarea
            className="pc-textarea"
            value={text}
            onChange={(e) => { setText(e.target.value); setSuccess(null); setError(null); }}
            placeholder="Say something about you…"
            rows={4}
          />
        </div>

        <div className="pc-actions">
          <button className="pc-save" disabled={!canSave} onClick={() => void saveProfile()}>
            {saving ? "Saving…" : "Save profile"}
          </button>
        </div>

        {error && <div className="pc-status error">{error}</div>}
        {success && <div className="pc-status success">{success}</div>}
      </div>
    </div>
  );
}

