// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"errors"
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
// on unless the user turns it off). ErrSchemaBehind before the release that
// added it, which callers read as off.
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

// PlacedFace is a person's face in a photo: its id, whose it is and where.
type PlacedFace struct {
	ID, PersonID string
	Box          FaceBox
}

// UnnamedFacesIn is the faces the face model found in hash (kind 0) that
// belong to a person with no name - the faces Pets may take off for being
// a pet's own (files_manager/pets.go).
func (dao *Dao) UnnamedFacesIn(hash string) ([]PlacedFace, error) {
	rows, err := dao.db.Query("select `f`.`id`, `f`.`person_id`, `f`.`bbox_x`, `f`.`bbox_y`, `f`.`bbox_w`, `f`.`bbox_h` from `faces` as `f`"+
		" join `people` as `p` on `p`.`id` = `f`.`person_id`"+
		" where `f`.`hash` = ? and `f`.`kind` = 0 and `p`.`name` = '' order by `f`.`id`", hash)
	if err != nil {
		return nil, schemaBehind(err)
	}
	defer rows.Close()
	var out []PlacedFace
	for rows.Next() {
		var f PlacedFace
		if err := rows.Scan(&f.ID, &f.PersonID, &f.Box.X, &f.Box.Y, &f.Box.W, &f.Box.H); err != nil {
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
