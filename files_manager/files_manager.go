// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/exifinfo"
	facerecognition "github.com/alonsovidales/otc/face_recognition"
	"github.com/alonsovidales/otc/geotag"
	"github.com/alonsovidales/otc/images_tagger"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
	"github.com/google/uuid"
	"github.com/jdeng/goheif"
	"github.com/jdeng/goheif/heif"
	"github.com/jdeng/goheif/heif/bmff"
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
	// faceRecognizer is nil until/unless [faces] is configured with both
	// model paths (issue #52) - every call site below treats a nil
	// recognizer as "the feature isn't set up on this device yet", not an
	// error, same as push.Push's nil apnsClient.
	faceRecognizer *facerecognition.Recognizer

	// reprocessing guards issue #73's full-library reprocess job - true
	// only while a goroutine started by *this process* is actively working
	// through it. Deliberately separate from the persisted
	// reprocess_state.status ('running' in the DB survives a crash/restart
	// with no goroutine left alive to match it) - see reprocess.go's
	// StartReprocess for how the two combine to tell "already running"
	// apart from "was interrupted, resume".
	reprocessMu  sync.Mutex
	reprocessing bool
	// reprocessCancel stops the active run (see CancelReprocess) - nil
	// whenever reprocessing is false. A cancelled run is treated exactly
	// like an interrupted one (status 'stopped', not 'completed'/'failed')
	// so the existing resume-from-last_hash path is what picks it back up
	// on the next "Reprocess" click, rather than a separate code path.
	reprocessCancel context.CancelFunc
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
	// thresholds-path is optional (issue #33): a per-tag calibration file
	// alongside the model, more accurate than one flat cutoff for every
	// tag. Leave it unset in config to keep the old flat-threshold behavior.
	mg.tagger, err = imagestagger.NewRAMTagger(
		cfg.GetStr("tagger", "model-path"),
		cfg.GetStr("tagger", "tags-path"),
		cfg.GetStr("tagger", "thresholds-path"),
		imagestagger.DefaultRAMOptions(),
	)

	if err != nil {
		log.Fatal("Error loading image encoders:", err)
	}

	// Issue #52: unlike the tagger above, a missing/misconfigured [faces]
	// section is not fatal - the feature is opt-in (off by default, see
	// db.sql's settings.face_recognition_enabled) and a device that never
	// turns it on shouldn't need these two extra models downloaded at all.
	mg.faceRecognizer, err = facerecognition.NewRecognizer(
		cfg.GetStr("faces", "detector-model-path"),
		cfg.GetStr("faces", "recognizer-model-path"),
	)
	if err != nil {
		log.Info("Face recognition not available (issue #52 stays off until this is configured):", err)
		mg.faceRecognizer = nil
	}

	go mg.tokenCollector()
	go mg.sharedLinksSweeper()

	// Issue #73 follow-up: a fresh process can't possibly have a
	// reprocess goroutine already running, so a "running" reprocess_state
	// row found right now is necessarily stale - the previous run was
	// killed outright (this process restarting is exactly that) rather
	// than cleanly cancelled. Left alone, the status just sits on
	// "running" forever with nothing left to ever move it off that,
	// which is what made Cancel look broken: there was nothing left
	// running to actually cancel.
	if err := dao.MarkStaleReprocessStopped(); err != nil {
		log.Error("error clearing a stale reprocess status at startup:", err)
	}

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

// commonDirPrefix returns the directory (with a trailing slash) shared by
// every path given, so a caller can strip it to get each file's location
// relative to what's actually common between them — e.g. two files both
// under "/a/b/" reduce to "c.jpg"/"d.jpg", while "/a/b/c.jpg" and
// "/a/e/f.jpg" (common dir only "/a/") keep just enough structure to tell
// them apart: "b/c.jpg"/"e/f.jpg". An empty or single-path input has
// nothing to compare against, so its own containing directory is "common"
// by definition.
func commonDirPrefix(paths []string) string {
	if len(paths) == 0 {
		return "/"
	}
	dirOf := func(p string) string {
		if idx := strings.LastIndex(p, "/"); idx >= 0 {
			return p[:idx+1]
		}
		return "/"
	}

	common := dirOf(paths[0])
	for _, p := range paths[1:] {
		d := dirOf(p)
		for !strings.HasPrefix(d, common) {
			trimmed := strings.TrimSuffix(common, "/")
			idx := strings.LastIndex(trimmed, "/")
			if idx < 0 {
				return "/"
			}
			common = trimmed[:idx+1]
		}
	}
	return common
}

