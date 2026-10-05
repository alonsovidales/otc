// SPDX-License-Identifier: AGPL-3.0-or-later

package social

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/dao"
	filesmanager "github.com/alonsovidales/otc/files_manager"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/profile"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/push"
	"github.com/alonsovidales/otc/session"
	"github.com/alonsovidales/otc/settings"
	"github.com/alonsovidales/otc/wsframe"
	"github.com/google/uuid"
	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	ActionCreate        = "create"
	ActionModify        = "modify"
	ActionDelete        = "delete"
	PublicationEvent    = "publication"
	LikeEvent           = "like_event"
	LikeCommentEvent    = "like_comment_event"
	CommentEvent        = "comment"
	DelPublicationEvent = "del_publication_event"
	DelCommentEvent     = "del_comment_event"
	// ForgetEvent (issue #174): "delete everything I shared with you and
	// drop the friendship" - logged for one ex-friend only (events.target)
	// when its device couldn't be reached straight away.
	ForgetEvent = "forget_event"

	// NewPublication's wait for a just-uploaded file's background thumbnail
	// (see its own doc comment) — polling interval and how many times to
	// poll before giving up. Was 20 attempts (~5s total), sized when the
	// thumbnail step was just a resize; it now also runs EXIF extraction,
	// HEIC container parsing, and orientation correction (issue #66) on
	// every image, not only HEIC ones — measured taking 8+ seconds for a
	// single large photo on a Raspberry Pi, comfortably past the old
	// budget, which is exactly what turned "publish a brand new photo"
	// into "error trying to create publication: ... no such file or
	// directory" for a real, un-raced upload that just hadn't finished yet.
	cThumbnailPollInterval = 250 * time.Millisecond
	cThumbnailPollAttempts = 119 // ~30s total at cThumbnailPollInterval, plus the first immediate try

	// cSocialVideoSizeLimit (issue #60): a video attached to a new post
	// larger than this gets compressed down (see
	// filesmanager.CompressVideoForSocial) to a new, separate file before
	// publishing, rather than distributing the original at full size to
	// every friend's feed. The original stays exactly as the owner has it
	// in Files/the gallery - only the *published* copy is the compressed
	// one.
	cSocialVideoSizeLimit = 10 * 1024 * 1024
)

type Social struct {
	dao          *dao.Dao
	filesmanager *filesmanager.Manager
	settings     *settings.Settings
	profile      *profile.Profile
	push         *push.Push

	// stuck is, per friend's domain, the post of theirs that the last
	// syncs stopped at and how many did (see cPostTries).
	stuckMu sync.Mutex
	stuck   map[string]stuckPost
}

type LikePublicationComment struct {
	Uuid         string `json:"uuid"`
	Action       string `json:"action"`
	CommentUUID  string `json:"comment_uuid"`
	Dt           int64  `json:"dt"`
	FriendDomain string `json:"friend_domain"`
}

type LikePublication struct {
	Uuid         string `json:"uuid"`
	Action       string `json:"action"`
	PubUUID      string `json:"pub_uuid"`
	Dt           int64  `json:"dt"`
	FriendDomain string `json:"friend_domain"`
}

type Publication struct {
	Uuid   string `json:"uuid"`
	Action string `json:"action"`
	Dt     int64  `json:"dt"`
	Text   string `json:"comment"`
}

// DelPublication is the event payload broadcast when the owner deletes one
// of their own posts (issue #34), so friends who cached a copy of it
// (received via PublicationEvent) remove theirs too on their next sync.
type DelPublication struct {
	PubUUID string `json:"pub_uuid"`
	Dt      int64  `json:"dt"`
}

// DelComment is the event payload broadcast when the owner deletes a
// comment on one of their own posts (issue #35), so friends who cached a
// copy of it (received via CommentEvent) remove theirs too.
type DelComment struct {
	CommentUUID string `json:"comment_uuid"`
	Dt          int64  `json:"dt"`
}

// Forget is ForgetEvent's payload. Domain is who it's for; what gets
// deleted is always the sender's own data, whatever it says.
type Forget struct {
	Domain string `json:"domain"`
	Dt     int64  `json:"dt"`
}

type Comment struct {
	Uuid          string `json:"uuid"`
	Action        string `json:"action"`
	PubUUID       string `json:"pub_uuid"`
	Dt            int64  `json:"dt"`
	Comment       string `json:"comment"`
	PublisherName string `json:"publisher_name"`
}

// expectPayload type-asserts a friend device's RespEnvelope.Payload to the
// shape this call expects, logging and returning an error instead of
// panicking when it isn't. Every RPC this device makes to a friend's
// device (SyncWithFriends' whole cycle, friendship requests) used to do
// this assertion unchecked - fine as long as every friend's device always
// answers exactly as expected, but a friend running a different protocol
// version, a bug on their end, or a device simply misbehaving turned any
// mismatch into a panic instead of a handled error. context names which
// call site this is, purely for the log line.
func expectPayload[T any](context, domain string, payload any) (T, error) {
	v, ok := payload.(T)
	if !ok {
		log.Error(context, "- unexpected response from", domain, ", got payload type", fmt.Sprintf("%T", payload))
		var zero T
		return zero, fmt.Errorf("%s: unexpected response from %s", context, domain)
	}
	return v, nil
}

// removePublication deletes a post and then the media stored for it (its
// files and thumbnails in unenc-storage-path) that no other post uses. The
// rows alone used to be deleted, leaving the photos and videos on disk.
func (sc *Social) removePublication(pubUuid string) error {
	hashes, _ := sc.dao.PublicationHashes(pubUuid)
	if err := sc.dao.DeleteSocialPublication(pubUuid); err != nil {
		return err
	}
	if !cfg.HasSection("otc") {
		return nil
	}
	sc.removeUnusedMedia(cfg.GetStr("otc", "unenc-storage-path"), hashes)
	return nil
}

// removeUnusedMedia removes from dir the media and thumbnails of hashes
// that no post uses any more. A stored hash that isn't one (rows from
// before they were checked) is never made into a path.
func (sc *Social) removeUnusedMedia(dir string, hashes []string) {
	for _, h := range hashes {
		if !dao.IsContentHash(h) {
			continue
		}
		if inUse, err := sc.dao.SocialHashInUse(h); err != nil || inUse {
			continue
		}
		os.Remove(filepath.Join(dir, h))
		os.Remove(filepath.Join(dir, h+"_thumbnail"))
	}
}

// storageMu keeps two friends' syncs from trimming at the same time.
var storageMu sync.Mutex

// EnforceStorageLimit (issue #153) removes friends' oldest posts while the
// media kept for friends' posts is over the owner's limit (5 GB by
// default) - so a friend who posts a lot can't fill this device. Run when
// a sync has just stored new posts, and when the limit is changed; the
// owner's own posts are never touched.
func (sc *Social) EnforceStorageLimit() {
	storageMu.Lock()
	defer storageMu.Unlock()

	limitMB, _ := sc.dao.SocialStorageLimitMB()
	limit := int64(limitMB) << 20
	used, err := sc.dao.FriendPostsBytes()
	if err != nil || used <= limit {
		return
	}
	removed := 0
	for used > limit {
		oldest, err := sc.dao.OldestFriendPublications(20)
		if err != nil || len(oldest) == 0 {
			break
		}
		for _, u := range oldest {
			if err := sc.removePublication(u); err != nil {
				log.Error("could not remove an old friend post:", u, err)
				return
			}
			removed++
		}
		if used, err = sc.dao.FriendPostsBytes(); err != nil {
			break
		}
	}
	log.Info("friends' posts over the", limitMB, "MB limit: removed the", removed, "oldest, now", used>>20, "MB")
}

// eventTime is when a friend's post, like or comment actually happened
// (issue #149): the time its author stamped on it, else the event's own,
// never the moment it reached this device - a new friend's whole history
// arrives at once, and every post of it used to read "just now". A time in
// the future (a friend's clock running fast) is capped at now, so it can't
// sit above everything else in the feed.
func eventTime(payloadDt int64, event *pb.Event) time.Time {
	now := time.Now()
	t := now
	switch {
	case payloadDt > 0:
		t = time.Unix(payloadDt, 0)
	case event.GetDt() != nil && event.GetDt().IsValid():
		t = event.GetDt().AsTime()
	}
	if t.After(now) {
		return now
	}

	return t
}

func Init(dao *dao.Dao, filesmanager *filesmanager.Manager, settings *settings.Settings, profile *profile.Profile, push *push.Push) *Social {
	return &Social{
		dao:          dao,
		filesmanager: filesmanager,
		settings:     settings,
		profile:      profile,
		push:         push,
	}
}

// writeFileAtomic writes a post's media or thumbnail under its final name
// only once it is whole and on disk. Written in place, a full disk, a
// power cut or a kill mid-write left a truncated file that was then served
// as the post's (and, from a friend's sync, never fetched again), and a
// reader of the same hash could see it half-written. A reader with the old
// file open keeps it whole.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".pub-*")
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
		err = os.Chmod(tmp.Name(), 0o600) // perms: rw------- (issue #157)
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// RemovePartialWrites removes the temp files writeFileAtomic left in dir
// (unenc-storage-path) when the process died mid-write, a power cut or an
// OOM kill: nothing renames or removes them later, no storage limit counts
// them, and a friend's video can be about 1 GB. Run at startup, before
// this process writes any; no other process writes to this instance's
// directory.
func RemovePartialWrites(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Error("could not look for partly written post files in", dir, ":", err)
		return
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasPrefix(e.Name(), ".pub-") {
			continue
		}
		switch err := os.Remove(filepath.Join(dir, e.Name())); {
		case err == nil:
			log.Info("removed a partly written post file:", e.Name())
		case !os.IsNotExist(err):
			log.Error("could not remove a partly written post file:", err)
		}
	}
}

// shouldCompressForSocial reports whether a file attached to a new post
// should be compressed down before publishing (issue #60) - scoped to
// exactly what the issue asked for: a video over cSocialVideoSizeLimit.
// Images are unaffected regardless of size - they're already distributed
// via their own thumbnail plus an on-demand hi-res fetch, not a wholesale
// copy of the original like a video's full playback would be.
func shouldCompressForSocial(mime string, size int) bool {
	return strings.HasPrefix(mime, "video/") && size > cSocialVideoSizeLimit
}

