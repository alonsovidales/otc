package social

import (
	"fmt"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/files_manager"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/session"
	"os"
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

func (sc *Social) NewPublication(ses *session.Session, text string, paths []string) (uuid string, err error) {
	hashes := make([]string, len(paths))
	for i, path := range paths {
		file, err := sc.filesmanager.GetFile(ses, path)
		if err != nil {
			log.Error("Error loading file:", err)
		}

		hashes[i] = file.Hash
		unencPath := fmt.Sprintf("%s/%s", cfg.GetStr("otc", "unenc-storage-path"), file.Hash)
		err = os.WriteFile(unencPath, file.Content, 0644) // perms: rw-r--r--
	}
	sc.dao.NewSocialPublication(text, hashes)

	return uuid, nil
}
