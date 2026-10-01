// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/image/draw"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/exifinfo"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/mediastream"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
)

// Issue #180: a shared gallery. The photos and videos are copied out of
// the library and re-encrypted under a key derived from a fresh secret
// that only the link carries (https://<device>/shared#<uuid>.<secret>):
// the device keeps the uuid, dates, size and counts, never the secret, so
// nothing in the gallery can be read without the link. Under
// <storage>/shared/<uuid>/: manifest (the description and the items),
// and per item <i>.orig, <i>.thumb and, for a format browsers can't show
// (HEIC, RAW, TIFF) and for any photo but a GIF, <i>.prev - a JPEG of
// screen size.
//
// The copy runs as a background job the owner's client polls
// (SharedGalleryJob); a visitor reads the manifest (OpenSharedGallery),
// thumbnails, previews and originals in 4 MiB parts
// (ReadSharedGalleryItem), and streams videos (SharedGalleryStream).

const (
	// Previews are what a visitor's viewer shows: screen-sized, so a photo
	// opens in a moment even through the bridge on mobile data - the
	// originals (often 10-30 MB) are only fetched to download.
	cGalleryPreviewMaxPx = 2560
	cGalleryPreviewQ     = 88
	cGalleryJobKeep      = time.Hour
	cGalleryMaxTTL       = 90 * 24 * time.Hour
)

var (
	galleryUUID   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	gallerySecret = regexp.MustCompile(`^[0-9a-f]{32,128}$`)
	// Two copies at once at most: each reads and writes whole files.
	galleryCopySlots = make(chan struct{}, 2)
)

// ErrNoSuchGallery is every "can't open it" answer to a visitor - no such
// link, wrong secret, expired - so none of them tells one from another.
var ErrNoSuchGallery = errors.New("this link doesn't exist or has expired")

type galleryManifest struct {
	Description string        `json:"description"`
	Created     int64         `json:"created"`
	Expires     int64         `json:"expires"`
	Items       []galleryItem `json:"items"`
}

type galleryItem struct {
	Name    string `json:"name"`
	Mime    string `json:"mime"`
	Size    int64  `json:"size"`
	Taken   int64  `json:"taken"`
	Thumb   bool   `json:"thumb"`
	Preview bool   `json:"preview"`
}

func galleryDir(id string) string {
	return filepath.Join(cfg.GetStr("otc", "storage-path"), "shared", id)
}

func galleryFile(id string, index int, part pb.GetSharedGalleryItem_Part) string {
	ext := map[pb.GetSharedGalleryItem_Part]string{
		pb.GetSharedGalleryItem_THUMBNAIL: "thumb",
		pb.GetSharedGalleryItem_PREVIEW:   "prev",
		pb.GetSharedGalleryItem_ORIGINAL:  "orig",
	}[part]
	return filepath.Join(galleryDir(id), fmt.Sprintf("%d.%s", index, ext))
}

func isGalleryMedia(mime string) bool {
	return strings.HasPrefix(mime, "image/") || strings.HasPrefix(mime, "video/")
}

// sharedGallerySource is the photos and videos src names, once each, and
// how many other files it held.
func (mg *Manager) sharedGallerySource(src *pb.SharedGallerySource) (files []*pb.File, skipped int, err error) {
	if src == nil {
		return nil, 0, errors.New("nothing to share")
	}
	var all []*pb.File
	switch {
	case len(src.Paths) > 0:
		for _, p := range src.Paths {
			f, err := mg.dao.GetFileByPath(p)
			if err != nil || f == nil {
				return nil, 0, fmt.Errorf("%s is not on this device", p)
			}
			all = append(all, f)
		}
	case src.GroupId != "":
		if all, err = mg.dao.SearchMedia("", nil, nil, src.GroupId, false, nil); err != nil {
			return nil, 0, err
		}
	case src.Directory != "":
		dir := strings.TrimSuffix(src.Directory, "/") + "/"
		if all, err = mg.dao.SearchMedia(dir, nil, nil, "", false, nil); err != nil {
			return nil, 0, err
		}
		if listed, lErr := mg.dao.GetFilesByPath(dir, true, false); lErr == nil {
			for _, f := range listed {
				if f.Mime != "inode/directory" && !isGalleryMedia(f.Mime) {
					skipped++
				}
			}
		}
	default:
		return nil, 0, errors.New("nothing to share")
	}
	seen := map[string]bool{}
	for _, f := range all {
		if !isGalleryMedia(f.Mime) {
			skipped++
			continue
		}
		if seen[f.Hash] {
			continue
		}
		seen[f.Hash] = true
		files = append(files, f)
	}
	return files, skipped, nil
}

