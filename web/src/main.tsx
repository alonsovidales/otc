// SPDX-License-Identifier: AGPL-3.0-or-later

// The page's entry. Most of the app is a chunk of its own, loaded with what
// it needs as this runs: the public page of a shared gallery (issue #180,
// /shared#<uuid>.<secret>), opened by anyone with its link, loads only what
// it shows (sharedEntry.tsx), never the whole app (appEntry.tsx).
if (window.location.pathname === "/shared") void import("./sharedEntry");
else void import("./appEntry");
