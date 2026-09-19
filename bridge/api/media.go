// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"google.golang.org/protobuf/proto"
)

// maxProxiedRange mirrors mediastream.MaxRangeBytes on the device: one
// HTTP range request costs exactly one relay round trip, and the device
// won't return more than this anyway. Asking for more would just mean
// answering with less than was promised.
const maxProxiedRange int64 = 4 << 20

// proxyMedia is the bridge half of issue #110: a browser or app talks
// ordinary HTTP to <device>.off-the.cloud/media/<token>, and this turns
// each range request into one ReqGetMediaRange over that device's relay
// tunnel.
//
// Answering with less than was asked for is deliberate and is what makes
// this streaming rather than a download: a player asking for "bytes=0-"
// gets the first few MB, starts playing, and comes back for the next span
// when it needs it. One HTTP request, one tunnel round trip, no whole
// file anywhere in the middle.
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
	if spec.suffixLen > 0 {
		// A suffix range ("bytes=-1024", the last 1024 bytes) can only
		// be turned into an offset once the total size is known, and the
		// bridge doesn't know it until the device says so. Players use
		// this to read an MP4's trailing index, so it has to work: one
		// cheap probe read gets the size, then the real read follows.
		_, total, _, ok := api.fetchMediaRange(w, r, token, 0, 1)
		if !ok {
			return
		}
		offset = total - spec.suffixLen
		if offset < 0 {
			offset = 0
		}
	}

	content, total, mime, ok := api.fetchMediaRange(w, r, token, offset, length)
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
		api.writeWholeFile(w, r, token, content, total)
		return
	}

	end := offset + int64(len(content)) - 1
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, end, total))
	w.Header().Set("Content-Length", strconv.FormatInt(int64(len(content)), 10))
	w.WriteHeader(http.StatusPartialContent)
	w.Write(content)
}

// writeWholeFile continues a no-Range response past the first span. Best
// effort: a client that goes away mid-file just ends the loop.
func (api *API) writeWholeFile(w http.ResponseWriter, r *http.Request, token string, first []byte, total int64) {
	if _, err := w.Write(first); err != nil {
		return
	}
	for offset := int64(len(first)); offset < total; {
		content, _, _, ok := api.fetchMediaRange(nil, r, token, offset, maxProxiedRange)
		if !ok || len(content) == 0 {
			return
		}
		if _, err := w.Write(content); err != nil {
			return
		}
		offset += int64(len(content))
	}
}

// fetchMediaRange asks the device for one span. w may be nil for
// continuation reads, where the response has already started and there is
// no longer any status code left to send.
func (api *API) fetchMediaRange(w http.ResponseWriter, r *http.Request, token string, offset, length int64) (content []byte, total int64, mime string, ok bool) {
	req := &pb.ReqEnvelope{
		Id: 1,
		Payload: &pb.ReqEnvelope_ReqGetMediaRange{
			ReqGetMediaRange: &pb.ReqGetMediaRange{
				Token:  token,
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

	respFrame, err := api.websocket.ForwardOneOff(r.Host, frame)
	if err != nil {
		// Same reasoning as proxyStaticAsset's own unreachable case: the
		// device is temporarily absent, not broken. A player gets a
		// status it can retry on rather than a hung request.
		log.Debug("device unreachable while streaming media:", r.Host, err)
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
		return rangeSpec{}, fmt.Errorf("unsupported range unit: %q", header)
	}
	first, _, _ := strings.Cut(spec, ",")
	startText, endText, ok := strings.Cut(strings.TrimSpace(first), "-")
	if !ok {
		return rangeSpec{}, fmt.Errorf("malformed range: %q", header)
	}

	if startText == "" {
		lenText := strings.TrimPrefix(strings.TrimSpace(first), "-")
		n, err := strconv.ParseInt(lenText, 10, 64)
		if err != nil || n <= 0 {
			return rangeSpec{}, fmt.Errorf("malformed suffix range: %q", header)
		}
		return rangeSpec{present: true, suffixLen: n}, nil
	}

	start, err := strconv.ParseInt(startText, 10, 64)
	if err != nil || start < 0 {
		return rangeSpec{}, fmt.Errorf("malformed range start: %q", header)
	}

	out := rangeSpec{present: true, start: start}
	if endText = strings.TrimSpace(endText); endText != "" {
		end, err := strconv.ParseInt(endText, 10, 64)
		if err != nil || end < start {
			return rangeSpec{}, fmt.Errorf("malformed range end: %q", header)
		}
		out.end, out.hasEnd = end, true
	}

	return out, nil
}