func (mg *Manager) GetSharedLink(session *session.Session, paths []string, domain string) (link string, err error) {
	files := make([]*pb.File, len(paths))
	for i, path := range paths {
		files[i], err = mg.GetFile(session, path)
		if err != nil {
			return "", err
		}
	}

	// The archive used to name every entry "."+file.Path - each selected
	// file's full path from the storage root - which reproduces the whole
	// directory tree down to that file instead of holding just what was
	// selected. Stripping the directory common to every file in *this*
	// share keeps the archive flat when everything came from one folder
	// (the reported case, and the common one), while still not colliding
	// two same-named files from different folders when a share spans more
	// than one.
	filePaths := make([]string, len(files))
	for i, file := range files {
		filePaths[i] = file.Path
	}
	prefix := commonDirPrefix(filePaths)

	var buff bytes.Buffer
	zw := zip.NewWriter(&buff)

	for _, file := range files {
		h := &zip.FileHeader{
			Name:   strings.TrimPrefix(file.Path, prefix),
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

// includeVideos (issue #60) only affects the no-filters case below - a
// tag- or person-filtered search already includes videos regardless
// (dao.SearchMedia only applies the images-only filter when browsing with
// no filters at all), so the Photo Gallery's own filtered search is
// unaffected either way. Callers that don't care (every existing one
// before issue #60) get exactly today's images-only default browsing by
// passing false.
//
// personIDs (issue #52 follow-up): folded into this same search rather
// than a separate RPC, since the Photo Gallery's search bar combines
// person and tag filters on one request - see dao.SearchMedia for the AND/
// OR semantics of combining them.
// before (issue #77, the date scrubber's "jump to date"): when non-nil,
// always starts a fresh search filtered to created <= *before, exactly as
// if oldToken had never been passed - there's no seekable SQL cursor to
// jump within, but since a token's whole result set is already computed
// once and cached (see the tokenFound branch below), treating a jump as a
// brand new search targeting a narrower result set reuses that same
// mechanism instead of needing one of its own.
func (mg *Manager) ImageSearch(session *session.Session, path string, tags []string, oldToken string, includeVideos bool, personIDs []string, before *time.Time) (files []*pb.File, token string, err error) {
	log.Debug("Image search, token:", oldToken)
	tokenFound := false
	if oldToken != "" && before == nil {
		var filesMap any
		filesMap, tokenFound = mg.searchTokens.Load(oldToken)
		files = filesMap.([]*pb.File)
		token = oldToken
	}
	if !tokenFound {
		files, err = mg.dao.SearchMedia(path, tags, personIDs, !includeVideos, before)
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

// PhotoDateBuckets (issue #77) answers "how many photos per month" for the
// gallery's date scrubber - a thin passthrough, no thumbnails/encryption
// involved since it's just counts, not files.
func (mg *Manager) PhotoDateBuckets(tags []string, personIDs []string, includeVideos bool) ([]dao.DateBucket, error) {
	return mg.dao.SearchMediaDateBuckets(tags, personIDs, !includeVideos)
}

func (mg *Manager) GetFile(session *session.Session, path string) (file *pb.File, err error) {
	file, err = mg.dao.GetFileByPath(path)
	if err == nil {
		encContent, err := os.ReadFile(fmt.Sprintf("%s/%s", cfg.GetStr("otc", "storage-path"), file.Hash))
		if err != nil {
			log.Error("error reading file from:", path, err)
		}
		content, err := session.Decrypt(encContent)
		if err != nil {
			log.Error("error decryptinig the data", err)
		}

		// Issue #44: the thumbnail is already a JPEG (converted at upload
		// time), but the full-size original was still served as raw HEIC
		// — unrenderable by an <img>/<video poster> in every browser
		// except Safari, so "view full size" just showed nothing. Convert
		// here too, the same way, so it actually displays everywhere.
		if isHeicFile(file.Path, file.Mime) {
			// Issue #66: read the orientation so the conversion below
			// rotates the pixels to match, instead of silently discarding it.
			orientation := 1
			if info, exifErr := exifinfo.FromHEIC(content); exifErr == nil {
				orientation = info.Orientation
			}
			if converted, convErr := mg.heicToJpeg(content, 90, orientation); convErr == nil {
				content = converted
				file.Mime = "image/jpeg"
			} else {
				log.Error("error converting HEIC to JPEG for GetFile:", convErr)
			}
		}
		file.Content = content
	}

	return
}

func isHeicFile(path, mime string) bool {
	return strings.HasSuffix(strings.ToUpper(path), ".HEIC") || strings.EqualFold(mime, "image/heic")
}

// GetFileInfo returns a photo/video's camera/EXIF metadata (issue #41),
// computed live from its own bytes on every call — nothing here is
// persisted, so "More info" works for files uploaded before this feature
// existed too, not just new ones.
func (mg *Manager) GetFileInfo(session *session.Session, path string) (info *pb.FileExifInfo, err error) {
	file, err := mg.dao.GetFileByPath(path)
	if err != nil {
		return nil, err
	}
	encContent, err := os.ReadFile(fmt.Sprintf("%s/%s", cfg.GetStr("otc", "storage-path"), file.Hash))
	if err != nil {
		return nil, err
	}
	content, err := session.Decrypt(encContent)
	if err != nil {
		return nil, err
	}

	ex, err := extractExif(content, file.Mime, file.Path)
	if err != nil {
		return nil, err
	}

	info = &pb.FileExifInfo{
		CameraMake:   ex.CameraMake,
		CameraModel:  ex.CameraModel,
		ExposureTime: ex.ExposureTime,
		FNumber:      ex.FNumber,
		Iso:          ex.ISO,
		FocalLength:  ex.FocalLength,
		Width:        ex.Width,
		Height:       ex.Height,
		HasGps:       ex.HasGPS,
		Latitude:     ex.Latitude,
		Longitude:    ex.Longitude,
	}
	if ex.HasTakenAt {
		info.TakenAt = timestamppb.New(ex.TakenAt)
	}
	if ex.HasGPS {
		if city, country, ok := geotag.ReverseGeocode(ex.Latitude, ex.Longitude); ok {
			info.City = city
			info.Country = country
		}
	}
	return info, nil
}

// extractExif dispatches to the right exifinfo reader for mime/path, since
// JPEG, HEIC, and video containers each embed EXIF/metadata differently.
func extractExif(content []byte, mime, path string) (*exifinfo.Info, error) {
	switch {
	case strings.HasPrefix(mime, "video/"):
		return exifinfo.FromVideo(content)
	case strings.HasSuffix(path, ".HEIC"):
		return exifinfo.FromHEIC(content)
	case strings.HasPrefix(mime, "image/"):
		return exifinfo.FromJPEG(content)
	default:
		return nil, fmt.Errorf("unsupported mime type for EXIF: %s", mime)
	}
}

// locationTags turns a GPS coordinate into extra searchable tags (issue
// #42) via a fully offline reverse-geocode (see geotag/) — no coordinates
// ever leave the device. Returns nil if there's no GPS data or nothing in
// the dataset is close enough to be meaningful.
func locationTags(ex *exifinfo.Info) []imagestagger.RAMTag {
	if ex == nil || !ex.HasGPS {
		return nil
	}
	city, country, ok := geotag.ReverseGeocode(ex.Latitude, ex.Longitude)
	if !ok {
		return nil
	}
	return []imagestagger.RAMTag{
		{Name: city, Score: 1},
		{Name: country, Score: 1},
	}
}

func (mg *Manager) DelFile(session *session.Session, path string) (err error) {
	file, err := mg.dao.GetFileByPath(path)
	if err != nil {
		return
	}
	hash := file.Hash

	if err = mg.dao.DelFileByPath(path); err != nil {
		return
	}

	// Files are deduplicated on disk by hash (more than one path can point
	// at the same blob), so the blob and thumbnail can only be removed
	// once no path references that hash any more. This used to re-fetch by
	// the exact path just deleted above, which — being freshly gone — a
	// dao.GetFileByPath scan miss doesn't report by returning nil: it
	// always returns a non-nil *pb.File regardless of whether the row was
	// found (see its own implementation), only the error says so. `file !=
	// nil` was therefore always true, so this returned early
	// unconditionally: the underlying blob and thumbnail were never
	// actually deleted from disk, no matter how many (zero, in the common
	// case) other paths still referenced that hash. Checking GetFileByHash
	// - and by hash, not the path that's now gone - is the check this was
	// actually meant to make: does any *other* file row still point at
	// this content.
	if _, hashErr := mg.dao.GetFileByHash(hash); hashErr == nil {
		return nil
	}

	fullPath := fmt.Sprintf("%s/%s", cfg.GetStr("otc", "storage-path"), hash)
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
		// Bug fix: this used to reassign `file` itself to the *existing*
		// row (old hash/size/mime), then re-store that same stale `file`
		// on the forceOverride path below — silently discarding the new
		// content's metadata. The DB row kept pointing at the old hash
		// even though DelFile may have just deleted that hash's on-disk
		// blob (if this was its last reference), while the real new
		// content sat orphaned on disk under the new hash nothing
		// referenced. Every "update an existing path" upload (the normal
		// case for any sync client — mac, iOS re-uploads, etc.) hit this.
		existing, err := mg.dao.GetFileByPath(path)
		if err != nil {
			return nil, err
		}
		if existing.Hash == hash {
			log.Debug("Same file with same content for:", path, hash)
			return existing, nil
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
		// Write to disk the content. This used to be unchecked: the DB row
		// for the file (StoreNewFile, above, before this goroutine even
		// starts) is already committed by the time this runs, so a
		// disk-full/IO error here used to mean the DB silently claimed the
		// file was safely stored while the bytes never actually landed on
		// disk - the worst failure mode for a backup product. Still no way
		// to tell the client after the fact (see issue #63) - logging
		// loudly and bailing out of the rest of this file's processing is
		// the best that can be done here today.
		if err := os.WriteFile(targetPath, session.Encrypt(content), 0644); err != nil { // perms: rw-r--r--
			log.Error("error writing uploaded file to disk, upload not actually persisted:", targetPath, err)
			return
		}
		log.Debug("Time writting file:", time.Since(start), targetPath)

		mg.processMediaContent(session, file, targetPath, content)

		log.Debug("Time processing image:", time.Since(start), targetPath)
	}(targetPath, file, content)

	return
}

// processMediaContent runs the tag/thumbnail/face pipeline against a
// file's original bytes - the part of UploadFile's background goroutine
// that doesn't care whether those bytes just arrived or have been sitting
// on disk for years. Factored out so Reprocess (issue #73: "re-run this
// against everything already uploaded, e.g. after a model change") drives
// the *exact* same code a fresh upload does, rather than a second copy
// that inevitably drifts. content is the file's original, undecoded bytes
// (whatever format it was stored in - HEIC handling happens right here,
// same as it always did); targetPath is only used to derive the
// "<hash>_thumbnail" sibling path.
func (mg *Manager) processMediaContent(session *session.Session, file *pb.File, targetPath string, content []byte) {
	// We will try to create a thumbnail of images only
	isHeic := strings.HasSuffix(file.Path, ".HEIC")
	if file.Mime[:5] == "image" || isHeic {
		// Issue #42: read GPS/EXIF from the *original* bytes before any
		// HEIC->JPEG re-encode below, which (like most re-encodes) drops
		// the EXIF segment entirely.
		var exif *exifinfo.Info
		var err error
		if isHeic {
			exif, err = exifinfo.FromHEIC(content)
		} else {
			exif, err = exifinfo.FromJPEG(content)
		}
		if err != nil {
			log.Debug("no EXIF metadata for", targetPath, ":", err)
			exif = nil
		}

		if isHeic {
			// Issue #66: quality 6 produced severe compression artifacts
			// ("colors are terrible") — 90 matches the quality used for
			// the full-size conversion in GetFile, so the thumbnail
			// derived from this below isn't starting from a
			// already-mangled source. Orientation comes from the EXIF
			// read above so the rotation baked in here actually matches
			// how the photo was taken.
			orientation := 1
			if exif != nil {
				orientation = exif.Orientation
			}
			content, err = mg.heicToJpeg(content, 90, orientation)
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

		// Issue #66 follow-up: a plain JPEG straight from the phone (no
		// HEIC involved at all - this is what actually reproduced the
		// bug report, since it turns out that photo was never HEIC in
		// the first place) was losing its rotation here just the same
		// as a HEIC one - the resize/re-encode below always produces a
		// brand new JPEG with no EXIF segment, so whatever Orientation
		// tag the original had is gone from the thumbnail the feed
		// actually shows. A HEIC source already had its rotation baked
		// into its pixels above (heicToJpeg) using the HEIF container's
		// own irot/imir, so this only runs for the non-HEIC case -
		// applying `exif`'s (pre-conversion) Orientation again here too
		// would double-rotate on the rare HEIC file that sets both a
		// HEIF-native transform and a non-default EXIF Orientation.
		if !isHeic && exif != nil {
			img = applyOrientation(img, exif.Orientation)
		}

		tags, err := mg.tagger.Tags(ctx, img, imagestagger.DefaultRAMOptions())
		if err != nil {
			log.Error("Error processing tags:", err)
		}
		tags = append(tags, locationTags(exif)...)
		log.Debug("Tags:", tags)

		mg.dao.AddTags(file, tags)

		log.Debug("Time classifying image:", time.Since(startClass), targetPath)

		startThumb := time.Now()
		// Issue #66 follow-up: img.Bounds() (not a fresh
		// image.DecodeConfig of content's raw bytes, as this used to
		// do) reflects the real, orientation-corrected shape — see
		// thumbnailSource's doc comment for why that distinction
		// matters.
		maxWidth := int(cfg.GetInt("otc", "max-thumbnail-width-px"))
		thumbImg := thumbnailSource(img, maxWidth)
		// A thumbnail must exist once a file is uploaded, full stop —
		// NewPublication, the social feed, etc. all read one back via
		// GetThumbnail unconditionally. This used to only write one
		// when resizing was actually needed (imgW > maxWidth), leaving
		// nothing on disk at all for an image that was already narrow
		// enough — a gap the orientation fix above made easy to hit for
		// real: a portrait photo's corrected (post-rotation) width can
		// end up smaller than maxWidth even when its original,
		// unrotated width wasn't, silently skipping the thumbnail a
		// post with that photo in it then failed to ever find.
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, thumbImg, &jpeg.Options{Quality: 80}); err != nil {
			log.Error("error encoding thumbnail:", err)
		} else {
			log.Debug("Thumbnail:", fmt.Sprintf("%s_thumbnail", targetPath))
			if err := os.WriteFile(fmt.Sprintf("%s_thumbnail", targetPath), session.Encrypt(buf.Bytes()), 0644); err != nil {
				log.Error("Error generating thumbnail:", err)
			}
		}
		log.Debug("Time processing thumbnail:", time.Since(startThumb), targetPath)

		mg.processFaces(session, file, img)
	} else if strings.HasPrefix(file.Mime, "video/") {
		// Videos get tagged the same way images do — search doesn't
		// need to know the difference, since it's all just file_tags
		// rows keyed by hash — just against a handful of frames
		// sampled across the video instead of the one still image.
		startClass := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		frames, err := extractVideoFrames(content, cVideoSampleFrames)
		if err != nil {
			log.Error("error extracting video frames:", err)
			return
		}

		// Issue #42: videos carry GPS in their own container metadata
		// (e.g. an iPhone's ISO-6709 "location" tag), separate from the
		// frame images sampled above.
		exif, err := exifinfo.FromVideo(content)
		if err != nil {
			log.Debug("no location metadata for", targetPath, ":", err)
			exif = nil
		}

		tags := tagVideoFrames(ctx, mg.tagger, frames)
		tags = append(tags, locationTags(exif)...)
		log.Debug("Tags:", tags)

		mg.dao.AddTags(file, tags)

		log.Debug("Time classifying video:", time.Since(startClass), targetPath)

		startThumb := time.Now()
		thumbSrc := frames[0]
		b := thumbSrc.Bounds()
		maxWidth := int(cfg.GetInt("otc", "max-thumbnail-width-px"))
		if b.Dx() > maxWidth {
			newH := int(float64(b.Dy()) * float64(maxWidth) / float64(b.Dx()))
			dst := image.NewRGBA(image.Rect(0, 0, maxWidth, newH))
			draw.CatmullRom.Scale(dst, dst.Bounds(), thumbSrc, thumbSrc.Bounds(), draw.Over, nil)
			var buf bytes.Buffer
			jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 80})
			log.Debug("Thumbnail:", fmt.Sprintf("%s_thumbnail", targetPath))
			err = os.WriteFile(fmt.Sprintf("%s_thumbnail", targetPath), session.Encrypt(buf.Bytes()), 0644)
			if err != nil {
				log.Error("Error generating video thumbnail:", err)
			}
		}
		log.Debug("Time processing thumbnail:", time.Since(startThumb), targetPath)
	}
}

// HasFile reports whether this device already has a file with this exact
// content, by hash (issue #58) — storage is already deduplicated by hash
// (see UploadFile's targetPath, and DelFile's "another reference with
// another path" check), this just lets a sync client (iOS/macOS) find
// that out *before* spending the bandwidth on a re-upload, via LinkFile
// below, rather than only after the fact like the existing
// duplicated-path check in UploadFile does.
func (mg *Manager) HasFile(hash string) (exists bool, err error) {
	_, err = mg.dao.GetFileByHash(hash)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// LinkFile registers path as pointing at content this device already has
// (hash) — the on-disk blob, its thumbnail, and its tags are all already
// keyed by hash (see UploadFile above), so a new path sharing an existing
// hash needs none of that redone, just a new `files` row. Mirrors
// UploadFile's own duplicated-path handling (same path already exists:
// no-op if the hash already matches, otherwise only overwritten with
// forceOverride) — the one difference is this never touches disk at all.
func (mg *Manager) LinkFile(session *session.Session, path, hash string, forceOverride bool, created *timestamppb.Timestamp) (file *pb.File, err error) {
	existing, err := mg.dao.GetFileByHash(hash)
	if err == sql.ErrNoRows {
		return nil, errors.New("no file with that hash on this device")
	}
	if err != nil {
		return nil, err
	}

	if created == nil {
		created = timestamppb.Now()
	}

	file = &pb.File{
		Created:  created,
		Modified: timestamppb.Now(),
		Path:     path,
		Mime:     existing.Mime,
		Hash:     hash,
		Size:     existing.Size,
	}

	duplicated, err := mg.dao.StoreNewFile(file)
	if err != nil {
		return nil, err
	}
	if duplicated {
		existingAtPath, err := mg.dao.GetFileByPath(path)
		if err != nil {
			return nil, err
		}
		if existingAtPath.Hash == hash {
			log.Debug("Same file already linked at:", path, hash)
			return existingAtPath, nil
		}
		if !forceOverride {
			return nil, errors.New("Duplicated file")
		}
		if err := mg.DelFile(session, path); err != nil {
			return nil, err
		}
		if _, err := mg.dao.StoreNewFile(file); err != nil {
			return nil, err
		}
	}

	return file, nil
}

// heicToJpeg re-encodes a HEIC file as JPEG, correcting for however this
// particular file actually stores its rotation. goheif.Decode returns the
// raw sensor-orientation pixel grid with no rotation applied at all (see
// heifTransform's doc comment for where the real signal usually lives
// instead), and Go's stdlib jpeg encoder has no way to carry rotation
// metadata forward on its own, so it has to be baked into the pixels here
// or it's lost for good (issue #66). fallbackOrientation is the source's
// raw EXIF Orientation tag (1-8, 0/1 meaning "no transform"), used only
// when the HEIC container itself carries no rotation/mirror of its own.
func (mg *Manager) heicToJpeg(heicData []byte, quality int, fallbackOrientation int) ([]byte, error) {
	if quality <= 0 || quality > 100 {
		quality = 90
	}

	// Decode HEIC from memory
	img, err := goheif.Decode(bytes.NewReader(heicData))
	if err != nil {
		return nil, err
	}

	img = applyHeicOrientation(heicData, img, fallbackOrientation)

	// Encode as JPEG to []byte
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}

	return out.Bytes(), nil
}

// applyHeicOrientation corrects for however this specific HEIC file
// actually stores its rotation. An earlier attempt at issue #66 read only
// the EXIF Orientation tag, which turned out not to fix real iPhone
// photos: Apple's Camera app (like most HEIC encoders) records a photo's
// actual rotation in the HEIF container's own "irot"/"imir" transformative
// item properties, not a traditional EXIF Orientation tag — which is
// usually left at its default (1, "normal") on an HEIC even when the photo
// is visibly rotated, simply because that's not where the signal lives.
// fallbackOrientation (the EXIF Orientation tag, read separately by the
// caller) is applied only when the container itself carries neither a
// rotation nor a mirror of its own, covering an encoder that went the
// EXIF-only route instead.
func applyHeicOrientation(heicData []byte, img image.Image, fallbackOrientation int) image.Image {
	rotations, hasMirror, mirrorAxis := heifTransform(heicData)
	if rotations == 0 && !hasMirror {
		return applyOrientation(img, fallbackOrientation)
	}

	if hasMirror {
		if mirrorAxis == 1 {
			img = applyOrientation(img, 4) // mirror about a horizontal axis: flip vertical
		} else {
			img = applyOrientation(img, 2) // mirror about a vertical axis: flip horizontal
		}
	}
	// Per the HEIF spec, mirroring (above) is applied before rotation.
	// orientation 8 is a single 90-degree counter-clockwise turn — the
	// same direction heif.Item.Rotations() counts in — so composing
	// `rotations` of them reproduces however many turns this file calls
	// for, reusing the already-verified rotation math instead of
	// duplicating it.
	for i := 0; i < rotations; i++ {
		img = applyOrientation(img, 8)
	}
	return img
}

// heifTransform reads the primary item's irot/imir transformative
// properties directly out of the HEIF container structure — a lightweight
// parse of box metadata (github.com/jdeng/goheif/heif), not a full image
// decode. rotations is the number of 90-degree counter-clockwise turns
// (0-3); mirrorAxis (only meaningful when hasMirror) is 0 for a mirror
// about a vertical axis (left-right flip) or 1 for a mirror about a
// horizontal axis (top-bottom flip), matching heif.Item.Mirror(). Returns
// all-zero/false on any parse error — heicToJpeg then falls back to
// whatever EXIF Orientation says, same as if this file just had neither
// property at all.
func heifTransform(heicData []byte) (rotations int, hasMirror bool, mirrorAxis int) {
	it, err := heif.Open(bytes.NewReader(heicData)).PrimaryItem()
	if err != nil {
		return 0, false, 0
	}
	rotations = ((it.Rotations() % 4) + 4) % 4
	for _, p := range it.Properties {
		if m, ok := p.(*bmff.ImageMirror); ok {
			return rotations, true, int(m.Mirror)
		}
	}
	return rotations, false, 0
}

// thumbnailSource returns the image a thumbnail should be encoded from:
// img resized down to maxWidth if it's wider than that, or img itself,
// unchanged, if it's already narrow enough. Always returns something to
// encode — a thumbnail must exist once a file is uploaded, full stop (see
// this function's call site), so "no resize needed" must never mean "no
// thumbnail". That distinction used to be missing here: the resize branch
// was the only place anything got written to disk, silently leaving
// nothing there at all for an already-narrow image — a gap the
// orientation-correction fix above made easy to hit for real, since a
// portrait photo's corrected (post-rotation) width can end up smaller than
// maxWidth even when its original, unrotated width wasn't.
func thumbnailSource(img image.Image, maxWidth int) image.Image {
	w := img.Bounds().Dx()
	if w <= maxWidth {
		return img
	}
	h := img.Bounds().Dy()
	newH := int(float64(h) * float64(maxWidth) / float64(w))
	dst := image.NewRGBA(image.Rect(0, 0, maxWidth, newH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Over, nil)
	return dst
}

// applyOrientation bakes an EXIF Orientation transform into the pixel data,
// returning a new image when a rotation/flip is needed (orientation outside
// 2-8 is returned unchanged as a no-op). See the EXIF/TIFF spec's Orientation
// tag (0x0112) for the 8 defined values.
func applyOrientation(img image.Image, orientation int) image.Image {
	if orientation <= 1 || orientation > 8 {
		return img
	}

	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	dstW, dstH := w, h
	if orientation >= 5 { // 5-8 rotate 90/270, swapping width and height
		dstW, dstH = h, w
	}
	dst := image.NewNRGBA(image.Rect(0, 0, dstW, dstH))

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := img.At(b.Min.X+x, b.Min.Y+y)
			var dx, dy int
			switch orientation {
			case 2: // mirror horizontal
				dx, dy = w-1-x, y
			case 3: // rotate 180
				dx, dy = w-1-x, h-1-y
			case 4: // mirror vertical
				dx, dy = x, h-1-y
			case 5: // transpose (mirror horizontal + rotate 270 CW)
				dx, dy = y, x
			case 6: // rotate 90 CW
				dx, dy = h-1-y, x
			case 7: // transverse (mirror horizontal + rotate 90 CW)
				dx, dy = h-1-y, w-1-x
			case 8: // rotate 270 CW
				dx, dy = y, w-1-x
			}
			dst.Set(dx, dy, c)
		}
	}
	return dst
}
