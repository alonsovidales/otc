// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/session"
)

// Issue #192: folders kept out of Images. Photos and videos under a
// flagged folder still get their thumbnails (Files shows them), but are
// never tagged or searched for faces, and Images - the photo search, its
// date buckets, collections - leaves them out. Tags, faces and thumbnails
// are keyed by content hash, and one hash can be held by several paths,
// kept versions included, so the rule is about content:
//
//	content is kept out when a row of it (files or file_versions) is under
//	a flagged folder and no row is outside them all.
//
// Kept-out content has no tags and no faces - they are deleted when it
// becomes kept out (a folder flagged, its last shown path deleted) and
// never worked out while it is - and is recorded in skipped_analysis; the
// tag list and People are then right without a change to their queries.
// Content a path outside the flagged folders holds is analysed as before,
// and shown in Images once, through that path - unless that path is only
// a kept version's: Images never lists versions, so such content keeps
// tags and faces (#132) that the tag list and People show while Images
// doesn't (a known gap). When a skipped one is
// shown again (a folder unflagged, a copy or move out of one), it goes
// back to pending_analysis and the lanes.
//
// Every decision and record of it happens under outOfImagesMu: the
// analysis guard's check-and-mark, flagging and unflagging, and
// reconcileAnalysis, which runs after anything that changes which paths
// hold a hash. Without it a folder shown again while an analysis job was
// about to mark its content skipped would leave that content unanalysed
// for good. Lock order: a hash's lock, then outOfImagesMu, then
// faceRefsMu; every caller has released its hash lock first.

// cMaxFolderPath is the longest folder path a flag takes, before its
// slash: the column holds 768.
const cMaxFolderPath = 767

// outOfImagesFolder validates path for SetOutOfImages and returns it as
// out_of_images_folders stores it, with its trailing slash. Never the
// whole library: root has no row in any listing, so no client sends it.
func outOfImagesFolder(path string) (string, error) {
	p := strings.TrimSuffix(path, "/")
	switch {
	case !strings.HasPrefix(path, "/"):
		return "", errors.New("the folder must be a full path")
	case p == "":
		return "", errors.New("the whole library can't be kept out of Images")
	case len(p) > cMaxFolderPath:
		return "", errors.New("the folder's path is too long")
	case strings.ContainsRune(p, 0):
		return "", errors.New("the folder's path is not valid")
	}
	for _, c := range strings.Split(p[1:], "/") {
		if c == "" || c == "." || c == ".." {
			return "", errors.New("the folder's path is not valid")
		}
	}
	return p + "/", nil
}

// OutOfImagesByParentError is SetOutOfImages' refusal to show a folder
// inside another one kept out of Images (#186's rule for upload-only
// folders): the outer one still covers it, so an "ok" would lie. Parent is
// the nearest flagged folder above it; OwnFlag, that the folder is flagged
// itself too, so it stays out once Parent is shown and needs showing
// after it.
type OutOfImagesByParentError struct {
	Folder, Parent string
	OwnFlag        bool
}

func (e *OutOfImagesByParentError) Error() string {
	name := filepath.Base(strings.TrimSuffix(e.Folder, "/"))
	parent := strings.TrimSuffix(e.Parent, "/")
	if e.OwnFlag {
		return fmt.Sprintf("%s is inside %s, which is kept out of Images - show %s in Images first, then %s", name, parent, parent, name)
	}
	return fmt.Sprintf("%s is inside %s, which is kept out of Images - show %s in Images to show %s", name, parent, parent, name)
}

// OutOfImagesFolders is the folders kept out of Images, each with its
// trailing slash, from memory once read. The slice is shared: callers
// never change it.
func (mg *Manager) OutOfImagesFolders() ([]string, error) {
	if c := mg.outOfImagesCache.Load(); c != nil {
		return *c, nil
	}
	mg.outOfImagesMu.Lock()
	defer mg.outOfImagesMu.Unlock()
	return mg.outOfImagesFoldersLocked()
}

// outOfImagesFoldersLocked is OutOfImagesFolders for a caller holding
// outOfImagesMu.
func (mg *Manager) outOfImagesFoldersLocked() ([]string, error) {
	if c := mg.outOfImagesCache.Load(); c != nil {
		return *c, nil
	}
	return mg.reloadOutOfImagesLocked()
}

// reloadOutOfImagesLocked reads the flagged folders into the cache after
// a write; on an error the cache is emptied, so the next use reads again.
func (mg *Manager) reloadOutOfImagesLocked() ([]string, error) {
	folders, err := mg.dao.GetOutOfImagesFolders()
	if err != nil {
		mg.outOfImagesCache.Store(nil)
		return nil, err
	}
	if folders == nil {
		folders = []string{}
	}
	mg.outOfImagesCache.Store(&folders)
	return folders, nil
}

