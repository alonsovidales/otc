// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Pets (release 180): the dogs and cats the pet models find in the photos
// (files_manager/pets.go). Each pet is a person in People like anyone - its
// faces are faces rows whose kind says what they are (1 a dog, 2 a cat; 0,
// a person's face) - and pet_scans keeps the photos looked at, so each is
// looked at once. See db.sql.

// Face kinds (faces.kind).
const (
	FaceKindPerson = 0
	FaceKindDog    = 1
	FaceKindCat    = 2
)

// PetsEnabled is whether Pets is on for this library (settings.pets_enabled,
// off unless the user turns it on since release 182). ErrSchemaBehind before
// the release that added it, which callers read as off.
func (dao *Dao) PetsEnabled() (on bool, err error) {
	err = dao.db.QueryRow("select `pets_enabled` from `settings`").Scan(&on)
	return on, schemaBehind(err)
}

// SetPetsEnabled turns Pets on or off. Nothing is deleted: the pets found
// stay in People until deleted there.
func (dao *Dao) SetPetsEnabled(on bool) error {
	_, err := dao.db.Exec("update `settings` set `pets_enabled` = ?", on)
	return schemaBehind(err)
}

// SetPetScanned records that model looked at hash for pets.
// ErrSchemaBehind without the table.
func (dao *Dao) SetPetScanned(hash, model string) error {
	_, err := dao.db.Exec("insert into `pet_scans` (`hash`, `model`, `made`) values (?, ?, ?)"+
		" on duplicate key update `model` = values(`model`), `made` = values(`made`)",
		hash, model, time.Now().UTC())
	return schemaBehindTable(err)
}

// PetScanned is whether model looked at hash for pets. ErrSchemaBehind
// without the table.
func (dao *Dao) PetScanned(hash, model string) (bool, error) {
	var one int
	err := dao.db.QueryRow("select 1 from `pet_scans` where `hash` = ? and `model` = ?", hash, model).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, schemaBehindTable(err)
}

