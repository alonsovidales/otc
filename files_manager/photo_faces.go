// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"strings"

	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
)

// PhotoFaces is the faces found in the photo at path, for the viewer's Info
// (GetPhotoFaces; owner, 2026-11-01, from a user: "see what face it has,
// especially on new images ... and be able to remove faces from there"):
// left to right, each with its person, its crop decrypted with ses's key
// and its box; then the people added to it by hand (owner, 2026-11-09:
// "add people to the photos and videos by hand"), in the order they were
// added, each with the face People shows for them and no box. A video has
// only those. None while face recognition is off (on false). A face, or a
// person added by hand, is taken off its person by RemoveFromPerson, with
// the content's hash; AddToPerson adds one (adds_people), to a photo or a
// video Images shows: content kept out of Images has no people, as it has
// no faces, and dao.AddToPerson leaves it out.
func (mg *Manager) PhotoFaces(ses *session.Session, path string, on bool) (*pb.PhotoFaces, error) {
	out := &pb.PhotoFaces{FaceRecognition: on, AddsPeople: true}
	if !on {
		return out, nil
	}
	file, err := mg.dao.GetFileByPath(path)
	if err != nil {
		return nil, err
	}
	out.Hash = file.Hash
	media := strings.HasPrefix(file.Mime, "image/") || strings.HasPrefix(file.Mime, "video/")
	out.AddsPeople = media && !mg.keptOutOfImages(file.Hash)
	faces, err := mg.dao.FacesInPhoto(file.Hash)
	if err != nil {
		return nil, err
	}
	found := map[string]bool{}
	for _, f := range faces {
		found[f.PersonID] = true
		face := &pb.PhotoFace{PersonId: f.PersonID, Name: f.Name, X: int32(f.X), Y: int32(f.Y), W: int32(f.W), H: int32(f.H)}
		// Listed without its crop when it won't open: the apps show a
		// blank face.
		if face.Thumbnail, err = openCrop(ses, f.Thumbnail); err != nil {
			log.Error("a face's crop could not be decrypted:", err)
		}
		out.Faces = append(out.Faces, face)
	}
	byHand, err := mg.dao.PeopleByHandIn(file.Hash)
	if err != nil {
		return nil, err
	}
	for _, p := range byHand {
		if found[p.PersonID] {
			// A face of theirs takes the row's place where it is stored
			// (dao.AddFace and the moves): them there, listed once.
			continue
		}
		found[p.PersonID] = true
		face := &pb.PhotoFace{PersonId: p.PersonID, Name: p.Name, ByHand: true}
		if face.Thumbnail, err = openCrop(ses, p.Thumbnail); err != nil {
			log.Error("the face of a person added by hand could not be decrypted:", err)
		}
		out.Faces = append(out.Faces, face)
	}
	return out, nil
}

// openCrop is a face's picture stored encrypted at rest, decrypted with
// ses's key; nil when there is none, or it won't open (err).
func openCrop(ses *session.Session, sealed []byte) ([]byte, error) {
	if ses == nil || len(sealed) == 0 {
		return nil, nil
	}
	plain, err := ses.Decrypt(sealed)
	if err != nil {
		return nil, err
	}
	return plain, nil
}