// noOutOfImages is whether no folder is kept out, as far as memory knows -
// every device that doesn't use the feature: nothing to decide, no lock.
func (mg *Manager) noOutOfImages() bool {
	c := mg.outOfImagesCache.Load()
	return c != nil && len(*c) == 0
}

// IsOutOfImages is whether path (a file, or a folder given with or
// without its slash) is, or is inside, a folder kept out of Images.
func (mg *Manager) IsOutOfImages(path string, isDir bool) (bool, error) {
	folders, err := mg.OutOfImagesFolders()
	if err != nil {
		return false, err
	}
	return underFolders(path, isDir, folders), nil
}

// SetOutOfImages keeps a folder out of Images, or shows it there again.
//
// Keeping one out is synchronous: once it returns, the tags and faces of
// content only the folder holds are gone, so the tag list and People are
// right at once. Should that cleanup fail, the flag stays on (Images
// already leaves the folder out) and the error is returned; flagging
// again, or the next start (ReconcileOutOfImages), finishes it.
//
// Showing one again is refused with OutOfImagesByParentError when a
// flagged folder above it still covers it, and is a no-op for a folder
// with no flag. Its content is then analysed in the background, as if
// uploaded now: tags when image tagging is on (place tags either way),
// faces when face recognition is on now - an explicit owner action on
// named content, which the clients' confirmation says, not the switch's
// retroactive sweep that #52/#178 rule out.
func (mg *Manager) SetOutOfImages(ses *session.Session, path string, on bool) error {
	folder, err := outOfImagesFolder(path)
	if err != nil {
		return err
	}
	mg.outOfImagesMu.Lock()
	defer mg.outOfImagesMu.Unlock()
	if on {
		if err := mg.dao.SetOutOfImagesFolder(folder, true); err != nil {
			return err
		}
		folders, err := mg.reloadOutOfImagesLocked()
		// A token holds a whole search's results for minutes: a client
		// still scrolling one would keep being handed this folder's photos.
		mg.keepOutOfSearchTokens(folders, err)
		if err != nil {
			return err
		}
		hashes, err := mg.dao.MediaHashesUnder(folder)
		if err != nil {
			return err
		}
		return mg.keepOutLocked(hashes, folders)
	}

	folders, err := mg.outOfImagesFoldersLocked()
	if err != nil {
		return err
	}
	if parent := nearestFolderAbove(folder, folders); parent != "" {
		return &OutOfImagesByParentError{Folder: folder, Parent: parent, OwnFlag: slices.Contains(folders, folder)}
	}
	if !slices.Contains(folders, folder) {
		return nil
	}
	if err := mg.dao.SetOutOfImagesFolder(folder, false); err != nil {
		return err
	}
	folders, err = mg.reloadOutOfImagesLocked()
	mg.keepOutOfSearchTokens(folders, err)
	if err != nil {
		return err
	}
	hashes, err := mg.dao.MediaHashesUnder(folder)
	if err != nil {
		return err
	}
	// Subfolders flagged themselves are still in folders: what only they
	// hold stays out.
	visible, err := mg.dao.VisibleHashes(hashes, folders)
	if err != nil {
		return err
	}
	return mg.handBackLocked(ses, inSet(hashes, visible))
}

// keepOutOfSearchTokens brings the search tokens in line with folders,
// just read (err, its reload's error): every token is dropped when they
// couldn't be.
func (mg *Manager) keepOutOfSearchTokens(folders []string, err error) {
	if err != nil {
		mg.searchTokens.clear()
		return
	}
	mg.searchTokens.keepOut(folders)
}

// inSet is those of hashes in set, in order.
func inSet(hashes []string, set map[string]bool) []string {
	var out []string
	for _, h := range hashes {
		if set[h] {
			out = append(out, h)
		}
	}
	return out
}

// keepOutLocked drops the analysis of those of hashes no path outside
// folders holds. The caller holds outOfImagesMu.
func (mg *Manager) keepOutLocked(hashes, folders []string) error {
	if len(hashes) == 0 {
		return nil
	}
	visible, err := mg.dao.VisibleHashes(hashes, folders)
	if err != nil {
		return err
	}
	var kept []string
	for _, h := range hashes {
		if !visible[h] {
			kept = append(kept, h)
		}
	}
	return mg.dropAnalysisLocked(kept)
}

