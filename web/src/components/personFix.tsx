// SPDX-License-Identifier: AGPL-3.0-or-later
//
// "Not this person" and "Someone else…": face recognition matched photos to
// the wrong person, and among one person's photos (Images opened on a face
// from People, or one person in the search) the selection bar and the
// photo viewer take them off that person - the match only, never the
// photos (RemoveFromPerson). Not this person asks first, then the faces go
// to no one, and face recognition never files them under anyone again;
// Someone else… lists the other people found and moves the faces to the
// one picked. Images then drops the photos from the person's grid, as after
// a delete, and says what was done (personFixDone.ts). The phones word it
// the same (app.photos.person.*). Styled in PhotoGallery.css (.pg-pick*).
import { useEffect, useId, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import { useT } from "../i18n";
import { useWS } from "../net/useWS";
import type { Person, ReqEnvelope, RespEnvelope } from "../proto/messages";
import { ConfirmDialog, Modal } from "./ConfirmDialog";
import { personLabel, reloadPeople, usePeople } from "./libraryStore";
import Spinner from "./Spinner";
import { say, type UiText } from "./uiText";
import { named, type PersonFixDone } from "./personFixDone";

// With more people than this, the list gets a field to find a name.
const cFindFrom = 8;

// true once the device has done it; throws when it didn't answer.
async function removeFromPerson(personId: string, hashes: string[], toPersonId: string): Promise<boolean> {
  const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
    e.payload = { $case: "reqRemoveFromPerson", reqRemoveFromPerson: { personId, hashes, toPersonId } };
  });
  return resp.payload?.$case === "respAck" && resp.payload.respAck.ok;
}

// Icons in Images' style (PhotoGallery.tsx's Svg): 24px box, round 1.6
// strokes. A head and shoulders with a minus, and with an arrow.
function Glyph({ size, children }: { size: number; children: ReactNode }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.6}
      strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
      {children}
    </svg>
  );
}
export const NotThisPersonIcon = ({ size = 22 }: { size?: number }) => (
  <Glyph size={size}><circle cx="10" cy="8.5" r="3.5" /><path d="M3.5 19.5c1-3.3 3.4-5 6.5-5 1.4 0 2.6.3 3.6 1" /><path d="M16 17.5h5" /></Glyph>
);
export const SomeoneElseIcon = ({ size = 22 }: { size?: number }) => (
  <Glyph size={size}><circle cx="10" cy="8.5" r="3.5" /><path d="M3.5 19.5c1-3.3 3.4-5 6.5-5 1.4 0 2.6.3 3.6 1" /><path d="M15.5 17.5h5.5M18.5 15l2.5 2.5-2.5 2.5" /></Glyph>
);
const FaceGlyph = () => (
  <Glyph size={22}><circle cx="12" cy="9.5" r="3.5" /><path d="M5.5 19.5c1.2-3.2 3.6-4.8 6.5-4.8s5.3 1.6 6.5 4.8" /></Glyph>
);

/** Not this person: the question, and the request once it is answered
 *  Remove. Its errors show in it; onDone closes it. */
export function RemoveFromPersonDialog({ personId, hashes, onCancel, onDone }: {
  personId: string;
  hashes: string[];
  onCancel: () => void;
  onDone: (done: PersonFixDone) => void;
}) {
  const t = useT();
  const people = usePeople();
  const person = people.items.find((p) => p.id === personId);
  const name = named(person);
  const count = hashes.length;
  const confirm = async (): Promise<UiText | null> => {
    try {
      if (!(await removeFromPerson(personId, hashes, ""))) return say("web.images.person.remove_failed");
    } catch {
      return say("web.images.error.unreachable");
    }
    onDone({ hashes: new Set(hashes), from: person, moved: false });
    return null;
  };
  return (
    <ConfirmDialog
      title={name ? t("web.images.person.remove_title", { name, count }) : t("web.images.person.remove_title_unnamed", { count })}
      text={t("web.images.person.remove_text")}
      confirm={t("common.remove")}
      busyText={t("web.images.person.removing")}
      // Nothing is lost: the photos stay, and only a wrong match goes.
      tone="primary"
      onConfirm={confirm}
      onCancel={onCancel}
    />
  );
}

/** Someone else…: the other people found (as People lists them, with
 *  their faces), a field to find a name when there are many, and the
 *  request when one is picked. Its errors show in it; onDone closes it. */
