package social

import (
	"fmt"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/files_manager"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/profile"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
	"os"
	"time"
)

type Social struct {
	dao          *dao.Dao
	filesmanager *filesmanager.Manager
}

func Init(dao *dao.Dao, filesmanager *filesmanager.Manager) *Social {
	return &Social{
		dao:          dao,
		filesmanager: filesmanager,
	}
}

func (sc *Social) NewPublication(ses *session.Session, text string, paths []string) (pubUuID string, err error) {
	files := make([]*pb.File, len(paths))
	for i, path := range paths {
		file, err := sc.filesmanager.GetFile(ses, path)
		if err != nil {
			log.Error("Error loading file:", err)
		}

		files[i] = file
		unencPath := fmt.Sprintf("%s/%s", cfg.GetStr("otc", "unenc-storage-path"), file.Hash)
		err = os.WriteFile(unencPath, file.Content, 0644) // perms: rw-r--r--
		if err != nil {
			return "", err
		}

		unEncThumb, err := sc.filesmanager.GetThumbnail(ses, file)
		if err != nil {
			return "", err
		}
		unencPathThumb := fmt.Sprintf("%s/%s_thumbnail", cfg.GetStr("otc", "unenc-storage-path"), file.Hash)
		err = os.WriteFile(unencPathThumb, unEncThumb, 0644) // perms: rw-r--r--
		if err != nil {
			return "", err
		}

		log.Debug("Publication in path:", path)
	}

	return sc.dao.NewSocialPublication(text, files)
}

func (sc *Social) GetPublications(pr *profile.Profile, since time.Time, total int32, own bool) (publications *pb.SocialPublications, err error) {
	publications, err = sc.dao.GetSocialPublications(since, total, own)
	if err != nil {
		log.Debug("error retriving publications", err)
		return
	}

	// Populate the files content
	for _, pub := range publications.Publications {
		pub.Publisher = &pb.Profile{
			Name:  pr.Name,
			Image: pr.Image,
			Text:  pr.Text,
		}

		for _, file := range pub.Files {
			file.Content, err = os.ReadFile(fmt.Sprintf("%s/%s_thumbnail", cfg.GetStr("otc", "unenc-storage-path"), file.Hash))
			if err != nil {
				return nil, err
			}
		}

		pub.Comments, err = sc.dao.GetSocialPublicationComments(pub.Uuid)
		if err != nil {
			return nil, err
		}
	}

	return
}

func (sc *Social) NewSocialComment(pr *profile.Profile, pubUuid, comment string) (err error) {
	return sc.dao.NewComment(pr.Name, pubUuid, comment)
}
