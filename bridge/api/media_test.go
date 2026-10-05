// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	pb "github.com/alonsovidales/otc/proto/generated"
	"google.golang.org/protobuf/proto"
)

// Issue #110: the Range header is the whole contract between a video
// player and this proxy - misreading one means either the wrong bytes or
// a player that refuses to start.
func TestParseRange(t *testing.T) {
	for _, tc := range []struct {
		name      string
		header    string
		wantErr   bool
		present   bool
		start     int64
		end       int64
		hasEnd    bool
		suffixLen int64
	}{
		{name: "no header means the whole file", header: ""},
		// What every browser sends to open a video.
		{name: "open-ended from the start", header: "bytes=0-", present: true},
		{name: "a seek lands mid-file", header: "bytes=1048576-2097151", present: true, start: 1048576, end: 2097151, hasEnd: true},
		// A player probing an MP4's header asks for a few bytes;
		// answering with megabytes breaks the contract and wastes the
		// bandwidth this feature exists to save.
		{name: "a short probe keeps its end", header: "bytes=0-31", present: true, end: 31, hasEnd: true},
		{name: "an end before the start is malformed", header: "bytes=100-50", wantErr: true},
		// Players read an MP4's trailing index this way when the moov
		// atom is at the end, so this has to be understood rather than
		// treated as a start offset of zero.
		{name: "suffix range", header: "bytes=-1024", present: true, suffixLen: 1024},
		{name: "multi-range takes the first span", header: "bytes=100-199,300-399", present: true, start: 100, end: 199, hasEnd: true},
		{name: "whitespace is tolerated", header: " bytes=42- ", present: true, start: 42},
		{name: "a unit that isn't bytes is refused", header: "items=0-1", wantErr: true},
		{name: "a missing dash is malformed", header: "bytes=100", wantErr: true},
		{name: "a negative start is malformed", header: "bytes=-0", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseRange(tc.header)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseRange(%q) = %+v, want an error", tc.header, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRange(%q): %v", tc.header, err)
			}
			if got.present != tc.present || got.start != tc.start || got.suffixLen != tc.suffixLen ||
				got.end != tc.end || got.hasEnd != tc.hasEnd {
				t.Errorf("parseRange(%q) = %+v, want {present:%v start:%d end:%d hasEnd:%v suffixLen:%d}",
					tc.header, got, tc.present, tc.start, tc.end, tc.hasEnd, tc.suffixLen)
			}
		})
	}
}

// fakeMediaDevice answers ReqGetMediaRange over file like the device's
// mediastream.Server.Range, recording each length asked for.
func fakeMediaDevice(file []byte, lengths *[]int64) func(string, []byte) ([]byte, error) {
	return func(_ string, frame []byte) ([]byte, error) {
		var req pb.ReqEnvelope
		if err := proto.Unmarshal(frame, &req); err != nil {
			return nil, err
		}
		rq := req.GetReqGetMediaRange()
		*lengths = append(*lengths, rq.Length)
		size := int64(len(file))
		if rq.Offset < 0 || rq.Offset > size {
			return proto.Marshal(&pb.RespEnvelope{Id: req.Id, Error: true})
		}
		n := rq.Length
		if n <= 0 || n > maxProxiedRange {
			n = maxProxiedRange
		}
		n = min(n, size-rq.Offset)
		return proto.Marshal(&pb.RespEnvelope{Id: req.Id, Payload: &pb.RespEnvelope_RespMediaRange{RespMediaRange: &pb.RespMediaRange{
			Content: file[rq.Offset : rq.Offset+n], Offset: rq.Offset, TotalSize: size, Mime: "video/mp4",
		}}})
	}
}

// HEAD answers exactly what GET would, from one byte per device read
// instead of the whole file or range (net/http throws a HEAD's body
// away, so streaming it would only spend the device's uplink).
func TestMediaHeadAnswersLikeGetWithoutTheBody(t *testing.T) {
	file := make([]byte, 2*maxProxiedRange+123)
	for i := range file {
		file[i] = byte(i)
	}
	size := int64(len(file))
	for _, rng := range []string{"", "bytes=0-", "bytes=0-31", "bytes=5000000-", "bytes=-100", "bytes=-99999999",
		fmt.Sprintf("bytes=%d-", size), fmt.Sprintf("bytes=%d-", size+10), "items=0-1"} {
		var getLens, headLens []int64
		do := func(method string, lengths *[]int64) *httptest.ResponseRecorder {
			api := &API{forwardOneOff: fakeMediaDevice(file, lengths)}
			req := httptest.NewRequest(method, "/media/tok", nil)
			req.SetPathValue("token", "tok")
			if rng != "" {
				req.Header.Set("Range", rng)
			}
			rec := httptest.NewRecorder()
			api.proxyMedia(rec, req)
			return rec
		}
		get, head := do(http.MethodGet, &getLens), do(http.MethodHead, &headLens)
		if get.Code != head.Code {
			t.Errorf("%q: HEAD %d, GET %d", rng, head.Code, get.Code)
		}
		for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges"} {
			if get.Header().Get(h) != head.Header().Get(h) {
				t.Errorf("%q: %s HEAD %q, GET %q", rng, h, head.Header().Get(h), get.Header().Get(h))
			}
		}
		if head.Body.Len() != 0 {
			t.Errorf("%q: HEAD wrote %d body bytes", rng, head.Body.Len())
		}
		if len(headLens) > 2 {
			t.Errorf("%q: HEAD made %d device reads", rng, len(headLens))
		}
		for _, n := range headLens {
			if n != 1 {
				t.Errorf("%q: HEAD read %d bytes from the device, want 1", rng, n)
			}
		}
		// And GET still delivers what it promised.
		if get.Code == http.StatusOK && !bytes.Equal(get.Body.Bytes(), file) {
			t.Errorf("%q: GET body is %d bytes, want the whole file", rng, get.Body.Len())
		}
		if cl := get.Header().Get("Content-Length"); get.Code == http.StatusPartialContent && cl != strconv.Itoa(get.Body.Len()) {
			t.Errorf("%q: GET wrote %d bytes, Content-Length %s", rng, get.Body.Len(), cl)
		}
	}
}
