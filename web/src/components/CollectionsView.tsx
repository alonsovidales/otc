// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import type { ImageGroup } from "../proto/messages";
import { reloadGroups, useGroups } from "./libraryStore";
import type { Reload } from "./libraryStore";
import { CollectionsIcon } from "./NavIcons";
import { openGroup, showAll } from "./photoFilter";
import "./CollectionsView.css";

// The Collections page: every collection (an image group in the protocol)
// as a card with its cover, newest first as the device lists them. A card
// opens Images on that collection, which is where one is renamed, shared
// or deleted.

type Props = {
  // A collection was opened (photoFilter.openGroup already called): show Images.
  onOpenPhotos: () => void;
};

const cSkeletonCards = 8;
// How long the first listing may take before the page offers to ask again.
const cSlowMs = 10_000;

const itemsLabel = (n: number) => `${n.toLocaleString()} ${n === 1 ? "item" : "items"}`;
const nameOf = (g: ImageGroup) => g.name.trim() || "Untitled collection";

export default function CollectionsView({ onOpenPhotos }: Props) {
  const { items, thumbs, loaded } = useGroups();
  const { failed, slow, retry } = useFreshList(loaded, reloadGroups);

  const open = (g: ImageGroup) => {
    openGroup({ id: g.id, name: g.name, fileCount: g.fileCount });
    onOpenPhotos();
  };

  let body: ReactNode;
  if (loaded && items.length === 0) {
    body = (
      <div className="cv-state">
        <span className="cv-state-art"><EmptyArt /></span>
        <h2 className="cv-state-title">No collections yet</h2>
        <p className="cv-state-text">Select photos in Images, then choose <strong>Add to collection</strong>.</p>
        <button type="button" className="cv-btn" onClick={() => { showAll(); onOpenPhotos(); }}>
          Go to Images
        </button>
      </div>
    );
  } else if (loaded) {
    body = (
      <ul className="cv-grid" role="list">
        {items.map((g) => {
          const cover = thumbs.get(g.id);
          const name = nameOf(g);
          return (
            <li key={g.id}>
              <button type="button" className="cv-card" onClick={() => open(g)} aria-label={`${name}, ${itemsLabel(g.fileCount)}`}>
                <span className="cv-cover">
                  {cover
                    ? <img src={cover} alt="" draggable={false} decoding="async" />
                    : <span className="cv-cover-ph"><CollectionsIcon size={36} /></span>}
                </span>
                <span className="cv-name" title={name}>{name}</span>
                <span className="cv-count">{itemsLabel(g.fileCount)}</span>
              </button>
            </li>
          );
        })}
      </ul>
    );
  } else if (failed || slow) {
    body = (
      <div className="cv-state" role="status">
        <span className="cv-state-art"><ProblemIcon /></span>
        <h2 className="cv-state-title">{failed ? "Couldn't load your collections" : "Still waiting for your device"}</h2>
        <p className="cv-state-text">
          {failed
            ? "Check that your device is online, then try again."
            : "Your collections are taking longer than usual to load."}
        </p>
        <button type="button" className="cv-btn" onClick={retry}>Try again</button>
      </div>
    );
  } else {
    body = (
      <ul className="cv-grid cv-skeleton" aria-busy="true" aria-label="Loading collections">
        {Array.from({ length: cSkeletonCards }, (_, i) => (
          <li key={i} aria-hidden="true">
            <span className="cv-cover" />
            <span className="cv-name"><span className="cv-bar" /></span>
            <span className="cv-count"><span className="cv-bar" /></span>
          </li>
        ))}
      </ul>
    );
  }

  return (
    <div className="cv-root">
      <header className="cv-head">
        <h1 className="cv-title">Collections</h1>
      </header>
      {body}
    </div>
  );
}

// The list is asked for again on every visit (covers and counts change as
// photos are added elsewhere), and what the store already holds shows
// meanwhile. When nothing is loaded yet the store's own hook is listing it
// already, and asking again would list it twice - with a second, different
// set of random covers swapping in - so the page waits on that listing,
// and its failure shows at once. A listing that failed, or a first one
// that is slow, gets a "Try again".
function useFreshList(loaded: boolean, reload: Reload) {
  const [asked, setAsked] = useState<"no" | "running" | "done">("no");
  const [slow, setSlow] = useState(false);
  const wait = useCallback((listing: () => Promise<void>) => {
    setAsked("running");
    setSlow(false);
    listing().catch(() => {}).finally(() => setAsked("done"));
  }, []);
  const retry = useCallback(() => wait(reload), [wait, reload]);
  const loadedAtMount = useRef(loaded);
  useEffect(() => { wait(loadedAtMount.current ? reload : reload.join); }, [wait, reload]);
  useEffect(() => {
    if (loaded || asked === "done") return;
    const t = window.setTimeout(() => setSlow(true), cSlowMs);
    return () => window.clearTimeout(t);
  }, [loaded, asked]);
  return { failed: !loaded && asked === "done", slow: !loaded && slow, retry };
}

// Two photos, one over the other, and a gold "+": put photos together.
function EmptyArt() {
  return (
    <svg width="88" height="88" viewBox="0 0 80 80" fill="none" stroke="currentColor" strokeWidth="1.8"
      strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <rect className="cv-art-back" x="26" y="14" width="36" height="44" rx="6" transform="rotate(8 44 36)" />
      <g transform="rotate(-6 34 44)">
        <rect className="cv-art-front" x="16" y="22" width="36" height="44" rx="6" />
        <circle className="cv-art-sun" cx="27" cy="33" r="3.5" />
        <path d="M20 59l8-8 6 6 5-5 9 9" />
      </g>
      <circle className="cv-art-badge" cx="58" cy="61" r="9" />
      <path className="cv-art-plus" d="M58 56.5v9M53.5 61h9" />
    </svg>
  );
}

function ProblemIcon() {
  return (
    <svg width="40" height="40" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.4"
      strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <circle cx="12" cy="12" r="9" />
      <path d="M12 7.5V13M12 16.5h.01" />
    </svg>
  );
}