// shouldTrimForSocial reports whether trim actually asks for a cut of this
// file (issue #108). A nil trim, a non-video, and a trim that would return
// the whole clip anyway (starts at zero and has no end) all mean "publish
// it whole" - which matters beyond tidiness, because trimming is a
// re-encode, and re-encoding a video to change nothing about it is pure
// quality and CPU lost for free.
func shouldTrimForSocial(mime string, trim *pb.VideoTrim) bool {
	if trim == nil || !strings.HasPrefix(mime, "video/") {
		return false
	}
	return trim.StartSecs > 0 || trim.EndSecs > trim.StartSecs
}

func (sc *Social) NewPublication(ses *session.Session, text string, paths []string, trims []*pb.VideoTrim) (pubUuID string, err error) {
	// Issue #108: at most one trim per path, and a path with no entry is
	// published whole - so an older client that sends no trims at all
	// (and a post with no video in it) takes exactly the path it always
	// did.
	trimByPath := make(map[string]*pb.VideoTrim, len(trims))
	for _, t := range trims {
		if t != nil {
			trimByPath[t.Path] = t
		}
	}

	files := make([]*pb.File, len(paths))
	for i, path := range paths {
		// A file uploaded a moment ago (a photo or video just taken)
		// may not be written yet.
		sc.filesmanager.WaitForContent(path, 2*time.Minute)
		unencDir := cfg.GetStr("otc", "unenc-storage-path")
		maxThumb := int(cfg.GetInt("otc", "max-thumbnail-width-px"))

		// Issue #166: at most one file of the post is in memory at a
		// time, and a video never is - see publishVideo.
		file, isTransient, err := sc.publishFile(ses, path, trimByPath[path], unencDir)
		if err != nil {
			return "", err
		}
		files[i] = file

		var unEncThumb []byte
		if isTransient {
			// Nothing to poll for - there's no `files` row, so no
			// background goroutine is ever going to write an encrypted
			// thumbnail for this hash. Generate one directly instead.
			unEncThumb, err = filesmanager.GenerateVideoThumbnailFrom(filepath.Join(unencDir, file.Hash), maxThumb)
			if err != nil {
				return "", fmt.Errorf("generating thumbnail for compressed video %q: %w", path, err)
			}
		} else {
			// Issue #49: UploadFile writes the thumbnail from a background
			// goroutine (see files_manager.UploadFile), not before
			// returning - fine for the original flow (a post is always
			// composed well after a prior, separate sync finished), but a
			// client that uploads and immediately posts the same file
			// (issue #49's Phone-source picker) can race that goroutine
			// and find no thumbnail yet. Retried rather than failing the
			// whole post over what's normally a sub-second delay.
			for attempt := 0; ; attempt++ {
				unEncThumb, err = sc.filesmanager.GetThumbnail(ses, file)
				if err == nil {
					break
				}
				if attempt >= cThumbnailPollAttempts-1 {
					return "", err
				}
				time.Sleep(cThumbnailPollInterval)
			}
		}
		unencPathThumb := fmt.Sprintf("%s/%s_thumbnail", unencDir, file.Hash)
		err = writeFileAtomic(unencPathThumb, unEncThumb)
		if err != nil {
			return "", err
		}

		log.Debug("Publication in path:", path)
	}

	pubUuID = uuid.New().String()
	json, _ := json.Marshal(Publication{
		Uuid:   pubUuID,
		Action: ActionCreate,
		Dt:     time.Now().Unix(),
		Text:   text,
	})
	err = sc.dao.NewEvent(PublicationEvent, json)
	if err != nil {
		return "", err
	}

	return pubUuID, sc.dao.NewSocialPublication(pubUuID, text, sc.profile.Domain(), true, files, time.Now())
}

// publishFile writes the plaintext copy a post distributes for the file at
// path into unencDir, named by its hash, and returns its File (without
// Content) and whether it is a transient copy (trimmed or compressed)
// rather than the owner's file.
func (sc *Social) publishFile(ses *session.Session, path string, trim *pb.VideoTrim, unencDir string) (*pb.File, bool, error) {
	meta, err := sc.dao.GetFileByPath(path)
	if err != nil || meta == nil {
		return nil, false, fmt.Errorf("error loading file %q: %v", path, err)
	}
	if strings.HasPrefix(meta.Mime, "video/") {
		return sc.publishVideo(ses, meta, trim, unencDir)
	}

	// A photo is read whole (a HEIC is converted to JPEG for everyone
	// else's screens), within the download memory budget.
	release := sc.filesmanager.ReserveForDownload(path, "")
	defer release()
	file, err := sc.filesmanager.GetFile(ses, path, "")
	if err != nil {
		log.Error("Error loading file:", err)
		return nil, false, fmt.Errorf("error loading file %q: %w", path, err)
	}
	if err := writeFileAtomic(filepath.Join(unencDir, file.Hash), file.Content); err != nil {
		return nil, false, err
	}
	file.Content = nil
	return file, false, nil
}

// publishVideo: issue #60 compresses an oversized video into a new copy
// and issue #108 cuts it to the owner's trim - one ffmpeg pass for both,
// so it is only ever encoded once. That copy only exists for this post: no
// `files` row, no entry in Files, no tags or faces (see
// BuildTransientFile). Whether to downscale is decided from the original's
// size: a source big enough to need it is distributed at social
// resolution however much of it survives the cut. The video is never
// loaded: ffmpeg streams the stored file, or it is decrypted straight into
// place (issue #166).
func (sc *Social) publishVideo(ses *session.Session, file *pb.File, trim *pb.VideoTrim, unencDir string) (*pb.File, bool, error) {
	downscale := shouldCompressForSocial(file.Mime, int(file.Size))
	var tr *filesmanager.TrimRange
	if shouldTrimForSocial(file.Mime, trim) {
		tr = &filesmanager.TrimRange{Start: trim.StartSecs, End: trim.EndSecs}
	}
	return sc.filesmanager.ExportVideoForPost(ses, file, tr, downscale, unencDir)
}

// GetEvents is the page of events requester's device is served (issue
// #174: broadcast events plus those meant for it alone).
func (sc *Social) GetEvents(pr *profile.Profile, since time.Time, total int32, requester string) (events []*pb.Event, err error) {
	events, err = sc.dao.GetEvents(since, total, requester)
	if err != nil {
		log.Debug("error retriving events", err)
	}
	return
}

// GetPublicationMedia returns the full bytes of one file in a publication
// (issue #107), as opposed to GetPublicationFiles' thumbnails.
//
// Every publication file is written to unenc-storage-path under its own
// hash when the post is created (see NewPublication), which is what makes
// this possible without a path - publication files have none.
func (sc *Social) GetPublicationMedia(pubUuid, hash string) (content []byte, mime string, err error) {
	if !dao.IsContentHash(hash) {
		return nil, "", fmt.Errorf("no file %q in publication %q", hash, pubUuid)
	}
	mime, found, err := sc.dao.PublicationFileMime(pubUuid, hash)
	if err != nil {
		return nil, "", err
	}
	if !found {
		return nil, "", fmt.Errorf("no file %q in publication %q", hash, pubUuid)
	}

	content, err = os.ReadFile(fmt.Sprintf("%s/%s", cfg.GetStr("otc", "unenc-storage-path"), hash))
	if err != nil {
		return nil, "", err
	}
	return content, mime, nil
}

func (sc *Social) GetPublicationFiles(uuid string) (files []*pb.File, err error) {
	all, err := sc.dao.GetSocialPublicationFiles(uuid)
	if err != nil {
		return nil, err
	}

	files = make([]*pb.File, 0, len(all))
	for _, file := range all {
		if !dao.IsContentHash(file.Hash) {
			continue // never a path (see storeFriendFile)
		}
		content, readErr := os.ReadFile(fmt.Sprintf("%s/%s_thumbnail", cfg.GetStr("otc", "unenc-storage-path"), file.Hash))
		if readErr != nil {
			// A single missing/corrupted thumbnail used to fail this
			// whole request - one bad file permanently breaking a post
			// (and, from GetPublications, the entire feed) for everyone
			// until someone noticed and fixed the file on disk. Skipping
			// it and continuing contains the damage to just this file.
			log.Error("skipping missing/corrupted thumbnail for publication", uuid, "hash", file.Hash, ":", readErr)
			continue
		}
		file.Content = content
		files = append(files, file)
	}

	return files, nil
}

func (sc *Social) GetPublications(pr *profile.Profile, since time.Time, total int32, ownOnly bool, exclude []string) (publications *pb.SocialPublications, err error) {
	publications, err = sc.dao.GetSocialPublications(since, total, ownOnly, exclude, pr.Name(), pr.Text(), pr.Image(), pr.Domain())
	if err != nil {
		log.Debug("error retriving publications", err)
		return
	}

	// The page's comments in one query (they were one query per post).
	uuids := make([]string, 0, len(publications.Publications))
	for _, pub := range publications.Publications {
		uuids = append(uuids, pub.Uuid)
	}
	comments, err := sc.dao.GetSocialPublicationsComments(uuids, pr.Domain())
	if err != nil {
		return nil, err
	}

	// Populate the files content. A missing/corrupted thumbnail is skipped
	// rather than failing the whole feed - see GetPublicationFiles' doc
	// comment for why this used to be much worse than "this one photo is
	// missing from this one post".
	for _, pub := range publications.Publications {
		goodFiles := make([]*pb.File, 0, len(pub.Files))
		for _, file := range pub.Files {
			if !dao.IsContentHash(file.Hash) {
				continue
			}
			content, readErr := os.ReadFile(fmt.Sprintf("%s/%s_thumbnail", cfg.GetStr("otc", "unenc-storage-path"), file.Hash))
			if readErr != nil {
				log.Error("skipping missing/corrupted thumbnail in feed for publication", pub.Uuid, "hash", file.Hash, ":", readErr)
				continue
			}
			file.Content = content
			goodFiles = append(goodFiles, file)
		}
		pub.Files = goodFiles

		pub.Comments = comments[pub.Uuid]
		if pub.Comments == nil {
			pub.Comments = []*pb.Comment{}
		}
	}

	return
}

