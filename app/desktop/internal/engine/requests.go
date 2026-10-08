// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// A folder's requests to the device (config.Request): Keep out of Images
// (issue #192, out_of_images.go) and Upload only (issue #132,
// upload_only.go). Each is recorded in config.json with the folder - when
// it is added, or later from its menu - and sent for the folder's device
// path once it exists: at the start of the folder's passes and at connect,
// until the device acknowledges it, and then never again. Unlike
// markUploadOnly, which makes a backup upload only at every start, they
// are not re-sent: that would undo a change the owner made since from the
// web or a phone. As SyncModel's sendRequest on the Mac.

// requestKind is what tells one kind of request from the other.
type requestKind struct {
	field config.Request
	// failure is what the log says could not be done, with the folder's
	// device path for its %s.
	failure string
	// unsupported is logged when the device answers unknown_payload.
	unsupported string
	timeout     time.Duration
	// byParent is the error code that means the folder above decides: the
	// request is dropped and the device's message shown on the folder.
	byParent string
	// skipRoot: "/" is never sent (the device refuses it) - cleared as done.
	skipRoot bool
	// build sets the request for path (with its slash) on r.
	build func(r *pb.ReqEnvelope, path string, on bool)
	// images: Keep out of Images, whose folders the device lists
	// (ListOutOfImages) - read again after an answer that changes them (an
	// ack, a refusal by the folder above, which the list names), and
	// forgotten when the device says it can't.
	images bool
}

// requestState is what the engine keeps about one kind of request: a
// folder's request under way, the last error logged for it, the device's
// refusal of its last request (byParent), a request the device answered
// that config.json couldn't be cleared of yet (the value), and whether the
// device can (nil until known; forgotten when the device or password
// changes).
type requestState struct {
	busy      map[string]chan struct{}
	err       map[string]string
	note      map[string]string
	acked     map[string]bool
	supported *bool
}

func newRequestState() requestState {
	return requestState{
		busy:  map[string]chan struct{}{},
		err:   map[string]string{},
		note:  map[string]string{},
		acked: map[string]bool{},
	}
}

// state is k's requestState.
func (e *Engine) state(k *requestKind) *requestState {
	if k.field == config.UploadOnlyRequest {
		return &e.upOnly
	}

	return &e.images
}

// requestKinds lists every kind, for what applies to all of them.
func requestKinds() []*requestKind { return []*requestKind{imagesRequest, uploadOnlyRequest} }

// requestLocked is folder id's pending request of kind k and its device
// path (with the slash); ok is false when the folder isn't configured.
// e.mu must be held.
func (e *Engine) requestLocked(k *requestKind, id string) (want *bool, devicePath string, ok bool) {
	if e.cfg == nil {
		return nil, "", false
	}
	for _, f := range e.cfg.Folders {
		if f.ID == id {
			want, _ = e.cfg.PendingRequest(k.field, id)
			return want, e.remotePathFor(f.Path) + "/", true
		}
	}
	for _, f := range e.cfg.RemoteFolders {
		if f.ID == id {
			want, _ = e.cfg.PendingRequest(k.field, id)
			return want, strings.TrimRight(f.RemotePath, "/") + "/", true
		}
	}

	return nil, "", false
}

// unsupportedLocked: the device said it can't; e.mu must be held.
func (s *requestState) unsupportedLocked() bool {
	return s.supported != nil && !*s.supported
}

