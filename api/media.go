// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/mediastream"
)

// serveMedia is issue #110's streaming endpoint: the URL a <video>
// element or AVPlayer is pointed at, answering ordinary HTTP range
// requests so a player starts on the first chunk instead of waiting for a
// whole video to arrive over the socket.
//
// Plain HTTP with no session of its own, like /check_healty and
// /internal/metrics - the path's token is the entire credential, minted
// over the authenticated socket for this one file (see mediastream). A
// bad or expired token is a flat 404: that a token once existed, or was
// for a real file, isn't something an unauthenticated caller should be
// able to learn.
func (api *API) serveMedia(w http.ResponseWriter, r *http.Request) {
	media := api.websocket.Media()
	if media == nil {
		http.NotFound(w, r)
		return
	}

	token := r.PathValue("token")
	stream, err := media.Open(token)
	if err != nil {
		if !errors.Is(err, mediastream.ErrUnknownToken) {
			log.Error("error opening media stream:", err)
		}
		http.NotFound(w, r)
		return
	}
	defer stream.Close()

	if stream.Mime != "" {
		w.Header().Set("Content-Type", stream.Mime)
	}
	// http.ServeContent does the whole range protocol for us: Range
	// parsing, 206 with Content-Range, 416 on an unsatisfiable range,
	// HEAD, and Accept-Ranges. Reimplementing that by hand is how
	// seeking ends up subtly broken in one player and not another.
	//
	// The zero modtime deliberately suppresses Last-Modified/If-Modified
	// -Since: a token is already short-lived, and caching negotiation on
	// top of it buys nothing.
	http.ServeContent(w, r, "", time.Time{}, stream.ReadSeeker())
}
