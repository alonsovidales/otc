package filesmanager

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
	"golang.org/x/image/draw"
	"google.golang.org/protobuf/types/known/timestamppb"
	"image"
	"image/jpeg"
	_ "image/jpeg"
	"net/http"
	"os"
	"runtime"
)

const (
	// CRegenerateGroupKey Endpoint used to regenerate the security key for
	// a shard
	CGet = "/get"
)

// Manager Structure that provides HTTP access to manage all the different
// groups and shards on each grorup
type Manager struct {
	baseUrl    string
	dao        *dao.Dao
	maxUploads chan bool
}

func Init(baseUrl string, dao *dao.Dao) *Manager {
	mg := &Manager{
		baseUrl:    baseUrl,
		dao:        dao,
		maxUploads: make(chan bool, runtime.NumCPU()-2), // Leave two CPUs free for other stuff
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
	fullPath := fmt.Sprintf("%s/%s", cfg.GetStr("otc", "storage-path"), file.Hash)
	if err = os.Remove(fullPath); err != nil {
		return err
	}
	os.Remove(fmt.Sprintf("%s_thumbnail", fullPath))
	return
}

func (mg *Manager) UploadFile(session *session.Session, path string, content []byte, forceOverride bool) (file *pb.File, err error) {
	mimeType := http.DetectContentType(content)
	log.Debug("Mime type:", mimeType)

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

	// Limit the amounth of concurrent writes
	mg.maxUploads <- true

	go func() {
		defer func() { <-mg.maxUploads }()

		// Write to disk the content
		targetPath := fmt.Sprintf("%s/%s", cfg.GetStr("otc", "storage-path"), hash)
		err = os.WriteFile(targetPath, session.Encrypt(content), 0644) // perms: rw-r--r--

		// We will try to create a thumbnail of images only
		if mimeType[:5] == "image" {
			img, _, err := image.Decode(bytes.NewReader(content))
			if err != nil {
				log.Error("error decoding the image:", err)
				return
			}
			imgCfg, _, err := image.DecodeConfig(bytes.NewReader(content))
			if err != nil {
				log.Error("error decoding image config:", err)
				return
			}
			maxWidth := int(cfg.GetInt("otc", "max-thumbnail-width-px"))
			if imgCfg.Width > maxWidth {
				newH := int(float64(imgCfg.Height) * float64(maxWidth) / float64(imgCfg.Width))
				dst := image.NewRGBA(image.Rect(0, 0, maxWidth, newH))
				draw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Over, nil)
				var buf bytes.Buffer
				jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 80})
				log.Debug("Thumbnail:", fmt.Sprintf("%s_thumbnail", targetPath))
				err = os.WriteFile(fmt.Sprintf("%s_thumbnail", targetPath), session.Encrypt(buf.Bytes()), 0644)
			}
		}
	}()

	return
}