// applyRequest sends folder id's pending request of kind k until the
// device has acknowledged what is pending now, and returns once it has, or
// once sending is pointless (nothing pending, not connected, a device that
// can't), or failed. A request already under way for the folder is waited
// for, not sent twice: a pass that starts meanwhile carries on once the
// device has it.
//
// The answer decides what happens to the request:
//   - ok: cleared - compare-and-clear, in config.json too, so a change the
//     tray or the command line made meanwhile goes next instead;
//   - unknown_payload: kept, and the device marked as unable; it goes by
//     itself after the device is updated (the next connect);
//   - k.byParent (out_of_images_by_parent, locked_by_parent): dropped, with
//     the device's message shown on the folder - it can't happen until the
//     folder above changes;
//   - anything else (the link dropping included): kept for the folder's
//     next pass, the error logged once - unless the request was changed
//     while this one was under way: the change goes now.
func (e *Engine) applyRequest(k *requestKind, id string) {
	st := e.state(k)
	e.mu.Lock()
	if ch := st.busy[id]; ch != nil {
		e.mu.Unlock()
		<-ch

		return
	}
	ch := make(chan struct{})
	st.busy[id] = ch
	e.mu.Unlock()
	// The value this call is through with - its request failed, or only
	// config.json was left to clear: it stops there unless the request
	// changes meanwhile.
	var through *bool
	for {
		connected := e.ws.IsConnected()
		e.mu.Lock()
		want, path, ok := e.requestLocked(k, id)
		acked, diskOnly := st.acked[id]
		if diskOnly && (want == nil || *want != acked) {
			delete(st.acked, id) // another request since, or none
			diskOnly = false
		}
		if !ok || want == nil || (through != nil && *through == *want) ||
			(!diskOnly && (st.unsupportedLocked() || !connected)) {
			// Let go in the same hold as the look that found nothing more
			// to send: a request recorded after it finds the folder free
			// and goes by itself, instead of waiting on this call.
			delete(st.busy, id)
			close(ch)
			e.mu.Unlock()

			return
		}
		sent := *want
		domain := e.cfg.Domain
		e.mu.Unlock()
		if diskOnly || (k.skipRoot && path == "/") {
			// diskOnly: the device has it already, only config.json still
			// says otherwise (clearRequest). "/": the device's whole
			// library, which it refuses to keep out - nothing to send, ever.
			e.clearRequest(k, id, sent)
			through = &sent

			continue
		}
		resp, err := e.requestWithin(k.timeout, func(r *pb.ReqEnvelope) {
			k.build(r, path, sent)
		})
		e.mu.Lock()
		_, _, still := e.requestLocked(k, id)
		sameDevice := e.cfg.Domain == domain
		e.mu.Unlock()
		through = &sent
		switch {
		case !still || !sameDevice:
			// Removed, or another device: the answer is not about it.
		case err != nil:
			e.noteRequestError(k, id, path, err.Error())
		case unknownPayload(resp):
			log.Printf("%s: %s stays pending", k.unsupported, path)
			e.setRequestSupported(k, false)
		case resp.Error && resp.ErrorCode == k.byParent:
			e.clearRequest(k, id, sent)
			e.mu.Lock()
			st.note[id] = resp.ErrorMessage
			delete(st.err, id)
			e.mu.Unlock()
			if k.images {
				e.refreshOutOfImages() // the folder above, for the menu
			}
		case resp.Error:
			msg := resp.ErrorMessage
			if msg == "" {
				msg = "refused"
			}
			e.noteRequestError(k, id, path, msg)
		default:
			e.setRequestSupported(k, true)
			e.mu.Lock()
			delete(st.err, id)
			delete(st.note, id)
			e.mu.Unlock()
			e.clearRequest(k, id, sent)
			if k.images {
				e.refreshOutOfImages()
			}
			through = nil
		}
		// What is pending now - a change made while this one was under way -
		// goes next.
	}
}

// noteRequestError logs a failed request once per distinct error, not at
// every pass (as markUploadOnly).
func (e *Engine) noteRequestError(k *requestKind, id, path, msg string) {
	st := e.state(k)
	e.mu.Lock()
	logIt := st.err[id] != msg
	st.err[id] = msg
	e.mu.Unlock()
	if logIt {
		log.Printf("could not %s (tried again later): %s", fmt.Sprintf(k.failure, path), msg)
	}
}

func (e *Engine) setRequestSupported(k *requestKind, on bool) {
	st := e.state(k)
	e.mu.Lock()
	changed := st.supported == nil || *st.supported != on
	st.supported = &on
	if !on && k.images {
		e.imagesFolders = nil
	}
	e.mu.Unlock()
	if changed {
		e.notify()
	}
}

// saveConfig writes config.json; a variable, so a test can make it fail.
var saveConfig = (*config.Config).Save

// clearRequest drops folder id's request of kind k once the device has
// answered it - in config.json first, then in memory, each only while it
// still holds the value sent (config.ClearRequest).
//
// When config.json can't be written, the request stays in memory, as it
// is on disk, marked as answered (acked): nothing sends it again, the menu
// shows the device's state, and a reload of config.json - any tray or
// command-line edit - finds the value it already had, not a new request.
// The clear is tried again at the folder's next pass and at each reload
// (changedRequestsLocked). Cleared from memory alone, the reload would
// bring it back as new and send it again, undoing a change the owner made
// since from the web or a phone. Only a restart before any of those
// retries succeeds sends it once more.
func (e *Engine) clearRequest(k *requestKind, id string, sent bool) {
	st := e.state(k)
	if err := clearRequestOnDisk(k.field, id, sent); err != nil {
		e.mu.Lock()
		_, already := st.acked[id]
		st.acked[id] = sent
		e.mu.Unlock()
		if !already {
			log.Printf("could not record that the device has answered folder %s's request (tried again later): %v", id, err)
		}
		e.notify()

		return
	}
	e.mu.Lock()
	e.cfg.ClearRequest(k.field, id, sent)
	delete(st.acked, id)
	e.mu.Unlock()
	e.notify()
}