// PreviewSharedGallery is what the confirmation shows: how many files and
// how much space the copy takes.
func (mg *Manager) PreviewSharedGallery(src *pb.SharedGallerySource) (*pb.SharedGalleryPreview, error) {
	files, skipped, err := mg.sharedGallerySource(src)
	if err != nil {
		return nil, err
	}
	var total int64
	for _, f := range files {
		total += int64(f.Size)
	}
	return &pb.SharedGalleryPreview{Files: int32(len(files)), Bytes: total, Skipped: int32(skipped)}, nil
}

type galleryJob struct {
	mu       sync.Mutex
	state    *pb.SharedGalleryJob
	finished time.Time
}

var galleryJobs = struct {
	sync.Mutex
	m map[string]*galleryJob
}{m: map[string]*galleryJob{}}

func (j *galleryJob) update(f func(s *pb.SharedGalleryJob)) {
	j.mu.Lock()
	f(j.state)
	j.mu.Unlock()
}

// StartSharedGallery begins copying src into a new gallery and returns the
// job to poll. domain is where the link points (the device's address).
func (mg *Manager) StartSharedGallery(ses *session.Session, src *pb.SharedGallerySource, description string, ttlHours int32, domain string) (*pb.SharedGalleryJob, error) {
	files, _, err := mg.sharedGallerySource(src)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, errors.New("there are no photos or videos to share there")
	}
	ttl := mg.sharedLinkTTL
	if ttlHours > 0 {
		ttl = min(time.Duration(ttlHours)*time.Hour, cGalleryMaxTTL)
	}
	var total int64
	for _, f := range files {
		total += int64(f.Size)
	}
	job := &galleryJob{state: &pb.SharedGalleryJob{JobId: uuid.NewString(), Total: int32(len(files)), BytesTotal: total}}
	galleryJobs.Lock()
	for id, j := range galleryJobs.m {
		if !j.finished.IsZero() && time.Since(j.finished) > cGalleryJobKeep {
			delete(galleryJobs.m, id)
		}
	}
	galleryJobs.m[job.state.JobId] = job
	galleryJobs.Unlock()

	go mg.safely("sharing a gallery of", fmt.Sprintf("%d files", len(files)), func() {
		link, err := mg.buildSharedGallery(ses, files, description, ttl, domain, job)
		job.update(func(s *pb.SharedGalleryJob) {
			s.Finished = true
			if err != nil {
				s.Error = err.Error()
			} else {
				s.Link = link
			}
		})
		job.mu.Lock()
		job.finished = time.Now()
		job.mu.Unlock()
	})
	return job.snapshot(), nil
}

// SharedGalleryJobState is a job's progress; ok false for an unknown job.
func SharedGalleryJobState(id string) (*pb.SharedGalleryJob, bool) {
	galleryJobs.Lock()
	j := galleryJobs.m[id]
	galleryJobs.Unlock()
	if j == nil {
		return nil, false
	}
	return j.snapshot(), true
}

// snapshot is the job's state as it is now, for a reply.
func (j *galleryJob) snapshot() *pb.SharedGalleryJob {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := j.state
	return &pb.SharedGalleryJob{JobId: s.JobId, Done: s.Done, Total: s.Total, BytesDone: s.BytesDone, BytesTotal: s.BytesTotal, Finished: s.Finished, Error: s.Error, Link: s.Link}
}

