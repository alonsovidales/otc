// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useWS } from "../net/useWS";
import { isBridgeAnswer } from "../net/deviceStatus";
import type { ReqEnvelope, RespEnvelope, Profile as PbProfile } from "../proto/messages";
import DevicePhotoPicker from "./DevicePhotoPicker";
import PhotoCropDialog from "./PhotoCropDialog";
import "./ProfileCard.css";
import { useT } from "../i18n";
import { asIs, say, type UiText } from "./uiText";
import { refuseOnDemo } from "../net/demo";
import { useDeviceBack } from "./useDeviceBack";

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
  // Whether the profile came: until it has, nothing can be edited or saved
  // - Save sends the name, the text and the picture together, so a form
  // saved over a load that failed erased the bio and the picture friends
  // see, and typing before the load finished was overwritten by it.
  const [loaded, setLoaded] = useState(false);
  // Why it didn't come, with Try again.
  const [loadError, setLoadError] = useState<UiText | null>(null);
  const [saving, setSaving]   = useState(false);
  const [error, setError]     = useState<UiText | null>(null);
  const [success, setSuccess] = useState<UiText | null>(null);

  // The picture's blob URL, released when another takes its place and when
  // the card goes (the unmount used to see the first one only, null).
  const imgUrlRef = useRef<string | null>(null);
  const showImage = useCallback((url: string | null) => {
    if (imgUrlRef.current && imgUrlRef.current !== url) URL.revokeObjectURL(imgUrlRef.current);
    imgUrlRef.current = url;
    setImgUrl(url);
  }, []);
  useEffect(() => () => { if (imgUrlRef.current) URL.revokeObjectURL(imgUrlRef.current); }, []);

  const loadSeq = useRef(0);
  // A profile that didn't come is asked again once the device answers
  // again (its screen lies over the card, which stays as it was).
  const loadBack = useDeviceBack(() => void loadRef.current());
  const load = useCallback(async () => {
    const seq = ++loadSeq.current;
    setLoading(true);
    setLoadError(null);
    setError(null);
    setSuccess(null);
    let failure: UiText;
    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqGetProfile", reqGetProfile: {} };
      }); // toggle if your profile is public
      if (seq !== loadSeq.current) return;
      if (resp.payload?.$case === "respProfile") {
        const p: PbProfile = resp.payload.respProfile;
        setName(p.name ?? "");
        setText(p.text ?? "");
        const bytes = (p.image as unknown as Uint8Array) || undefined;
        setImgBytes(bytes ?? null);
        showImage(bytesToObjectURL(bytes, "image/jpeg"));
        setLoaded(true);
        setLoading(false);
        loadBack.ok();
        return;
      }
      const message = isBridgeAnswer(resp) ? "" : resp.payload?.$case === "respAck" ? resp.payload.respAck.errorMsg : resp.errorMessage;
      failure = message ? asIs(message) : say("web.social.profile.load_failed");
    } catch (e: unknown) {
      if (seq !== loadSeq.current) return;
      failure = errorText(e) ?? say("web.social.profile.load_failed");
    }
    setLoadError(() => failure);
    setLoading(false);
    loadBack.failed();
  }, [showImage, loadBack]);
  const loadRef = useRef(load);
  loadRef.current = load;

  // Load profile on mount; an answer that comes after the card went is
  // dropped.
  useEffect(() => {
    void load();
    const seq = loadSeq;
    return () => { seq.current++; };
  }, [load]);

  const onPickImage = (file?: File | null) => {

    if (refuseOnDemo()) return; // a demo device (net/demo.ts)
    setSuccess(null);
    setError(null);
    if (file) setCropping(file);
  };

  const onCropped = (jpeg: Uint8Array, url: string) => {
    setCropping(null);
    setImgBytes(jpeg);
    showImage(url);
  };

  const canSave = useMemo(() => {
    if (!authenticated) return false;
    if (saving || !loaded) return false;
    // Minimal validation: name can be empty if you want; enforce what you need:
    return true;
  }, [authenticated, saving, loaded]);

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
              {loadError && <div className="pc-status error">{loadError(t)}</div>}
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
        <label className={`pc-btn${loaded ? "" : " is-disabled"}`} aria-disabled={!loaded || undefined}>
          {t("web.social.profile.from_computer")}
          <input
            type="file"
            accept="image/*"
            hidden
            disabled={!loaded}
            onChange={(e) => { onPickImage(e.target.files?.[0] ?? null); e.target.value = ""; }}
          />
        </label>
        <button className="pc-btn pc-link" onClick={() => setPickingFromDevice(true)} disabled={!loaded}>{t("web.social.profile.from_device")}</button>
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
            disabled={!loaded}
            onChange={(e) => { setName(e.target.value); setSuccess(null); setError(null); }}
            placeholder={t("web.social.profile.name")}
          />
        </div>
        <div className="pc-row">
          <textarea
            className="pc-textarea"
            value={text}
            disabled={!loaded}
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

        {/* The profile that didn't come: why, and Try again; nothing can
            be saved over it meanwhile. */}
        {loadError && (
          <div className="pc-status error pc-load-error" role="alert">
            <span>{loadError(t)}</span>
            <button type="button" className="pc-btn pc-retry" onClick={() => void load()} disabled={loading}>{t("common.try_again")}</button>
          </div>
        )}
        {error && <div className="pc-status error">{error(t)}</div>}
        {success && <div className="pc-status success">{success(t)}</div>}
      </div>
    </div>
  );
}

