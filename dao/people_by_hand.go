// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// People added by hand (release 179; owner, 2026-11-09: "add people to the
// photos and videos by hand ... select one of the faces that are not
// already in the photo"): a person put in a photo face recognition didn't
// find them in, or in a video, where no face is looked for. A
// people_by_hand row (db.sql) is the person in the content and nothing
// more - no box, crop, embedding or quality - kept apart from faces so it
// never reaches what faces drive: matching (ListFaceEmbeddings), covers
// and medoids (BestFaces, PersonFaceIn), the looks pass, HashHasFaces.
//
// The content is theirs wherever a face makes it so: their photos in
// Images (searchMediaOnlyClauses, estimateMedia, rowClauses, RankMedia),
// People's count (ListPeople), the memories and hiding a person from them
// (FacesOfHashes), Info (PeopleByHandIn). The rows follow their content
// and their person as faces do: gone with the content's last file or kept
// version (DelFacesByHash, the orphan sweep) and when it is kept out of
// Images (DelFacesByHashes), copied to an edit (CopyPeopleByHand), taken
// off or moved by RemoveFromPerson, moved by MergePeople, gone with
// DeletePerson and a fresh Reprocess (WipeTagsAndFaces). A face of the
// person stored in the content takes the row's place (AddFace, CopyFaces
// and the moves, dropByHandUnderFaces), so a person is in a content once;
// an unnamed person keeps living while a row of theirs does.
//
// A database whose release 179 script hasn't run (a per-user one it
// skipped) has no such table: the reads take it as empty (cByHand), the
// deletes have nothing to delete, and AddToPerson is ErrSchemaBehind.

// cByHand stands for people_by_hand in the statements that read it
// (byHandQuery, byHandScan, byHandExec): run with the table, or - on a
// database without it - with byHandNone, so the placeholders and their
// arguments are the same either way. Every use names an alias:
// byHandNone is a derived table.
const cByHand = "{people_by_hand}"

// byHandNone is people_by_hand's columns, empty: faces' own, of the same
// types and collation. Never in a statement that writes `faces`.
const byHandNone = "(select `hash`, `person_id`, `created` from `faces` where 0 = 1)"

// isNoByHand is a statement's failure for want of people_by_hand.
func isNoByHand(err error) bool {
	return isNoSuchTable(err) && strings.Contains(err.Error(), "people_by_hand")
}

// sqlRunner is a *sql.DB or a *sql.Tx.
type sqlRunner interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
	Exec(query string, args ...any) (sql.Result, error)
}

func withByHand(query string) string { return strings.ReplaceAll(query, cByHand, "`people_by_hand`") }

func withoutByHand(query string) string { return strings.ReplaceAll(query, cByHand, byHandNone) }

// byHandQuery runs query in q with people_by_hand as cByHand, or with
// none on a database without the table. A statement in a transaction that
// fails for a missing table changes nothing, and the transaction goes on.
func byHandQuery(q sqlRunner, query string, args ...any) (*sql.Rows, error) {
	rows, err := q.Query(withByHand(query), args...)
	if isNoByHand(err) {
		return q.Query(withoutByHand(query), args...)
	}
	return rows, err
}

// byHandScan is byHandQuery of one row, scanned into dest.
func byHandScan(q sqlRunner, query string, args []any, dest ...any) error {
	err := q.QueryRow(withByHand(query), args...).Scan(dest...)
	if isNoByHand(err) {
		return q.QueryRow(withoutByHand(query), args...).Scan(dest...)
	}
	return err
}

// byHandExec is byHandQuery of a statement that writes another table.
func byHandExec(q sqlRunner, query string, args ...any) (sql.Result, error) {
	res, err := q.Exec(withByHand(query), args...)
	if isNoByHand(err) {
		return q.Exec(withoutByHand(query), args...)
	}
	return res, err
}

func (dao *Dao) byHandQuery(query string, args ...any) (*sql.Rows, error) {
	return byHandQuery(dao.db, query, args...)
}

