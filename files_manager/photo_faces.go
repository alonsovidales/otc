// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
)

// PhotoFaces is the faces found in the photo at path, for the viewer's Info
// (GetPhotoFaces; owner, 2026-11-01, from a user: "see what face it has,
// especially on new images ... and be able to remove faces from there"):
// left to right, each with its person, its crop decrypted with ses's key
// and its box. None while face recognition is off (on false). A face is
// taken off its person by RemoveFromPerson, with the photo's hash.
func (mg *Manager) PhotoFaces(ses *session.Session, path string, on bool) (*pb.PhotoFaces, error) {
	out := &pb.PhotoFaces{FaceRecognition: on}
	if !on {
		return out, nil
	}
	file, err := mg.dao.GetFileByPath(path)
	if err != nil {
		return nil, err
	}
	out.Hash = file.Hash
	faces, err := mg.dao.FacesInPhoto(file.Hash)
	if err != nil {
		return nil, err
	}
	for _, f := range faces {
		face := &pb.PhotoFace{PersonId: f.PersonID, Name: f.Name, X: int32(f.X), Y: int32(f.Y), W: int32(f.W), H: int32(f.H)}
		if ses != nil && len(f.Thumbnail) > 0 {
			if plain, err := ses.Decrypt(f.Thumbnail); err == nil {
				face.Thumbnail = plain
			} else {
				// Listed without its crop: the apps show a blank face.
				log.Error("a face's crop could not be decrypted:", err)
			}
		}
		out.Faces = append(out.Faces, face)
	}
	return out, nil
}
