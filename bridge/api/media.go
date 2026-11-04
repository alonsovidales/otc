// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/alonsovidales/otc/bridge/limits"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"google.golang.org/protobuf/proto"
)

// maxProxiedRange mirrors mediastream.MaxRangeBytes on the device: the
// most one ReqGetMediaRange is answered with. A response longer than this
// is fetched a span of it at a time.
const maxProxiedRange int64 = 4 << 20

// cMediaWriteStall is how long a media response may wait on a client that
// has stopped reading before it is cut (limits.WriteAll). Players stop
// reading on purpose, paused or with a full buffer, and ask for a new
// range when they resume, so it is far longer than limits.WriteIdleTimeout:
// it only ends the stall that never ends. It also outlasts the longest
// device fetch between two spans, which runs under the same deadline.
const cMediaWriteStall = 5 * time.Minute

// cMediaReadAhead is how many spans a response keeps on their way from the
// device while it writes one: enough that the device's link never waits
// for a round trip between two spans, and well within what the device
// gives a connection that hasn't signed in (4 requests at once, each span
// holding twice its size of the device's memory while it is answered).
const cMediaReadAhead = 2

// spanSource is where one media response's spans come from: a session with
// the device (websocket.Session: one of its connections for the whole
// response), or a stand-in for one in tests.
type spanSource interface {
	Forward(frame []byte) ([]byte, error)
	Close()
}

// oneOffSpans is a spanSource of one-off requests (forwardOneOff, a fake
// device in tests).
type oneOffSpans struct {
	forward func(domain string, frame []byte) ([]byte, error)
	domain  string
}

func (o oneOffSpans) Forward(frame []byte) ([]byte, error) { return o.forward(o.domain, frame) }
func (o oneOffSpans) Close()                               {}

// openSpans is the spanSource for one response of domain's device.
func (api *API) openSpans(domain string) spanSource {
	if api.forwardOneOff != nil {
		return oneOffSpans{api.forwardOneOff, domain}
	}
	return api.websocket.OpenSession(domain)
}

// proxyMedia is the bridge half of issue #110: a browser or app talks
// ordinary HTTP to <device>.off-the.cloud/media/<token>, and this turns
// each range request into ReqGetMediaRanges over that device's relay
// tunnel - all of one response's on one of the device's connections, the
// next spans asked for while one goes out.
//
// A range is answered whole, however long (see below): what makes this
// streaming rather than a download is that it goes out a span at a time
// as the spans arrive, so a player starts after the first and one that
// hangs up stops the fetching; no whole file is ever held in the middle.
func (api *API) proxyMedia(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		http.NotFound(w, r)
		return
	}

	spec, err := parseRange(r.Header.Get("Range"))
	if err != nil {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}

	// One HTTP request is one of the address's one-off requests, however
	// many spans it takes: a player reading ahead fast took the whole
	// allowance, span by span, and its next request (or the page's
	// assets) got 429.
	if !api.allowOneOff(w, r) {
		return
	}
	m := &mediaFetch{src: api.openSpans(r.Host), token: token, host: r.Host}
	defer m.src.Close()
	// A client that hangs up ends the session at once, its first span, a
	// HEAD's byte and a suffix's probe included: each request holds one
	// of the device's few one-off slots, and one the device is slow on
	// kept it after the player had given up.
	stop := context.AfterFunc(r.Context(), m.src.Close)
	defer stop()

	offset := spec.start
	// Never more than the device would hand over in one go, and never
	// more than the client actually asked for.
	length := maxProxiedRange
	if spec.hasEnd {
		length = min(length, spec.end-spec.start+1)
	}
	if spec.suffixLen > 0 {
		length = min(length, spec.suffixLen)
	}
	// HEAD needs the size and type, not the bytes: net/http eats a HEAD
	// response's body writes without an error, so the walk below would
	// otherwise pull the whole file through the device's uplink, even
	// after the client hung up. One byte at the same offset keeps every
	// status (404 past the end, 416 at it) what GET would get.
	head := r.Method == http.MethodHead
	if head {
		length = 1
	}
	if spec.suffixLen > 0 {
		// A suffix range ("bytes=-1024", the last 1024 bytes) can only
		// be turned into an offset once the total size is known, and the
		// bridge doesn't know it until the device says so. Players use
		// this to read an MP4's trailing index, so it has to work: one
		// cheap probe read gets the size, then the real read follows.
		_, total, _, ok := m.fetch(w, 0, 1)
		if !ok {
			return
		}
		offset = total - spec.suffixLen
		if offset < 0 {
			offset = 0
		}
	}

	content, total, mime, ok := m.fetch(w, offset, length)
	if !ok {
		return
	}

	if mime != "" {
		w.Header().Set("Content-Type", mime)
	}
	w.Header().Set("Accept-Ranges", "bytes")

	if !spec.present {
		// No Range header at all (curl, a download manager) - that asks
		// for the whole file, and a 200 has to actually deliver it, so
		// walk the rest of it a span at a time rather than lying about
		// the length.
		w.Header().Set("Content-Length", strconv.FormatInt(total, 10))
		w.WriteHeader(http.StatusOK)
		if head {
			return
		}
		m.writeSpan(w, r, 0, content, total-1)
		return
	}

	// The whole range that was asked for, never less: AVFoundation (every
	// iPhone video) treats a shorter answer to its range as an error -
	// "content range mismatch" and a crossed-out play button - even
	// though HTTP allows it. It is still streaming: the rest comes from
	// the device a span at a time and goes out as it arrives, so playback
	// starts after the first span, and a player that hangs up early stops
	// the fetching.
	last := total - 1
	if spec.hasEnd && spec.end < last {
		last = spec.end
	}
	if total == 0 || offset > last {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", total))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	if int64(len(content)) > last-offset+1 {
		content = content[:last-offset+1]
	}
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, last, total))
	w.Header().Set("Content-Length", strconv.FormatInt(last-offset+1, 10))
	w.WriteHeader(http.StatusPartialContent)
	if head {
		return
	}
	m.writeSpan(w, r, offset, content, last)
}