// dropAnalysisLocked records kept as skipped and deletes their tags and
// faces (crops and embeddings: biometric data, which would also keep
// matching new photos against people the owner chose to hide). An unnamed
// person left with no face goes; a named one stays, with no photos, as
// after deleting their photos. The caller holds outOfImagesMu.
func (mg *Manager) dropAnalysisLocked(kept []string) error {
	if len(kept) == 0 {
		return nil
	}
	// Recorded first: should a delete below fail, showing the content
	// again still hands it back to the lanes, and the next start finishes
	// the cleanup.
	if err := mg.dao.AddSkippedAnalysis(kept); err != nil {
		return err
	}
	if err := mg.dao.DelTagsByHashes(kept); err != nil {
		return err
	}
	mg.faceRefsMu.Lock()
	defer mg.faceRefsMu.Unlock()
	gone, err := mg.dao.DelFacesByHashes(kept)
	if err != nil {
		// Whether the faces went is unknown: build the set again from
		// what is stored.
		mg.faceRefs = nil
		return err
	}
	if len(gone) > 0 {
		mg.forgetFacesLocked(gone)
		log.Debug("removed", len(gone), "face(s) of content kept out of Images")
	}
	return nil
}

// handBackLocked hands back to the lanes those of shown - content a path
// outside the flagged folders holds - recorded as skipped: moved to
// pending_analysis in one go, then queued, so a stop half way leaves the
// rest to ResumePendingAnalysis. Without a session nothing is taken: it
// stays recorded. The caller holds outOfImagesMu.
func (mg *Manager) handBackLocked(ses *session.Session, shown []string) error {
	if len(shown) == 0 || ses == nil {
		return nil
	}
	back, err := mg.dao.TakeSkippedAnalysis(shown)
	if err != nil {
		return err
	}
	for _, h := range back {
		mg.enqueueAnalysis(ses, h)
	}
	return nil
}

// reconcileAnalysis applies the rule to hashes after anything that can
// change which paths hold them - an upload or link of known content, a
// delete, an override, an analysis that just ran: content kept out by now
// loses its tags and faces and is recorded as skipped; content recorded
// as skipped that a shown path holds again goes back to pending_analysis
// and the lanes. ses nil (processMedia's check) never queues anything:
// what is skipped stays recorded for the next caller with a key. Errors
// are logged: the change that got here has already happened.
func (mg *Manager) reconcileAnalysis(ses *session.Session, hashes []string) {
	if mg == nil || mg.dao == nil || mg.noOutOfImages() {
		return
	}
	// A deleted folder's every hash comes here: a set, not a scan per hash.
	var unique []string
	seen := make(map[string]bool, len(hashes))
	for _, h := range hashes {
		if h != "" && !seen[h] {
			seen[h] = true
			unique = append(unique, h)
		}
	}
	if len(unique) == 0 {
		return
	}
	mg.outOfImagesMu.Lock()
	defer mg.outOfImagesMu.Unlock()
	folders, err := mg.outOfImagesFoldersLocked()
	if err != nil {
		log.Error("could not read the folders kept out of Images:", err)
		return
	}
	// With no flag nothing is kept out, and the last flag cleared handed
	// back what it covered; what a failure left is the next start's.
	if len(folders) == 0 {
		return
	}
	visible, err := mg.dao.VisibleHashes(unique, folders)
	if err != nil {
		log.Error("could not check content against the folders kept out of Images:", err)
		return
	}
	under, err := mg.dao.HashesWithRowsUnder(unique, folders)
	if err != nil {
		log.Error("could not check content against the folders kept out of Images:", err)
		return
	}
	var kept []string
	for _, h := range unique {
		if under[h] && !visible[h] {
			kept = append(kept, h)
		}
	}
	if err := mg.dropAnalysisLocked(kept); err != nil {
		log.Error("could not drop the analysis of content kept out of Images:", err)
	}
	if err := mg.handBackLocked(ses, inSet(unique, visible)); err != nil {
		log.Error("could not queue the analysis of content shown in Images again:", err)
	}
}

// guardAnalysis is stages without the analysis when the content hash is
// kept out of Images: the thumbnail is still made (Files shows it), but no
// tags - place tags included, they would reveal where through the tag
// list - and no faces. Content it holds back is recorded as skipped,
// content it lets through forgotten there. When it can't be answered the
// analysis is skipped too (logged, and recorded so the next start checks
// again): never analysed against the owner's wish.
func (mg *Manager) guardAnalysis(hash string, stages mediaStages) mediaStages {
	if stages&stageAnalysis == 0 || mg.dao == nil || mg.noOutOfImages() {
		return stages
	}
	if mg.mayAnalyse(hash) {
		return stages
	}
	return stages &^ stageAnalysis
}

