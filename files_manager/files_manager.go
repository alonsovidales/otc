// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/exifinfo"
	facerecognition "github.com/alonsovidales/otc/face_recognition"
	"github.com/alonsovidales/otc/geotag"
	"github.com/alonsovidales/otc/images_tagger"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/modelserver"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
	"github.com/google/uuid"
	"github.com/jdeng/goheif"
	"github.com/jdeng/goheif/heif"
	"github.com/jdeng/goheif/heif/bmff"
	"golang.org/x/image/draw"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/jpeg"

	// Thumbnails for more than JPEG/PNG: without these decoders every GIF,
	// WebP, BMP and TIFF failed as "image: unknown format".
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
	"io"
	"math"
	//"net/http"
	"github.com/gabriel-vasile/mimetype"
	"os"
	"path/filepath"
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
	baseUrl   string
	dao       *dao.Dao
	lanes     *mediaLanes
	lanesOnce sync.Once
	// contentBudget bounds file content held in memory by downloads - see
	// membudget.go.
	contentBudget *memBudget
	// videoSourceFn gives ffmpeg a stored video to read (see videoSource).
	videoSourceFn VideoSourceFunc
	// tagger is loaded in the background (see Init) - read it through
	// waitForTagger, never directly, or an upload arriving in the first
	// seconds of a boot dereferences a nil.
	// Issue #167: the local RAM++ model on the primary instance, the
	// primary's shared one (modelserver.Client) on a supervised child.
	tagger         modelserver.Tagger
	taggerReady    chan struct{}
	searchTokens   *sync.Map
	tokensToExpire *sync.Map
	sharedLinkTTL  time.Duration
	// galleryCache holds shared galleries' decrypted manifests (see
	// openGallery), made on first use under galleryMu.
	galleryMu    sync.Mutex
	galleryCache map[string]*galleryCacheEntry
	// faceRecognizer is nil until/unless [faces] is configured with both
	// model paths (issue #52) - every call site below treats a nil
	// recognizer as "the feature isn't set up on this device yet", not an
	// error, same as push.Push's nil apnsClient.
	faceRecognizer modelserver.FaceDetector
	// faceRefs is issue #173's in-memory matching set (see face_refs.go),
	// nil until processFaces first needs it and again after
	// InvalidateFaceRefs. One per Manager is enough: there is one Manager
	// per process (bin/otc.go), each process serves one library database
	// (extra users on a device run as their own supervised instances with
	// their own database, issue #82), and the faces/people tables carry no
	// per-user column. faceRefsMu also serializes
	// processFaces' match-and-store step, so two concurrent uploads can't
	// both create a new person for the same face.
	faceRefsMu sync.Mutex
	faceRefs   faceRefs

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

// waitForTagger blocks until the model loaded by Init is usable. Callers
// are all on the upload path, which is both rare in the first seconds of a
// boot and already slow enough that waiting here is invisible - unlike
// making the whole device unreachable while it loads (see Init).
func (mg *Manager) waitForTagger() modelserver.Tagger {
	<-mg.taggerReady
	return mg.tagger
}

func Init(baseUrl string, dao *dao.Dao) *Manager {
	mg := &Manager{
		searchTokens:   new(sync.Map),
		tokensToExpire: new(sync.Map),
		baseUrl:        baseUrl,
		dao:            dao,
		contentBudget:  newMemBudget(contentBudgetBytes()),
		sharedLinkTTL:  sharedLinkTTLFromCfg(),
	}

	// Issue #105 follow-up: loading the RAM++ model is ~870MB of work and
	// takes well over ten seconds on a Pi. It used to happen right here,
	// synchronously, before bin/otc.go ever reached websocket.Init - so
	// the device spent that whole time invisible to the bridge, and every
	// client got "device unreachable" until it finished. Measured at 15s
	// of downtime on every restart, which is also what made a `make pi`
	// deploy look like an outage.
	//
	// Loaded in the background instead: nothing else in startup depends on
	// it, and the only two things that actually use it (photo and video
	// tagging, both on the upload path) wait for it via waitForTagger.
	// A failure is still fatal - a device that can't tag is misconfigured
	// - just fatal a few seconds later than it used to be.
	//
	// thresholds-path is optional (issue #33): a per-tag calibration file
	// alongside the model, more accurate than one flat cutoff for every
	// tag. Leave it unset in config to keep the old flat-threshold behavior.
	mg.taggerReady = make(chan struct{})
	// Issue #167: a supervised child uses the primary's models, over its
	// socket, instead of loading its own copies (~870 MB for RAM++ alone).
	if sock := os.Getenv(modelserver.EnvSocket); sock != "" {
		client := &modelserver.Client{Path: sock}
		mg.tagger = client
		close(mg.taggerReady)
		if has, err := client.HasFaces(); err != nil {
			log.Info("could not ask the primary instance about face recognition, assuming it's available:", err)
			mg.faceRecognizer = client
		} else if has {
			mg.faceRecognizer = client
		} else {
			log.Info("Face recognition not available (the primary instance has no face models)")
		}
		log.Info("using the models shared by the primary instance on", sock)
		go mg.tokenCollector()
		go mg.sharedLinksSweeper()
		return mg.initRest()
	}
	go func() {
		started := time.Now()
		tagger, err := imagestagger.NewRAMTagger(
			cfg.GetStr("tagger", "model-path"),
			cfg.GetStr("tagger", "tags-path"),
			cfg.GetStr("tagger", "thresholds-path"),
			imagestagger.DefaultRAMOptions(),
		)
		if err != nil {
			log.Fatal("Error loading image encoders:", err)
		}
		mg.tagger = tagger
		close(mg.taggerReady)
		log.Info("image tagger ready after", time.Since(started).Round(time.Millisecond))
	}()

	// Issue #52: unlike the tagger above, a missing/misconfigured [faces]
	// section is not fatal - the feature is opt-in (off by default, see
	// db.sql's settings.face_recognition_enabled) and a device that never
	// turns it on shouldn't need these two extra models downloaded at all.
	// Guard with HasSection (same idiom as push.go's [apns] check) rather
	// than calling cfg.GetStr directly: an entirely absent section makes
	// cfg.go's loadSection call log.Fatal and kill the whole process
	// before NewRecognizer's own error handling below ever gets a chance
	// to run - a real device (Cala, re-provisioned from scratch) hit
	// exactly this crash-loop when its freshly generated ini had no
	// [faces] section at all.
	if cfg.HasSection("faces") {
		// Through a local: a nil *Recognizer in the interface isn't nil.
		rec, recErr := facerecognition.NewRecognizer(
			cfg.GetStr("faces", "detector-model-path"),
			cfg.GetStr("faces", "recognizer-model-path"),
		)
		if recErr != nil {
			log.Info("Face recognition not available (issue #52 stays off until this is configured):", recErr)
		} else {
			mg.faceRecognizer = rec
		}
	} else {
		log.Info("Face recognition not available ([faces] section not configured)")
	}

	// Issue #167: the primary shares these with the supervised children.
	go func() {
		if err := modelserver.Serve(modelserver.SocketPath(), mg.waitForTagger, mg.faceRecognizer); err != nil {
			log.Error("could not serve the models to the other instances:", err)
		}
	}()

	go mg.tokenCollector()
	go mg.sharedLinksSweeper()
	return mg.initRest()
}

