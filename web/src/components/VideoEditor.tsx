// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The video cut: the viewer's Edit on a video (MediaViewer.tsx opens it, a
// lazy part of its own), the post composer's trimmer (VideoTrimmer.tsx)
// over the viewer, playing what the viewer already has for the video - its
// stream URL, or the clip it read in pieces - never fetching it again. Its
// two handles choose a start and an end; Save asks Replace video or Save as
// copy, and the rest is the photo editor's (PhotoEditor.tsx): Replace asks
// the device first how many paths hold the video (GetEditState) and, above
// one, whether to replace it in all of them or only here; then EditVideo
// goes (videoEdit.ts), the dialog's button saying Saving… until the device
// answers, for as long as the cut takes - EditVideo has no clock of its
// own (ws.ts' cOpenEnded): a multi-GB recording is a minute or more on a
// Pi. A refusal is said in the dialog in the device's words, and Save can
// be tried again - except "changed" (the video's path holds other content
// now), which the viewer takes over as its Delete's: the editor and the
// viewer close, and the page lists its photos again. Cancel, Escape and
// Back ask first to discard a changed range. The device makes the cut, a
// lossless one (the streams copied), so there is no preview to draw for
// the tile: it shows the grid's tile without a thumbnail, as a new upload
// does, until the page lists the video again with the device's own. The
// phones' cuts do the same.
import { useEffect, useRef, useState } from "react";
import { useT } from "../i18n";
import { useWS } from "../net/useWS";
import type { ReqEnvelope, RespEnvelope } from "../proto/messages";
import { ConfirmDialog } from "./ConfirmDialog";
import type { ViewerItem } from "./MediaViewer";
import { copiesAsk, copiesFrom, editAnswer, editStatePayload, refusalOf, type EditDone, type EditHow } from "./photoEdit";
import { say, type UiText } from "./uiText";
import { cutPayload, type Cut } from "./videoEdit";
import VideoTrimmer from "./VideoTrimmer";

// The question showing: Save's (Replace video or Save as copy), the
// copies' after Replace, or Cancel's with the range changed.
type Ask = { kind: "save" } | { kind: "copies"; count: number } | { kind: "discard" };

export default function VideoEditor({ item, src, onCancel, onSaved, onChanged }: {
  item: ViewerItem;
  /** The video as the viewer plays it: its stream URL or its clip's blob
   *  URL, "" while the viewer is still getting it. */
  src: string;
  /** Closed with nothing saved. */
  onCancel: () => void;
  /** The device saved the cut: in the video's place, or as a copy. */
  onSaved: (done: EditDone) => void;
  /** The device refused it as "changed", in these words: the caller
   *  closes the editor (and the viewer). Without it the dialog says so. */
  onChanged?: (text: UiText) => void;
}) {
  const t = useT();
  const [ask, setAsk] = useState<Ask | null>(null);
  // The cut Save was pressed for, which the questions after it send.
  const cutRef = useRef<Cut | null>(null);

  const mountedRef = useRef(true);
  useEffect(() => () => { mountedRef.current = false; }, []);

  // ---- saving ----
  // How many paths hold the video (GetEditState): 1 when the device can't
  // say, or nothing came.
  const askCopies = async (): Promise<number> => {
    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => { e.payload = editStatePayload(item.path); });
      return copiesFrom(resp);
    } catch {
      return copiesFrom(null);
    }
  };
  // EditVideo: done (the editor closes), or what to say in the dialog.
  const send = async (how: EditHow, copies: number): Promise<UiText | null> => {
    const c = cutRef.current;
    if (!c) return say("web.images.edit.failed");
    let resp: RespEnvelope;
    try {
      resp = await useWS.request((e: Partial<ReqEnvelope>) => { e.payload = cutPayload(item, c, how); });
    } catch {
      return say("web.images.edit.failed");
    }
    const answer = editAnswer(resp);
    if (answer.kind === "refused") {
      const refused = refusalOf(answer, false);
      if (!refused.closes || !onChanged) return refused.text;
      onChanged(refused.text);
      return null;
    }
    if (mountedRef.current) onSaved({ kind: how.asCopy ? "copy" : "replace", file: answer.file, thumb: null, copies, every: how.every });
    return null;
  };
  const replace = async (): Promise<UiText | null> => {
    const copies = await askCopies();
    if (!mountedRef.current) return null;
    const q = copiesAsk(copies);
    if (q.kind === "copies") {
      setAsk({ kind: "copies", count: q.count });
      return null;
    }
    return send({ asCopy: false, every: false }, copies);
  };

  return (
    <VideoTrimmer
      src={src}
      onCancel={onCancel}
      cut={{
        onSave: (c) => {
          cutRef.current = c;
          setAsk({ kind: "save" });
        },
        onDiscard: () => setAsk({ kind: "discard" }),
        asking: ask != null,
      }}
    >
      {ask?.kind === "save" && (
        <ConfirmDialog
          title={t("web.images.edit.save_title")}
          text={t("web.images.edit.video_save_text")}
          confirm={t("web.images.edit.video_replace")}
          busyText={t("web.images.edit.saving")}
          tone="primary"
          onConfirm={replace}
          other={{ label: t("web.images.edit.as_copy"), onConfirm: () => send({ asCopy: true, every: false }, 1) }}
          onCancel={() => setAsk(null)}
        />
      )}
      {ask?.kind === "copies" && (
        <ConfirmDialog
          title={t("web.images.edit.save_title")}
          text={t("web.images.edit.video_copies_text", { count: ask.count })}
          confirm={t("web.images.edit.copies_all", { count: ask.count })}
          busyText={t("web.images.edit.saving")}
          tone="primary"
          onConfirm={() => send({ asCopy: false, every: true }, ask.count)}
          other={{ label: t("web.images.edit.copies_only_this"), onConfirm: () => send({ asCopy: false, every: false }, ask.count) }}
          onCancel={() => setAsk(null)}
        />
      )}
      {ask?.kind === "discard" && (
        <ConfirmDialog
          title={t("web.images.edit.discard_title")}
          confirm={t("web.images.edit.discard")}
          busyText={t("web.images.edit.discard")}
          onConfirm={async () => { onCancel(); return null; }}
          onCancel={() => setAsk(null)}
        />
      )}
    </VideoTrimmer>
  );
}