func (mg *Manager) buildSharedGallery(ses *session.Session, files []*pb.File, description string, ttl time.Duration, domain string, job *galleryJob) (link string, err error) {
	galleryCopySlots <- struct{}{}
	defer func() { <-galleryCopySlots }()

	id := uuid.NewString()
	secret := randomSecret()
	keys := linkKeys{getCipher(secret)}
	dir := galleryDir(id)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(dir)
		}
	}()

	now := time.Now()
	man := galleryManifest{Description: description, Created: now.Unix(), Expires: now.Add(ttl).Unix()}
	var stored int64
	for i, f := range files {
		item := galleryItem{Name: filepath.Base(f.Path), Mime: f.Mime, Size: int64(f.Size)}
		if f.Created != nil {
			item.Taken = f.Created.AsTime().Unix()
		}
		n, err := copyBlob(ses, f.Hash, galleryFile(id, i, pb.GetSharedGalleryItem_ORIGINAL), keys)
		if err != nil {
			return "", fmt.Errorf("could not copy %s: %w", item.Name, err)
		}
		stored += n
		// Every photo gets a screen-sized preview (a 9 MB original took
		// seconds to show, blurred meanwhile); a GIF stays itself, as it
		// may be animated.
		var shown image.Image
		if strings.HasPrefix(f.Mime, "image/") && f.Mime != "image/gif" {
			if prev, img, err := mg.galleryPreview(ses, f); err == nil {
				shown = img
				if n, err := writeSealed(galleryFile(id, i, pb.GetSharedGalleryItem_PREVIEW), prev, keys); err == nil {
					item.Preview = true
					stored += n
				}
			} else {
				log.Error("shared gallery: no preview for a", f.Mime, "file:", err)
			}
		}
		// The library's thumbnail - or, for a file the device hasn't
		// processed yet (just uploaded: 13 of 76 tiles were empty on a
		// freshly installed device), one made here.
		thumb, err := mg.readThumbnail(ses, f)
		if err != nil || len(thumb) == 0 {
			thumb = mg.galleryThumbnail(ses, f, shown)
		}
		if len(thumb) > 0 {
			if n, err := writeSealed(galleryFile(id, i, pb.GetSharedGalleryItem_THUMBNAIL), thumb, keys); err == nil {
				item.Thumb = true
				stored += n
			}
		}
		man.Items = append(man.Items, item)
		job.update(func(s *pb.SharedGalleryJob) { s.Done = int32(i + 1); s.BytesDone += int64(f.Size) })
	}
	raw, _ := json.Marshal(man)
	n, err := writeSealed(filepath.Join(dir, "manifest"), raw, keys)
	if err != nil {
		return "", err
	}
	stored += n
	// The description is the owner's, so the list in Settings shows it:
	// under the owner's own key in the database, under the link's in the
	// manifest.
	if err := mg.dao.InsertSharedGallery(id, stored, len(files), ses.Encrypt([]byte(description)), now.Add(ttl)); err != nil {
		return "", err
	}
	return "https://" + domain + "/shared#" + id + "." + secret, nil
}

// copyBlob decrypts a library blob and writes it sealed under keys, a
// segment at a time, returning what it takes on disk.
func copyBlob(ses *session.Session, hash, target string, keys linkKeys) (int64, error) {
	src, err := blobstore.Open(blobPath(hash), ses)
	if err != nil {
		return 0, err
	}
	defer src.Close()
	w, err := blobstore.Create(target, keys)
	if err != nil {
		return 0, err
	}
	if _, err := io.Copy(w, io.NewSectionReader(src, 0, src.Size())); err != nil {
		w.Abort()
		return 0, err
	}
	if err := w.Commit(); err != nil {
		return 0, err
	}
	return fileSize(target), nil
}

func writeSealed(target string, data []byte, keys linkKeys) (int64, error) {
	w, err := blobstore.Create(target, keys)
	if err != nil {
		return 0, err
	}
	if _, err := w.Write(data); err != nil {
		w.Abort()
		return 0, err
	}
	if err := w.Commit(); err != nil {
		return 0, err
	}
	return fileSize(target), nil
}

func fileSize(p string) int64 {
	if fi, err := os.Stat(p); err == nil {
		return fi.Size()
	}
	return 0
}

// galleryPreview is a JPEG of a photo, at most cGalleryPreviewMaxPx on its
// long side and turned the way the photo is meant to be seen (its EXIF
// orientation, as thumbnails do) - HEIC the way the library shows it.
func (mg *Manager) galleryPreview(ses *session.Session, f *pb.File) ([]byte, image.Image, error) {
	release := mg.ReserveBytes(int64(f.Size) * cDownloadCopies)
	defer release()
	content, err := blobstore.ReadAll(blobPath(f.Hash), ses)
	if err != nil {
		return nil, nil, err
	}
	var img image.Image
	if isHeicFile(f.Path, f.Mime) {
		orientation := 1
		if info, err := exifinfo.FromHEIC(content); err == nil {
			orientation = info.Orientation
		}
		full, err := mg.heicToJpeg(content, cGalleryPreviewQ, orientation)
		if err != nil {
			return nil, nil, err
		}
		if img, err = decodeImage(full); err != nil {
			return full, nil, nil
		}
	} else {
		var err error
		if img, err = decodeImage(content); err != nil {
			if img, err = decodeWithFFmpeg(content); err != nil {
				return nil, nil, err
			}
		}
		if ex, err := extractExif(content, f.Mime, f.Path); err == nil && ex != nil {
			img = applyOrientation(img, ex.Orientation)
		}
	}
	if b := img.Bounds(); max(b.Dx(), b.Dy()) > cGalleryPreviewMaxPx {
		w, h := cGalleryPreviewMaxPx, b.Dy()*cGalleryPreviewMaxPx/b.Dx()
		if b.Dy() > b.Dx() {
			w, h = b.Dx()*cGalleryPreviewMaxPx/b.Dy(), cGalleryPreviewMaxPx
		}
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
		img = dst
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: cGalleryPreviewQ}); err != nil {
		return nil, nil, err
	}
	return buf.Bytes(), img, nil
}

