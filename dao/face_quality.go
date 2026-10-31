// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

// The face the device chooses for a person (owner, 2026-10-31: "it would
// be better to get the best looking one"): faces.quality is how good each
// face looks, measured when it is found (AddFace) or, for one found before
// release 157, by a background pass after a sign-in
// (files_manager/face_covers.go); BestFaces ranks a person's faces by it and
// by their photo's own score (On this day's NIMA), and files_manager keeps
// the best of those that are clearly them.

// faceScoreSQL ranks a measured face: its own looks for most of it, and
// its photo's aesthetic score (NIMA's 1 to 10 mean, 3 to 7 mapped to 0 to
// 1; a photo not scored yet counts as middling) for the rest.
const faceScoreSQL = "(0.8 * `f`.`quality` + 0.2 * coalesce(least(greatest((`q`.`aesthetic` - 3) / 4, 0), 1), 0.5))"

// FaceCandidate is one of a person's faces as BestFaces ranks them: its
// embedding still encrypted, its own quality and its rank's score.
type FaceCandidate struct {
	ID        string
	Embedding []byte
	Quality   float64
	Score     float64
}

// BestFaces is personID's measured faces, best-looking first, at most
// limit.
func (dao *Dao) BestFaces(personID string, limit int) ([]FaceCandidate, error) {
	rows, err := dao.db.Query(
		"select `f`.`id`, `f`.`embedding`, `f`.`quality`, "+faceScoreSQL+" as `score` from `faces` as `f`"+
			" left join `photo_quality` as `q` on `q`.`hash` = `f`.`hash`"+
			" where `f`.`person_id` = ? and `f`.`quality` is not null order by `score` desc, `f`.`id` limit ?",
		personID, limit)
	if err != nil {
		return nil, schemaBehind(err)
	}
	defer rows.Close()
	var out []FaceCandidate
	for rows.Next() {
		var c FaceCandidate
		if err := rows.Scan(&c.ID, &c.Embedding, &c.Quality, &c.Score); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// FaceToMeasure is a face found before its looks were measured: its crop
// (encrypted) and the size of its box in the photo.
type FaceToMeasure struct {
	ID        string
	Thumbnail []byte
	W, H      int
}

// FacesToMeasure is up to limit faces of someone with no quality yet,
// after the face id `after`, in id order.
func (dao *Dao) FacesToMeasure(after string, limit int) ([]FaceToMeasure, error) {
	rows, err := dao.db.Query(
		"select `id`, `thumbnail`, `bbox_w`, `bbox_h` from `faces` where `quality` is null and `person_id` <> '' and `id` > ? order by `id` limit ?",
		after, limit)
	if err != nil {
		return nil, schemaBehind(err)
	}
	defer rows.Close()
	var out []FaceToMeasure
	for rows.Next() {
		var f FaceToMeasure
		if err := rows.Scan(&f.ID, &f.Thumbnail, &f.W, &f.H); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// CountFacesToMeasure is how many faces FacesToMeasure has left.
func (dao *Dao) CountFacesToMeasure() (n int64, err error) {
	err = dao.db.QueryRow("select count(*) from `faces` where `quality` is null and `person_id` <> ''").Scan(&n)
	return n, schemaBehind(err)
}

// SetFaceQuality records how good a face looks.
func (dao *Dao) SetFaceQuality(id string, quality float64) error {
	_, err := dao.db.Exec("update `faces` set `quality` = ? where `id` = ?", quality, id)
	return err
}

// FaceQualityDone says whether the pass over the faces found before
// release 157 has run.
func (dao *Dao) FaceQualityDone() (done bool, err error) {
	err = dao.db.QueryRow("select `face_quality_done` from `settings`").Scan(&done)
	return done, schemaBehind(err)
}

// SetFaceQualityDone records that it has.
func (dao *Dao) SetFaceQualityDone() error {
	_, err := dao.db.Exec("update `settings` set `face_quality_done` = 1")
	return schemaBehind(err)
}

// PeopleWithDeviceCover is every person whose face the device chooses
// (the owner chose none).
func (dao *Dao) PeopleWithDeviceCover() ([]string, error) {
	rows, err := dao.db.Query("select `id` from `people` where `cover_chosen` = 0")
	if err != nil {
		return nil, schemaBehind(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