// mediaFetch is one media response's requests to the device.
type mediaFetch struct {
	src    spanSource
	token  string
	host   string
	nextID atomic.Int32 // the envelopes' ids: distinct within the session
}

// spanResult is one span asked for ahead.
type spanResult struct {
	content []byte
	ok      bool
}

// writeSpan writes first (the bytes from offset on) and then the rest up to
// last, inclusive, fetched from the device a span at a time and flushed as
// each arrives - up to cMediaReadAhead spans asked for ahead of the one
// being written, so the device is sending the next while this goes out.
// Best effort: a client that goes away (nothing more is asked for it), or
// a device that stops answering, ends it.
func (m *mediaFetch) writeSpan(w http.ResponseWriter, r *http.Request, offset int64, first []byte, last int64) {
	ctx := r.Context()
	flusher, _ := w.(http.Flusher)
	pos := offset + int64(len(first)) // the next byte to write after first
	next := pos                       // the next byte to ask for
	type span struct {
		n    int64
		done chan spanResult
	}
	var ahead []span
	askAhead := func() {
		for len(ahead) < cMediaReadAhead && next <= last && ctx.Err() == nil {
			sp := span{n: min(maxProxiedRange, last-next+1), done: make(chan spanResult, 1)}
			go func(at, n int64, done chan<- spanResult) {
				content, _, _, ok := m.fetch(nil, at, n)
				done <- spanResult{content, ok}
			}(next, sp.n, sp.done)
			ahead = append(ahead, sp)
			next += sp.n
		}
	}

	askAhead() // on its way while the first goes out
	if err := limits.WriteAll(w, first, cMediaWriteStall); err != nil {
		return
	}
	if flusher != nil {
		flusher.Flush()
	}
	for pos <= last {
		askAhead()
		if len(ahead) == 0 {
			return // the client is gone: nothing more is asked for it
		}
		var res spanResult
		select {
		case res = <-ahead[0].done:
		case <-ctx.Done():
			return
		}
		asked := ahead[0].n
		ahead = ahead[1:]
		if !res.ok || len(res.content) == 0 {
			return
		}
		content := res.content
		// Never more than was asked (the spans asked after it start
		// there) nor than the range.
		if int64(len(content)) > asked {
			content = content[:asked]
		}
		if int64(len(content)) > last-pos+1 {
			content = content[:last-pos+1]
		}
		if err := limits.WriteAll(w, content, cMediaWriteStall); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
		pos += int64(len(content))
		if int64(len(content)) < asked {
			// Shorter than asked: no device answers so before the end of
			// the file, but if one did, the spans asked after it don't
			// start where this one ended - asked again from there.
			ahead, next = nil, pos
		}
	}
}