// galleryThumbnail makes a thumbnail for a file the library has none for
// yet: from the photo already decoded for its preview, or a frame of the
// video. Nil when it can't.
func (mg *Manager) galleryThumbnail(ses *session.Session, f *pb.File, shown image.Image) []byte {
	width := int(cfg.GetInt("otc", "max-thumbnail-width-px"))
	if width <= 0 {
		width = 1000
	}
	if strings.HasPrefix(f.Mime, "video/") {
		src, done, err := mg.videoSource(ses, f)
		if err != nil {
			return nil
		}
		defer done()
		thumb, err := GenerateVideoThumbnailFrom(src, width)
		if err != nil {
			log.Error("shared gallery: no thumbnail for a video:", err)
			return nil
		}
		return thumb
	}
	if shown == nil {
		return nil
	}
	img := shown
	if b := img.Bounds(); b.Dx() > width {
		dst := image.NewRGBA(image.Rect(0, 0, width, b.Dy()*width/b.Dx()))
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
		img = dst
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		return nil
	}
	return buf.Bytes()
}

// openGallery checks a visitor's link and reads its manifest.
func (mg *Manager) openGallery(id, secret string) (*galleryManifest, linkKeys, error) {
	if !galleryUUID.MatchString(id) || !gallerySecret.MatchString(secret) {
		return nil, linkKeys{}, ErrNoSuchGallery
	}
	created, expires, err := mg.dao.GetSharedLinkExpiry(id)
	if err != nil {
		return nil, linkKeys{}, ErrNoSuchGallery
	}
	if sharedLinkExpired(created, expires, time.Now(), mg.sharedLinkTTL) {
		mg.deleteSharedLink(id)
		return nil, linkKeys{}, ErrNoSuchGallery
	}
	keys := linkKeys{getCipher(secret)}
	raw, err := blobstore.ReadAll(filepath.Join(galleryDir(id), "manifest"), keys)
	if err != nil {
		// A wrong secret fails to decrypt, the same as no gallery at all.
		return nil, linkKeys{}, ErrNoSuchGallery
	}
	var man galleryManifest
	if err := json.Unmarshal(raw, &man); err != nil {
		return nil, linkKeys{}, ErrNoSuchGallery
	}
	return &man, keys, nil
}

// sharedLinkExpired: past its own expiry, or the device's default when it
// has none.
func sharedLinkExpired(created time.Time, expires sql.NullTime, now time.Time, ttl time.Duration) bool {
	if expires.Valid {
		return now.After(expires.Time)
	}
	return isSharedLinkExpired(created, now, ttl)
}

// OpenSharedGallery is what a visitor's page shows first. Counts as one
// opening.
func (mg *Manager) OpenSharedGallery(id, secret string) (*pb.SharedGallery, error) {
	man, _, err := mg.openGallery(id, secret)
	if err != nil {
		return nil, err
	}
	if err := mg.dao.RecordSharedLinkOpen(id); err != nil {
		log.Error("shared gallery: could not count an opening:", err)
	}
	out := &pb.SharedGallery{
		Description: man.Description,
		Created:     timestamppb.New(time.Unix(man.Created, 0)),
		Expires:     timestamppb.New(time.Unix(man.Expires, 0)),
	}
	for i, it := range man.Items {
		out.Items = append(out.Items, &pb.SharedGalleryItem{
			Index: int32(i), Name: it.Name, Mime: it.Mime, Size: it.Size,
			Taken: timestamppb.New(time.Unix(it.Taken, 0)), HasPreview: it.Preview,
		})
	}
	return out, nil
}

