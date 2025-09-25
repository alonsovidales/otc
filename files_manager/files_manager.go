package filesmanager

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/images_tagger"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
	"github.com/google/uuid"
	"github.com/jdeng/goheif"
	"golang.org/x/image/draw"
	"google.golang.org/protobuf/types/known/timestamppb"
	"image"
	"image/jpeg"
	_ "image/jpeg"
	"io"
	"math"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
)

const (
	CDownloadAttr = "?download="
)

// Manager Structure that provides HTTP access to manage all the different
// groups and shards on each grorup
type Manager struct {
	baseUrl    string
	dao        *dao.Dao
	maxUploads chan bool
	tagger     *imagestagger.RAMTagger
}

func Init(baseUrl string, dao *dao.Dao) *Manager {
	mg := &Manager{
		baseUrl:    baseUrl,
		dao:        dao,
		maxUploads: make(chan bool, runtime.NumCPU()-1), // Leave one CPU free for other stuff and also power issues
	}

	var err error
	mg.tagger, err = imagestagger.NewRAMTagger(cfg.GetStr("tagger", "model-path"), cfg.GetStr("tagger", "tags-path"), imagestagger.DefaultRAMOptions())

	if err != nil {
		log.Fatal("Error loading image encoders:", err)
	}

	return mg
}

func (mg *Manager) ListFiles(session *session.Session, path string) (files []*pb.File, err error) {
	return mg.dao.GetFilesByPath(path, true)
}

func (mg *Manager) cosineSimilarity(a, b []float32) float32 {
	var dot, na, nb float32
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	return dot / (float32(math.Sqrt(float64(na))) * float32(math.Sqrt(float64(nb))))
}

func getCipher(secret string) (cp cipher.AEAD) {
	keyHash := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(keyHash[:])
	if err != nil {
		log.Fatal("error ciper:", err)
		return
	}

	// We replace the secret by the one in the DB
	cp, err = cipher.NewGCM(block)
	if err != nil {
		log.Fatal("error ciper:", err)
		return
	}

	return
}

func (mg *Manager) GetSharedLink(session *session.Session, paths []string, domain string) (link string, err error) {
	files := make([]*pb.File, len(paths))
	for i, path := range paths {
		files[i], err = mg.GetFile(session, path)
		if err != nil {
			return "", err
		}
	}

	var buff bytes.Buffer
	zw := zip.NewWriter(&buff)

	for _, file := range files {
		h := &zip.FileHeader{
			Name:   "." + file.Path,
			Method: zip.Deflate,
		}
		// set mod time (zip format stores DOS time; Go handles conversion)
		h.SetModTime(file.Modified.AsTime())
		h.SetMode(0644)

		wr, err := zw.CreateHeader(h)
		if err != nil {
			return "", err
		}
		if _, err := wr.Write(file.Content); err != nil {
			return "", err
		}
	}

	zw.Close()

	zipBytes := buff.Bytes()
	secret := uuid.New().String()
	cipher := getCipher(secret)

	// GCM requires a unique nonce per encryption
	nonce := make([]byte, cipher.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		panic(err)
	}

	encZipBytes := cipher.Seal(nonce, nonce, zipBytes, nil)

	pathUuid := uuid.New().String()
	targetPath := fmt.Sprintf("%s/%s", cfg.GetStr("otc", "storage-path"), pathUuid)
	err = os.WriteFile(targetPath, encZipBytes, 0644) // perms: rw-r--r--
	if err != nil {
		return "", err
	}

	link = "https://" + domain + "/" + CDownloadAttr + pathUuid + "_" + secret
	err = mg.dao.InsertSharedLink(pathUuid, len(encZipBytes))

	return
}

func (mg *Manager) OpenSharedLink(uuid, secret string) (content []byte, err error) {
	cipher := getCipher(secret)
	encContent, err := os.ReadFile(fmt.Sprintf("%s/%s", cfg.GetStr("otc", "storage-path"), uuid))
	if err != nil {
		return nil, err
	}

	nonceSize := cipher.NonceSize()
	if len(encContent) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}

	nonce, ciphertext := encContent[:nonceSize], encContent[nonceSize:]

	return cipher.Open(nil, nonce, ciphertext, nil)
}

