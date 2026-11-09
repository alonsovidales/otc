// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"errors"
	"fmt"
)

// "Not this person": face recognition matched a photo to the wrong
// person, and the owner takes it off them - or moves it to the right one -
// without touching the photo. A face taken off someone and given to no one
// keeps its row with `person_id` '' (see db.sql's `faces` table): it still
// says the content's faces were found (HashHasFaces), so the photo isn't
// analysed again, and it goes with the content as every face does
// (DelFacesByHash), but it is nobody's - no person counts it, shows it or
// is searched by it, and face matching never learns from it
// (ListFaceEmbeddings leaves it out).

// ErrNoSuchPerson is RemoveFromPerson's answer when the person the faces
// were to go to isn't there (deleted or merged meanwhile), and
// AddToPerson's when the person to add is not. Nothing changed.
var ErrNoSuchPerson = errors.New("no such person")

// RemoveFromPerson takes every face of personID found in the contents
// hashes off that person, in one transaction: to toPersonID when it is
// set (that person must exist, else ErrNoSuchPerson and nothing changes),
// else to no one (""). The people added by hand go the same way
// (people_by_hand.go): taken off, or moved to toPersonID - a row of
// theirs that toPersonID has already there, or where a face of theirs is,
// goes instead. It returns how many faces moved and how many of personID's
// rows added by hand left them; hashes without either add nothing. A
// person whose cover was one of the moved faces gets none (ListPeople then
// shows their oldest face, until files_manager works out a new one), and
// personID, left with no face, goes when unnamed and added by hand to
// nothing - as after DelFacesByHash; a named one stays: the name is the
// owner's.
func (dao *Dao) RemoveFromPerson(personID string, hashes []string, toPersonID string) (faces, byHand int, err error) {
	// The empty id is no one: never a person to take faces from, and
	// moving faces to themselves changes nothing.
	if personID == "" || personID == toPersonID || len(hashes) == 0 {
		return 0, 0, nil
	}
	tx, err := dao.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	if toPersonID != "" {
		// Locked until the commit, so a delete or merge of that person
		// can't leave the faces on someone who no longer exists.
		var one int
		err := tx.QueryRow("select 1 from `people` where `id` = ? for update", toPersonID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, 0, ErrNoSuchPerson
		}
		if err != nil {
			return 0, 0, fmt.Errorf("looking up the person: %w", err)
		}
	}

	var ids []string
	seen := map[string]bool{}
	var unique []string
	for _, h := range hashes {
		if !seen[h] {
			seen[h] = true
			unique = append(unique, h)
		}
	}
	for _, chunk := range hashChunks(unique) {
		ph, args := inPlaceholders(chunk)
		rows, err := tx.Query("select `id` from `faces` where `person_id` = ? and `hash` in ("+ph+") for update", append([]any{personID}, args...)...)
		if err != nil {
			return 0, 0, fmt.Errorf("listing the person's faces: %w", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return 0, 0, err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return 0, 0, err
		}
	}

	for _, chunk := range hashChunks(ids) {
		ph, args := inPlaceholders(chunk)
		if _, err := tx.Exec("update `faces` set `person_id` = ? where `id` in ("+ph+")", append([]any{toPersonID}, args...)...); err != nil {
			return 0, 0, fmt.Errorf("moving faces: %w", err)
		}
		if _, err := tx.Exec("update `people` set `cover_face_id` = null, `cover_chosen` = (`cover_image` is not null) where `cover_face_id` in ("+ph+")", args...); err != nil {
			return 0, 0, fmt.Errorf("clearing covers: %w", err)
		}
	}
	// After the faces: a face moved to toPersonID takes the place of their
	// row in its content.
	for _, chunk := range hashChunks(unique) {
		ph, args := inPlaceholders(chunk)
		var n int
		if toPersonID == "" {
			n, err = execIfByHand(tx, "delete from `people_by_hand` where `person_id` = ? and `hash` in ("+ph+")", append([]any{personID}, args...)...)
		} else {
			n, err = moveByHand(tx, toPersonID, []string{personID}, " and `hash` in ("+ph+")", args...)
		}
		if err != nil {
			return 0, 0, fmt.Errorf("taking off the people added by hand: %w", err)
		}
		byHand += n
	}
	if len(ids) == 0 && byHand == 0 {
		return 0, 0, nil
	}
	if _, err := byHandExec(tx, "delete from `people` where `id` = ?"+cNoOtherRefs, personID); err != nil {
		return 0, 0, fmt.Errorf("deleting a person left with no face: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return len(ids), byHand, nil
}
