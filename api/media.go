// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"errors"
	"net/http"
	"strings"
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
	serveMediaFrom(w, r, api.websocket.Media())
}

// serveMediaFrom is serveMedia over media. ?download=1 (the web viewer's
// Download button) answers the same bytes as an attachment, saved under
// the file's own name; nothing else about the token changes - the same
// resource, the same expiry. Devices before it ignore the parameter, and
// so does the bridge, which serves the same URL from ReqGetMediaRange: the
// web app's link carries the name in its download attribute too.
func serveMediaFrom(w http.ResponseWriter, r *http.Request, media *mediastream.Server) {
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

	h := w.Header()
	// The browser takes the type it is given and never guesses one. Only
	// media is ever shown in place: anything else (HTML or SVG would run
	// script in the device's origin, and a type that doesn't parse can't
	// be trusted to be what it says) goes out as bytes to save.
	h.Set("X-Content-Type-Options", "nosniff")
	ctype, inline := mediastream.ServedType(stream.Mime)
	h.Set("Content-Type", ctype)
	if !inline || r.URL.Query().Get("download") == "1" {
		h.Set("Content-Disposition", attachment(stream.Name))
	}
	// http.ServeContent does the whole range protocol for us: Range
	// parsing, 206 with Content-Range, 416 on an unsatisfiable range,
	// HEAD, and Accept-Ranges. Reimplementing that by hand is how
	// seeking ends up subtly broken in one player and not another.
	//
	// The zero modtime deliberately suppresses Last-Modified/If-Modified
	// -Since: a token is already short-lived, and caching negotiation on
	// top of it buys nothing. Without a validator a browser can't resume
	// an interrupted download either (it starts over); a download is one
	// GET, though, so it isn't cut off by the token's expiry here - only
	// the bridge resolves the token again per span, so through it a
	// download has to finish within the token's TTL.
	http.ServeContent(w, r, "", time.Time{}, stream.ReadSeeker())
}

// attachment is a Content-Disposition that saves the response as name
// (RFC 6266): the exact name in filename* (UTF-8, percent-encoded, RFC
// 8187), which every current browser prefers, and an ASCII stand-in in
// filename for anything older. No name, no filename: the browser picks one.
func attachment(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '_'
		}
		return r
	}, strings.ToValidUTF8(name, "_"))
	if name == "" {
		return "attachment"
	}
	return `attachment; filename="` + asciiName(name) + `"; filename*=UTF-8''` + encodeExtValue(name)
}

// asciiName is name with every byte a quoted-string can't carry as itself,
// or that an old client might decode ('%'), or that names a folder
// ('/', '\'), swapped for '_'.
func asciiName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' || r == '%' || r == '/' {
			b.WriteByte('_')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// encodeExtValue percent-encodes every byte of s but RFC 8187's attr-char.
func encodeExtValue(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9',
			strings.IndexByte("!#$&+-.^_`|~", c) >= 0:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}
