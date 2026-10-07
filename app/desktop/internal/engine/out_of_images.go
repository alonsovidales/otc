// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// Issue #192: a folder kept out of Images. Its photos and videos still go
// to the device and Files shows them, but the device never tags them,
// searches them for faces or shows them in Images. It is asked for when a
// folder is added (the tray's question, `otc-sync add|backup|add-remote
// --keep-out-of-images`) or later from the folder's menu (`otc-sync
// images`), and recorded in config.json as the folder's OutOfImages: a
// request the engine sends once the folder's device path exists - at the
// start of the folder's passes and at connect - until the device
// acknowledges it, and then never again. Unlike SetUploadOnly it is not
// re-sent at every start: that would undo a change the owner made since
// from the web or a phone. As SyncModel's applyOutOfImages on the Mac.

// The words every client uses for it (OutOfImagesText on the Mac).
const (
	// OutOfImagesExplain is the one explanation, wherever it is offered.
	OutOfImagesExplain = "Photos and videos here aren't tagged, searched for faces or shown in Images. Files still shows them."
	// OutOfImagesAddCaption is the add flow's: the folder may be on the
	// device already (one synced from it, or added again), and keeping it
	// out deletes the tags and faces found there.
	OutOfImagesAddCaption = "Photos and videos in it aren't tagged, searched for faces or shown in Images. Files still has them. Any tags and faces already found in them are deleted."
	// OutOfImagesNeedsUpdate: a device before release 108.
	OutOfImagesNeedsUpdate = "Your device needs an update to keep folders out of Images."
)

// ImagesState is how a synced folder stands with Images on the device
// (SyncPaths.outOfImagesState on the Mac).
type ImagesState string

const (
	// ImagesUnknown: not known yet (the device hasn't listed its folders
	// kept out, or can't): the menu shows nothing.
	ImagesUnknown ImagesState = ""
	// ImagesShown: shown in Images.
	ImagesShown ImagesState = "shown"
	// ImagesKeptOut: kept out of Images by its own flag.
	ImagesKeptOut ImagesState = "kept_out"
	// ImagesKeptOutByParent: a folder above it is kept out, which covers it
	// (FolderStatus.OutOfImagesBy names it).
	ImagesKeptOutByParent ImagesState = "by_parent"
	// ImagesKeeping and ImagesShowing: a request not acknowledged yet.
	ImagesKeeping ImagesState = "keeping"
	ImagesShowing ImagesState = "showing"
	// ImagesUnsupported: a request the device can't take - it needs an
	// update; sent by itself once it has one.
	ImagesUnsupported ImagesState = "unsupported"
)

// Kept reports whether the state shows the folder as kept out (or about
// to be).
func (s ImagesState) Kept() bool {
	return s == ImagesKeptOut || s == ImagesKeptOutByParent || s == ImagesKeeping
}

// OutOfImagesState is a folder's ImagesState: devicePath is its device
// path with the trailing slash, folders what ListOutOfImages answered
// (each with its slash; nil while unknown), pending the request not yet
// acknowledged, supported whether the device can (nil while unknown).
// With a folder above it kept out, by is that folder's path without the
// slash. Paths compare byte for byte, as the device does (issue #172), so
// /kim/ never covers /kimono/.
func OutOfImagesState(devicePath string, folders []string, pending, supported *bool) (state ImagesState, by string) {
	unsupported := supported != nil && !*supported
	switch {
	case pending != nil && unsupported:
		return ImagesUnsupported, ""
	case unsupported:
		return ImagesUnknown, ""
	case pending != nil && *pending:
		return ImagesKeeping, ""
	case pending != nil:
		return ImagesShowing, ""
	case folders == nil:
		return ImagesUnknown, ""
	}
	own := false
	parent := ""
	for _, f := range folders {
		if f == devicePath {
			own = true
		} else if strings.HasPrefix(devicePath, f) && len(f) > len(parent) {
			parent = f
		}
	}
	switch {
	case parent != "":
		// Above its own flag: showing it would be refused until the folder
		// above is shown.
		return ImagesKeptOutByParent, strings.TrimSuffix(parent, "/")
	case own:
		return ImagesKeptOut, ""
	}

	return ImagesShown, ""
}

// unknownPayload: the device is older than the request (release 108 for
// these), which it says with error_code "unknown_payload" - or, before
// that code existed, the bare message.
func unknownPayload(resp *pb.RespEnvelope) bool {
	return resp != nil && resp.Error && (resp.ErrorCode == "unknown_payload" || resp.ErrorMessage == "unknown payload")
}