// initRest is the part of Init every instance runs, whichever models it
// uses.
func (mg *Manager) initRest() *Manager {
	dao := mg.dao
	// Issue #141: rows whose content is missing from the disk, reported
	// to the owner once a day when there are new ones.
	go mg.integrityChecker()

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

	// Before anything of this process writes to storage: bin/otc.go starts
	// the websocket and the API only after Init returns.
	mg.sweepOrphanedStorage()

	return mg
}

// randomSecret is session.RandomSecret, reachable where a parameter named
// session hides the package.
var randomSecret = session.RandomSecret

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
	mg.forgetGallery(pathUuid)
	targetPath := fmt.Sprintf("%s/%s", cfg.GetStr("otc", "storage-path"), pathUuid)
	if err := os.Remove(targetPath); err != nil && !os.IsNotExist(err) {
		log.Error("error removing expired shared link content:", pathUuid, err)
		return
	}
	// Issue #180: a gallery is a folder of its own.
	if galleryUUID.MatchString(pathUuid) {
		if err := os.RemoveAll(galleryDir(pathUuid)); err != nil {
			log.Error("error removing a shared gallery:", pathUuid, err)
			return
		}
	}

	if err := mg.dao.DeleteSharedLink(pathUuid); err != nil {
		log.Error("error deleting expired shared link row:", pathUuid, err)
	} else {
		log.Debug("Expired shared link removed:", pathUuid)
	}
}

func (mg *Manager) ListFiles(session *session.Session, path string, recursive bool) (files []*pb.File, err error) {
	files, err = mg.dao.GetFilesByPath(path, recursive, false)
	if err != nil {
		return nil, err
	}
	// Issue #132: say which entries live under an upload-only folder and
	// how many older versions each file keeps, so the clients can show the
	// lock and the versions badge without a request per row.
	folders, err := mg.dao.GetUploadOnlyFolders()
	if err != nil {
		return nil, err
	}
	versions, err := mg.dao.CountFileVersions(path, !recursive)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		f.UploadOnly = underUploadOnly(f.Path, f.Mime == "inode/directory", folders)
		f.Versions = versions[f.Path]
		// Issue #141: a file whose content is missing (or empty) on the
		// disk is listed with no hash, so a sync client that has the file
		// sees a difference and sends it again - which restores it (see
		// UploadFile). Listed with its real hash, every client thought it
		// was in sync and the loss was permanent.
		// Issue #173: from what the device knows is missing, not a stat per
		// file (missingblobs.go).
		if f.Mime != "inode/directory" && f.Hash != "" && missingBlobs.has(f.Hash) {
			f.Hash = ""
		}
	}

	return files, nil
}

// alert (issue #64) is how background processing tells the owner about a
// file it could not handle: an Error notification in the Alerts section
// (grouped by dao.AddErrorNotification so a failing batch is one row),
// plus the server log as before. what completes "<file name> ..." - e.g.
// "could not be processed".
func (mg *Manager) alert(what string, path string, err error) {
	log.Error("a file "+what+":", err)
	title := filepath.Base(path) + " " + what
	if e := mg.dao.AddErrorNotification(title, err.Error()); e != nil {
		log.Error("error recording the alert:", e)
	}
}

// ErrUploadOnly is the refusal for a delete under an upload-only folder
// (issue #132); the handler turns it into RespEnvelope.error_code.
var ErrUploadOnly = errors.New("this folder is upload only: nothing in it can be deleted")

// folderPath is a folder as upload_only_folders stores it: with its
// trailing slash, so "/kim/" never also covers "/kimono/".
func folderPath(path string) string {
	if !strings.HasSuffix(path, "/") {
		return path + "/"
	}

	return path
}

func underUploadOnly(path string, isDir bool, folders []string) bool {
	p := path
	if isDir {
		p = folderPath(p)
	}
	for _, f := range folders {
		if strings.HasPrefix(p, f) {
			return true
		}
	}

	return false
}

// isUploadOnly is whether path (a file, or a folder given with or without
// its slash) is inside a folder the owner flagged with SetUploadOnly.
func (mg *Manager) isUploadOnly(path string, isDir bool) (bool, error) {
	folders, err := mg.dao.GetUploadOnlyFolders()
	if err != nil {
		return false, err
	}

	return underUploadOnly(path, isDir, folders), nil
}

// SetUploadOnly flags or clears a folder (issue #132).
func (mg *Manager) SetUploadOnly(path string, on bool) error {
	return mg.dao.SetUploadOnlyFolder(folderPath(path), on)
}