// GetPublication (issue #78) is GetPublications' single-post equivalent -
// tapping a notification for a post outside whatever page of the feed
// happens to be loaded needs to fetch just that one directly. Populates
// thumbnails/comments the same way GetPublications does for its list.
func (sc *Social) GetPublication(pr *profile.Profile, pubUuid string) (pub *pb.SocialPublication, err error) {
	pub, err = sc.dao.GetSocialPublicationByUUID(pubUuid, pr.Name(), pr.Text(), pr.Image(), pr.Domain())
	if err != nil {
		return nil, err
	}

	goodFiles := make([]*pb.File, 0, len(pub.Files))
	for _, file := range pub.Files {
		if !dao.IsContentHash(file.Hash) {
			continue
		}
		content, readErr := os.ReadFile(fmt.Sprintf("%s/%s_thumbnail", cfg.GetStr("otc", "unenc-storage-path"), file.Hash))
		if readErr != nil {
			log.Error("skipping missing/corrupted thumbnail for publication", pub.Uuid, "hash", file.Hash, ":", readErr)
			continue
		}
		file.Content = content
		goodFiles = append(goodFiles, file)
	}
	pub.Files = goodFiles

	pub.Comments, err = sc.dao.GetSocialPublicationComments(pub.Uuid, pr.Domain())
	if err != nil {
		return nil, err
	}

	return pub, nil
}

// ListNotifications (issue #78 follow-up: avatar + post/comment thumbnail
// on each row) wraps dao.ListNotifications the same way GetPublications
// wraps dao.GetSocialPublications - ActorImage is already a plain DB blob
// by the time it gets here (dao's own job), but a thumbnail lives on disk
// under a hash, not in the DB, so resolving thumbHashes into actual bytes
// is filesystem I/O and belongs at this layer instead. A missing/corrupted
// thumbnail is skipped (Thumbnail just stays unset), same "don't fail the
// whole list over one bad file" reasoning as GetPublications' own files.
func (sc *Social) ListNotifications(limit int) ([]*pb.Notification, error) {
	notifications, thumbHashes, err := sc.dao.ListNotifications(limit)
	if err != nil {
		return nil, err
	}
	for _, n := range notifications {
		hash, ok := thumbHashes[n.Uuid]
		if !ok || !dao.IsContentHash(hash) {
			continue
		}
		content, readErr := os.ReadFile(fmt.Sprintf("%s/%s_thumbnail", cfg.GetStr("otc", "unenc-storage-path"), hash))
		if readErr != nil {
			log.Error("skipping missing/corrupted thumbnail for notification", n.Uuid, "hash", hash, ":", readErr)
			continue
		}
		n.Thumbnail = content
	}
	return notifications, nil
}

// cDefaultFriendTLD is used when [otc] friend-domain-tld isn't set in
// config, so existing installs keep working unchanged.
const cDefaultFriendTLD = "off-the.cloud"

// friendDomainTLD returns the TLD every friend domain must end in before
// this device will dial out to it, from [otc] friend-domain-tld -
// configurable (rather than hardcoded to off-the.cloud) so someone running
// their own separate network of devices - their own bridge under their own
// domain - can set their own value instead.
func friendDomainTLD() string {
	if cfg.HasSection("otc") {
		if tld := cfg.GetStr("otc", "friend-domain-tld"); tld != "" {
			return tld
		}
	}
	return cDefaultFriendTLD
}

// isAllowedFriendDomain reports whether domain is a subdomain of the
// configured friend TLD. connectToDevice is the one chokepoint every
// outbound friend/bridge connection goes through (SendFriendshipReq,
// SyncWithFriends, ExternalFriendshipRequest), so enforcing this here
// rather than at each call site closes all of them at once: without it, an
// inbound ReqFriendshipInterRequest naming an arbitrary domain (LAN
// address, internal hostname, anything) made this device dial wherever a
// stranger pointed it - a classic SSRF shape, and reachable pre-auth,
// since a friend request has to be usable by someone not a friend yet.
func isAllowedFriendDomain(domain string) bool {
	tld := strings.ToLower(friendDomainTLD())
	domain = strings.ToLower(domain)
	return domain == tld || strings.HasSuffix(domain, "."+tld)
}

func (sc *Social) connectToDevice(domain string) (conn *wsframe.Client, err error) {
	if !isAllowedFriendDomain(domain) {
		return nil, fmt.Errorf("domain %q is not a %s address", domain, friendDomainTLD())
	}

	// If this is a bridge connection, we will retry to connect to the bridge
	u := url.URL{Scheme: "wss", Host: domain, Path: "/ws"}
	log.Debug("Connecting to external:", domain, u)
	h := http.Header{}
	h.Set("Sec-WebSocket-Protocol", "protobuf")
	conn, err = wsframe.Dial(u.String(), h)
	if err != nil {
		log.Error("dialing websocket:", err)
		return
	}
	log.Debug("Connected to external...")

	return
}

type friendship struct {
	conn *wsframe.Client
	data *pb.Friendship
	dao  *dao.Dao
	sc   *Social
}

func (sc *Social) SyncWithFriends() (err error) {
	log.Debug("Sync with frens")
	friendships, err := sc.GetFriendships()
	if err != nil {
		log.Error("Error trying to get friendships from the DB:", err)
		return err
	}
	for _, data := range friendships {
		// Issue #174: waiting for this ex-friend's device to delete what we
		// shared - nothing of theirs is synced in meanwhile. Their device
		// still pulls from us, to get the request.
		if data.Leaving {
			continue
		}
		friend := &friendship{
			sc:   sc,
			data: data,
			dao:  sc.dao,
		}

		log.Debug("Updating friendship status for:", data.OriginProfile.Domain)
		friend.conn, err = sc.connectToDevice(friend.data.OriginProfile.Domain)
		if err != nil {
			log.Error("Error connecting to external device:", friend.data.OriginProfile.Domain, err)
			continue
		}
		friend.sync()
		friend.conn.Close()
	}

	return
}

// sync is one friend's pass: its status and, once accepted, its profile
// and events. Every step has a deadline (issue #169), so a friend whose
// device stops answering costs this pass a timeout, not every friend
// after it.
func (friend *friendship) sync() {
	err := friend.updateFriendshipStatus()
	if err != nil {
		log.Error("Error trying to update friendship status:", err)
		return
	}

	if friend.data.Status == pb.FriendShipStatus_Accepted {
		log.Debug("Auth as friend:", friend.data.OriginProfile.Domain)
		err = friend.autAsFriend()
		if err != nil {
			log.Error("Error trying to auth as friend:", err)
			return
		}

		// issue #26: pick up the friend's current name/photo/bio on
		// every sync, not just whatever was true when the friendship
		// was accepted. Non-fatal — a failure here shouldn't stop the
		// events pull that follows.
		if err = friend.refreshProfile(); err != nil {
			log.Error("Error trying to refresh friend profile:", err)
		}

		err = friend.updateFriendEvents()
		if err != nil {
			log.Error("Error trying to update friendship:", err)
			return
		}
	}
}

func (fr *friendship) updateFriendshipStatus() (err error) {
	log.Debug("Update friendship:", fr.data.OriginProfile.Domain)
	if !fr.data.Sent {
		log.Debug("We are the receivers, we decide, no need to sync")
		// If we are the senders we can't change the status
		return
	}

	msg := &pb.ReqEnvelope{
		Id: 1,
		Payload: &pb.ReqEnvelope_ReqGetFriendshipStatus{
			ReqGetFriendshipStatus: &pb.GetFriendshipStatus{
				Domain: fr.sc.settings.Domain(), // We want to get our status, so our domain
				Secret: fr.data.Secret,
			},
		},
	}
	b, _ := proto.Marshal(msg)
	if err = fr.conn.WriteMessage(gorilla.BinaryMessage, b); err != nil {
		log.Error("write error in external:", err)
		return
	}

	_, data, err := fr.conn.ReadMessage()
	if err != nil {
		log.Error("read error in external:", err)
		return
	}

	log.Debug("Getting response for update friendship:", fr.data.OriginProfile.Domain, data)
	var respProf pb.RespEnvelope
	if err = proto.Unmarshal(data, &respProf); err != nil {
		return
	}
	if respProf.Error {
		log.Debug("Error reading friendship status:", respProf.ErrorMessage)
		return errors.New(respProf.ErrorMessage)
	}
	resp, err := expectPayload[*pb.RespEnvelope_RespFriendshipStatus]("update friendship status", fr.data.OriginProfile.Domain, respProf.Payload)
	if err != nil {
		return err
	}
	// Heard from: the friend's device answered (see cRelinkFreshness).
	if err := fr.dao.TouchFriend(fr.data.OriginProfile.Domain); err != nil {
		log.Error("could not record contact with", fr.data.OriginProfile.Domain, ":", err)
	}
	// Issue #25: the receiver deleted our request while we could not be
	// told (it was off, or we were). Only a still-pending request is
	// removed on the strength of this - an accepted friendship that the
	// other side no longer knows about is left for the owner to decide on.
	if resp.RespFriendshipStatus.NotFound {
		if fr.data.Status == pb.FriendShipStatus_Pending {
			log.Info("friend request to", fr.data.OriginProfile.Domain, "was deleted on the other side, removing it here too")
			return fr.dao.DeleteFriendship(fr.data.OriginProfile.Domain)
		}
		log.Info("friendship with", fr.data.OriginProfile.Domain, "is unknown on the other side, leaving it as is")
		return nil
	}

	status := resp.RespFriendshipStatus.Status
	log.Debug("Remote friendship status:", fr.data.OriginProfile.Domain, status)

	// Issue #43 follow-up (push) / #78 (in-app notification): notify exactly
	// on the Pending -> Accepted transition, comparing against fr.data.
	// Status as loaded at the top of this sync cycle (before ChangeFriendStatus
	// below updates it) - this runs on every sync for every friendship we
	// sent the request for, so without this comparison an already-accepted
	// friendship would renotify every ~2 minutes forever.
	if fr.data.Status == pb.FriendShipStatus_Pending && status == pb.FriendShipStatus_Accepted {
		friendName := fr.data.OriginProfile.Name
		if friendName == "" {
			friendName = fr.data.OriginProfile.Domain
		}
		if err := fr.dao.NewNotification(pb.NotificationType_NotificationFriendAccepted, friendName, fr.data.OriginProfile.Domain, "", ""); err != nil {
			log.Error("could not record notification:", err)
		}
		if fr.sc.push != nil {
			fr.sc.push.NotifyFriendshipAccepted(friendName)
		}
	}

	return fr.dao.ChangeFriendStatus(fr.data.OriginProfile.Domain, status)
}