// outOfImagesRequestLocked is folder id's pending request and its device
// path (with the slash); ok is false when the folder isn't configured.
// e.mu must be held.
func (e *Engine) outOfImagesRequestLocked(id string) (want *bool, devicePath string, ok bool) {
	if e.cfg == nil {
		return nil, "", false
	}
	for _, f := range e.cfg.Folders {
		if f.ID == id {
			return f.OutOfImages, e.remotePathFor(f.Path) + "/", true
		}
	}
	for _, f := range e.cfg.RemoteFolders {
		if f.ID == id {
			return f.OutOfImages, strings.TrimRight(f.RemotePath, "/") + "/", true
		}
	}

	return nil, "", false
}

// imagesUnsupportedLocked: the device said it can't; e.mu must be held.
func (e *Engine) imagesUnsupportedLocked() bool {
	return e.imagesSupported != nil && !*e.imagesSupported
}

// imagesTimeout bounds one SetOutOfImages or ListOutOfImages: keeping a
// folder out deletes its tags and faces before the device answers, which
// takes seconds even for a large library.
const imagesTimeout = 2 * time.Minute

// applyOutOfImages sends folder id's pending request until the device has
// acknowledged what is pending now, and returns once it has, or once
// sending is pointless (nothing pending, not connected, a device that
// can't), or failed. A request already under way for the folder is waited
// for, not sent twice: a pass that starts meanwhile carries on once the
// flag is there.
//
// The answer decides what happens to the request:
//   - ok: cleared - compare-and-clear, in config.json too, so a change the
//     tray or the command line made meanwhile goes next instead;
//   - unknown_payload: kept, and the device marked as unable; it goes by
//     itself after the device is updated (the next connect);
//   - out_of_images_by_parent: dropped, with the device's message shown on
//     the folder - showing it can't happen until the folder above is shown;
//   - anything else (the link dropping included): kept for the folder's
//     next pass, the error logged once - unless the request was changed
//     while this one was under way: the change goes now.
func (e *Engine) applyOutOfImages(id string) {
	e.mu.Lock()
	if ch := e.imagesBusy[id]; ch != nil {
		e.mu.Unlock()
		<-ch

		return
	}
	ch := make(chan struct{})
	e.imagesBusy[id] = ch
	e.mu.Unlock()
	// The value this call is through with - its request failed, or only
	// config.json was left to clear: it stops there unless the request
	// changes meanwhile.
	var through *bool
	for {
		connected := e.ws.IsConnected()
		e.mu.Lock()
		want, path, ok := e.outOfImagesRequestLocked(id)
		acked, diskOnly := e.imagesAcked[id]
		if diskOnly && (want == nil || *want != acked) {
			delete(e.imagesAcked, id) // another request since, or none
			diskOnly = false
		}
		if !ok || want == nil || (through != nil && *through == *want) ||
			(!diskOnly && (e.imagesUnsupportedLocked() || !connected)) {
			// Let go in the same hold as the look that found nothing more
			// to send: a request recorded after it finds the folder free
			// and goes by itself, instead of waiting on this call.
			delete(e.imagesBusy, id)
			close(ch)
			e.mu.Unlock()

			return
		}
		sent := *want
		domain := e.cfg.Domain
		e.mu.Unlock()
		if diskOnly || path == "/" {
			// diskOnly: the device has it already, only config.json still
			// says otherwise (clearOutOfImagesRequest). "/": the device's
			// whole library, which it refuses to keep out - nothing to
			// send, ever.
			e.clearOutOfImagesRequest(id, sent)
			through = &sent

			continue
		}
		resp, err := e.requestWithin(imagesTimeout, func(r *pb.ReqEnvelope) {
			r.Payload = &pb.ReqEnvelope_ReqSetOutOfImages{ReqSetOutOfImages: &pb.SetOutOfImages{Path: path, OutOfImages: sent}}
		})
		e.mu.Lock()
		_, _, still := e.outOfImagesRequestLocked(id)
		sameDevice := e.cfg.Domain == domain
		e.mu.Unlock()
		through = &sent
		switch {
		case !still || !sameDevice:
			// Removed, or another device: the answer is not about it.
		case err != nil:
			e.noteImagesError(id, path, err.Error())
		case unknownPayload(resp):
			log.Printf("the device can't keep folders out of Images yet (it needs an update): %s stays pending", path)
			e.setImagesSupported(false)
		case resp.Error && resp.ErrorCode == "out_of_images_by_parent":
			e.clearOutOfImagesRequest(id, sent)
			e.mu.Lock()
			e.imagesNote[id] = resp.ErrorMessage
			delete(e.imagesErr, id)
			e.mu.Unlock()
			// The folder above that keeps it out, for the menu.
			e.refreshOutOfImages()
		case resp.Error:
			msg := resp.ErrorMessage
			if msg == "" {
				msg = "refused"
			}
			e.noteImagesError(id, path, msg)
		default:
			e.setImagesSupported(true)
			e.mu.Lock()
			delete(e.imagesErr, id)
			delete(e.imagesNote, id)
			e.mu.Unlock()
			e.clearOutOfImagesRequest(id, sent)
			e.refreshOutOfImages()
			through = nil
		}
		// What is pending now - a change made while this one was under way -
		// goes next.
	}
}