// FileVersions lists a path's older versions (issue #132), newest first.
func (mg *Manager) FileVersions(path string) ([]*pb.File, error) {
	return mg.dao.GetFileVersions(path)
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

// resolvePaths (issue #116) turns what a client selected into the files it
// meant. A directory has no row of its own - it exists only as a prefix
// shared by the paths under it - so anything that isn't a file row is
// taken to be one and expanded to every file beneath it, recursively.
// The trailing slash is normalised because clients disagree on it: the
// web's rows carry "/kim", the listing's own regexp wants "/kim/", and
// without the slash "/kim" would also match "/kimono/...".
//
// An empty directory can't be told apart from a typo (both have no rows),
// so both are an error rather than a silent no-op: sharing "nothing" is
// never what was asked for.
func (mg *Manager) resolvePaths(paths []string) (files []*pb.File, err error) {
	for _, path := range paths {
		if file, fileErr := mg.dao.GetFileByPath(path); fileErr == nil {
			files = append(files, file)
			continue
		}
		dir := strings.TrimSuffix(path, "/") + "/"
		under, listErr := mg.dao.GetFilesByPath(dir, true, false)
		if listErr != nil {
			return nil, listErr
		}
		if len(under) == 0 {
			return nil, fmt.Errorf("no such file or directory: %s", path)
		}
		files = append(files, under...)
	}
	return files, nil
}

func (mg *Manager) GetSharedLink(session *session.Session, paths []string, domain string) (link string, err error) {
	// Issue #116: a selection may hold directories; each becomes every
	// file under it, keeping its structure inside the archive (the common
	// prefix stripped below is the directory itself when one folder was
	// shared, so its contents land at the archive root with their own
	// subfolders intact).
	// resolvePaths' rows are already whole (the same columns
	// GetFileByPath reads): no second query per file.
	files, err := mg.resolvePaths(paths)
	if err != nil {
		return "", err
	}

	// Stripping the directory common to every file in this share keeps
	// the archive flat when everything came from one folder, while still
	// not colliding same-named files from different folders.
	filePaths := make([]string, len(files))
	for i, file := range files {
		filePaths[i] = file.Path
	}
	prefix := commonDirPrefix(filePaths)

	// The archive is streamed: each file is copied out of its blob a
	// segment at a time, into a zip that is itself sealed in segments under
	// the link's own key (the secret in the link, never stored) - nothing
	// is held whole in memory, whatever the share's size. The files go in
	// as they are stored (a HEIC stays HEIC).
	secret := randomSecret() // issue #157: not a UUID
	keys := linkKeys{getCipher(secret)}
	pathUuid := uuid.New().String()
	targetPath := fmt.Sprintf("%s/%s", cfg.GetStr("otc", "storage-path"), pathUuid)
	out, err := blobstore.Create(targetPath, keys)
	if err != nil {
		return "", err
	}
	zw := zip.NewWriter(out)
	for _, file := range files {
		h := &zip.FileHeader{Name: strings.TrimPrefix(file.Path, prefix), Method: zipMethodFor(file.Mime)}
		h.SetModTime(file.Modified.AsTime())
		h.SetMode(0644)
		wr, err := zw.CreateHeader(h)
		if err != nil {
			out.Abort()
			return "", err
		}
		blob, err := blobstore.Open(blobPath(file.Hash), session)
		if err != nil {
			out.Abort()
			mg.alert("could not be read", file.Path, err)
			return "", fmt.Errorf("the content of %s is missing or unreadable on this device", file.Path)
		}
		_, err = io.Copy(wr, io.NewSectionReader(blob, 0, blob.Size()))
		blob.Close()
		if err != nil {
			out.Abort()
			return "", err
		}
	}
	if err := zw.Close(); err != nil {
		out.Abort()
		return "", err
	}
	if err := out.Commit(); err != nil {
		return "", err
	}
	size := 0
	if info, statErr := os.Stat(targetPath); statErr == nil {
		size = int(info.Size())
	}

	if err = mg.dao.InsertSharedLink(pathUuid, size); err != nil {
		// Without its row the archive can never be opened nor expired.
		os.Remove(targetPath)
		return "", err
	}

	return "https://" + domain + "/" + CDownloadAttr + pathUuid + "_" + secret, nil
}

// zipMethodFor is how a file goes into a share archive: stored as it is
// when its format is already compressed (photos, videos, most audio,
// archives) - deflating those took most of the time a share of photos or
// videos took on a Pi, for about 1% - deflated otherwise, and whenever the
// type is unknown.
func zipMethodFor(mime string) uint16 {
	m := strings.ToLower(strings.TrimSpace(strings.SplitN(mime, ";", 2)[0]))
	switch {
	case strings.HasPrefix(m, "video/"):
		return zip.Store
	case strings.HasPrefix(m, "audio/") && m != "audio/wav" && m != "audio/x-wav" && m != "audio/vnd.wave" && m != "audio/aiff" && m != "audio/x-aiff":
		return zip.Store
	}
	switch m {
	case "image/jpeg", "image/heic", "image/heif", "image/heic-sequence", "image/heif-sequence", "image/png", "image/gif", "image/webp", "image/avif", "image/jxl",
		"application/zip", "application/gzip", "application/x-gzip", "application/x-7z-compressed", "application/x-rar-compressed", "application/vnd.rar", "application/x-xz", "application/x-bzip2", "application/zstd":
		return zip.Store
	}
	return zip.Deflate
}

// linkKeys opens a share link's archive with the key from its secret.
type linkKeys struct{ aead cipher.AEAD }

func (k linkKeys) AEAD() cipher.AEAD { return k.aead }

func (mg *Manager) OpenSharedLink(uuid, secret string) (content []byte, err error) {
	data, _, err := mg.OpenSharedLinkRange(uuid, secret, 0, -1)
	return data, err
}

// OpenSharedLinkRange reads length bytes of a share link's archive from
// offset (length < 0: all of it), and the archive's size. A range decrypts
// only the segments it covers.
func (mg *Manager) OpenSharedLinkRange(uuid, secret string, offset int64, length int) ([]byte, int64, error) {
	// Asked before signing in, so only an archive's own id gets this far:
	// a gallery's row (its own, longer expiry) or an id in another case
	// (the column's collation matches it, the folder name doesn't) could
	// otherwise be deleted below by anyone holding its link, secret or not.
	if !galleryUUID.MatchString(uuid) {
		return nil, 0, sql.ErrNoRows // the same answer as an unknown id
	}
	created, expires, err := mg.dao.GetArchiveLinkExpiry(uuid)
	if err != nil {
		return nil, 0, err
	}
	if sharedLinkExpired(created, expires, time.Now(), mg.sharedLinkTTL) {
		// Don't wait for the next sweep: drop the content and row now
		// that we know it's expired, and refuse the download.
		mg.deleteSharedLink(uuid)
		return nil, 0, errors.New("shared link has expired")
	}
	blob, err := blobstore.Open(fmt.Sprintf("%s/%s", cfg.GetStr("otc", "storage-path"), uuid), linkKeys{getCipher(secret)})
	if err != nil {
		return nil, 0, err
	}
	defer blob.Close()
	size := blob.Size()
	if offset < 0 || offset > size {
		return nil, size, fmt.Errorf("offset %d outside the archive", offset)
	}
	if length < 0 {
		// Issue #166: the whole archive in one reply only while it is
		// small; anything larger is fetched in parts (the web page does).
		if size-offset > MaxChunk {
			return nil, size, fmt.Errorf("the archive is %d bytes: download it in parts", size)
		}
		length = int(size - offset)
	}
	if length > MaxChunk {
		length = MaxChunk
	}
	if rest := size - offset; int64(length) > rest {
		length = int(rest)
	}
	buf := make([]byte, length)
	n, err := blob.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, size, err
	}
	return buf[:n], size, nil
}

