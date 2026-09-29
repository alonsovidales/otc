// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef, useState } from "react";
import "./PhotoCropDialog.css";

// The profile picture's circle crop (issue #178) - the same one the setup
// wizard's page has: drag the photo to centre the face, zoom with the
// slider (or the wheel), and what's inside the square is saved as a small
// JPEG, never the photo as picked (several MB, or HEIC).

const VIEW = 280;
const OUT = 320;
const MAX_BYTES = 45 * 1024;

type Props = {
  file: File;
  onCancel: () => void;
  onDone: (jpeg: Uint8Array, url: string) => void;
};

export default function PhotoCropDialog({ file, onCancel, onDone }: Props) {
  const canvas = useRef<HTMLCanvasElement>(null);
  const [img, setImg] = useState<HTMLImageElement | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [zoom, setZoom] = useState(1);
  const [pan, setPan] = useState({ x: 0, y: 0 });
  const drag = useRef<{ x: number; y: number; ox: number; oy: number } | null>(null);

  useEffect(() => {
    const url = URL.createObjectURL(file);
    const im = new Image();
    im.onload = () => setImg(im);
    im.onerror = () => setError("This picture can't be opened here - try a JPEG or PNG.");
    im.src = url;
    return () => URL.revokeObjectURL(url);
  }, [file]);

  const scale = (im: HTMLImageElement, z: number) => z * Math.max(VIEW / im.width, VIEW / im.height);

  // The photo always covers the square: pan stops at its edges.
  const clamp = (p: { x: number; y: number }, z: number) => {
    if (!img) return p;
    const s = scale(img, z);
    const mx = Math.max(0, (img.width * s - VIEW) / 2);
    const my = Math.max(0, (img.height * s - VIEW) / 2);
    return { x: Math.min(mx, Math.max(-mx, p.x)), y: Math.min(my, Math.max(-my, p.y)) };
  };

  const paint = (ctx: CanvasRenderingContext2D, size: number, mask: boolean) => {
    if (!img) return;
    const k = size / VIEW;
    const s = scale(img, zoom) * k;
    const w = img.width * s;
    const h = img.height * s;
    ctx.fillStyle = "#1e1f22";
    ctx.fillRect(0, 0, size, size);
    ctx.drawImage(img, (size - w) / 2 + pan.x * k, (size - h) / 2 + pan.y * k, w, h);
    if (mask) {
      ctx.fillStyle = "rgba(0,0,0,.55)";
      ctx.beginPath();
      ctx.rect(0, 0, size, size);
      ctx.arc(size / 2, size / 2, size / 2 - 2, 0, Math.PI * 2, true);
      ctx.fill("evenodd");
    }
  };

  useEffect(() => {
    const ctx = canvas.current?.getContext("2d");
    if (ctx) paint(ctx, VIEW, true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [img, zoom, pan]);

  const setZoomClamped = (z: number) => {
    const next = Math.min(4, Math.max(1, z));
    setZoom(next);
    setPan((p) => clamp(p, next));
  };

  const save = async () => {
    if (!img) return;
    const out = document.createElement("canvas");
    out.width = out.height = OUT;
    const ctx = out.getContext("2d");
    if (!ctx) return;
    paint(ctx, OUT, false);
    let blob: Blob | null = null;
    for (const q of [0.85, 0.7, 0.55, 0.4]) {
      blob = await new Promise<Blob | null>((r) => out.toBlob(r, "image/jpeg", q));
      if (blob && blob.size <= MAX_BYTES) break;
    }
    if (!blob) {
      setError("The picture could not be prepared.");
      return;
    }
    onDone(new Uint8Array(await blob.arrayBuffer()), URL.createObjectURL(blob));
  };

  const cssK = () => VIEW / (canvas.current?.getBoundingClientRect().width || VIEW);

  return (
    <div className="pcd-backdrop" role="dialog" aria-modal="true" aria-label="Crop your profile picture">
      <div className="pcd-dialog">
        <h3>Profile picture</h3>
        {error ? (
          <p className="pcd-error">{error}</p>
        ) : (
          <>
            <canvas
              ref={canvas}
              width={VIEW}
              height={VIEW}
              className="pcd-canvas"
              onPointerDown={(e) => {
                drag.current = { x: e.clientX, y: e.clientY, ox: pan.x, oy: pan.y };
                e.currentTarget.setPointerCapture(e.pointerId);
              }}
              onPointerMove={(e) => {
                const d = drag.current;
                if (!d) return;
                const k = cssK();
                setPan(clamp({ x: d.ox + (e.clientX - d.x) * k, y: d.oy + (e.clientY - d.y) * k }, zoom));
              }}
              onPointerUp={() => { drag.current = null; }}
              onPointerCancel={() => { drag.current = null; }}
              onWheel={(e) => setZoomClamped(zoom * (e.deltaY < 0 ? 1.08 : 1 / 1.08))}
            />
            <label className="pcd-zoom">
              Zoom
              <input type="range" min={1} max={4} step={0.01} value={zoom} onChange={(e) => setZoomClamped(Number(e.target.value))} />
            </label>
            <p className="pcd-hint">Drag the photo to centre your face.</p>
          </>
        )}
        <div className="pcd-actions">
          <button className="pcd-btn" onClick={onCancel}>Cancel</button>
          <button className="pcd-btn pcd-primary" onClick={() => void save()} disabled={!img}>Use photo</button>
        </div>
      </div>
    </div>
  );
}