// execIfByHand runs a statement that writes people_by_hand itself: nothing
// to write on a database without it (0 rows).
func execIfByHand(q sqlRunner, query string, args ...any) (int, error) {
	res, err := q.Exec(query, args...)
	if isNoSuchTable(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// cNoOtherRefs is the condition of the unnamed people deleted when their
// last face goes (DelFacesByHash, RemoveFromPerson): no face, and no one
// added them by hand to anything - that person stays.
const cNoOtherRefs = " and `name` = '' and not exists (select 1 from `faces` where `faces`.`person_id` = `people`.`id`)" +
	" and not exists (select 1 from " + cByHand + " as `bh` where `bh`.`person_id` = `people`.`id`)"

// dropByHandUnderFaces deletes, in q, the rows of people_by_hand that
// where (a condition on its own columns, with args) keeps and whose person
// has a face found in their content: the face is them there.
func dropByHandUnderFaces(q sqlRunner, where string, args ...any) error {
	_, err := execIfByHand(q, "delete from `people_by_hand` where "+where+
		" and exists (select 1 from `faces` where `faces`.`hash` = `people_by_hand`.`hash` and `faces`.`person_id` = `people_by_hand`.`person_id`)", args...)
	return err
}

// cMediaMime is the mimes people can be added to: photos and videos.
const cMediaMime = "(`f`.`mime` like 'image/%' or `f`.`mime` like 'video/%')"

// AddToPerson puts personID in the contents hashes by hand (AddToPerson in
// messages.proto), in one transaction: each distinct hash a file holds as
// a photo or a video, but where a face of theirs was found (their face is
// them there), where they were added before, and content kept out of
// Images, which has no people as it has no faces (skipped_analysis, issue
// #192). The person's row is locked until the commit, so a delete or merge
// can't leave rows of someone who no longer exists: ErrNoSuchPerson when
// they are gone, and nothing changes. added is how many rows were made;
// held whether any of hashes is a photo or video a file holds at all (the
// caller's "no longer on this device"). ErrSchemaBehind on a database
// without people_by_hand.
func (dao *Dao) AddToPerson(personID string, hashes []string) (added int, held bool, err error) {
	if len(hashes) == 0 {
		return 0, false, nil
	}
	tx, err := dao.db.Begin()
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()

	var one int
	err = tx.QueryRow("select 1 from `people` where `id` = ? for update", personID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, ErrNoSuchPerson
	}
	if err != nil {
		return 0, false, fmt.Errorf("looking up the person: %w", err)
	}

	seen := map[string]bool{}
	var unique []string
	for _, h := range hashes {
		if !seen[h] {
			seen[h] = true
			unique = append(unique, h)
		}
	}
	now := time.Now().UTC()
	for _, chunk := range hashChunks(unique) {
		// A plain read, not a locking one: processFaces, which stores
		// faces, is kept out by the caller's faceRefsMu.
		ph, args := inPlaceholders(chunk)
		rows, err := tx.Query("select distinct `f`.`hash`,"+
			" (select 1 from `faces` as `x` where `x`.`person_id` = ? and `x`.`hash` = `f`.`hash` limit 1) is null"+
			" and (select 1 from `skipped_analysis` as `s` where `s`.`hash` = `f`.`hash`) is null"+
			" from `files` as `f` where `f`.`hash` in ("+ph+") and "+cMediaMime, append([]any{personID}, args...)...)
		if err != nil {
			return 0, false, fmt.Errorf("reading the photos and videos: %w", err)
		}
		var add []string
		for rows.Next() {
			var h string
			var free bool
			if err := rows.Scan(&h, &free); err != nil {
				rows.Close()
				return 0, false, err
			}
			held = true
			if free {
				add = append(add, h)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return 0, false, err
		}
		if len(add) == 0 {
			continue
		}
		values := strings.TrimSuffix(strings.Repeat("(?, ?, ?),", len(add)), ",")
		vals := make([]any, 0, 3*len(add))
		for _, h := range add {
			vals = append(vals, h, personID, now)
		}
		res, err := tx.Exec("insert ignore into `people_by_hand` (`hash`, `person_id`, `created`) values "+values, vals...)
		if isNoSuchTable(err) {
			return 0, false, fmt.Errorf("%w: %v", ErrSchemaBehind, err)
		}
		if err != nil {
			return 0, false, fmt.Errorf("adding the person: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, false, err
		}
		added += int(n)
	}
	if added == 0 {
		return 0, held, nil
	}
	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	return added, held, nil
}

// PersonByHand is a person added to a content by hand (PeopleByHandIn):
// their name ("" while unnamed) and the face People shows for them, as
// stored (encrypted at rest; nil when People shows none).
type PersonByHand struct {
	PersonID  string
	Name      string
	Thumbnail []byte
}

// PeopleByHandIn is the people added by hand to the content hash, in the
// order they were added, each with the face ListPeople shows for them
// (personCoverSQL). None on a database without people_by_hand.
func (dao *Dao) PeopleByHandIn(hash string) ([]PersonByHand, error) {
	rows, err := dao.byHandQuery("select `b`.`person_id`, `p`.`name`, "+personCoverSQL+
		" from "+cByHand+" as `b` join `people` as `p` on `p`.`id` = `b`.`person_id`"+
		" where `b`.`hash` = ? order by `b`.`created`, `b`.`person_id`", hash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PersonByHand
	for rows.Next() {
		var p PersonByHand
		if err := rows.Scan(&p.PersonID, &p.Name, &p.Thumbnail); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CopyPeopleByHand gives to the people added by hand to from, in one
// statement, each as added then: an edit (a turn, a crop, a cut, a
// compress) keeps who is in it, as CopyFaces keeps its faces - copied, not
// moved, so a kept original keeps its own for Revert. A person to has
// already, by hand or by a face found there, is left as they are. Answers
// how many were given; none on a database without people_by_hand.
func (dao *Dao) CopyPeopleByHand(from, to string) (int, error) {
	if from == to {
		return 0, nil
	}
	return execIfByHand(dao.db, "insert ignore into `people_by_hand` (`hash`, `person_id`, `created`)"+
		" select ?, `b`.`person_id`, `b`.`created` from `people_by_hand` as `b` where `b`.`hash` = ?"+
		" and not exists (select 1 from `faces` where `faces`.`hash` = ? and `faces`.`person_id` = `b`.`person_id`)", to, from, to)
}

// moveByHand moves, in tx, the rows of people_by_hand of the people ids
// that where (a condition on its own columns, after the person's, with
// args) keeps to the person to - each person in a content once: a row to
// has there already stays, and the one that would join it goes - and
// answers how many of their rows left them. Then a row of to's whose
// content has a face of theirs goes (dropByHandUnderFaces).
func moveByHand(tx sqlRunner, to string, ids []string, where string, args ...any) (int, error) {
	ph, idArgs := inPlaceholders(ids)
	cond := "`person_id` in (" + ph + ")" + where
	condArgs := append(idArgs, args...)
	moved, err := execIfByHand(tx, "update ignore `people_by_hand` set `person_id` = ? where "+cond, append([]any{to}, condArgs...)...)
	if err != nil {
		return 0, fmt.Errorf("moving the people added by hand: %w", err)
	}
	left, err := execIfByHand(tx, "delete from `people_by_hand` where "+cond, condArgs...)
	if err != nil {
		return 0, fmt.Errorf("dropping the people added by hand twice: %w", err)
	}
	if err := dropByHandUnderFaces(tx, "`person_id` = ?"+where, append([]any{to}, args...)...); err != nil {
		return 0, fmt.Errorf("dropping the people added by hand who have a face there: %w", err)
	}
	return moved + left, nil
}