// noteImagesError logs a failed request once per distinct error, not at
// every pass (as markUploadOnly).
func (e *Engine) noteImagesError(id, path, msg string) {
	e.mu.Lock()
	logIt := e.imagesErr[id] != msg
	e.imagesErr[id] = msg
	e.mu.Unlock()
	if logIt {
		log.Printf("could not keep %s out of Images, or show it there (tried again later): %s", path, msg)
	}
}

func (e *Engine) setImagesSupported(on bool) {
	e.mu.Lock()
	changed := e.imagesSupported == nil || *e.imagesSupported != on
	e.imagesSupported = &on
	if !on {
		e.imagesFolders = nil
	}
	e.mu.Unlock()
	if changed {
		e.notify()
	}
}

// saveConfig writes config.json; a variable, so a test can make it fail.
var saveConfig = (*config.Config).Save

// clearOutOfImagesRequest drops folder id's request once the device has
// answered it - in config.json first, then in memory, each only while it
// still holds the value sent (config.ClearOutOfImagesRequest).
//
// When config.json can't be written, the request stays in memory, as it
// is on disk, marked as answered (imagesAcked): nothing sends it again,
// the menu shows the device's state, and a reload of config.json - any
// tray or command-line edit - finds the value it already had, not a new
// request. The clear is tried again at the folder's next pass and at each
// reload (changedOutOfImagesLocked). Cleared from memory alone, the reload
// would bring it back as new and send it again, undoing a change the
// owner made since from the web or a phone. Only a restart before any of
// those retries succeeds sends it once more.
func (e *Engine) clearOutOfImagesRequest(id string, sent bool) {
	if err := clearOutOfImagesOnDisk(id, sent); err != nil {
		e.mu.Lock()
		_, already := e.imagesAcked[id]
		e.imagesAcked[id] = sent
		e.mu.Unlock()
		if !already {
			log.Printf("could not record that the device has the Images setting of folder %s (tried again later): %v", id, err)
		}
		e.notify()

		return
	}
	e.mu.Lock()
	e.cfg.ClearOutOfImagesRequest(id, sent)
	delete(e.imagesAcked, id)
	e.mu.Unlock()
	e.notify()
}

// clearOutOfImagesOnDisk is config.ClearOutOfImagesRequest on config.json,
// read again just before, so whatever the tray or the command line wrote
// meanwhile stays. A read or a write that races another process's
// rename-over (Windows) is tried again.
func clearOutOfImagesOnDisk(id string, sent bool) error {
	var err error
	for try := 0; try < 3; try++ {
		if try > 0 {
			time.Sleep(50 * time.Millisecond)
		}
		var cfg *config.Config
		if cfg, err = config.Load(); err != nil {
			continue
		}
		if !cfg.ClearOutOfImagesRequest(id, sent) {
			return nil // another request since, or the folder gone
		}
		if err = saveConfig(cfg); err == nil {
			return nil
		}
	}

	return err
}

// imagesAtConnect asks what the device keeps out of Images, then sends
// the requests still pending. At every connect, whatever the device said
// before: updating it restarts it, so one that couldn't may now.
func (e *Engine) imagesAtConnect() {
	e.mu.Lock()
	if e.imagesUnsupportedLocked() {
		e.imagesSupported = nil
	}
	e.mu.Unlock()
	e.refreshOutOfImages()
	e.applyPendingOutOfImages()
}

// applyPendingOutOfImages sends every folder's pending request, one after
// another: at connect (a device updated since says it can now), and when
// config.json brings a new one.
func (e *Engine) applyPendingOutOfImages(ids ...string) {
	if len(ids) == 0 {
		e.mu.Lock()
		for _, f := range e.cfg.Folders {
			if f.OutOfImages != nil {
				ids = append(ids, f.ID)
			}
		}
		for _, f := range e.cfg.RemoteFolders {
			if f.OutOfImages != nil {
				ids = append(ids, f.ID)
			}
		}
		e.mu.Unlock()
	}
	for _, id := range ids {
		e.applyOutOfImages(id)
	}
}