func (mg *Manager) GetThumbnail(session *session.Session, file *pb.File) (content []byte, err error) {
	content, err = mg.readThumbnail(session, file)
	if err != nil {
		log.Error("error reading the thumbnail of", file.Hash, err)
	}

	return content, err
}

// Thumbnails limits for the Files grid (GetThumbnails).
const (
	MaxThumbnailsPerRequest = 48
	maxThumbnailsBytes      = 8 << 20
)

// Thumbnails returns, for each path that is a photo or video with a stored
// thumbnail, its row with the thumbnail as content - the Files grid view.
// Anything else (a folder, a document, a thumbnail not made yet) is left
// out, and it stops at maxThumbnailsBytes: the client asks for the rest.
func (mg *Manager) Thumbnails(ses *session.Session, paths []string) []*pb.File {
	if len(paths) > MaxThumbnailsPerRequest {
		paths = paths[:MaxThumbnailsPerRequest]
	}
	var out []*pb.File
	total := 0
	for _, p := range paths {
		f, err := mg.dao.GetFileByPath(p)
		if err != nil || !isMedia(f) {
			continue
		}
		thumb, err := mg.readThumbnail(ses, f)
		if err != nil {
			continue
		}
		if total+len(thumb) > maxThumbnailsBytes && len(out) > 0 {
			break
		}
		total += len(thumb)
		out = append(out, &pb.File{Path: f.Path, Hash: f.Hash, Mime: f.Mime, Size: f.Size, Content: thumb})
	}
	return out
}

