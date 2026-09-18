// SPDX-License-Identifier: AGPL-3.0-or-later

// A small inline "working on it" indicator for actions that take long
// enough that silence reads as nothing having happened.
//
// The case that prompted it: "Download as ZIP" and "Share link" both ask
// the device to gather the selected files and build an archive before any
// link exists. On a handful of photos that is seconds, during which the
// button looked completely inert - so people click it again, which starts
// a second archive (see issue #104 for what duplicate work looks like from
// the other end).
import "./Spinner.css";

export default function Spinner({ label }: { label?: string }) {
  return (
    <span className="spinner-wrap">
      {/* aria-hidden on the ring itself: it carries no information a
          screen reader can use. The status text beside it is the
          announcement, and role="status" makes it a polite live region so
          it's read when it appears without interrupting anything. */}
      <span className="spinner" aria-hidden="true" />
      {label && <span role="status">{label}</span>}
    </span>
  );
}