// refreshOutOfImages asks the device which folders it keeps out of Images
// - what the menu shows next to each folder. At connect, after each
// acknowledged request and with the one-minute poll (a change made from
// the web or a phone shows within a minute).
func (e *Engine) refreshOutOfImages() {
	if !e.ws.IsConnected() {
		return
	}
	e.mu.Lock()
	gen := e.imagesGen
	e.mu.Unlock()
	resp, err := e.requestWithin(imagesTimeout, func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqListOutOfImages{ReqListOutOfImages: &pb.ListOutOfImages{}}
	})
	if err != nil {
		return
	}
	e.mu.Lock()
	if e.imagesGen != gen {
		e.mu.Unlock() // another device or password meanwhile

		return
	}
	switch list, ok := resp.Payload.(*pb.RespEnvelope_RespOutOfImagesFolders); {
	case unknownPayload(resp):
		f := false
		e.imagesSupported = &f
		e.imagesFolders = nil
	case resp.Error || !ok:
		// A failure on the device: what was known stays.
	default:
		t := true
		e.imagesSupported = &t
		e.imagesFolders = append([]string{}, list.RespOutOfImagesFolders.GetPaths()...)
		// A refusal to show a folder inside one kept out is about that
		// folder above: once it is shown (from the web or a phone), the
		// refusal no longer holds.
		for id := range e.imagesNote {
			if state, _, _ := e.imagesStateLocked(id); state != ImagesKeptOutByParent {
				delete(e.imagesNote, id)
			}
		}
	}
	e.mu.Unlock()
	e.notify()
}

// forgetOutOfImagesLocked: another device or password - nothing known about
// the old one counts. imagesAcked stays: a request the device answered is
// cleared from config.json whichever device comes next, as it would have
// been had the write not failed. e.mu must be held.
func (e *Engine) forgetOutOfImagesLocked() {
	e.imagesGen++
	e.imagesFolders = nil
	e.imagesSupported = nil
	e.imagesErr = map[string]string{}
	e.imagesNote = map[string]string{}
}

// changedOutOfImagesLocked lists the folders cfg asks something new of,
// compared with old: they are sent at once. A folder new to the config is
// not one of them - its first pass sends it before anything else. Their
// last refusal is forgotten. Listed too, with nothing to send, a folder
// whose answered request config.json still holds (imagesAcked): its clear
// is written again. e.mu must be held.
func (e *Engine) changedOutOfImagesLocked(old, cfg *config.Config) []string {
	before := map[string]*bool{}
	for _, f := range old.Folders {
		before[f.ID] = f.OutOfImages
	}
	for _, f := range old.RemoteFolders {
		before[f.ID] = f.OutOfImages
	}
	var ids []string
	check := func(id string, now *bool) {
		if acked, ok := e.imagesAcked[id]; ok {
			if now != nil && *now == acked {
				// The value the device already has, still on disk: not a
				// request, whatever else this reload changed.
				ids = append(ids, id)

				return
			}
			delete(e.imagesAcked, id) // another request, or none
		}
		was, existed := before[id]
		if !existed || now == nil || (was != nil && *was == *now) {
			return
		}
		delete(e.imagesNote, id)
		delete(e.imagesErr, id)
		ids = append(ids, id)
	}
	for _, f := range cfg.Folders {
		check(f.ID, f.OutOfImages)
	}
	for _, f := range cfg.RemoteFolders {
		check(f.ID, f.OutOfImages)
	}

	return ids
}

// imagesStateLocked is folder id's ImagesState, and the folder above that
// keeps it out; ok is false when it isn't configured. A request the device
// has answered, which only config.json still holds, is not pending. e.mu
// must be held.
func (e *Engine) imagesStateLocked(id string) (state ImagesState, by string, ok bool) {
	pending, path, ok := e.outOfImagesRequestLocked(id)
	if !ok {
		return ImagesUnknown, "", false
	}
	if acked, answered := e.imagesAcked[id]; answered && pending != nil && *pending == acked {
		pending = nil
	}
	state, by = OutOfImagesState(path, e.imagesFolders, pending, e.imagesSupported)

	return state, by, true
}

// imagesStatusLocked fills a folder's Images fields of the snapshot: the
// device's refusal only while a folder above still keeps it out. e.mu must
// be held.
func (e *Engine) imagesStatusLocked(fs *config.FolderStatus) {
	state, by, ok := e.imagesStateLocked(fs.ID)
	if !ok {
		return
	}
	fs.OutOfImages, fs.OutOfImagesBy = string(state), by
	if state == ImagesKeptOutByParent {
		fs.OutOfImagesNote = e.imagesNote[fs.ID]
	}
}

// requestWithin is request with a shorter timeout than a transfer's.
func (e *Engine) requestWithin(d time.Duration, build func(*pb.ReqEnvelope)) (*pb.RespEnvelope, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()

	return e.ws.Request(ctx, build)
}
