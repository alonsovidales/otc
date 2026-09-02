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
	//"net/http"
	"github.com/gabriel-vasile/mimetype"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	CDownloadAttr = "?download="
	cToeknsTTL    = 5 * time.Minute

	// cDefaultSharedLinkTTL is used when [otc] shared-link-ttl-hours is
	// absent or invalid in the config file.
	cDefaultSharedLinkTTL = 7 * 24 * time.Hour
	// cSharedLinksSweepInterval is how often expired shared links are
	// swept from disk and the DB.
	cSharedLinksSweepInterval = time.Hour
	// cTokensCollectInterval is how often expired search tokens are
	// swept from memory.
	cTokensCollectInterval = time.Minute
)

// Manager Structure that provides HTTP access to manage all the different
// groups and shards on each grorup
type Manager struct {
	baseUrl        string
	dao            *dao.Dao
	maxUploads     chan bool
	tagger         *imagestagger.RAMTagger
	searchTokens   *sync.Map
	tokensToExpire *sync.Map
	sharedLinkTTL  time.Duration
}

func Init(baseUrl string, dao *dao.Dao) *Manager {
	mg := &Manager{
		searchTokens:   new(sync.Map),
		tokensToExpire: new(sync.Map),
		baseUrl:        baseUrl,
		dao:            dao,
		maxUploads:     make(chan bool, runtime.NumCPU()-1), // Leave one CPU free for other stuff and also power issues
		sharedLinkTTL:  sharedLinkTTLFromCfg(),
	}

	var err error
	mg.tagger, err = imagestagger.NewRAMTagger(cfg.GetStr("tagger", "model-path"), cfg.GetStr("tagger", "tags-path"), imagestagger.DefaultRAMOptions())

	if err != nil {
		log.Fatal("Error loading image encoders:", err)
	}

	go mg.tokenCollector()
	go mg.sharedLinksSweeper()

	return mg
}

// sharedLinkTTLFromCfg reads [otc] shared-link-ttl-hours, falling back to
// cDefaultSharedLinkTTL when it's absent, zero or negative.
func sharedLinkTTLFromCfg() time.Duration {
	return sharedLinkTTLFromHours(cfg.GetInt("otc", "shared-link-ttl-hours"))
}

// sharedLinkTTLFromHours is the pure part of sharedLinkTTLFromCfg, split out
// so the fallback rule can be unit tested without a loaded config file.
func sharedLinkTTLFromHours(hours int64) time.Duration {
	if hours <= 0 {
		return cDefaultSharedLinkTTL
	}

	return time.Duration(hours) * time.Hour
}

// tokenCollector periodically sweeps expired search tokens out of memory.
// Previously this ran a single pass via a bare "go mg.collector()" call, so
// tokens were only ever swept once at startup (when tokensToExpire was
// still empty) and never again for the life of the process.
func (mg *Manager) tokenCollector() {
	ticker := time.NewTicker(cTokensCollectInterval)
	defer ticker.Stop()

	for range ticker.C {
		mg.collectExpiredTokens()
	}
}

func (mg *Manager) collectExpiredTokens() {
	t := time.Now()
	mg.tokensToExpire.Range(func(token, expire any) bool {
		if t.Sub(expire.(time.Time)) > cToeknsTTL {
			mg.tokensToExpire.Delete(token.(string))
			mg.searchTokens.Delete(token.(string))
			log.Debug("Expired token:", token)
		}

		return true
	})
}

// isSharedLinkExpired reports whether a shared link created at "created"
// has outlived ttl, as of "now". Pulled out as a pure function so the
// expiry rule can be unit tested without a DB.
func isSharedLinkExpired(created, now time.Time, ttl time.Duration) bool {
	return now.Sub(created) > ttl
}

// sharedLinksSweeper periodically removes shared links (both the DB row and
// the encrypted zip on disk) once they are older than mg.sharedLinkTTL.
func (mg *Manager) sharedLinksSweeper() {
	ticker := time.NewTicker(cSharedLinksSweepInterval)
	defer ticker.Stop()

	for range ticker.C {
		mg.expireSharedLinks()
	}
}

func (mg *Manager) expireSharedLinks() {
	cutoff := time.Now().Add(-mg.sharedLinkTTL)

	uuids, err := mg.dao.GetExpiredSharedLinkUuids(cutoff)
	if err != nil {
		log.Error("error listing expired shared links:", err)
		return
	}

	for _, pathUuid := range uuids {
		mg.deleteSharedLink(pathUuid)
	}
}

// deleteSharedLink removes the on-disk content for a shared link and its DB
// row. The disk file is removed first: if that fails we keep the DB row
// around so the sweep retries next time, rather than losing track of
// content still sitting on disk.
func (mg *Manager) deleteSharedLink(pathUuid string) {
	targetPath := fmt.Sprintf("%s/%s", cfg.GetStr("otc", "storage-path"), pathUuid)
	if err := os.Remove(targetPath); err != nil && !os.IsNotExist(err) {
		log.Error("error removing expired shared link content:", pathUuid, err)
		return
	}

	if err := mg.dao.DeleteSharedLink(pathUuid); err != nil {
		log.Error("error deleting expired shared link row:", pathUuid, err)
	} else {
		log.Debug("Expired shared link removed:", pathUuid)
	}
}

func (mg *Manager) ListFiles(session *session.Session, path string, recursive bool) (files []*pb.File, err error) {
	return mg.dao.GetFilesByPath(path, recursive, false)
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
	created, err := mg.dao.GetSharedLinkCreated(uuid)
	if err != nil {
		return nil, err
	}
	if isSharedLinkExpired(created, time.Now(), mg.sharedLinkTTL) {
		// Don't wait for the next sweep: drop the content and row now
		// that we know it's expired, and refuse the download.
		mg.deleteSharedLink(uuid)
		return nil, errors.New("shared link has expired")
	}

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

func (mg *Manager) GetThumbnail(session *session.Session, file *pb.File) (content []byte, err error) {
	encContent, err := os.ReadFile(fmt.Sprintf("%s/%s_thumbnail", cfg.GetStr("otc", "storage-path"), file.Hash))
	if err != nil {
		log.Error("error reading thumbnail from:", file.Path, err)
		return nil, err
	}

	return session.Decrypt(encContent)
}

func (mg *Manager) ImageSearch(session *session.Session, path string, tags []string, oldToken string) (files []*pb.File, token string, err error) {
	log.Debug("Image search, token:", oldToken)
	tokenFound := false
	if oldToken != "" {
		var filesMap any
		filesMap, tokenFound = mg.searchTokens.Load(oldToken)
		files = filesMap.([]*pb.File)
		token = oldToken
	}
	if !tokenFound {
		if len(tags) > 0 {
			files, err = mg.dao.SearchByTags(path, tags)
		} else {
			files, err = mg.dao.GetFilesByPath(path, true, true)
		}
		if err != nil {
			return
		}
		token = uuid.New().String()
		log.Debug("New Token:", token)
	}

	toReturn := int(cfg.GetInt("tagger", "max-images-search"))
	if len(files) > toReturn {
		mg.searchTokens.Store(token, files[toReturn:])
		mg.tokensToExpire.Store(token, time.Now())
		files = files[:toReturn]
	} else {
		log.Debug("End for token:", token)
		token = "" // We reached the end
	}

	for _, file := range files {
		file.Content, err = mg.GetThumbnail(session, file)
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
	mimeType := mimetype.Detect(content)
	//mimeType := http.DetectContentType(content)
	log.Debug("Mime type:", mimeType.String())

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
		Mime:     mimeType.String(),
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
		if file.Mime[:5] == "image" || isHeic {
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