func (fr *friendship) autAsFriend() (err error) {
	msg := &pb.ReqEnvelope{
		Id: 1,
		Payload: &pb.ReqEnvelope_ReqAuthAsFriend{
			ReqAuthAsFriend: &pb.AuthAsFriend{
				Domain: fr.sc.settings.Domain(),
				Secret: fr.data.Secret,
			},
		},
	}
	b, _ := proto.Marshal(msg)
	if err = fr.conn.WriteMessage(gorilla.BinaryMessage, b); err != nil {
		log.Error("write error trying to auth as friend:", err)
		return
	}

	_, data, err := fr.conn.ReadMessage()
	if err != nil {
		log.Error("read error trying to auth as friend:", err)
		return
	}

	log.Debug("Getting response for update friendship:", fr.data.OriginProfile.Domain)
	var respProf pb.RespEnvelope
	if err = proto.Unmarshal(data, &respProf); err != nil {
		return
	}
	if respProf.Error {
		log.Debug("Error trying to auth as friend:", respProf.ErrorMessage)
		return errors.New(respProf.ErrorMessage)
	}
	resp, err := expectPayload[*pb.RespEnvelope_RespAck]("auth as friend", fr.data.OriginProfile.Domain, respProf.Payload)
	if err != nil {
		return err
	}
	if !resp.RespAck.Ok {
		return errors.New("Error authenticating as friend")
	}

	return
}

// refreshProfile fetches the friend's current name/image/bio and updates
// our locally cached copy if it changed (issue #26): the friendship row
// only ever stored a snapshot taken when the request was sent/accepted, so
// without this a friend renaming themselves or changing their photo would
// never be reflected on the friends who already added them.
func (fr *friendship) refreshProfile() (err error) {
	msg := &pb.ReqEnvelope{
		Id: 1,
		Payload: &pb.ReqEnvelope_ReqGetProfile{
			ReqGetProfile: &pb.GetProfile{},
		},
	}
	b, _ := proto.Marshal(msg)
	if err = fr.conn.WriteMessage(gorilla.BinaryMessage, b); err != nil {
		log.Error("write error trying to get profile from friend:", fr.data.OriginProfile.Domain, err)
		return
	}

	_, data, err := fr.conn.ReadMessage()
	if err != nil {
		log.Error("read error trying to get profile from friend:", fr.data.OriginProfile.Domain, err)
		return
	}

	var respProf pb.RespEnvelope
	if err = proto.Unmarshal(data, &respProf); err != nil {
		return
	}
	if respProf.Error {
		log.Debug("Error trying to get profile from friend:", respProf.ErrorMessage)
		return errors.New(respProf.ErrorMessage)
	}
	profResp, err := expectPayload[*pb.RespEnvelope_RespProfile]("refresh friend profile", fr.data.OriginProfile.Domain, respProf.Payload)
	if err != nil {
		return err
	}
	remote := profResp.RespProfile

	origin := fr.data.OriginProfile
	if remote.Name == origin.Name && remote.Text == origin.Text && bytes.Equal(remote.Image, origin.Image) {
		return nil
	}

	log.Debug("Friend profile changed, updating local cache for:", origin.Domain)
	if err = fr.dao.UpdateFriendshipProfile(origin.Domain, remote.Name, remote.Text, remote.Image); err != nil {
		return err
	}
	origin.Name = remote.Name
	origin.Text = remote.Text
	origin.Image = remote.Image
	return nil
}

// getPublicationMedia fetches the full bytes of one file in a friend's
// publication (issue #107), using the same RPC this device serves to its
// own friends.
//
// Pulled at sync time rather than when someone presses play, per the
// owner's call: a friend's device is frequently asleep, behind a dropped
// bridge connection, or simply off - fetching on demand would mean a
// video that plays only when its author happens to be online, which is
// not what a timeline should do.
func (fr *friendship) getPublicationMedia(pubUuid, hash string) (content []byte, err error) {
	msg := &pb.ReqEnvelope{
		Id: 1,
		Payload: &pb.ReqEnvelope_ReqGetPublicationMedia{
			ReqGetPublicationMedia: &pb.GetPublicationMedia{
				PubUuid: pubUuid,
				Hash:    hash,
			},
		},
	}
	b, _ := proto.Marshal(msg)
	if err = fr.conn.WriteMessage(gorilla.BinaryMessage, b); err != nil {
		log.Error("write error trying to get publication media from friend:", fr.data.OriginProfile.Domain, err)
		return nil, err
	}

	_, data, err := fr.conn.ReadMedia()
	if err != nil {
		log.Error("read error trying to get publication media from friend:", fr.data.OriginProfile.Domain, err)
		return nil, err
	}

	var resp pb.RespEnvelope
	if err = proto.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	if resp.Error {
		return nil, errors.New(resp.ErrorMessage)
	}
	rf, err := expectPayload[*pb.RespEnvelope_RespFile]("get publication media", fr.data.OriginProfile.Domain, resp.Payload)
	if err != nil {
		return nil, err
	}
	return rf.RespFile.Content, nil
}

// errFriendTransport marks a failure of the connection to a friend's
// device, as opposed to an answer about one post.
var errFriendTransport = errors.New("friend connection failed")

func (fr *friendship) getPublicationFiles(uuid string) (files []*pb.File, err error) {
	msg := &pb.ReqEnvelope{
		Id: 1,
		Payload: &pb.ReqEnvelope_ReqGetSocialPublicationFiles{
			ReqGetSocialPublicationFiles: &pb.GetSocialPublicationFiles{
				Uuid: uuid,
			},
		},
	}
	b, _ := proto.Marshal(msg)
	if err = fr.conn.WriteMessage(gorilla.BinaryMessage, b); err != nil {
		log.Error("write error trying to get publication files from friend:", fr.data.OriginProfile.Domain, err)
		return nil, fmt.Errorf("%w: %v", errFriendTransport, err)
	}

	_, data, err := fr.conn.ReadMessage()
	if err != nil {
		log.Error("read error trying to get publication files from friend:", fr.data.OriginProfile.Domain, err)
		return nil, fmt.Errorf("%w: %v", errFriendTransport, err)
	}

	log.Debug("Getting response for publication files:", fr.data.OriginProfile.Domain)
	var respProf pb.RespEnvelope
	if err = proto.Unmarshal(data, &respProf); err != nil {
		return
	}
	if respProf.Error {
		log.Debug("Error trying to get publications from friend:", respProf.ErrorMessage)
		return nil, errors.New(respProf.ErrorMessage)
	}
	filesResp, err := expectPayload[*pb.RespEnvelope_RespSocialPublicationFiles]("get publication files", fr.data.OriginProfile.Domain, respProf.Payload)
	if err != nil {
		return nil, err
	}
	return filesResp.RespSocialPublicationFiles.Files, nil
}

// storeFriendFile writes one file of a friend's post to dir: its thumbnail
// and then, unless it is already here, its media. ok is false for a file
// that can't be stored here at all; wrote is whether anything was written
// for it; err is a failed thumbnail write. file.Size becomes what the file
// takes on this disk, thumbnail included: the size the friend states is
// only its word, and the storage limit for friends' posts (issue #153)
// adds these up.
func (fr *friendship) storeFriendFile(pubUuid string, file *pb.File, dir string) (ok, wrote bool, err error) {
	// Issue #107: the friend's own hash is kept, not replaced with a hash
	// of the thumbnail bytes as this used to do. That hash is how the
	// friend addresses the file, so overwriting it left no way to ask them
	// for the actual media - and it's also what our own rows store, so the
	// two now agree and GetPublicationMedia can find what it serves.
	if file.Hash == "" {
		// Defensive: a friend on an older build might not send one.
		// Falling back to the old behaviour keeps the thumbnail working;
		// only the media is lost.
		sum := sha256.Sum256(file.Content)
		file.Hash = hex.EncodeToString(sum[:])
	}
	// The hash names this file on disk, and a friend's device chose it:
	// "../<hash>" wrote (and, once the post was deleted, removed) the
	// owner's own files. Every release sends a SHA-256 in hex.
	if !dao.IsContentHash(file.Hash) {
		log.Error("ignoring a file with an invalid hash in publication", pubUuid, "from", fr.data.OriginProfile.Domain)
		return false, false, nil
	}

	unencPathThumb := filepath.Join(dir, file.Hash+"_thumbnail")
	// A thumbnail a post here already uses stays as it is: naming the hash
	// of someone else's photo doesn't replace what everyone is shown.
	if _, statErr := os.Stat(unencPathThumb); statErr != nil || !fr.socialHashInUse(file.Hash) {
		log.Debug("Storing file thumbnail in path:", unencPathThumb)
		if err := writeFileAtomic(unencPathThumb, file.Content); err != nil {
			return false, false, err
		}
		wrote = true
	}
	var stored int64
	if info, statErr := os.Stat(unencPathThumb); statErr == nil {
		stored = info.Size()
	}

	// Then the full media, so the post is playable/viewable later whether
	// or not its author is reachable. Failing here is not fatal to the
	// post: the thumbnail above is already stored, so the timeline still
	// renders and only full-size playback is missing - better than
	// dropping the publication entirely over one large file.
	unencPath := filepath.Join(dir, file.Hash)
	if info, statErr := os.Stat(unencPath); statErr == nil {
		file.Size = storedSize(stored + info.Size())
		return true, wrote, nil // already have it (a re-sync, or shared with another post)
	}
	// Held until the file is written: it is read whole, then copied.
	release := fr.sc.filesmanager.ReserveFriendMedia(int64(file.Size))
	media, mediaErr := fr.getPublicationMedia(pubUuid, file.Hash)
	if mediaErr != nil {
		log.Error("could not fetch media", file.Hash, "for publication", pubUuid, "from",
			fr.data.OriginProfile.Domain, ":", mediaErr)
	} else if err := writeFileAtomic(unencPath, media); err != nil {
		log.Error("error storing friend publication media:", err)
	} else {
		stored += int64(len(media))
		wrote = true
	}
	release()
	file.Size = storedSize(stored)
	return true, wrote, nil
}

// storedSize is n as a file row's size, which is an int32.
func storedSize(n int64) int32 {
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(n)
}