export function MoveToPersonDialog({ personId, hashes, onCancel, onDone }: {
  personId: string;
  hashes: string[];
  onCancel: () => void;
  onDone: (done: PersonFixDone) => void;
}) {
  const t = useT();
  const titleId = useId();
  const people = usePeople();
  // Fresh counts and faces (and anyone found meanwhile) when the list was
  // loaded before; usePeople lists them the first time itself.
  const loadedAtStart = useRef(people.loaded);
  useEffect(() => {
    if (loadedAtStart.current) void reloadPeople().catch(() => {});
  }, []);
  const from = people.items.find((p) => p.id === personId);
  const others = useMemo(() => people.items.filter((p) => p.id !== personId), [people.items, personId]);
  const [find, setFind] = useState("");
  const shown = useMemo(() => {
    const q = find.trim().toLocaleLowerCase();
    return q ? others.filter((p) => personLabel(p).toLocaleLowerCase().includes(q)) : others;
  }, [others, find]);
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<UiText | null>(null);
  // A mouse and keyboard start in the field; a touch screen's keyboard
  // stays down until the field is tapped.
  const [typeFirst] = useState(() => window.matchMedia("(hover: hover)").matches);

  const pick = async (to: Person) => {
    if (busy) return;
    setBusy(to.id);
    setError(null);
    try {
      if (await removeFromPerson(personId, hashes, to.id)) {
        onDone({ hashes: new Set(hashes), from, to, moved: true });
        return;
      }
      setError(() => say("web.images.person.move_failed"));
      // The person picked may have gone (deleted or merged elsewhere).
      void reloadPeople().catch(() => {});
    } catch {
      setError(() => say("web.images.error.unreachable"));
    }
    setBusy(null);
  };

  const showFind = others.length > cFindFrom;
  return (
    <Modal labelledBy={titleId} busy={busy != null} onClose={onCancel}>
      <h2 id={titleId} className="pg-dlg-title">{t("web.images.person.move_title", { count: hashes.length })}</h2>
      {showFind && (
        <input
          type="text"
          className="pg-input"
          value={find}
          onChange={(e) => setFind(e.target.value)}
          placeholder={t("web.images.person.move_find")}
          aria-label={t("web.images.person.move_find")}
          disabled={busy != null}
          data-autofocus={typeFirst ? true : undefined}
          onKeyDown={(e) => {
            // Escape in a field with text clears it; the dialog's own
            // Escape closes it once the field is empty.
            if (e.key === "Escape" && find) {
              e.preventDefault();
              e.stopPropagation();
              setFind("");
            }
          }}
        />
      )}
      {!people.loaded ? (
        <ul className="pg-pick-list" aria-hidden="true">
          {[0, 1, 2].map((i) => (
            <li key={i} className="pg-pick pg-pick-face pg-pick-skel"><span className="pg-pick-cover" /><span className="pg-pick-text"><span /><span /></span></li>
          ))}
        </ul>
      ) : others.length === 0 ? (
        <p className="pg-dlg-text" role="status">{t("web.images.person.move_none")}</p>
      ) : shown.length === 0 ? (
        <p className="pg-dlg-text" role="status">{t("web.images.person.move_no_match")}</p>
      ) : (
        <ul className="pg-pick-list" aria-label={t("web.images.people.title")}>
          {shown.map((p) => {
            const face = people.thumbs.get(p.id);
            const hasName = named(p) !== "";
            return (
              <li key={p.id}>
                <button type="button" className="pg-pick pg-pick-face" onClick={() => void pick(p)} disabled={busy != null} aria-busy={busy === p.id}>
                  {face
                    ? <img className="pg-pick-cover" src={face} alt="" loading="lazy" />
                    : <span className="pg-pick-cover"><FaceGlyph /></span>}
                  <span className="pg-pick-text">
                    <span className={`pg-pick-name${hasName ? "" : " unnamed"}`}>{personLabel(p)}</span>
                    <span className="pg-pick-count">{t("web.images.people.photos", { count: p.faceCount })}</span>
                  </span>
                  {busy === p.id && <Spinner />}
                </button>
              </li>
            );
          })}
        </ul>
      )}
      {error && <p className="pg-dlg-error" role="alert">{error(t)}</p>}
      <div className="pg-dlg-actions">
        <button type="button" className="pg-btn quiet" onClick={onCancel} disabled={busy != null}
          data-autofocus={!showFind || !typeFirst ? true : undefined}>{t("common.cancel")}</button>
      </div>
    </Modal>
  );
}
