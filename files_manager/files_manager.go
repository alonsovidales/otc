package filesmanager

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
	"google.golang.org/protobuf/types/known/timestamppb"
	"net/http"
	"os"
)

const (
	// CRegenerateGroupKey Endpoint used to regenerate the security key for
	// a shard
	CGet = "/get"
)

// Manager Structure that provides HTTP access to manage all the different
// groups and shards on each grorup
type Manager struct {
	baseUrl string
	dao     *dao.Dao
}

func Init(baseUrl string, dao *dao.Dao) *Manager {
	mg := &Manager{
		baseUrl: baseUrl,
		dao:     dao,
	}

	return mg
}

// RegenerateGroupKey Creates a new random key for a group
func (mg *Manager) Get(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")

	file := r.FormValue("file")

	w.WriteHeader(200)
	w.Write([]byte(fmt.Sprintf("Good : %s", file)))
}

func (mg *Manager) ListFiles(session *session.Session, globbing bool, path string) (files []*pb.File, err error) {
	return mg.dao.GetFilesByPath(globbing, path)
}

func (mg *Manager) GetFile(session *session.Session, path string) (file *pb.File, err error) {
	file, err = mg.dao.GetFileByPath(path)
	if err == nil {
		encContent, err := os.ReadFile(fmt.Sprintf("%s/%s", cfg.GetStr("otc", "storage-path"), file.Hash))
		if err != nil {
			log.Error("error reading file from:", path, err)
		}
		file.Content, err = session.Decrypt(encContent)
		if err != nil {
			log.Error("error decryptinig the datra", err)
		}
	}

	return
}

func (mg *Manager) DelFile(session *session.Session, path string) (err error) {
	file, err := mg.dao.GetFileByPath(path)
	if err != nil {
		return
	}
	err = mg.dao.DelFileByPath(path)
	if err != nil {
		return
	}
	err = os.Remove(fmt.Sprintf("%s/%s", cfg.GetStr("otc", "storage-path"), file.Hash))
	return
}

func (mg *Manager) UploadFile(session *session.Session, path string, content []byte, forceOverride bool) (file *pb.File, err error) {
	mimeType := http.DetectContentType(content)
	log.Debug("Mime type: ", mimeType)

	// Calculate the SHA256 of the file to be used as unique hash
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	log.Debug("Calculated Hash:", hash)

	file = &pb.File{
		Created:  timestamppb.Now(),
		Modified: timestamppb.Now(),
		Path:     path,
		Mime:     mimeType,
		Hash:     hash,
		Size:     int32(len(content)),
	}

	duplicated, err := mg.dao.StoreNewFile(file)
	if err != nil {
		return
	}

	if duplicated {
		if forceOverride {
			mg.DelFile(session, path)
			_, err = mg.dao.StoreNewFile(file)
			if err != nil {
				return nil, err
			}
		} else {
			return nil, errors.New("Duplicated file")
		}
	}

	// Write to disk the content
	err = os.WriteFile(fmt.Sprintf("%s/%s", cfg.GetStr("otc", "storage-path"), hash), session.Encrypt(content), 0644) // perms: rw-r--r--

	return
}