// fetch asks the device for one span. w may be nil for continuation
// reads, where the response has already started and there is no longer
// any status code left to send.
func (m *mediaFetch) fetch(w http.ResponseWriter, offset, length int64) (content []byte, total int64, mime string, ok bool) {
	req := &pb.ReqEnvelope{
		Id: m.nextID.Add(1),
		Payload: &pb.ReqEnvelope_ReqGetMediaRange{
			ReqGetMediaRange: &pb.ReqGetMediaRange{
				Token:  m.token,
				Offset: offset,
				Length: length,
			},
		},
	}
	frame, err := proto.Marshal(req)
	if err != nil {
		log.Error("error marshaling media range request:", err)
		writeStatus(w, http.StatusInternalServerError)
		return nil, 0, "", false
	}

	respFrame, err := m.src.Forward(frame)
	if err != nil {
		// Same reasoning as proxyStaticAsset's own unreachable case: the
		// device is temporarily absent, not broken. A player gets a
		// status it can retry on rather than a hung request.
		log.Debug("device unreachable while streaming media:", m.host, err)
		if w != nil {
			w.Header().Set("Retry-After", "30")
		}
		writeStatus(w, http.StatusServiceUnavailable)
		return nil, 0, "", false
	}

	var resp pb.RespEnvelope
	if err := proto.Unmarshal(respFrame, &resp); err != nil {
		log.Error("bad proto from device while streaming media:", err)
		writeStatus(w, http.StatusBadGateway)
		return nil, 0, "", false
	}
	rng, isRange := resp.Payload.(*pb.RespEnvelope_RespMediaRange)
	if resp.Error || !isRange {
		// An expired or unknown token lands here, and 404 is the honest
		// answer - it's indistinguishable from a file that isn't there,
		// which is the point.
		writeStatus(w, http.StatusNotFound)
		return nil, 0, "", false
	}

	return rng.RespMediaRange.Content, rng.RespMediaRange.TotalSize, rng.RespMediaRange.Mime, true
}

func writeStatus(w http.ResponseWriter, status int) {
	if w != nil {
		w.WriteHeader(status)
	}
}

// rangeSpec is the part of a Range header that matters here: where to
// start. How much comes back is capped by the device regardless of what
// was asked for, so the end of the requested span never changes the
// answer - only whether it was counted from the front or the back.
type rangeSpec struct {
	present bool
	start   int64
	// end/hasEnd carry a closed range's last byte ("bytes=0-31"). It has
	// to be honoured: answering with *less* than was asked for is
	// ordinary HTTP and is what makes this streaming, but answering with
	// more is not - a player probing a file's header or its trailing
	// index asks for a few hundred bytes, and handing it megabytes
	// instead breaks the contract and wastes exactly the bandwidth this
	// issue exists to save.
	end    int64
	hasEnd bool
	// suffixLen > 0 means the request was "the last N bytes", which
	// can't become an offset until the total size is known.
	suffixLen int64
}

// parseRange reads a Range header. A multi-range request (several spans
// at once) is answered with its first span, which is within what RFC 9110
// allows a server to do.
func parseRange(header string) (rangeSpec, error) {
	if header == "" {
		return rangeSpec{}, nil
	}
	spec, found := strings.CutPrefix(strings.TrimSpace(header), "bytes=")
	if !found {
		return rangeSpec{}, fmt.Errorf("unsupported range unit: %q", header) // i18n-ignore: answered with a bare status, never sent
	}
	first, _, _ := strings.Cut(spec, ",")
	startText, endText, ok := strings.Cut(strings.TrimSpace(first), "-")
	if !ok {
		return rangeSpec{}, fmt.Errorf("malformed range: %q", header) // i18n-ignore: answered with a bare status, never sent
	}

	if startText == "" {
		lenText := strings.TrimPrefix(strings.TrimSpace(first), "-")
		n, err := strconv.ParseInt(lenText, 10, 64)
		if err != nil || n <= 0 {
			return rangeSpec{}, fmt.Errorf("malformed suffix range: %q", header) // i18n-ignore: answered with a bare status, never sent
		}
		return rangeSpec{present: true, suffixLen: n}, nil
	}

	start, err := strconv.ParseInt(startText, 10, 64)
	if err != nil || start < 0 {
		return rangeSpec{}, fmt.Errorf("malformed range start: %q", header) // i18n-ignore: answered with a bare status, never sent
	}

	out := rangeSpec{present: true, start: start}
	if endText = strings.TrimSpace(endText); endText != "" {
		end, err := strconv.ParseInt(endText, 10, 64)
		if err != nil || end < start {
			return rangeSpec{}, fmt.Errorf("malformed range end: %q", header) // i18n-ignore: answered with a bare status, never sent
		}
		out.end, out.hasEnd = end, true
	}

	return out, nil
}
