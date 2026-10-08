// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"time"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// Issue #132: an upload-only folder on the device keeps every older
// version of a file and refuses deletes. A backup is made upload only at
// every start (markUploadOnly). A two-way folder added from this computer
// can ask for it too (the tray's options, `otc-sync add --upload-only`):
// recorded in config.json as the folder's UploadOnly, one of its requests
// (requests.go), sent until the device acknowledges it and then never
// again - the owner may lift it later from the web or a phone. What a
// two-way pass does with a delete the device refuses is in
// reconcileRemoteFolder (keptOnDevice). As SyncModel's upload-only
// request on the Mac.

// The words every client uses for it (UploadOnlyText on the Mac).
const (
	// UploadOnlyLabel is the option's name.
	UploadOnlyLabel = "Upload only"
	// UploadOnlyCaption says what it is.
	UploadOnlyCaption = "The device keeps every older version of a file, and nothing in the folder can be deleted on the device."
	// UploadOnlyTwoWay says what it means for a two-way folder, in one
	// sentence.
	UploadOnlyTwoWay = "Files you delete on this computer stay on the device and aren't downloaded again; files added or changed on the device still come down."
	// UploadOnlyLater says how it is undone, and what that brings back
	// (baselinesFor).
	UploadOnlyLater = "You can turn it off later from the web app or the phone app (the lock on the folder in Files); the files you deleted on this computer while it was on then come back here."
	// UploadOnlyBackup is the backup's fixed line: always on.
	UploadOnlyBackup = "Always on for a backup."
	// UploadOnlyNeedsUpdate: a device that answers unknown_payload.
	UploadOnlyNeedsUpdate = "Your device needs an update to make folders upload only."
)

// UploadOnlyState is how a folder's upload-only request stands, for the
// menu and state.json: nothing on its way (the device has it, or nothing
// was asked), a request not acknowledged yet, or one the device can't take.
type UploadOnlyState string

const (
	UploadOnlyNothing UploadOnlyState = ""
	// UploadOnlyMaking: a request to make it upload only, on its way.
	UploadOnlyMaking UploadOnlyState = "making"
	// UploadOnlyLifting: a request to lift it (no menu asks for it; a
	// config.json edited by hand can).
	UploadOnlyLifting UploadOnlyState = "lifting"
	// UploadOnlyUnsupported: the device needs an update; sent by itself
	// once it has one.
	UploadOnlyUnsupported UploadOnlyState = "unsupported"
)

// UploadOnlyRequestState is a folder's UploadOnlyState from its pending
// request and whether the device can (nil while unknown).
func UploadOnlyRequestState(pending, supported *bool) UploadOnlyState {
	switch {
	case pending == nil:
		return UploadOnlyNothing
	case supported != nil && !*supported:
		return UploadOnlyUnsupported
	case *pending:
		return UploadOnlyMaking
	default:
		return UploadOnlyLifting
	}
}

// uploadOnlyRequest is Upload only among a folder's requests.
var uploadOnlyRequest = &requestKind{
	field:       config.UploadOnlyRequest,
	failure:     "make %s upload only",
	unsupported: "the device can't make folders upload only yet (it needs an update)",
	timeout:     uploadOnlyTimeout,
	// Only lifting it is refused that way (issue #186); never asked here.
	byParent: "locked_by_parent",
	build: func(r *pb.ReqEnvelope, path string, on bool) {
		r.Payload = &pb.ReqEnvelope_ReqSetUploadOnly{ReqSetUploadOnly: &pb.SetUploadOnly{Path: path, UploadOnly: on}}
	},
}

// uploadOnlyTimeout bounds one SetUploadOnly: a row in a table.
const uploadOnlyTimeout = time.Minute

// uploadOnlyAtConnect sends the upload-only requests still pending - at
// every connect, whatever the device said before: updating it restarts
// it, so one that couldn't may now.
func (e *Engine) uploadOnlyAtConnect() {
	e.mu.Lock()
	if e.upOnly.unsupportedLocked() {
		e.upOnly.supported = nil
	}
	e.mu.Unlock()
	e.applyPendingRequests(uploadOnlyRequest)
}

// uploadOnlyStatusLocked fills a folder's upload-only fields of the
// snapshot. e.mu must be held.
func (e *Engine) uploadOnlyStatusLocked(fs *config.FolderStatus) {
	pending, _, ok := e.pendingLocked(uploadOnlyRequest, fs.ID)
	if !ok {
		return
	}
	fs.UploadOnly = string(UploadOnlyRequestState(pending, e.upOnly.supported))
	fs.UploadOnlyNote = e.upOnly.note[fs.ID]
}