// socialHashInUse is whether a post here has a file with hash; when that
// can't be told, it is taken to be.
func (fr *friendship) socialHashInUse(hash string) bool {
	inUse, err := fr.dao.SocialHashInUse(hash)
	return err != nil || inUse
}

// notifyIfOwnPublication notifies about a like/comment only when pubUuid is
// one of the device owner's own posts - like/comment events arriving here
// can just as easily be about some other friend's post this device also
// has a cached copy of, which isn't the owner's business to be notified
// about. Records a row in the in-app notification timeline (issue #78)
// unconditionally once ownership is confirmed, then sends a push too if
// one is configured - push is optional at runtime (see push.Push's own
// doc comments), the in-app bell isn't. commentUuid is only set for a new-
// comment notification (so the client can additionally highlight that
// specific comment once the post is open); pass "" for a plain like.
func (fr *friendship) notifyIfOwnPublication(pubUuid, commentUuid, action string, notifType pb.NotificationType) {
	own, err := fr.dao.IsOwnPublication(pubUuid)
	if err != nil {
		log.Error("could not check publication ownership for a notification:", err)
		return
	}
	if !own {
		return
	}
	friendName := fr.data.OriginProfile.Name
	if friendName == "" {
		friendName = fr.data.OriginProfile.Domain
	}
	if err := fr.dao.NewNotification(notifType, friendName, fr.data.OriginProfile.Domain, pubUuid, commentUuid); err != nil {
		log.Error("could not record notification:", err)
	}
	if fr.sc.push != nil {
		fr.sc.push.Notify(friendName, action, push.Target{Kind: push.TargetPost, PubUUID: pubUuid, CommentUUID: commentUuid})
	}
}

// notifyIfOwnComment is notifyIfOwnPublication's counterpart for a like on
// a comment - only the device owner's own comments are worth notifying
// about. Resolves the comment's own publication too (dao.GetCommentPubUuid)
// so a click on this notification can always land on "the post", not just
// know which comment was liked.
func (fr *friendship) notifyIfOwnComment(commentUuid, action string, notifType pb.NotificationType) {
	own, err := fr.dao.IsOwnComment(commentUuid)
	if err != nil {
		log.Error("could not check comment ownership for a notification:", err)
		return
	}
	if !own {
		return
	}
	friendName := fr.data.OriginProfile.Name
	if friendName == "" {
		friendName = fr.data.OriginProfile.Domain
	}
	pubUuid, err := fr.dao.GetCommentPubUuid(commentUuid)
	if err != nil {
		log.Error("could not resolve comment's publication for a notification:", err)
	}
	if err := fr.dao.NewNotification(notifType, friendName, fr.data.OriginProfile.Domain, pubUuid, commentUuid); err != nil {
		log.Error("could not record notification:", err)
	}
	if fr.sc.push != nil {
		fr.sc.push.Notify(friendName, action, push.Target{Kind: push.TargetPost, PubUUID: pubUuid, CommentUUID: commentUuid})
	}
}

// cEventsSyncPageSize caps how many of a friend's events get replayed per
// sync cycle (120s) - also doubles as the "have we drained the whole
// backlog yet" signal updateFriendEvents uses below (issue #92): a page
// that comes back shorter than this means there's nothing older left
// queued behind it.
const cEventsSyncPageSize = 20

// cPostTries is how many syncs in a row may stop at the same post of a
// friend's that the connection or this disk failed, so it is asked for
// again, before it is given up like a post the friend no longer has. One
// that always fails (an answer that always outlasts the read deadline, a
// full disk) would otherwise hold back every later event of that friend's,
// deletions included, for good.
const cPostTries = 3

type stuckPost struct {
	pub   string
	stops int
}

// retryPost counts one more sync stopping at domain's post pubUuid, and
// reports whether it may stop there again: false once that makes
// cPostTries, and the post is to be given up.
func (sc *Social) retryPost(domain, pubUuid string) bool {
	sc.stuckMu.Lock()
	defer sc.stuckMu.Unlock()
	s := sc.stuck[domain]
	if s.pub != pubUuid {
		s = stuckPost{pub: pubUuid}
	}
	s.stops++
	if s.stops >= cPostTries {
		delete(sc.stuck, domain)
		return false
	}
	if sc.stuck == nil {
		sc.stuck = make(map[string]stuckPost)
	}
	sc.stuck[domain] = s
	return true
}

// postPassed forgets the syncs that stopped at domain's post pubUuid, once
// one has got past it.
func (sc *Social) postPassed(domain, pubUuid string) {
	sc.stuckMu.Lock()
	defer sc.stuckMu.Unlock()
	if sc.stuck[domain].pub == pubUuid {
		delete(sc.stuck, domain)
	}
}

func (fr *friendship) updateFriendEvents() (err error) {
	log.Debug("Updating events")
	// Issue #92: "accepting an invite while doing the first sync" floods
	// the owner with a notification for every one of that friend's
	// pre-existing likes/comments/posts, all replayed at once, unless
	// this is suppressed. catchingUp stays true across every sync cycle
	// needed to drain a friend's whole backlog (not just the first one -
	// a very active long-time friend can take several 120s cycles at
	// cEventsSyncPageSize per cycle), until a cycle finally comes back
	// with fewer events than it asked for.
	catchingUp := !fr.data.NotificationsStarted
	// Issue #153: whether this sync stored new posts - the only time the
	// space friends' posts take can have grown past the limit.
	newPosts := false
	msg := &pb.ReqEnvelope{
		Id: 1,
		Payload: &pb.ReqEnvelope_ReqGetEvents{
			ReqGetEvents: &pb.GetEvents{
				Since: fr.data.LatestSync,
				Total: cEventsSyncPageSize,
			},
		},
	}
	b, _ := proto.Marshal(msg)
	if err = fr.conn.WriteMessage(gorilla.BinaryMessage, b); err != nil {
		log.Error("write error trying to get events from friend:", fr.data.OriginProfile.Domain, err)
		return
	}

	_, data, err := fr.conn.ReadMessage()
	if err != nil {
		log.Error("read error trying to get publications from friend:", fr.data.OriginProfile.Domain, err)
		return
	}

	log.Debug("Getting response for update events:", fr.data.OriginProfile.Domain)
	var respProf pb.RespEnvelope
	if err = proto.Unmarshal(data, &respProf); err != nil {
		return
	}
	if respProf.Error {
		log.Debug("Error trying to publications from friend:", respProf.ErrorMessage)
		return errors.New(respProf.ErrorMessage)
	}
	resp, err := expectPayload[*pb.RespEnvelope_RespEvents]("get events", fr.data.OriginProfile.Domain, respProf.Payload)
	if err != nil {
		return err
	}
	log.Debug("Events to update", len(resp.RespEvents.Events))
	// at is the second this page last moved the cursor to (see stopAtPost).
	var at *timestamppb.Timestamp
event_loop:
	for _, event := range resp.RespEvents.Events {
		switch event.Type {
		case PublicationEvent:
			var pubData Publication
			json.Unmarshal([]byte(event.Content), &pubData)

			// Already here (a re-delivery, or a uuid another post has):
			// nothing to fetch, and nothing of it to overwrite.
			if _, _, found, _ := fr.dao.PublicationOwner(pubData.Uuid); found {
				break
			}

			files, err := fr.getPublicationFiles(pubData.Uuid)
			if err != nil {
				log.Error("Error getting publication:", err)
				if !errors.Is(err, errFriendTransport) {
					// An answer about the post (deleted since): skipped.
					fr.sc.postPassed(fr.data.OriginProfile.Domain, pubData.Uuid)
					continue event_loop
				}
				// The connection failed, not the post: stop here, so the
				// next sync asks for it again instead of the events after
				// it moving the cursor past it for good.
				if fr.stopAtPost(pubData.Uuid, event, at) {
					fr.stopPage(newPosts)
					return err
				}
				break // given up: the cursor moves past it
			}

			// Store the files in the local drive first. A file that can't be
			// stored here at all is left out of the post.
			unencDir := cfg.GetStr("otc", "unenc-storage-path")
			kept, written, err := fr.storeFriendFiles(pubData.Uuid, files, unencDir)
			if err != nil {
				// This disk, not the post: what was written for it goes,
				// and it is asked for again next sync.
				log.Error("Error trying to write file from an external event:", err)
				fr.sc.removeUnusedMedia(unencDir, written)
				if fr.stopAtPost(pubData.Uuid, event, at) {
					fr.stopPage(newPosts)
					return err
				}
				break // given up: the cursor moves past it
			}

			err = fr.dao.NewSocialPublication(pubData.Uuid, pubData.Text, fr.data.OriginProfile.Domain, false, kept, eventTime(pubData.Dt, event))
			fr.sc.postPassed(fr.data.OriginProfile.Domain, pubData.Uuid)
			if err != nil {
				log.Error("Error creating social publication for friend:", err)
				// No post refers to what was just written: it would take
				// space no storage limit counts, for good.
				fr.sc.removeUnusedMedia(unencDir, written)
				continue
			}
			newPosts = true

			// Issue #43: a post already here, served again (a second sent
			// again by stopAtPost, or a re-delivery), stops at the
			// PublicationOwner check above - every PublicationEvent
			// reaching this point is a genuinely new post, exactly once.
			// Issue #92: except during the one-time backlog catch-up,
			// where "genuinely new to us" still means "years old to the
			// friend who posted it" - not worth a push.
			if fr.sc.push != nil && !catchingUp {
				friendName := fr.data.OriginProfile.Name
				if friendName == "" {
					friendName = fr.data.OriginProfile.Domain
				}
				fr.sc.push.NotifyNewPost(friendName, pubData.Uuid)
			}

		case LikeEvent:
			fr.applyLike(event, catchingUp)

		case LikeCommentEvent:
			fr.applyCommentLike(event, catchingUp)

		case CommentEvent:
			var comment Comment
			json.Unmarshal([]byte(event.Content), &comment)
			// false: this is a friend's comment, synced in - see
			// NewSocialComment for the device owner's own-comment path.
			// Issue #174: the author is the device this came from, never
			// who the event says wrote it.
			if err := fr.dao.NewComment(comment.Uuid, comment.PublisherName, comment.PubUUID, comment.Comment, fr.data.OriginProfile.Domain, false, eventTime(comment.Dt, event)); err == nil && !catchingUp {
				fr.notifyIfOwnPublication(comment.PubUUID, comment.Uuid, "commented on your post", pb.NotificationType_NotificationNewComment)
			}

		case DelPublicationEvent:
			// issue #34: the owner deleted one of their posts — remove our
			// cached copy too.
			var del DelPublication
			json.Unmarshal([]byte(event.Content), &del)
			// Issue #174: only a post of the friend this came from - never
			// the owner's own, or another friend's cached here.
			if fr.mayDeletePublication(del.PubUUID) {
				fr.sc.removePublication(del.PubUUID)
			}

		case DelCommentEvent:
			// issue #35: the post owner deleted a comment — remove our
			// cached copy too.
			var del DelComment
			json.Unmarshal([]byte(event.Content), &del)
			// Issue #174: the friend this came from may delete a comment on
			// one of its own posts, or one it wrote - nothing else.
			if fr.mayDeleteComment(del.CommentUUID) {
				fr.dao.DeleteSocialComment(del.CommentUUID)
			}

		case ForgetEvent:
			var fg Forget
			json.Unmarshal([]byte(event.Content), &fg)
			if fg.Domain != fr.sc.settings.Domain() {
				break // only ever served to its target; ignore otherwise
			}
			fr.sc.forgetFriend(fr.data.OriginProfile.Domain, fr.data.Secret)
			return nil
		}

		if err = fr.dao.UpdateLatestSync(fr.data.OriginProfile.Domain, event.Dt); err == nil {
			at = event.Dt
		}
	}

	// Issue #92: a page shorter than what we asked for means there's
	// nothing older left queued behind it - the backlog (if there ever
	// was one) is now fully drained, so every event from the next sync
	// cycle onward is genuinely new and safe to notify about normally.
	// Checked here (once per cycle) rather than inside the loop, since a
	// friend with zero pending events still needs to flip this the very
	// first time they're ever synced.
	if catchingUp && len(resp.RespEvents.Events) < cEventsSyncPageSize {
		if err := fr.dao.MarkNotificationsStarted(fr.data.OriginProfile.Domain); err != nil {
			log.Error("could not mark notifications as started for", fr.data.OriginProfile.Domain, ":", err)
		}
	}

	fr.stopPage(newPosts)
	return
}

