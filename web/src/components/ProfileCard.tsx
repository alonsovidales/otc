// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useMemo, useState } from "react";
import { useWS } from "../net/useWS";
import type { ReqEnvelope, RespEnvelope, Profile as PbProfile } from "../proto/messages";
import DevicePhotoPicker from "./DevicePhotoPicker";
import PhotoCropDialog from "./PhotoCropDialog";
import "./ProfileCard.css";
import { useT } from "../i18n";
import { asIs, say, type UiText } from "./uiText";
import { refuseOnDemo } from "../net/demo";

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

// A thrown error's own words (an empty one shows nothing, as before).
const errorText = (e: unknown): UiText | null => {
  const m = (e as { message?: unknown } | null | undefined)?.message;
  const message = m != null ? String(m) : String(e);
  return message ? asIs(message) : null;
};

export default function ProfileCard({ authenticated }: Props) {
  const t = useT();
  // Loaded data
  const [name, setName]   = useState("");
  const [text, setText]   = useState("");
  const [imgBytes, setImgBytes] = useState<Uint8Array | null>(null);
  const [imgUrl, setImgUrl] = useState<string | null>(null);
  // A picked photo waiting in the crop dialog.
  const [cropping, setCropping] = useState<File | null>(null);
  const [pickingFromDevice, setPickingFromDevice] = useState(false);

  // UI state
  const [loading, setLoading] = useState(true);
  const [saving, setSaving]   = useState(false);
  const [error, setError]     = useState<UiText | null>(null);
  const [success, setSuccess] = useState<UiText | null>(null);

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
            const message = resp.payload.respAck.errorMsg;
            setError(() => (message ? asIs(message) : say("web.social.profile.load_failed")));
          } else {
            setError(() => say("web.social.profile.unexpected"));
          }
        }
      } catch (e: any) {
        if (!cancelled) setError(() => errorText(e));
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

  const onPickImage = (file?: File | null) => {

    if (refuseOnDemo()) return; // a demo device (net/demo.ts)
    setSuccess(null);
    setError(null);
    if (file) setCropping(file);
  };

  const onCropped = (jpeg: Uint8Array, url: string) => {
    setCropping(null);
    setImgBytes(jpeg);
    if (imgUrl) URL.revokeObjectURL(imgUrl);
    setImgUrl(url);
  };

  const canSave = useMemo(() => {
    if (!authenticated) return false;
    if (saving) return false;
    // Minimal validation: name can be empty if you want; enforce what you need:
    return true;
  }, [authenticated, saving]);

  const saveProfile = async () => {

    if (refuseOnDemo()) return; // a demo device (net/demo.ts)
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
        setSuccess(() => say("web.social.profile.saved"));
        // The top bar's account button (AccountMenu.tsx) shows the new
        // name or picture at once.
        window.dispatchEvent(new Event("otc-profile-changed"));
      } else if (resp.payload?.$case === "respAck") {
        const message = resp.payload.respAck.errorMsg;
        setError(() => (message ? asIs(message) : say("web.social.profile.save_failed")));
      } else {
        setError(() => say("web.social.profile.unexpected"));
      }
    } catch (e: any) {
      setError(() => errorText(e));
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
            <img className="pc-avatar" src={imgUrl} alt={name || t("web.social.profile.avatar")} />
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
              <h3 className="pc-name">{name || t("web.social.profile.unnamed")}</h3>
              <p className="pc-text">{text || t("web.social.profile.no_bio")}</p>
              {error && <div className="pc-status error">{error(t)}</div>}
            </>
          )}
        </div>
      </div>
    );
  }

  // Editable form when authenticated
  return (
    <div className="pc-card">
      <div className="pc-left">
        {imgUrl ? (
          <img className="pc-avatar pc-round" src={imgUrl} alt={name || t("web.social.profile.avatar")} />
        ) : (
          <div className="pc-avatar pc-placeholder">👤</div>
        )}
        {/* Issue #178: from this computer, or from the photos on the device. */}
        <label className="pc-btn">
          {t("web.social.profile.from_computer")}
          <input
            type="file"
            accept="image/*"
            hidden
            onChange={(e) => { onPickImage(e.target.files?.[0] ?? null); e.target.value = ""; }}
          />
        </label>
        <button className="pc-btn pc-link" onClick={() => setPickingFromDevice(true)}>{t("web.social.profile.from_device")}</button>
        {pickingFromDevice && (
          <DevicePhotoPicker
            onCancel={() => setPickingFromDevice(false)}
            onPicked={(file) => { setPickingFromDevice(false); onPickImage(file); }}
          />
        )}
        {cropping && <PhotoCropDialog file={cropping} onCancel={() => setCropping(null)} onDone={onCropped} />}
      </div>

      <div className="pc-right">
        <div className="pc-row">
          <input
            className="pc-input"
            value={name}
            onChange={(e) => { setName(e.target.value); setSuccess(null); setError(null); }}
            placeholder={t("web.social.profile.name")}
          />
        </div>
        <div className="pc-row">
          <textarea
            className="pc-textarea"
            value={text}
            onChange={(e) => { setText(e.target.value); setSuccess(null); setError(null); }}
            placeholder={t("web.social.profile.bio")}
            rows={4}
          />
        </div>

        <div className="pc-actions">
          <button className="pc-save" disabled={!canSave} onClick={() => void saveProfile()}>
            {saving ? t("web.social.profile.saving") : t("web.social.profile.save")}
          </button>
        </div>

        {error && <div className="pc-status error">{error(t)}</div>}
        {success && <div className="pc-status success">{success(t)}</div>}
      </div>
    </div>
  );
}