// clearRequestOnDisk is config.ClearRequest on config.json, read again
// just before, so whatever the tray or the command line wrote meanwhile
// stays. A read or a write that races another process's rename-over
// (Windows) is tried again.
func clearRequestOnDisk(field config.Request, id string, sent bool) error {
	var err error
	for try := 0; try < 3; try++ {
		if try > 0 {
			time.Sleep(50 * time.Millisecond)
		}
		var cfg *config.Config
		if cfg, err = config.Load(); err != nil {
			continue
		}
		if !cfg.ClearRequest(field, id, sent) {
			return nil // another request since, or the folder gone
		}
		if err = saveConfig(cfg); err == nil {
			return nil
		}
	}

	return err
}

// applyPendingRequests sends every folder's pending request of kind k (or
// those of ids), one after another: at connect (a device updated since
// says it can now), and when config.json brings a new one.
func (e *Engine) applyPendingRequests(k *requestKind, ids ...string) {
	if len(ids) == 0 {
		e.mu.Lock()
		for _, f := range e.cfg.Folders {
			if want, _ := e.cfg.PendingRequest(k.field, f.ID); want != nil {
				ids = append(ids, f.ID)
			}
		}
		for _, f := range e.cfg.RemoteFolders {
			if want, _ := e.cfg.PendingRequest(k.field, f.ID); want != nil {
				ids = append(ids, f.ID)
			}
		}
		e.mu.Unlock()
	}
	for _, id := range ids {
		e.applyRequest(k, id)
	}
}

// applyRequestsBeforePass is a pass's first step: the folder's pending
// requests, each waiting for one already under way rather than sending it
// twice. Upload only first: it is what protects the folder.
func (e *Engine) applyRequestsBeforePass(id string) {
	e.applyRequest(uploadOnlyRequest, id)
	e.applyRequest(imagesRequest, id)
}

// forgetRequestsLocked: another device or password - nothing known about
// the old one counts. acked stays: a request the device answered is
// cleared from config.json whichever device comes next, as it would have
// been had the write not failed. e.mu must be held.
func (e *Engine) forgetRequestsLocked(k *requestKind) {
	st := e.state(k)
	st.supported = nil
	st.err = map[string]string{}
	st.note = map[string]string{}
}

// dropRequestsLocked forgets what is kept about a removed folder's
// requests. e.mu must be held.
func (e *Engine) dropRequestsLocked(id string) {
	for _, k := range requestKinds() {
		st := e.state(k)
		delete(st.err, id)
		delete(st.note, id)
		delete(st.acked, id)
	}
}

// changedRequestsLocked lists the folders cfg asks something new of, for
// kind k, compared with old: they are sent at once. A folder new to the
// config is not one of them - its first pass sends it before anything
// else. Their last refusal is forgotten. Listed too, with nothing to send,
// a folder whose answered request config.json still holds (acked): its
// clear is written again. e.mu must be held.
func (e *Engine) changedRequestsLocked(k *requestKind, old, cfg *config.Config) []string {
	st := e.state(k)
	before := map[string]*bool{}
	for _, f := range old.Folders {
		before[f.ID], _ = old.PendingRequest(k.field, f.ID)
	}
	for _, f := range old.RemoteFolders {
		before[f.ID], _ = old.PendingRequest(k.field, f.ID)
	}
	var ids []string
	check := func(id string) {
		now, _ := cfg.PendingRequest(k.field, id)
		if acked, ok := st.acked[id]; ok {
			if now != nil && *now == acked {
				// The value the device already has, still on disk: not a
				// request, whatever else this reload changed.
				ids = append(ids, id)

				return
			}
			delete(st.acked, id) // another request, or none
		}
		was, existed := before[id]
		if !existed || now == nil || (was != nil && *was == *now) {
			return
		}
		delete(st.note, id)
		delete(st.err, id)
		ids = append(ids, id)
	}
	for _, f := range cfg.Folders {
		check(f.ID)
	}
	for _, f := range cfg.RemoteFolders {
		check(f.ID)
	}

	return ids
}

// pendingLocked is folder id's request of kind k as the menu shows it: one
// the device has answered, which only config.json still holds, is not
// pending. ok is false when the folder isn't configured. e.mu must be held.
func (e *Engine) pendingLocked(k *requestKind, id string) (pending *bool, devicePath string, ok bool) {
	pending, devicePath, ok = e.requestLocked(k, id)
	if acked, answered := e.state(k).acked[id]; answered && pending != nil && *pending == acked {
		pending = nil
	}

	return pending, devicePath, ok
}

// unknownPayload: the device is older than the request (release 108 for
// Images), which it says with error_code "unknown_payload" - or, before
// that code existed, the bare message.
func unknownPayload(resp *pb.RespEnvelope) bool {
	return resp != nil && resp.Error && (resp.ErrorCode == "unknown_payload" || resp.ErrorMessage == "unknown payload")
}