// stopPage is what ends a page of a friend's events, however it ends: if
// it stored new posts, friends' posts may now be over the storage limit.
func (fr *friendship) stopPage(newPosts bool) {
	if newPosts {
		fr.sc.EnforceStorageLimit()
	}
}

// stopAtPost is for a post of the friend's that the connection or this
// disk failed, not the post itself. It reports whether the page stops
// there, so the next sync asks for the post again; once syncs have
// stopped at it cPostTries times in a row it is given up instead, and the
// friend's later events go on.
//
// at is the second this page last moved the cursor to. The next sync asks
// for events after the cursor, so if an earlier event of the post's own
// second moved it there, it goes back a second and the whole second is
// served again, this post with it (a friend serves whole seconds). What
// of that second is already here is skipped then: the post (found by
// PublicationOwner), a like (stored once per domain), a comment (its uuid
// is unique); a deletion finds nothing left to delete.
func (fr *friendship) stopAtPost(pubUuid string, event *pb.Event, at *timestamppb.Timestamp) bool {
	domain := fr.data.OriginProfile.Domain
	if !fr.sc.retryPost(domain, pubUuid) {
		log.Error("giving up on publication", pubUuid, "from", domain, "after", cPostTries, "syncs stopped at it")
		return false
	}
	if at != nil && at.GetSeconds() == event.GetDt().GetSeconds() {
		if err := fr.dao.UpdateLatestSync(domain, timestamppb.New(time.Unix(at.GetSeconds()-1, 0))); err != nil {
			log.Error("could not move the event cursor of", domain, "back to publication", pubUuid, ":", err)
		}
	}
	return true
}

// storeFriendFiles stores each file of a friend's post (storeFriendFile),
// leaving out those that can't be stored here at all. written is the
// hashes something was written for; err is a failed write, which ends it.
func (fr *friendship) storeFriendFiles(pubUuid string, files []*pb.File, dir string) (kept []*pb.File, written []string, err error) {
	kept = make([]*pb.File, 0, len(files))
	for _, file := range files {
		ok, wrote, err := fr.storeFriendFile(pubUuid, file, dir)
		if wrote {
			written = append(written, file.Hash)
		}
		if err != nil {
			return nil, written, err
		}
		if ok {
			kept = append(kept, file)
		}
	}
	return kept, written, nil
}

// applyLike stores a friend's like of a post, or removes it: an unlike is
// an event of its own (with a new uuid), and used to be stored as one more
// like - counted, listed and notified again. Only the friend's own like
// goes, whatever the payload says: the domain is always the device the
// event came from. Any other action, "" from older releases included, is a
// like, and only a like not already stored is notified.
func (fr *friendship) applyLike(event *pb.Event, catchingUp bool) {
	var like LikePublication
	json.Unmarshal([]byte(event.Content), &like)
	if like.Action == ActionDelete {
		if err := fr.dao.DeleteLikePublication(like.PubUUID, fr.data.OriginProfile.Domain); err != nil {
			log.Error("error removing a friend's like:", err)
		}
		return
	}
	if inserted, err := fr.dao.NewLikePublication(like.Uuid, like.PubUUID, fr.data.OriginProfile.Domain, eventTime(like.Dt, event)); err == nil && inserted && !catchingUp {
		fr.notifyIfOwnPublication(like.PubUUID, "", "liked your post", pb.NotificationType_NotificationLikePublication)
	}
}

// applyCommentLike is applyLike for a like of a comment.
func (fr *friendship) applyCommentLike(event *pb.Event, catchingUp bool) {
	var like LikePublicationComment
	json.Unmarshal([]byte(event.Content), &like)
	if like.Action == ActionDelete {
		if err := fr.dao.DeleteLikePublicationComment(like.CommentUUID, fr.data.OriginProfile.Domain); err != nil {
			log.Error("error removing a friend's comment like:", err)
		}
		return
	}
	if inserted, err := fr.dao.NewLikePublicationComment(like.Uuid, like.CommentUUID, fr.data.OriginProfile.Domain, eventTime(like.Dt, event)); err == nil && inserted && !catchingUp {
		fr.notifyIfOwnComment(like.CommentUUID, "liked your comment", pb.NotificationType_NotificationLikeComment)
	}
}

func (sc *Social) GetRemoteProfile(domain string, conn *wsframe.Client) (name, text string, image []byte, err error) {
	// Get the profile data from the other device
	log.Debug("Getting remote profile:", domain)
	msg := &pb.ReqEnvelope{
		Id: 1,
		Payload: &pb.ReqEnvelope_ReqGetProfile{
			ReqGetProfile: &pb.GetProfile{},
		},
	}
	b, _ := proto.Marshal(msg)
	if err = conn.WriteMessage(gorilla.BinaryMessage, b); err != nil {
		log.Error("write error in external:", err)
		return
	}

	log.Debug("We got remote profile:", domain)
	// We should get back the Ack
	_, data, err := conn.ReadMessage()
	if err != nil {
		log.Error("read error in external:", err)
		return
	}

	var respProf pb.RespEnvelope
	if err = proto.Unmarshal(data, &respProf); err != nil {
		return
	}
	profResp, err := expectPayload[*pb.RespEnvelope_RespProfile]("get remote profile", domain, respProf.Payload)
	if err != nil {
		return "", "", nil, err
	}
	prof := profResp.RespProfile
	log.Debug("Remote profile looks good:", domain, prof.Name)

	return prof.Name, prof.Text, prof.Image, nil
}

// ErrFriendsAgain is SendFriendshipReq's "success, and already friends":
// the other device re-linked an accepted friendship with this device's new
// identity (issue #140), so there is nothing left to accept.
var ErrFriendsAgain = errors.New("friends again")

func (sc *Social) SendFriendshipReq(domain string) (err error) {
	conn, err := sc.connectToDevice(domain)
	if err != nil {
		log.Error("Error connecting to external device:", err)
		return err
	}
	defer conn.Close()

	remoteName, remoteText, remoteImg, err := sc.GetRemoteProfile(domain, conn)
	if err != nil {
		return
	}

	secret := session.RandomSecret() // issue #157: not a UUID

	// Store the remote data and then send the real request: the other
	// device calls back (DidSendFriendshipReq) to check this row exists.
	// Issue #140: a row may already be there (a re-sent request, or a
	// friend whose device was re-created) - reuse it with the new secret,
	// and put it back as it was if the request doesn't go through. A new
	// row that the other side refused is removed again, so a failed
	// request leaves nothing behind.
	previous, err := sc.dao.FriendshipByDomain(domain)
	if err != nil {
		return err
	}
	if previous != nil && previous.Status == "blocked" {
		return errors.New("you blocked this device - unblock it before sending a request")
	}
	if previous != nil {
		err = sc.dao.RelinkFriendship(domain, secret, remoteName, remoteText, remoteImg, "pending", true)
	} else {
		err = sc.dao.NewFriendship(domain, secret, remoteName, remoteText, remoteImg, true)
	}
	log.Debug("Remote profile stored")
	if err != nil {
		return
	}
	delivered := false
	defer func() {
		if delivered {
			return
		}
		var rbErr error
		if previous != nil {
			rbErr = sc.dao.RelinkFriendship(domain, previous.Secret, previous.Name, previous.Text, previous.Image, previous.Status, previous.Sent)
		} else {
			rbErr = sc.dao.DeleteFriendshipRow(domain)
		}
		if rbErr != nil {
			log.Error("could not undo the failed friend request to", domain, rbErr)
		}
	}()
	msg := &pb.ReqEnvelope{
		Id: 1,
		Payload: &pb.ReqEnvelope_ReqFriendshipInterRequest{
			ReqFriendshipInterRequest: &pb.FriendshipInterRequest{
				Domain: sc.settings.Domain(),
				Secret: secret,
				OriginProfile: &pb.Profile{
					Name:  sc.profile.Name(),
					Image: sc.profile.Image(),
					Text:  sc.profile.Text(),
				},
			},
		},
	}
	b, _ := proto.Marshal(msg)
	if err := conn.WriteMessage(gorilla.BinaryMessage, b); err != nil {
		log.Error("write error in external:", err)
		return err
	}
	log.Debug("Friendship request sent internally")

	// We should get back the Ack
	_, data, err := conn.ReadMessage()
	if err != nil {
		log.Error("read error in friendship internal response:", err)
		return
	}

	var respAck pb.RespEnvelope
	if err = proto.Unmarshal(data, &respAck); err != nil {
		log.Error("read error in unmarshalling friendship internal requestrespons3:", err)
		return
	}
	ackResp, err := expectPayload[*pb.RespEnvelope_RespAck]("send friendship request", domain, respAck.Payload)
	if err != nil {
		return err
	}
	if ackResp.RespAck.Ok {
		log.Debug("Frienship request internal accepted...")
		delivered = true
		if ackResp.RespAck.Code == "accepted" {
			// They already had us as a friend and re-linked the
			// friendship to this device: accepted on both sides now.
			if err := sc.dao.RelinkFriendship(domain, secret, remoteName, remoteText, remoteImg, "accepted", true); err != nil {
				return err
			}
			return ErrFriendsAgain
		}
		return nil
	}

	log.Debug("Frienship request failed...", ackResp.RespAck.ErrorMsg)
	return errors.New(ackResp.RespAck.ErrorMsg)
}