// mayAnalyse is guardAnalysis' decision, made and recorded under
// outOfImagesMu so a folder shown again meanwhile finds what it skipped.
func (mg *Manager) mayAnalyse(hash string) bool {
	mg.outOfImagesMu.Lock()
	defer mg.outOfImagesMu.Unlock()
	folders, err := mg.outOfImagesFoldersLocked()
	if err == nil && len(folders) == 0 {
		return true
	}
	var visible map[string]bool
	if err == nil {
		visible, err = mg.dao.VisibleHashes([]string{hash}, folders)
	}
	if err != nil {
		log.Error("could not check", hash, "against the folders kept out of Images, not analysing it:", err)
	} else if visible[hash] {
		if err := mg.dao.DelSkippedAnalysis(hash); err != nil {
			log.Error("could not forget the skipped analysis of", hash, ":", err)
		}
		return true
	}
	// Recorded only while something still holds it: a file deleted while
	// it waited needs nothing (its job clears its pending row).
	if referenced, refErr := mg.dao.HashReferenced(hash); refErr != nil || referenced {
		if err := mg.dao.AddSkippedAnalysis([]string{hash}); err != nil {
			log.Error("could not record the skipped analysis of", hash, ":", err)
		}
	}
	return false
}

// ReconcileOutOfImages puts right, once per process at the first sign-in
// (before ResumePendingAnalysis), what an interruption left: the cleanup
// of a flagged folder a crash cut short (its content's tags and faces
// deleted again), skipped content a path outside the flagged folders
// holds (an unflag that failed half way: recorded in pending_analysis,
// which ResumePendingAnalysis queues next - it needs the key, hence the
// sign-in) and skipped rows of content that is gone.
func (mg *Manager) ReconcileOutOfImages(ses *session.Session) {
	if mg == nil || mg.dao == nil {
		return
	}
	mg.outOfImagesMu.Lock()
	defer mg.outOfImagesMu.Unlock()
	folders, err := mg.outOfImagesFoldersLocked()
	if err != nil {
		log.Error("could not read the folders kept out of Images:", err)
		return
	}
	for _, f := range folders {
		if nearestFolderAbove(f, folders) != "" {
			continue // covered by the folder above
		}
		hashes, err := mg.dao.MediaHashesUnder(f)
		if err == nil {
			err = mg.keepOutLocked(hashes, folders)
		}
		if err != nil {
			log.Error("could not finish keeping a folder out of Images:", err)
		}
	}

	skipped, err := mg.dao.SkippedAnalysis()
	if err != nil {
		log.Error("could not list the content kept out of Images:", err)
		return
	}
	if len(skipped) == 0 {
		return
	}
	visible, err := mg.dao.VisibleHashes(skipped, folders)
	if err != nil {
		log.Error("could not check the content kept out of Images:", err)
		return
	}
	under, err := mg.dao.HashesWithRowsUnder(skipped, folders)
	if err != nil {
		log.Error("could not check the content kept out of Images:", err)
		return
	}
	var shown, gone []string
	for _, h := range skipped {
		switch {
		case visible[h]:
			shown = append(shown, h)
		case !under[h]:
			gone = append(gone, h)
		}
	}
	if err := mg.dao.DelSkippedAnalysis(gone...); err != nil {
		log.Error("could not forget the skipped analysis of deleted content:", err)
	}
	// Moved to pending_analysis, which ResumePendingAnalysis queues next.
	back, err := mg.dao.TakeSkippedAnalysis(shown)
	if err != nil {
		log.Error("could not hand back the content shown in Images again:", err)
		return
	}
	if len(back) > 0 {
		log.Info(fmt.Sprintf("%d photo(s) or video(s) shown in Images again go back to the analysis", len(back)))
	}
}

// clearOutOfImagesUnder clears the flags at and under folder - a folder
// DelPath deleted. A failure is only logged: the delete has happened.
func (mg *Manager) clearOutOfImagesUnder(folder string) {
	if mg.noOutOfImages() {
		return
	}
	mg.outOfImagesMu.Lock()
	defer mg.outOfImagesMu.Unlock()
	folders, err := mg.outOfImagesFoldersLocked()
	if err != nil {
		log.Error("could not read the folders kept out of Images:", err)
		return
	}
	if !slices.ContainsFunc(folders, func(f string) bool { return strings.HasPrefix(f, folder) }) {
		return
	}
	if err := mg.dao.DelOutOfImagesUnder(folder); err != nil {
		log.Error("could not clear the flags of a deleted folder:", err)
	}
	if _, err := mg.reloadOutOfImagesLocked(); err != nil {
		log.Error("could not read the folders kept out of Images:", err)
	}
}

// foldersNotCovering is folders without those dir is in (dir itself
// included): a gallery shared from a folder leaves out the folders kept
// out of Images below it - sharing a parent shouldn't sweep up a private
// subfolder unnoticed - but a folder kept out and shared itself, or from
// inside one, is shared on purpose and keeps what it holds.
func foldersNotCovering(dir string, folders []string) []string {
	dir = folderPath(dir)
	var out []string
	for _, f := range folders {
		if !strings.HasPrefix(dir, f) {
			out = append(out, f)
		}
	}
	return out
}
