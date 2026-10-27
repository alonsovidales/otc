// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useState } from "react";

// A share link's archive (issue #104): /#download=<uuid>_<secret> - the
// secret only in the fragment, which no request carries (galleries' links
// do the same) - or ?download= in the links from before, still taken.
export function downloadLinkOf(hash: string, search: URLSearchParams): string | null {
  const m = /^#download=([^&]+)$/.exec(hash);
  return m ? m[1] : search.get("download");
}

// downloadLinkOf for this page, following its fragment: a link pasted
// into a tab already on the device changes only that.
export function useDownloadLink(search: URLSearchParams): string | null {
  const [hash, setHash] = useState(() => window.location.hash);
  useEffect(() => {
    const onHash = () => setHash(window.location.hash);
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);
  return downloadLinkOf(hash, search);
}
