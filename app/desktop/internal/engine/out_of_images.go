// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"strings"
	"time"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// Issue #192: a folder kept out of Images. Its photos and videos still go
// to the device and Files shows them, but the device never tags them,
// searches them for faces or shows them in Images. It is asked for when a
// folder from this computer is added (the tray's options, `otc-sync
// add|backup --keep-out-of-images`) or later from the folder's menu
// (`otc-sync images`), and recorded in config.json as the folder's
// OutOfImages: one of the folder's requests (requests.go), sent until the
// device acknowledges it and then never again. As SyncModel's Images
// request on the Mac.

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

// imagesRequest is Keep out of Images among a folder's requests
// (requests.go).
var imagesRequest = &requestKind{
	field:       config.OutOfImagesRequest,
	failure:     "keep %s out of Images, or show it there",
	unsupported: "the device can't keep folders out of Images yet (it needs an update)",
	// Keeping a folder out deletes its tags and faces before the device
	// answers, which takes seconds even for a large library.
	timeout:  imagesTimeout,
	byParent: "out_of_images_by_parent",
	skipRoot: true,
	build: func(r *pb.ReqEnvelope, path string, on bool) {
		r.Payload = &pb.ReqEnvelope_ReqSetOutOfImages{ReqSetOutOfImages: &pb.SetOutOfImages{Path: path, OutOfImages: on}}
	},
	images: true,
}

// imagesTimeout bounds one SetOutOfImages or ListOutOfImages.
const imagesTimeout = 2 * time.Minute

// imagesUnsupportedLocked: the device said it can't; e.mu must be held.
func (e *Engine) imagesUnsupportedLocked() bool {
	return e.images.unsupportedLocked()
}

// applyOutOfImages sends folder id's pending Images request until the
// device has acknowledged it (applyRequest).
func (e *Engine) applyOutOfImages(id string) { e.applyRequest(imagesRequest, id) }

// imagesAtConnect asks what the device keeps out of Images, then sends
// the requests still pending. At every connect, whatever the device said
// before: updating it restarts it, so one that couldn't may now.
func (e *Engine) imagesAtConnect() {
	e.mu.Lock()
	if e.imagesUnsupportedLocked() {
		e.images.supported = nil
	}
	e.mu.Unlock()
	e.refreshOutOfImages()
	e.applyPendingOutOfImages()
}

// applyPendingOutOfImages sends every folder's pending Images request (or
// those of ids), one after another: at connect (a device updated since
// says it can now), and when config.json brings a new one.
func (e *Engine) applyPendingOutOfImages(ids ...string) {
	e.applyPendingRequests(imagesRequest, ids...)
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
		e.images.supported = &f
		e.imagesFolders = nil
	case resp.Error || !ok:
		// A failure on the device: what was known stays.
	default:
		t := true
		e.images.supported = &t
		e.imagesFolders = append([]string{}, list.RespOutOfImagesFolders.GetPaths()...)
		// A refusal to show a folder inside one kept out is about that
		// folder above: once it is shown (from the web or a phone), the
		// refusal no longer holds.
		for id := range e.images.note {
			if state, _, _ := e.imagesStateLocked(id); state != ImagesKeptOutByParent {
				delete(e.images.note, id)
			}
		}
	}
	e.mu.Unlock()
	e.notify()
}

// forgetOutOfImagesLocked: another device or password - nothing known about
// the old one counts (forgetRequestsLocked), and what it keeps out is
// asked again. e.mu must be held.
func (e *Engine) forgetOutOfImagesLocked() {
	e.imagesGen++
	e.imagesFolders = nil
	e.forgetRequestsLocked(imagesRequest)
}

// imagesStateLocked is folder id's ImagesState, and the folder above that
// keeps it out; ok is false when it isn't configured. A request the device
// has answered, which only config.json still holds, is not pending. e.mu
// must be held.
func (e *Engine) imagesStateLocked(id string) (state ImagesState, by string, ok bool) {
	pending, path, ok := e.pendingLocked(imagesRequest, id)
	if !ok {
		return ImagesUnknown, "", false
	}
	state, by = OutOfImagesState(path, e.imagesFolders, pending, e.images.supported)

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
		fs.OutOfImagesNote = e.images.note[fs.ID]
	}
}

// requestWithin is request with a shorter timeout than a transfer's.
func (e *Engine) requestWithin(d time.Duration, build func(*pb.ReqEnvelope)) (*pb.RespEnvelope, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()

	return e.ws.Request(ctx, build)
}