// DelPetScans forgets that hashes were looked at for pets: content nothing
// uses any more, or kept out of Images (whose faces went too: shown again,
// it is looked at again). Without the table there is nothing to forget.
func (dao *Dao) DelPetScans(hashes ...string) error {
	for _, chunk := range hashChunks(hashes) {
		ph, args := inPlaceholders(chunk)
		_, err := dao.db.Exec("delete from `pet_scans` where `hash` in ("+ph+")", args...)
		if isNoSuchTable(err) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// AddPetFace is AddFace for a pet: kind is FaceKindDog or FaceKindCat, the
// box is the whole animal's, embedding the pet model's (opaque here) and
// thumbnail its crop, both sealed by the caller.
func (dao *Dao) AddPetFace(id, hash, personID string, kind, x, y, w, h int, embedding, thumbnail []byte, quality float64) (err error) {
	tx, err := dao.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(
		"insert into `faces` (`id`, `hash`, `person_id`, `bbox_x`, `bbox_y`, `bbox_w`, `bbox_h`, `embedding`, `thumbnail`, `created`, `quality`, `kind`) "+
			"values (?, ?, ?, ?, ?, ?, ?, ?, ?, now(), ?, ?)",
		id, hash, personID, x, y, w, h, embedding, thumbnail, quality, kind); err != nil {
		return schemaBehind(err)
	}
	if _, err = execIfByHand(tx, "delete from `people_by_hand` where `person_id` = ? and `hash` = ?", personID, hash); err != nil {
		return err
	}
	return tx.Commit()
}

// PlacedFace is a face the face model found in a photo: its id, whose it
// is (PersonID "": no one's), whether that person has a name, and where.
type PlacedFace struct {
	ID, PersonID string
	Named        bool
	Box          FaceBox
}

// PersonFacesIn is the faces the face model found in hash (kind 0),
// whoever's they are: Pets gives the people its detector saw there one
// each, and takes off its unnamed person a face that is a pet's own
// (files_manager/pets.go).
func (dao *Dao) PersonFacesIn(hash string) ([]PlacedFace, error) {
	rows, err := dao.db.Query("select `f`.`id`, `f`.`person_id`, coalesce(`p`.`name`, '') <> '', `f`.`bbox_x`, `f`.`bbox_y`, `f`.`bbox_w`, `f`.`bbox_h` from `faces` as `f`"+
		" left join `people` as `p` on `p`.`id` = `f`.`person_id`"+
		" where `f`.`hash` = ? and `f`.`kind` = 0 order by `f`.`id`", hash)
	if err != nil {
		return nil, schemaBehind(err)
	}
	defer rows.Close()
	var out []PlacedFace
	for rows.Next() {
		var f PlacedFace
		if err := rows.Scan(&f.ID, &f.PersonID, &f.Named, &f.Box.X, &f.Box.Y, &f.Box.W, &f.Box.H); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// PetEmbedding is a stored pet face's identity, for matching a pet newly
// found against: FaceEmbedding with its kind.
type PetEmbedding struct {
	FaceEmbedding
	Kind int
}

// ListPetEmbeddings is ListFaceEmbeddings for the pets: every pet face of
// someone (person_id "" is no one's, never matched against).
func (dao *Dao) ListPetEmbeddings() ([]PetEmbedding, error) {
	return dao.petEmbeddings("select `id`, `person_id`, `embedding`, `kind` from `faces` where `person_id` <> '' and `kind` <> 0")
}

// ListPersonPetEmbeddings is ListPetEmbeddings for one person's pet faces.
func (dao *Dao) ListPersonPetEmbeddings(personID string) ([]PetEmbedding, error) {
	if personID == "" {
		return nil, nil
	}
	return dao.petEmbeddings("select `id`, `person_id`, `embedding`, `kind` from `faces` where `person_id` = ? and `kind` <> 0", personID)
}

func (dao *Dao) petEmbeddings(query string, args ...any) ([]PetEmbedding, error) {
	rows, err := dao.db.Query(query, args...)
	if err != nil {
		return nil, schemaBehind(err)
	}
	defer rows.Close()
	var out []PetEmbedding
	for rows.Next() {
		var p PetEmbedding
		if err := rows.Scan(&p.ID, &p.PersonID, &p.Embedding, &p.Kind); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PetFace is a pet's face as Pets groups them (files_manager/pet_groups.go):
// whose it is, whether they have a name, its photo, its kind and its
// embedding (sealed).
type PetFace struct {
	ID, PersonID, Hash string
	Kind               int
	Named              bool
	Embedding          []byte
}

// ListPetFaces is every pet face of someone (person_id "" is no one's,
// never grouped), in id order.
func (dao *Dao) ListPetFaces() ([]PetFace, error) {
	rows, err := dao.db.Query("select `f`.`id`, `f`.`person_id`, `f`.`hash`, `f`.`kind`, coalesce(`p`.`name`, '') <> '', `f`.`embedding` from `faces` as `f`" +
		" left join `people` as `p` on `p`.`id` = `f`.`person_id`" +
		" where `f`.`person_id` <> '' and `f`.`kind` <> 0 order by `f`.`id`")
	if err != nil {
		return nil, schemaBehind(err)
	}
	defer rows.Close()
	var out []PetFace
	for rows.Next() {
		var f PetFace
		if err := rows.Scan(&f.ID, &f.PersonID, &f.Hash, &f.Kind, &f.Named, &f.Embedding); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// GivePetFaces makes the pet faces ids personID's: some of a pet's faces
// another pet's, as Pets groups them (files_manager/pet_groups.go). Only
// pets' faces of someone move (a person's face, or one taken off its
// person, never does). As RemoveFromPerson moves faces: where personID was
// added by hand to one of their photos that row goes (a face of theirs is
// there now, never both), a face that was its pet's cover is no one's
// cover now, and a pet left with no face, no name and nowhere added by
// hand goes.
func (dao *Dao) GivePetFaces(ids []string, personID string) error {
	if personID == "" || len(ids) == 0 {
		return nil
	}
	tx, err := dao.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	from := map[string]bool{}
	for _, chunk := range hashChunks(ids) {
		ph, args := inPlaceholders(chunk)
		rows, err := tx.Query("select `person_id` from `faces` where `id` in ("+ph+") and `person_id` <> '' and `kind` <> 0 for update", args...)
		if err != nil {
			return schemaBehind(err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			from[id] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if _, err := execIfByHand(tx, "delete from `people_by_hand` where `person_id` = ? and `hash` in (select `hash` from `faces` where `id` in ("+ph+"))", append([]any{personID}, args...)...); err != nil {
			return fmt.Errorf("dropping where the pet was added by hand: %w", err)
		}
		if _, err := tx.Exec("update `faces` set `person_id` = ? where `id` in ("+ph+") and `person_id` <> '' and `kind` <> 0", append([]any{personID}, args...)...); err != nil {
			return fmt.Errorf("moving the pet's faces: %w", err)
		}
		if _, err := tx.Exec("update `people` set `cover_face_id` = null, `cover_chosen` = (`cover_image` is not null) where `cover_face_id` in ("+ph+") and `id` <> ?", append(args, personID)...); err != nil {
			return fmt.Errorf("clearing covers: %w", err)
		}
	}
	delete(from, personID)
	if len(from) > 0 {
		left := make([]string, 0, len(from))
		for id := range from {
			left = append(left, id)
		}
		sort.Strings(left)
		ph, args := inPlaceholders(left)
		if _, err := byHandExec(tx, "delete from `people` where `id` in ("+ph+")"+cNoOtherRefs, args...); err != nil {
			return fmt.Errorf("deleting the pets left with no face: %w", err)
		}
	}
	return tx.Commit()
}

// PeopleNamed is which of ids are people now, each with whether they have
// a name: Pets checks a grouping still holds before it files it
// (files_manager/pet_groups.go) - the owner may have named, merged or
// deleted a pet meanwhile.
func (dao *Dao) PeopleNamed(ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, chunk := range hashChunks(ids) {
		ph, args := inPlaceholders(chunk)
		rows, err := dao.db.Query("select `id`, `name` <> '' from `people` where `id` in ("+ph+")", args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			var named bool
			if err := rows.Scan(&id, &named); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = named
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