// ReadSharedGalleryItem reads up to length bytes (at most MaxChunk) of one
// part of an item from offset, with that part's total size and type.
func (mg *Manager) ReadSharedGalleryItem(id, secret string, index int, part pb.GetSharedGalleryItem_Part, offset int64, length int) ([]byte, int64, string, error) {
	man, keys, err := mg.openGallery(id, secret)
	if err != nil {
		return nil, 0, "", err
	}
	if index < 0 || index >= len(man.Items) {
		return nil, 0, "", ErrNoSuchGallery
	}
	it := man.Items[index]
	mime := it.Mime
	switch part {
	case pb.GetSharedGalleryItem_THUMBNAIL:
		if !it.Thumb {
			return nil, 0, "", errors.New("no thumbnail")
		}
		mime = "image/jpeg"
	case pb.GetSharedGalleryItem_PREVIEW:
		if it.Preview {
			mime = "image/jpeg"
		} else {
			part = pb.GetSharedGalleryItem_ORIGINAL
		}
	}
	blob, err := blobstore.Open(galleryFile(id, index, part), keys)
	if err != nil {
		return nil, 0, "", ErrNoSuchGallery
	}
	defer blob.Close()
	size := blob.Size()
	if offset < 0 || offset > size {
		return nil, size, mime, fmt.Errorf("offset %d outside the file", offset)
	}
	if length <= 0 || length > MaxChunk {
		length = MaxChunk
	}
	if rest := size - offset; int64(length) > rest {
		length = int(rest)
	}
	buf := make([]byte, length)
	n, err := blob.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, size, mime, err
	}
	return buf[:n], size, mime, nil
}

// SharedGalleryStream is a gallery video as a stream resource: played in
// the browser by ranges, decrypting only what is played.
func (mg *Manager) SharedGalleryStream(id, secret string, index int) (mediastream.Resource, error) {
	man, keys, err := mg.openGallery(id, secret)
	if err != nil {
		return mediastream.Resource{}, err
	}
	if index < 0 || index >= len(man.Items) {
		return mediastream.Resource{}, ErrNoSuchGallery
	}
	it := man.Items[index]
	rel, _ := filepath.Rel(cfg.GetStr("otc", "storage-path"), galleryFile(id, index, pb.GetSharedGalleryItem_ORIGINAL))
	return mediastream.Resource{Kind: mediastream.KindLibraryFile, Path: it.Name, Hash: rel, Mime: it.Mime, Size: it.Size, Keys: keys}, nil
}

// ListSharedLinks is the owner's list (Settings), descriptions decrypted.
func (mg *Manager) ListSharedLinks(ses *session.Session) ([]*pb.SharedLinkInfo, error) {
	rows, err := mg.dao.ListSharedLinks()
	if err != nil {
		return nil, err
	}
	var out []*pb.SharedLinkInfo
	for _, r := range rows {
		info := &pb.SharedLinkInfo{Uuid: r.Uuid, Kind: r.Kind, Created: timestamppb.New(r.Created), Opens: int32(r.Opens), Bytes: r.Bytes, Files: int32(r.Files)}
		if len(r.Description) > 0 {
			if d, err := ses.Decrypt(r.Description); err == nil {
				info.Description = string(d)
			}
		}
		if r.Expires.Valid {
			info.Expires = timestamppb.New(r.Expires.Time)
		} else {
			info.Expires = timestamppb.New(r.Created.Add(mg.sharedLinkTTL))
		}
		if r.LastOpened.Valid {
			info.LastOpened = timestamppb.New(r.LastOpened.Time)
		}
		out = append(out, info)
	}
	return out, nil
}

// DeleteSharedLinkNow removes a share link and everything it shared.
func (mg *Manager) DeleteSharedLinkNow(id string) error {
	if !galleryUUID.MatchString(id) {
		return errors.New("no such link")
	}
	if _, _, err := mg.dao.GetSharedLinkExpiry(id); err != nil {
		return errors.New("no such link")
	}
	mg.deleteSharedLink(id)
	return nil
}

// CountSharedLinkOpen counts one opening of a share link (a download of an
// archive starting).
func (mg *Manager) CountSharedLinkOpen(id string) {
	if err := mg.dao.RecordSharedLinkOpen(id); err != nil {
		log.Error("could not count a share link opening:", err)
	}
}