func (sc *Social) ExternalFriendshipRequest(extDomain, secret, name, profileText string, image []byte) (err error) {
	log.Debug("Got an internal friendship req, checking foreign domain:", extDomain)
	// Check if the request came from the other side
	conn, err := sc.connectToDevice(extDomain)
	if err != nil {
		log.Error("Error connecting to external device:", err)
		return err
	}
	defer conn.Close()

	// Check if the other device sent the request
	msg := &pb.ReqEnvelope{
		Id: 1,
		Payload: &pb.ReqEnvelope_ReqDidSendFriendshipReq{
			ReqDidSendFriendshipReq: &pb.DidSendFriendshipReq{
				Domain: sc.settings.Domain(),
				Secret: secret,
			},
		},
	}
	b, _ := proto.Marshal(msg)
	if err := conn.WriteMessage(gorilla.BinaryMessage, b); err != nil {
		log.Error("write error in external:", err)
		return err
	}

	log.Debug("Processing response form External Domain:", extDomain)
	// We should get back the Ack
	_, data, err := conn.ReadMessage()
	if err != nil {
		log.Error("read error validating external friendship request:", err)
		return
	}

	var respAck pb.RespEnvelope
	if err = proto.Unmarshal(data, &respAck); err != nil {
		log.Error("read error unmarshalling external friendship request:", err)
		return
	}
	ackResp, err := expectPayload[*pb.RespEnvelope_RespAck]("external friendship request", extDomain, respAck.Payload)
	if err != nil {
		return err
	}
	if ackResp.RespAck.Ok {
		log.Debug("Frienship ack request sent...")
		// Issue #140: the device answering for extDomain just confirmed it
		// sent this request, so a friendship this device already has with
		// that domain belongs to a device that was re-created (or lost its
		// database) under the same name: take the new secret instead of
		// failing on the existing row.
		existing, found, err := sc.dao.FriendshipStatusByDomain(extDomain)
		if err != nil {
			return err
		}
		lastSeen, _ := sc.dao.FriendLastSeen(extDomain)
		action, status := relinkDecision(existing, found, lastSeen, time.Now())
		switch action {
		case relinkRefuse:
			log.Info("friend request from a blocked domain refused:", extDomain)
			return errors.New("friendship request refused")
		case relinkUpdate:
			log.Info("friendship with", extDomain, "re-linked to its new device, status", status)
			if err = sc.dao.RelinkFriendship(extDomain, secret, name, profileText, image, status, false); err != nil {
				return err
			}
			if status == "accepted" {
				// Still friends: nothing for the owner to decide, and
				// the requester is told so (Ack code "accepted").
				return ErrFriendsAgain
			}
		default:
			if err = sc.dao.NewFriendship(extDomain, secret, name, profileText, image, false); err != nil {
				return err
			}
		}
		notifyName := name
		if notifyName == "" {
			notifyName = extDomain
		}
		if err := sc.dao.NewNotification(pb.NotificationType_NotificationFriendRequest, notifyName, extDomain, "", ""); err != nil {
			log.Error("could not record notification:", err)
		}
		if sc.push != nil {
			sc.push.NotifyFriendshipRequest(notifyName)
		}
		return nil
	}

	log.Debug("Frienship ack request failed...", ackResp.RespAck.ErrorMsg)
	return errors.New(ackResp.RespAck.ErrorMsg)
}

type relinkAction int

const (
	relinkInsert relinkAction = iota // no friendship yet: a new request
	relinkUpdate                     // one exists: new secret, status as returned
	relinkRefuse                     // blocked: stays blocked, request refused
)

// relinkDecision (issue #140) is what a verified friend request does to an
// existing friendship with the same domain: an accepted friend stays
// accepted on its new device, a pending one becomes this new incoming
// request, and a blocked domain stays blocked.
// cRelinkFreshness: a re-link keeps an accepted friendship only if the
// friend's device was heard from this recently. A device name can change
// hands (released and taken by someone else), and a new holder used to
// inherit the friendship and the friend's posts; after a week of silence
// the request goes through the normal accept/refuse instead.
const cRelinkFreshness = 7 * 24 * time.Hour

func relinkDecision(existingStatus string, found bool, lastSeen, now time.Time) (relinkAction, string) {
	if !found {
		return relinkInsert, "pending"
	}
	switch existingStatus {
	case "blocked":
		return relinkRefuse, "blocked"
	case "accepted":
		if !lastSeen.IsZero() && now.Sub(lastSeen) <= cRelinkFreshness {
			return relinkUpdate, "accepted"
		}
		return relinkUpdate, "pending"
	default:
		return relinkUpdate, "pending"
	}
}

func (sc *Social) GetFriendship(domain, secret string) (friendship *pb.Friendship, err error) {
	status, name, text, image, _, err := sc.dao.GetFriendship(domain, secret)
	if err != nil {
		return nil, err
	}

	return &pb.Friendship{
		OriginProfile: &pb.Profile{
			Name:   name,
			Image:  image,
			Text:   text,
			Domain: domain,
		},
		Status: sc.statusToPb(status),
	}, nil
}

func (sc *Social) GetFriendships() (friendships []*pb.Friendship, err error) {
	return sc.dao.GetFriendships()
}

func (sc *Social) statusToPb(status string) (pbStatus pb.FriendShipStatus) {
	switch status {
	case "pending":
		return pb.FriendShipStatus_Pending
	case "accepted":
		return pb.FriendShipStatus_Accepted
	case "blocked":
		return pb.FriendShipStatus_Blocked
	}

	return
}

// NewLikePublicationComment toggles pr's like of commentUuid: if pr hasn't
// liked it yet, it likes it; if pr already liked it, it undoes that like
// instead. Returns the resulting liked state.
func (sc *Social) NewLikePublicationComment(pr *profile.Profile, commentUuid string) (liked bool, err error) {
	alreadyLiked, err := sc.dao.HasLikedComment(commentUuid, pr.Domain())
	if err != nil {
		return false, err
	}

	action := ActionCreate
	if alreadyLiked {
		action = ActionDelete
	}
	// Reused for both the event content's identity and the like row's
	// primary key, so a friend device consuming this event ends up with
	// a row keyed the same as the origin device's.
	likeUuid := uuid.New().String()

	eventPayload, err := json.Marshal(LikePublicationComment{
		Uuid:         likeUuid,
		Action:       action,
		CommentUUID:  commentUuid,
		Dt:           time.Now().Unix(),
		FriendDomain: pr.Domain(),
	})
	if err != nil {
		return false, err
	}
	if err = sc.dao.NewEvent(LikeCommentEvent, eventPayload); err != nil {
		return false, err
	}

	if alreadyLiked {
		return false, sc.dao.DeleteLikePublicationComment(commentUuid, pr.Domain())
	}
	_, err = sc.dao.NewLikePublicationComment(likeUuid, commentUuid, pr.Domain(), time.Now())
	return true, err
}

// NewLikePublication toggles pr's like of pubUuid: if pr hasn't liked it
// yet, it likes it; if pr already liked it, it undoes that like instead.
// Returns the resulting liked state.
func (sc *Social) NewLikePublication(pr *profile.Profile, pubUuid string) (liked bool, err error) {
	alreadyLiked, err := sc.dao.HasLikedPublication(pubUuid, pr.Domain())
	if err != nil {
		return false, err
	}

	action := ActionCreate
	if alreadyLiked {
		action = ActionDelete
	}
	// Reused for both the event content's identity and the like row's
	// primary key, so a friend device consuming this event ends up with
	// a row keyed the same as the origin device's.
	likeUuid := uuid.New().String()

	eventPayload, err := json.Marshal(LikePublication{
		Uuid:         likeUuid,
		Action:       action,
		PubUUID:      pubUuid,
		Dt:           time.Now().Unix(),
		FriendDomain: pr.Domain(),
	})
	if err != nil {
		return false, err
	}
	if err = sc.dao.NewEvent(LikeEvent, eventPayload); err != nil {
		return false, err
	}

	if alreadyLiked {
		return false, sc.dao.DeleteLikePublication(pubUuid, pr.Domain())
	}
	_, err = sc.dao.NewLikePublication(likeUuid, pubUuid, pr.Domain(), time.Now())
	return true, err
}

// resolveLikerProfiles turns a list of liker domains (self or friends) into
// displayable profiles (issue #29). A domain matching our own is resolved
// to our own current profile; anything else is looked up in the friendship
// cache (kept fresh by issue #26's periodic refresh) and simply skipped if
// unknown, e.g. an ex-friend since removed.
func (sc *Social) resolveLikerProfiles(domains []string) (likers []*pb.Profile, err error) {
	likers = []*pb.Profile{}
	for _, domain := range domains {
		if domain == sc.settings.Domain() {
			likers = append(likers, &pb.Profile{
				Name:   sc.profile.Name(),
				Text:   sc.profile.Text(),
				Image:  sc.profile.Image(),
				Domain: domain,
			})
			continue
		}

		name, text, image, err := sc.dao.GetFriendProfile(domain)
		if err != nil {
			log.Debug("Skipping unknown liker domain:", domain, err)
			continue
		}
		likers = append(likers, &pb.Profile{
			Name:   name,
			Text:   text,
			Image:  image,
			Domain: domain,
		})
	}
	return likers, nil
}