// readThumbnail is GetThumbnail without the logging, for callers where a
// thumbnail that doesn't exist yet is expected (os.IsNotExist on err).
func (mg *Manager) readThumbnail(session *session.Session, file *pb.File) ([]byte, error) {
	return blobstore.ReadAll(fmt.Sprintf("%s/%s_thumbnail", cfg.GetStr("otc", "storage-path"), file.Hash), session)
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
func (mg *Manager) ImageSearch(session *session.Session, path string, tags []string, oldToken string, includeVideos bool, personIDs []string, groupID string, before *time.Time, have int32) (files []*pb.File, token string, err error) {
	log.Debug("Image search, token:", oldToken)
	tokenFound := false
	if oldToken != "" && before == nil {
		// Both halves of this have to be checked before the value is
		// used. A token the device no longer holds - expired after
		// cToeknsTTL of no scrolling, or simply gone because the process
		// restarted since the client got it - makes Load return a nil
		// value, and asserting a type on that panics. It did: the
		// connection handler's recover caught it, answered "internal
		// error" and closed the connection, which a client reads as its
		// gallery quietly refusing to load any more photos halfway down
		// the grid.
		//
		// !tokenFound below is already the intended answer for an
		// unknown token - start the search again from the beginning -
		// it just never got the chance to run.
		if cached, ok := mg.searchTokens.Load(oldToken); ok {
			if cachedFiles, isFiles := cached.([]*pb.File); isFiles {
				files = cachedFiles
				token = oldToken
				tokenFound = true
			}
		}
	}
	if !tokenFound {
		files, err = mg.dao.SearchMedia(path, tags, personIDs, groupID, !includeVideos, before)
		// One tile per photo, not per path: the same picture synced from
		// two places (a phone that got a new install id and sent its
		// library again under a new folder, a copy in two folders on the
		// desktop) is one blob and showed up twice. Files still lists
		// every path; the first row of a hash is kept, in the search's
		// own order.
		files = uniqueByHash(files)
		if err != nil {
			return
		}
		// The client was resuming a search this device no longer holds a
		// token for. Starting it again from the top would hand back
		// results it already has (which it discards as duplicates),
		// leaving its grid looking like the library ended where the
		// token did - so skip forward to where it actually got to. Only
		// ever applied when resuming: a search starting from scratch
		// sends no token and must never skip anything.
		if oldToken != "" && have > 0 {
			if int(have) >= len(files) {
				files = nil
			} else {
				files = files[have:]
			}
			log.Debug("Resumed a search with an unknown token, skipped:", have)
		}
		token = uuid.New().String()
		log.Debug("New Token:", token)
	}

	// Issue #147: a photo or video shows up only once it is processed -
	// its thumbnail is the last thing processing writes, so a file
	// without one (just uploaded, still queued) is left out rather than
	// drawn as an empty box. The page is filled from further down the
	// results instead, so it stays full; a later search (the gallery's
	// refresh) picks the file up once it's ready. A missing thumbnail is
	// a plain failed read here, not a scan of the whole result set.
	toReturn := int(cfg.GetInt("tagger", "max-images-search"))
	page := make([]*pb.File, 0, toReturn)
	next := 0
	for ; next < len(files) && len(page) < toReturn; next++ {
		file := files[next]
		content, thumbErr := mg.readThumbnail(session, file)
		if thumbErr != nil {
			if !os.IsNotExist(thumbErr) {
				log.Error("error reading the thumbnail of", file.Hash, thumbErr)
			}
			continue
		}
		// A copy per page: the rows behind a token are shared by every
		// request that names it, and two of them (a quick double scroll)
		// wrote Content on the same *pb.File while another was marshalling
		// it (issue #171).
		file = proto.Clone(file).(*pb.File)
		file.Content = content
		page = append(page, file)
	}
	if next < len(files) {
		// A copy of what's left, not files[next:]: that slice shares the
		// whole result's backing array, so every file already served - its
		// thumbnail included - stayed reachable for as long as the token
		// lived (issue #173). The rows kept have no content yet.
		mg.searchTokens.Store(token, append([]*pb.File(nil), files[next:]...))
		mg.tokensToExpire.Store(token, time.Now())
	} else {
		log.Debug("End for token:", token)
		token = "" // We reached the end
	}

	return page, token, nil
}

// PhotoDateBuckets (issue #77) answers "how many photos per month" for the
// gallery's date scrubber - a thin passthrough, no thumbnails/encryption
// involved since it's just counts, not files.
func (mg *Manager) PhotoDateBuckets(tags []string, personIDs []string, groupID string, includeVideos bool) ([]dao.DateBucket, error) {
	return mg.dao.SearchMediaDateBuckets(tags, personIDs, groupID, !includeVideos)
}

// ListImageGroups (issue #115) lists the albums with a cover picture each:
// the dao picks a random member's hash, and this decrypts that member's
// thumbnail with the session's key - the same GetThumbnail every search
// result goes through, which only needs the hash. A group with no cover
// (empty, or every member since deleted) just has no picture.
func (mg *Manager) ListImageGroups(session *session.Session) ([]*pb.ImageGroup, error) {
	groups, err := mg.dao.ListImageGroups()
	if err != nil {
		return nil, err
	}
	out := make([]*pb.ImageGroup, 0, len(groups))
	for _, g := range groups {
		item := &pb.ImageGroup{Id: g.ID, Name: g.Name, FileCount: int32(g.FileCount)}
		if g.CoverHash != "" {
			if thumb, err := mg.GetThumbnail(session, &pb.File{Hash: g.CoverHash}); err == nil {
				item.CoverThumbnail = thumb
			} else {
				log.Error("error reading a group cover thumbnail:", err)
			}
		}
		out = append(out, item)
	}
	return out, nil
}

// GetFile serves the current content of path, or (issue #132) the older
// version with the given hash when one is named.
func (mg *Manager) GetFile(session *session.Session, path, versionHash string) (file *pb.File, err error) {
	if versionHash != "" {
		file, err = mg.dao.GetFileVersion(path, versionHash)
	} else {
		file, err = mg.dao.GetFileByPath(path)
	}
	if err == nil {
		// These used to be logged and swallowed - the inner err shadowed
		// this one - so a blob missing from the disk came back as a File
		// with empty content and no error. A two-way sync client wrote
		// that as a 0-byte file and, on its next pass, uploaded the empty
		// file back over the row. The owner hears about it (#64) and the
		// client gets an error it can retry.
		var content []byte
		content, err = blobstore.ReadAll(blobPath(file.Hash), session)
		if errors.Is(err, os.ErrNotExist) {
			// Alerted once, as ReadFile does: the daily integrity check
			// keeps reporting it.
			if !missingBlobs.has(file.Hash) {
				mg.alert("could not be read", path, err)
			}
			missingBlobs.set(file.Hash, true)
			return nil, fmt.Errorf("the content of %s is missing on this device", path)
		}
		if err != nil {
			mg.alert("could not be decrypted", path, err)
			return nil, fmt.Errorf("the content of %s is unreadable on this device", path)
		}

		// Issue #44: the thumbnail is already a JPEG (converted at upload
		// time), but the full-size original was still served as raw HEIC
		// — unrenderable by an <img>/<video poster> in every browser
		// except Safari, so "view full size" just showed nothing. Convert
		// here too, the same way, so it actually displays everywhere.
		if isHeicFile(file.Path, file.Mime) && !isJPEGContent(content) {
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

// WaitForContent waits, up to timeout, for the content of the file at path
// to be on disk. An upload is acknowledged as soon as its row is stored;
// the content is written right after, in the background - and posting a
// photo or video just taken (issue #150) comes that quickly, before a large
// video is written. Returns at once for a file that isn't there at all.
func (mg *Manager) WaitForContent(path string, timeout time.Duration) {
	file, err := mg.dao.GetFileByPath(path)
	if err != nil || file == nil {
		return
	}
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(blobPath(file.Hash)); err == nil || time.Now().After(deadline) {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// isJPEGContent reports whether content starts with a JPEG's signature.
func isJPEGContent(content []byte) bool {
	return len(content) > 3 && content[0] == 0xFF && content[1] == 0xD8 && content[2] == 0xFF
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
	var ex *exifinfo.Info
	if strings.HasPrefix(file.Mime, "video/") && mg.videoSourceFn != nil {
		// A video's metadata is in its container: ffprobe streams the
		// parts it needs instead of the video being loaded whole.
		src, done, srcErr := mg.videoSource(session, file)
		if srcErr != nil {
			return nil, srcErr
		}
		ex, err = exifinfo.FromVideoSource(src)
		done()
	} else {
		// Issue #166: read whole for its metadata, within the budget.
		release := mg.ReserveBytes(budgetSize(file))
		var content []byte
		content, err = blobstore.ReadAll(blobPath(file.Hash), session)
		if err == nil {
			ex, err = extractExif(content, file.Mime, file.Path)
		}
		release()
	}
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

// DelPath (issue #116) deletes a file, or - when path is a directory -
// every file under it. Files go one at a time through DelFile so the
// dedup bookkeeping there (a blob is only removed once its last path is
// gone) holds for each; a directory that becomes empty simply stops
// being listed, since it was never anything but its files' paths.
func (mg *Manager) DelPath(session *session.Session, path string) error {
	entries, err := mg.resolvePaths([]string{path})
	if err != nil {
		return err
	}
	// Issue #132: refused as a whole before anything goes, so a folder
	// holding an upload-only subfolder isn't half deleted.
	folders, err := mg.dao.GetUploadOnlyFolders()
	if err != nil {
		return err
	}
	if underUploadOnly(path, true, folders) {
		return ErrUploadOnly
	}
	for _, entry := range entries {
		if underUploadOnly(entry.Path, false, folders) {
			return ErrUploadOnly
		}
	}
	for _, entry := range entries {
		if err := mg.DelFile(session, entry.Path); err != nil {
			return err
		}
	}
	return nil
}

func (mg *Manager) DelFile(session *session.Session, path string) (err error) {
	// Issue #173: the deleted row's hash comes back from the delete's own
	// transaction instead of a GetFileByPath beforehand - one query less
	// per file (every file of a deleted folder comes through here), and
	// the hash is the one of the row actually deleted. A missing path
	// still fails with sql.ErrNoRows, as that GetFileByPath did.
	hash, err := mg.dao.DelFileByPathHash(path)
	if err != nil {
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
	// Issue #132: the path's kept versions go with it (the folder is no
	// longer upload only, or this never was); their blobs too, unless
	// another path or version still uses them.
	versionHashes, err := mg.dao.DelFileVersions(path)
	if err != nil {
		return err
	}
	for _, h := range append([]string{hash}, versionHashes...) {
		if err := mg.removeBlobIfUnused(h); err != nil {
			return err
		}
	}

	return nil
}

// UploadFile stores content under path. cloudID is the photo library's own
// name for the asset (UploadFile.cloud_id in the proto), kept with the row
// and attached to any other row of the same content.
func (mg *Manager) UploadFile(session *session.Session, path string, content []byte, forceOverride bool, created, modified *timestamppb.Timestamp, cloudID string) (file *pb.File, err error) {
	mimeType := mimetype.Detect(content)
	log.Debug("Mime type:", mimeType.String())

	// Calculate the SHA256 of the file to be used as unique hash
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])

	targetPath := blobPath(hash)

	// The content goes to disk before the row exists, not after: the row
	// used to be committed (and the client told "saved") while the write
	// still waited its turn in a background goroutine, so an update's
	// restart, a crash or running out of memory in between left a file the
	// device listed - with the right hash, so no client ever sent it again -
	// and had no content for (Cala, IMG_5259.MOV and three more, lost to
	// the restart of an update). Now a restart at any point leaves either
	// nothing or a complete file. Under the hash's lock (issue #141): a
	// DelFile of another path with this content checks-and-removes under
	// the same lock.
	wrote := false
	if !mg.hasBlob(hash) {
		unlock := lockBlob(hash)
		err = blobstore.WriteBytes(targetPath, session, content)
		unlock()
		if err != nil {
			mg.alert("could not be saved to disk", path, err)
			return nil, err
		}
		wrote = true
	}

	file, write, err := mg.registerUpload(session, path, hash, mimeType.String(), int64(len(content)), forceOverride, created, modified, cloudID)
	if err != nil {
		if wrote {
			mg.removeBlobIfUnused(hash)
		}
		return file, err
	}
	if !write {
		return file, nil
	}
	// Written above, but a DelFile of another path with the same content,
	// between the write and the row, could have taken it as unused.
	if !mg.hasBlob(hash) {
		unlock := lockBlob(hash)
		err = blobstore.WriteBytes(targetPath, session, content)
		unlock()
		if err != nil {
			mg.alert("could not be saved to disk", path, err)
			return nil, err
		}
	}

	// Processing happens in the background, from what is now on the disk
	// (issue #165): the thumbnail in the fast lane, tags and faces in the
	// slow one (lanes.go). The upload's memory is free as soon as this
	// returns; a video is streamed to ffmpeg rather than read whole.
	mg.enqueueMedia(session, file, targetPath)

	return
}

// registerUpload records the row for an upload of path with content hash
// (the part of an upload that isn't writing the bytes), shared by
// UploadFile and the chunked upload (FinishUpload). write reports whether
// the content still has to be written: false when the path already has
// exactly this content on disk.
func (mg *Manager) registerUpload(session *session.Session, path, hash, mime string, size int64, forceOverride bool, created, modified *timestamppb.Timestamp, cloudID string) (file *pb.File, write bool, err error) {
	if created == nil {
		created = timestamppb.Now()
	}
	// Issue #134: the file's own modification time when the client sent
	// it (the sync clients do), so it survives the round trip to another
	// computer; the upload time otherwise, as before.
	if modified == nil {
		modified = timestamppb.Now()
	}

	file = &pb.File{
		Created:  created,
		Modified: modified,
		Path:     path,
		Mime:     mime,
		Hash:     hash,
		Size:     int32(size),
	}

	duplicated, err := mg.dao.StoreNewFile(file, cloudID)
	if err != nil {
		return nil, false, err
	}
	if err := mg.dao.SetCloudIDForHash(hash, cloudID); err != nil {
		log.Error("error recording the cloud id:", err)
	}

	restoring := false
	if duplicated {
		// Bug fix: this used to reassign `file` itself to the *existing*
		// row (old hash/size/mime), then re-store that same stale `file`
		// on the forceOverride path below - silently discarding the new
		// content's metadata (see git history for the full story).
		existing, err := mg.dao.GetFileByPath(path)
		if err != nil {
			return nil, false, err
		}
		if existing.Hash == hash {
			if mg.hasBlob(hash) {
				log.Debug("Same file with same content for:", path, hash)
				return existing, false, nil
			}
			// Issue #141: the row is right but its content was missing (or
			// empty) on the disk - this upload brings it back; nothing
			// about the row changes, only the write happens.
			log.Info("restoring the missing content of", hash, "from this upload")
			restoring = true
		}
	}
	if duplicated && !restoring {
		// Issue #132: in an upload-only folder the old content is kept as
		// a version and the new one becomes current - whether or not the
		// client asked to override, nothing there is ever lost.
		uploadOnly, err := mg.isUploadOnly(path, false)
		if err != nil {
			return nil, false, err
		}
		switch {
		case uploadOnly:
			if err = mg.dao.ReplaceFileKeepingVersion(file, cloudID); err != nil {
				return nil, false, err
			}
		case forceOverride:
			mg.DelFile(session, path)
			if _, err = mg.dao.StoreNewFile(file, cloudID); err != nil {
				return nil, false, err
			}
		default:
			return nil, false, errors.New("Duplicated file")
		}
	}

	return file, true, nil
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
	if mg.processMedia(session, file, targetPath, content, stageAll) {
		mg.donePendingAnalysis(file.Hash)
	}
}

// imageTaggingEnabled is issue #181's switch, read per file like face
// recognition's. Tagging stays on when it can't be read.
func (mg *Manager) imageTaggingEnabled() bool {
	enabled, err := mg.dao.GetImageTaggingEnabled()
	if err != nil {
		log.Error("error checking image_tagging_enabled, tagging:", err)
		return true
	}
	return enabled
}

// mediaStages picks what processMedia does: the thumbnail (the fast lane,
// see lanes.go), the analysis - tags and faces - (the slow lane), or both
// from one decode (backfill, reprocess).
type mediaStages int

const (
	stageThumbnail mediaStages = 1 << iota
	stageAnalysis
	stageAll = stageThumbnail | stageAnalysis
)

// processMedia is processMediaContent limited to stages. It reports false
// when the file could not be decoded at all (already alerted), so the
// fast lane doesn't queue an analysis bound to fail the same way.
func (mg *Manager) processMedia(session *session.Session, file *pb.File, targetPath string, content []byte, stages mediaStages) bool {
	// Issue #171: the file may be deleted while this runs - its content,
	// thumbnail and tags were then written for a hash nothing uses.
	defer mg.dropIfOrphaned(file.Hash)
	// We will try to create a thumbnail of images only
	// A ".HEIC" that is really a JPEG (some apps export one under the
	// original's name) is decoded as the JPEG it is.
	isHeic := strings.HasSuffix(file.Path, ".HEIC") && !isJPEGContent(content)
	if file.Mime[:5] == "image" || strings.HasSuffix(file.Path, ".HEIC") {
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
				mg.alert("could not be converted from HEIC", file.Path, err)
				return false
			}
		}

		startClass := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		img, err := decodeImage(content)
		if err != nil && checkImageSize(content) == nil {
			// What Go can't read - JPEG 2000, Photoshop, camera RAW
			// (DNG), a JPEG cut short or with a damaged marker - ffmpeg
			// usually can: it is already here for videos.
			if fallback, ffErr := decodeWithFFmpeg(content); ffErr == nil {
				img, err = fallback, nil
			} else {
				log.Debug("ffmpeg could not decode", file.Path, "either:", ffErr)
			}
		}
		if err != nil {
			mg.alert("could not be processed", file.Path, err)
			return false
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

		if stages&stageThumbnail != 0 {
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
				mg.alert("has no thumbnail (it could not be encoded)", file.Path, err)
			} else {
				log.Debug("Thumbnail:", fmt.Sprintf("%s_thumbnail", targetPath))
				if err := blobstore.WriteBytes(fmt.Sprintf("%s_thumbnail", targetPath), session, buf.Bytes()); err != nil {
					mg.alert("has no thumbnail (it could not be written)", file.Path, err)
				}
			}
			log.Debug("Time processing thumbnail:", time.Since(startThumb), targetPath)

		}
		if stages&stageAnalysis != 0 {
			var tags []imagestagger.RAMTag
			if mg.imageTaggingEnabled() {
				tags, err = mg.waitForTagger().Tags(ctx, img, imagestagger.DefaultRAMOptions())
				if err != nil {
					mg.alert("could not be tagged", file.Path, err)
				}
			}
			tags = append(tags, locationTags(exif)...)
			log.Debug("Tags:", tags)

			mg.dao.AddTags(file, tags)

			log.Debug("Time classifying image:", time.Since(startClass), targetPath)

			mg.processFaces(session, file, img)
		}
	} else if strings.HasPrefix(file.Mime, "video/") {
		// Videos get tagged the same way images do — search doesn't
		// need to know the difference, since it's all just file_tags
		// rows keyed by hash — just against a handful of frames
		// sampled across the video instead of the one still image.
		startClass := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		// A stored video (content nil) is read by ffmpeg through the
		// device's own loopback stream - seeking to the frames it samples,
		// decrypting only those segments - instead of whole in memory or
		// as a plaintext temp file.
		// The thumbnail alone needs one frame; tagging samples several.
		frameCount := cVideoSampleFrames
		if stages&stageAnalysis == 0 {
			frameCount = 1
		}
		var frames []image.Image
		var exif *exifinfo.Info
		var err error
		if content == nil {
			src, done, srcErr := mg.videoSource(session, file)
			if srcErr != nil {
				mg.alert("could not be processed", file.Path, srcErr)
				return false
			}
			frames, err = extractVideoFramesFrom(src, frameCount)
			if err == nil {
				exif, _ = exifinfo.FromVideoSource(src)
			}
			done()
		} else {
			frames, err = extractVideoFrames(content, frameCount)
			if err == nil {
				exif, _ = exifinfo.FromVideo(content)
			}
		}
		if err != nil {
			mg.alert("could not be processed", file.Path, err)
			return false
		}
		if exif == nil {
			err = errors.New("no metadata")
		}
		if err != nil {
			log.Debug("no location metadata for", targetPath, ":", err)
			exif = nil
		}

		if stages&stageAnalysis != 0 {
			var tags []imagestagger.RAMTag
			if mg.imageTaggingEnabled() {
				tags = tagVideoFrames(ctx, mg.waitForTagger(), frames)
			}
			tags = append(tags, locationTags(exif)...)
			log.Debug("Tags:", tags)

			mg.dao.AddTags(file, tags)

			log.Debug("Time classifying video:", time.Since(startClass), targetPath)
		}

		if stages&stageThumbnail != 0 {
			startThumb := time.Now()
			// A thumbnail must exist once a file is uploaded, full stop -
			// the same rule the image branch above spells out, and the same
			// bug this had: it only wrote one when the frame was wider than
			// maxWidth, so a video narrower than the thumbnail cap ended up
			// with no thumbnail on disk at all. NewPublication reads one
			// back unconditionally, so posting such a video failed outright
			// ("open <hash>_thumbnail: no such file or directory") after
			// half a minute of polling for a file nothing was ever going to
			// write. thumbnailSource scales only when scaling is needed,
			// which is what makes "always write one" safe here.
			maxWidth := int(cfg.GetInt("otc", "max-thumbnail-width-px"))
			thumbImg := thumbnailSource(frames[0], maxWidth)
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, thumbImg, &jpeg.Options{Quality: 80}); err != nil {
				mg.alert("has no thumbnail (it could not be encoded)", file.Path, err)
			} else {
				log.Debug("Thumbnail:", fmt.Sprintf("%s_thumbnail", targetPath))
				if err := blobstore.WriteBytes(fmt.Sprintf("%s_thumbnail", targetPath), session, buf.Bytes()); err != nil {
					mg.alert("has no thumbnail (it could not be written)", file.Path, err)
				}
			}
			log.Debug("Time processing thumbnail:", time.Since(startThumb), targetPath)
		}
	}
	return true
}

// HasFile reports whether this device already has a file with this exact
// content, by hash (issue #58) — storage is already deduplicated by hash
// (see UploadFile's targetPath, and DelFile's "another reference with
// another path" check), this just lets a sync client (iOS/macOS) find
// that out *before* spending the bandwidth on a re-upload, via LinkFile
// below, rather than only after the fact like the existing
// duplicated-path check in UploadFile does.
// HasFile answers whether the device holds content with this hash. When it
// does and the client named the asset's cloud id, that id is attached to
// the hash's rows (see files.cloud_id in db.sql).
func (mg *Manager) HasFile(hash, cloudID string) (exists bool, err error) {
	_, err = mg.dao.GetFileByHash(hash)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// A row whose blob is gone from the disk must answer "no", or the
	// client links its path to nothing and never sends the bytes that
	// would repair it - which is exactly how a folder stayed unreadable
	// for days: every sync pass found the hash "already on the device".
	if !mg.hasBlob(hash) {
		log.Error("hash known but its content is missing, asking for an upload:", hash)
		return false, nil
	}
	if err := mg.dao.SetCloudIDForHash(hash, cloudID); err != nil {
		log.Error("error recording the cloud id:", err)
	}

	return true, nil
}

// HasCloudIDs is the photo sync's "which of these do you already have?",
// answered as cloud id -> hash so the client can LinkFile without ever
// downloading the asset.
func (mg *Manager) HasCloudIDs(ids []string) (map[string]string, error) {
	found, err := mg.dao.FindCloudIDs(ids)
	if err != nil {
		return nil, err
	}
	// As HasFile: content that is not on the disk is not "already here".
	for id, hash := range found {
		if !mg.hasBlob(hash) {
			log.Error("hash known but its content is missing, asking for an upload:", hash)
			delete(found, id)
		}
	}

	return found, nil
}

// Issue #141: removing a blob is "is anything still using this hash? no ->
// delete the file", and linking one is "is the file there? yes -> add a
// row using it". Run concurrently for the same hash, the removal could
// check, the link add its row, and the removal then delete content that
// row needs - a row pointing at nothing. Both sides, and the upload's own
// write, hold the hash's lock for their check-and-act. Striped (256
// locks) so memory stays flat however many hashes there are.
var blobLocks [256]sync.Mutex

func lockBlob(hash string) (unlock func()) {
	var stripe byte
	if len(hash) >= 2 {
		if b, err := hex.DecodeString(hash[:2]); err == nil {
			stripe = b[0]
		}
	}
	blobLocks[stripe].Lock()
	return blobLocks[stripe].Unlock
}

// withBlob runs fn (which stores a row using hash) only while the blob is
// on disk, and with no removal of it able to interleave.
func (mg *Manager) withBlob(hash string, fn func() error) error {
	unlock := lockBlob(hash)
	defer unlock()
	if !mg.hasBlob(hash) {
		return errors.New("the content for that hash is missing on this device: upload it instead")
	}
	return fn()
}

// removeBlobIfUnused deletes hash's blob and thumbnail once no file or
// kept version uses it any more.
func (mg *Manager) removeBlobIfUnused(hash string) error {
	unlock := lockBlob(hash)
	defer unlock()
	referenced, err := mg.dao.HashReferenced(hash)
	if err != nil || referenced {
		return err
	}
	fullPath := blobPath(hash)
	if err = os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	os.Remove(fullPath + "_thumbnail")
	os.Remove(fullPath + cNoThumbnailSuffix)
	return nil
}

// writeBlob stores a blob by writing a temporary file next to it and
// renaming it into place (issue #141). Two things the in-place write got
// wrong: a crash or power cut mid-write left a 0-byte blob behind - the
// empty files found on Cala - and a blob left by an older installation,
// owned by another account, could not be overwritten at all ("permission
// denied"), so its content could never be restored. A rename needs write
// access to the directory only, and readers see either the old file or
// the whole new one.
func writeBlob(target string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(target), ".upload-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o600) // perms: rw------- (issue #157: only the service reads it)
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), target)
}

// blobPath is where the encrypted content for hash lives.
func blobPath(hash string) string {
	return fmt.Sprintf("%s/%s", cfg.GetStr("otc", "storage-path"), hash)
}

// dropIfOrphaned removes what processing left for hash - content,
// thumbnail, tags - when no file or kept version uses it any more: the
// file was deleted while it was being processed (issue #171). Under the
// hash's lock, like every other decision to remove a blob, so an upload of
// the same content can't slip in between the check and the removal.
func (mg *Manager) dropIfOrphaned(hash string) {
	if hash == "" {
		return
	}
	unlock := lockBlob(hash)
	defer unlock()
	referenced, err := mg.dao.HashReferenced(hash)
	if err != nil || referenced {
		return
	}
	mg.donePendingAnalysis(hash)
	if err := mg.dao.DelTagsByHash(hash); err != nil {
		log.Error("could not remove the tags of deleted content", hash, ":", err)
	}
	full := blobPath(hash)
	os.Remove(full)
	os.Remove(full + "_thumbnail")
	os.Remove(full + cNoThumbnailSuffix)
	log.Info("removed what processing left for content deleted meanwhile:", hash)
}

// hasBlob is whether the content for hash is actually on the disk, not
// just in the database. An empty blob counts as missing: even empty
// content encrypts to a nonce and a tag.
func (mg *Manager) hasBlob(hash string) bool {
	fi, err := os.Stat(blobPath(hash))
	ok := err == nil && fi.Size() > 0
	missingBlobs.set(hash, !ok) // what listings go by (missingblobs.go)

	return ok
}

// LinkFile registers path as pointing at content this device already has
// (hash) — the on-disk blob, its thumbnail, and its tags are all already
// keyed by hash (see UploadFile above), so a new path sharing an existing
// hash needs none of that redone, just a new `files` row. Mirrors
// UploadFile's own duplicated-path handling (same path already exists:
// no-op if the hash already matches, otherwise only overwritten with
// forceOverride) — the one difference is this never touches disk at all.
func (mg *Manager) LinkFile(session *session.Session, path, hash string, forceOverride bool, created, modified *timestamppb.Timestamp, cloudID string) (file *pb.File, err error) {
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
	if modified == nil {
		modified = timestamppb.Now()
	}

	file = &pb.File{
		Created:  created,
		Modified: modified,
		Path:     path,
		Mime:     existing.Mime,
		Hash:     hash,
		Size:     existing.Size,
	}

	var duplicated bool
	if err := mg.withBlob(hash, func() (err error) {
		duplicated, err = mg.dao.StoreNewFile(file, cloudID)
		return err
	}); err != nil {
		return nil, err
	}
	if err := mg.dao.SetCloudIDForHash(hash, cloudID); err != nil {
		log.Error("error recording the cloud id:", err)
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
		// Issue #132: see UploadFile - an upload-only folder keeps the old
		// content as a version.
		uploadOnly, err := mg.isUploadOnly(path, false)
		if err != nil {
			return nil, err
		}
		if uploadOnly {
			if err := mg.withBlob(hash, func() error { return mg.dao.ReplaceFileKeepingVersion(file, cloudID) }); err != nil {
				return nil, err
			}

			return file, nil
		}
		if !forceOverride {
			return nil, errors.New("Duplicated file")
		}
		// Not under the lock: DelFile takes the locks of the hashes it
		// may remove, and one of them could share this hash's stripe.
		if err := mg.DelFile(session, path); err != nil {
			return nil, err
		}
		if err := mg.withBlob(hash, func() error { _, err := mg.dao.StoreNewFile(file, cloudID); return err }); err != nil {
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

	// Decode HEIC from memory - once its header says it's a sane size
	// (issue #165).
	if err := checkImageSize(heicData); err != nil {
		return nil, err
	}
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

// uniqueByHash keeps the first file of each content hash, in order.
func uniqueByHash(files []*pb.File) []*pb.File {
	seen := make(map[string]bool, len(files))
	out := files[:0]
	for _, f := range files {
		if seen[f.Hash] {
			continue
		}
		seen[f.Hash] = true
		out = append(out, f)
	}
	return out
}
