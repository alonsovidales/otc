// SPDX-License-Identifier: AGPL-3.0-or-later

import { Fragment, useContext, type ReactNode } from "react";
import { I18nContext } from "./context";
import type { MessageArgs, RichKey, RichTags } from "./messages";
import { richSegments } from "./runtime";

/** What each tag of a rich key renders its span as, from code. */
export type RichParts<K extends RichKey> = { readonly [T in RichTags[K]]: (chunk: string) => ReactNode };

export type TransProps<K extends RichKey> = {
  /** The key: one with tags (RichKey). */
  k: K;
  /** One function per tag of the text: (span) => element. */
  parts: RichParts<K>;
} & (MessageArgs[K] extends undefined ? { args?: undefined } : { args: MessageArgs[K] });

/**
 * A text with tags: <Trans k="web.x.policy" parts={{ link: (s) => <a href={url}>{s}</a> }} />.
 * Every span is plain text handed to the part for its tag; nothing from
 * the text is ever rendered as markup.
 */
export default function Trans<K extends RichKey>(props: TransProps<K>) {
  const snapshot = useContext(I18nContext);
  const parts = props.parts as Readonly<Record<string, ((chunk: string) => ReactNode) | undefined>>;
  return (
    <>
      {richSegments(snapshot, props.k, props.args).map((s, i) => (
        <Fragment key={i}>{s.tag === undefined ? s.text : (parts[s.tag]?.(s.text) ?? s.text)}</Fragment>
      ))}
    </>
  );
}