// GetPublicationLikers returns who liked pubUuid, most recent first.
func (sc *Social) GetPublicationLikers(pubUuid string) (likers []*pb.Profile, err error) {
	domains, err := sc.dao.GetPublicationLikerDomains(pubUuid)
	if err != nil {
		return nil, err
	}
	return sc.resolveLikerProfiles(domains)
}

// GetCommentLikers returns who liked commentUuid, most recent first.
func (sc *Social) GetCommentLikers(commentUuid string) (likers []*pb.Profile, err error) {
	domains, err := sc.dao.GetCommentLikerDomains(commentUuid)
	if err != nil {
		return nil, err
	}
	return sc.resolveLikerProfiles(domains)
}

func (sc *Social) NewSocialComment(pr *profile.Profile, pubUuid, comment string) (err error) {
	commentUuid := uuid.New().String()
	json, err := json.Marshal(Comment{
		Uuid:          commentUuid,
		Action:        ActionCreate,
		PubUUID:       pubUuid,
		Comment:       comment,
		Dt:            time.Now().Unix(),
		PublisherName: pr.Name(),
	})
	err = sc.dao.NewEvent(CommentEvent, json)
	if err != nil {
		return err
	}
	return sc.dao.NewComment(commentUuid, pr.Name(), pubUuid, comment, sc.settings.Domain(), true, time.Now())
}

// DeletePublication removes pubUuid, provided it's one of the device
// owner's own posts (issue #34), and logs an event so friends who cached a
// copy of it remove theirs too on their next sync.
func (sc *Social) DeletePublication(pubUuid string) (err error) {
	own, err := sc.dao.IsOwnPublication(pubUuid)
	if err != nil {
		return err
	}
	if !own {
		return errors.New("not your publication")
	}

	payload, err := json.Marshal(DelPublication{PubUUID: pubUuid, Dt: time.Now().Unix()})
	if err != nil {
		return err
	}
	if err = sc.dao.NewEvent(DelPublicationEvent, payload); err != nil {
		return err
	}

	return sc.removePublication(pubUuid)
}

// DeleteComment removes commentUuid, provided it's on one of the device
// owner's own posts — issue #35: "even if the comment is not yours, but
// only when the comment is in one of your posts." Logs an event so
// friends who cached a copy of the comment remove theirs too.
//
// Issue #174: also a comment the owner wrote, on anyone's post - friends
// apply the deletion because it comes from its author (mayDeleteComment).
func (sc *Social) DeleteComment(commentUuid string) (err error) {
	pubUuid, _, ownComment, found, err := sc.dao.CommentInfo(commentUuid)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("no such comment")
	}
	own, err := sc.dao.IsOwnPublication(pubUuid)
	if err != nil {
		return err
	}
	if !own && !ownComment {
		return errors.New("not your comment, nor a comment on one of your posts")
	}

	payload, err := json.Marshal(DelComment{CommentUUID: commentUuid, Dt: time.Now().Unix()})
	if err != nil {
		return err
	}
	if err = sc.dao.NewEvent(DelCommentEvent, payload); err != nil {
		return err
	}

	return sc.dao.DeleteSocialComment(commentUuid)
}

func (sc *Social) ChangeFriendStatus(domain string, status pb.FriendShipStatus) (err error) {
	return sc.dao.ChangeFriendStatus(domain, status)
}

// DeleteFriendship (issue #25) removes the friendship with domain from this
// device - in practice a request the owner wants gone, whichever side sent
// it - and asks the other device to drop its copy as well. That second
// part is best effort: the other device may be switched off, and the
// owner's decision must not depend on it. A sender whose counterpart
// deleted while it was off still converges on its next sync (see the
// not_found handling in updateFriendshipStatus).
//
// Issue #174: deleteTheirData also removes everything synced from domain;
// askThemToDeleteMine asks domain's device to remove everything this one
// shared there. That is sent straight away when the device answers; when
// it doesn't, a "forget me" event only it is served waits for its next
// sync, and the friendship stays - leaving - until its device has done it
// and says so (ExternalFriendshipDelete).
func (sc *Social) DeleteFriendship(domain string, deleteTheirData, askThemToDeleteMine bool) error {
	friendships, err := sc.dao.GetFriendships()
	if err != nil {
		return err
	}
	var fr *pb.Friendship
	for _, f := range friendships {
		if f.OriginProfile.Domain == domain {
			fr = f
			break
		}
	}
	if fr == nil {
		return fmt.Errorf("no friendship with %s", domain)
	}

	if deleteTheirData {
		if err := sc.purgeFriendData(domain); err != nil {
			return err
		}
	}
	if askThemToDeleteMine {
		if err := sc.notifyFriendshipDeleted(domain, fr.Secret, true); err == nil {
			return sc.dao.DeleteFriendship(domain)
		} else {
			log.Info("could not reach", domain, "- asking on its next sync instead:", err)
		}
		payload, err := json.Marshal(Forget{Domain: domain, Dt: time.Now().Unix()})
		if err != nil {
			return err
		}
		if err := sc.dao.NewEventFor(ForgetEvent, payload, domain); err != nil {
			return err
		}
		return sc.dao.SetForgetRequested(domain, time.Now())
	}
	if err := sc.notifyFriendshipDeleted(domain, fr.Secret, false); err != nil {
		log.Info("could not tell", domain, "the friendship was deleted, it will find out on its own:", err)
	}
	return sc.dao.DeleteFriendship(domain)
}

// purgeFriendData removes everything on this device that came from domain
// (issue #174): its posts with their media, comments and likes; its
// comments and likes on anyone's posts; the alerts about it. Never
// anything of the owner's or another friend's.
func (sc *Social) purgeFriendData(domain string) error {
	pubs, err := sc.dao.FriendPublicationUuids(domain)
	if err != nil {
		return err
	}
	for _, p := range pubs {
		if err := sc.removePublication(p); err != nil {
			return err
		}
	}
	if err := sc.dao.PurgeFriendActivity(domain); err != nil {
		return err
	}
	log.Info("removed everything synced from", domain, "-", len(pubs), "posts")
	return nil
}

// forgetFriend does what an ex-friend's "forget me" asks (issue #174):
// removes everything that came from it, tells its device it's done (the
// friendship there goes on that), and drops the friendship here.
func (sc *Social) forgetFriend(domain, secret string) {
	if err := sc.purgeFriendData(domain); err != nil {
		log.Error("could not remove the data of", domain, ":", err)
		return
	}
	if err := sc.notifyFriendshipDeleted(domain, secret, false); err != nil {
		log.Info("could not tell", domain, "its data is gone - it will find out on its own:", err)
	}
	if err := sc.dao.DeleteFriendship(domain); err != nil {
		log.Error("could not remove the friendship with", domain, ":", err)
	}
}

// mayDeletePublication: a friend's deletion applies to its own posts
// cached here only (issue #174) - never the owner's, or another friend's.
func (fr *friendship) mayDeletePublication(pubUuid string) bool {
	owner, own, found, err := fr.dao.PublicationOwner(pubUuid)
	if err != nil || !found {
		return false
	}
	if !own && owner == fr.data.OriginProfile.Domain {
		return true
	}
	log.Info("ignored", fr.data.OriginProfile.Domain, "deleting a post that isn't theirs:", pubUuid)
	return false
}

// mayDeleteComment: a friend's deletion applies to a comment on one of its
// own posts, or one it wrote (issue #174) - never to another's.
func (fr *friendship) mayDeleteComment(commentUuid string) bool {
	pubUuid, author, _, found, err := fr.dao.CommentInfo(commentUuid)
	if err != nil || !found {
		return false
	}
	from := fr.data.OriginProfile.Domain
	if author == from {
		return true
	}
	owner, own, found, err := fr.dao.PublicationOwner(pubUuid)
	if err == nil && found && !own && owner == from {
		return true
	}
	log.Info("ignored", from, "deleting a comment that isn't theirs:", commentUuid)
	return false
}

// notifyFriendshipDeleted tells domain's device that the friendship
// identified by secret is gone here (issue #25).
// forgetMe (issue #174) also asks it to remove everything this device
// shared there.
func (sc *Social) notifyFriendshipDeleted(domain, secret string, forgetMe bool) error {
	conn, err := sc.connectToDevice(domain)
	if err != nil {
		return err
	}
	defer conn.Close()

	msg := &pb.ReqEnvelope{
		Id: 1,
		Payload: &pb.ReqEnvelope_ReqFriendshipInterDelete{
			ReqFriendshipInterDelete: &pb.FriendshipInterDelete{
				Domain:   sc.settings.Domain(),
				Secret:   secret,
				ForgetMe: forgetMe,
			},
		},
	}
	b, _ := proto.Marshal(msg)
	if err := conn.WriteMessage(gorilla.BinaryMessage, b); err != nil {
		return err
	}
	// The owner is waiting on this; a device that never answers must not
	// hold their delete hostage.
	_, data, err := conn.ReadMessageUpTo(wsframe.Limit, 15*time.Second)
	if err != nil {
		return err
	}
	var resp pb.RespEnvelope
	if err := proto.Unmarshal(data, &resp); err != nil {
		return err
	}
	ack, err := expectPayload[*pb.RespEnvelope_RespAck]("delete friendship", domain, resp.Payload)
	if err != nil {
		return err
	}
	if !ack.RespAck.Ok {
		return errors.New(ack.RespAck.ErrorMsg)
	}
	return nil
}

// ExternalFriendshipDelete handles another device's FriendshipInterDelete
// (issue #25): that device deleted the friendship on its side, so drop our
// copy - but only if it knows the secret we hold for it.
//
// forgetMe (issue #174): also remove everything that device shared here -
// its own data only: domain is the one the secret proves it is.
func (sc *Social) ExternalFriendshipDelete(domain, secret string, forgetMe bool) error {
	if forgetMe {
		// The secret proves the caller is domain before anything goes.
		if _, _, _, _, _, err := sc.dao.GetFriendship(domain, secret); err != nil {
			return errors.New("Friendship not found")
		}
		if err := sc.purgeFriendData(domain); err != nil {
			return err
		}
	}
	removed, err := sc.dao.DeleteFriendshipWithSecret(domain, secret)
	if err != nil {
		return err
	}
	if !removed {
		return errors.New("Friendship not found")
	}
	log.Info("friendship with", domain, "removed at that device's request")
	return nil
}
