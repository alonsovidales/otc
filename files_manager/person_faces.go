// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"errors"

	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/session"
)

// RemoveFromPerson is People's "Not this person" (toPersonID empty) and
// "Someone else…" (toPersonID set): the faces of personID found in the
// contents hashes leave that person - see dao.RemoveFromPerson and the
// RemoveFromPerson proto message. The photos are never touched.
//
// Under faceRefsMu, as every change to faces is: processFaces can't match
// a new face against references this is changing, nor add one to a person
// it deletes. Then both people's references are built again from their own
// faces - a moved face is no longer the person's it left, and may become
// the other's; one given to no one is nobody's reference - and their covers
// worked out again over them, as processFaces does whenever a person's
// references change. A person left with no face keeps no references.
func (mg *Manager) RemoveFromPerson(ses *session.Session, personID string, hashes []string, toPersonID string) error {
	mg.faceRefsMu.Lock()
	defer mg.faceRefsMu.Unlock()
	moved, err := mg.dao.RemoveFromPerson(personID, hashes, toPersonID)
	if err != nil {
		if !errors.Is(err, dao.ErrNoSuchPerson) {
			// Rolled back, or a commit whose outcome is unknown: the set
			// is built again from what is stored.
			mg.faceRefs = nil
		}
		return err
	}
	if moved == 0 {
		return nil
	}
	// The apps take the photos out of the person's grid and lower their
	// have by them, as after a delete: no search page is served again
	// across this (searchCursor.resume), or they would come back.
	mg.searchTokens.noteDelete()
	log.Debug("moved", moved, "face(s) off a person")
	mg.rebuildPeopleLocked(ses, personID, toPersonID)
	return nil
}

// rebuildPeopleLocked builds these people's references afresh from their
// stored faces, puts them in the matching set (when it is built; a person
// with none left is taken out of it) and works their covers out again over
// them. A person whose faces can't be read keeps the cover the database
// has, and the whole set is built again before the next match. The empty
// id (no one) is skipped. The caller holds faceRefsMu.
func (mg *Manager) rebuildPeopleLocked(ses *session.Session, ids ...string) {
	for _, id := range ids {
		if id == "" {
			continue
		}
		if ses == nil {
			// Nothing to decrypt with: built when next needed.
			mg.faceRefs = nil
			continue
		}
		faces, err := mg.dao.ListPersonFaceEmbeddings(id)
		if err != nil {
			log.Error("could not read a person's faces again, reading everyone's before the next match:", err)
			mg.faceRefs = nil
			continue
		}
		built := faceRefs{}
		built.addStored(ses, faces)
		p := built[id]
		if mg.faceRefs != nil {
			if p != nil {
				mg.faceRefs[id] = p
			} else {
				delete(mg.faceRefs, id)
			}
			delete(mg.faceRefsStale, id)
		}
		updatePersonCoverFace(mg.dao, id, p)
	}
}