func (mg *Manager) ImageSearch(session *session.Session, path string, tags []string) (files []*pb.File, err error) {
	files, err = mg.dao.SearchByTags(path, tags)
	for _, file := range files {
		encContent, err := os.ReadFile(fmt.Sprintf("%s/%s_thumbnail", cfg.GetStr("otc", "storage-path"), file.Hash))
		if err != nil {
			log.Error("error reading thumbnail from:", file.Path, err)
		}
		file.Content, err = session.Decrypt(encContent)
		if err != nil {
			log.Error("error decryptinig the data", err)
		}
	}

	return
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
			log.Error("error decryptinig the data", err)
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

	file, _ = mg.dao.GetFileByPath(path)
	if file != nil {
		// We don't delete the file since we still have another
		// reference with another path
		return nil
	}

	fullPath := fmt.Sprintf("%s/%s", cfg.GetStr("otc", "storage-path"), file.Hash)
	if err = os.Remove(fullPath); err != nil {
		return err
	}
	os.Remove(fmt.Sprintf("%s_thumbnail", fullPath))
	return
}

func (mg *Manager) UploadFile(session *session.Session, path string, content []byte, forceOverride bool, created *timestamppb.Timestamp) (file *pb.File, err error) {
	mimeType := http.DetectContentType(content)
	log.Debug("Mime type:", mimeType)

	// Calculate the SHA256 of the file to be used as unique hash
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	log.Debug("Calculated Hash:", hash)

	if created == nil {
		created = timestamppb.Now()
	}

	file = &pb.File{
		Created:  created,
		Modified: timestamppb.Now(),
		Path:     path,
		Mime:     mimeType,
		Hash:     hash,
		Size:     int32(len(content)),
	}

	duplicated, err := mg.dao.StoreNewFile(file)
	if err != nil {
		return nil, err
	}

	if duplicated {
		file, err = mg.dao.GetFileByPath(path)
		if err != nil {
			return nil, err
		}
		if file.Hash == hash {
			log.Debug("Same file with same content for:", path, hash)
			return file, nil
		}
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

	targetPath := fmt.Sprintf("%s/%s", cfg.GetStr("otc", "storage-path"), hash)

	// Limit the amounth of concurrent writes
	mg.maxUploads <- true

	go func(targetPath string, file *pb.File, content []byte) {
		defer func() { <-mg.maxUploads }()

		start := time.Now()
		// Write to disk the content
		err = os.WriteFile(targetPath, session.Encrypt(content), 0644) // perms: rw-r--r--
		log.Debug("Time writting file:", time.Since(start), targetPath)

		// We will try to create a thumbnail of images only
		isHeic := strings.HasSuffix(file.Path, ".HEIC")
		if mimeType[:5] == "image" || isHeic {
			if isHeic {
				content, err = mg.heicToJpeg(content, 6)
				if err != nil {
					log.Error("error converting from HEIC to JPEG:", err)
					return
				}
			}

			startClass := time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			img, _, err := image.Decode(bytes.NewReader(content))
			if err != nil {
				log.Error("error decoding the image:", err)
				return
			}

			tags, err := mg.tagger.Tags(ctx, img, imagestagger.DefaultRAMOptions())
			if err != nil {
				log.Error("Error processing tags:", err)
			}
			log.Debug("Tags:", tags)

			mg.dao.AddTags(file, tags)

			log.Debug("Time classifying image:", time.Since(startClass), targetPath)

			startThumb := time.Now()
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
				if err != nil {
					log.Error("Error generating thumbnail:", err)
				}
			}
			log.Debug("Time processing thumbnail:", time.Since(startThumb), targetPath)
		}

		log.Debug("Time processing image:", time.Since(start), targetPath)
	}(targetPath, file, content)

	return
}

func (mg *Manager) heicToJpeg(heicData []byte, quality int) ([]byte, error) {
	if quality <= 0 || quality > 100 {
		quality = 90
	}

	// Decode HEIC from memory
	img, err := goheif.Decode(bytes.NewReader(heicData))
	if err != nil {
		return nil, err
	}

	// Encode as JPEG to []byte
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}

	return out.Bytes(), nil
}
