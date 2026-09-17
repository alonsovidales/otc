// SPDX-License-Identifier: AGPL-3.0-or-later

package social

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
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
	"github.com/google/uuid"
	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
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

	// cSocialVideoPath is the fixed, dot-prefixed directory social-only
	// video compressions are stored under - deliberately outside anywhere
	// the owner would browse to in the Files section, since these aren't
	// files they chose to keep: they exist purely so NewPublication has
	// something smaller to actually distribute for an oversized video.
	cSocialVideoPath = "/.otc-social-video-cache/"
)

type Social struct {
	dao          *dao.Dao
	filesmanager *filesmanager.Manager
	settings     *settings.Settings
	profile      *profile.Profile
	push         *push.Push
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

func Init(dao *dao.Dao, filesmanager *filesmanager.Manager, settings *settings.Settings, profile *profile.Profile, push *push.Push) *Social {
	return &Social{
		dao:          dao,
		filesmanager: filesmanager,
		settings:     settings,
		profile:      profile,
		push:         push,
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

func (sc *Social) NewPublication(ses *session.Session, text string, paths []string) (pubUuID string, err error) {
	files := make([]*pb.File, len(paths))
	for i, path := range paths {
		file, err := sc.filesmanager.GetFile(ses, path)
		if err != nil {
			log.Error("Error loading file:", err)
			return "", fmt.Errorf("error loading file %q: %w", path, err)
		}

		// Issue #60: an oversized video gets compressed and republished as
		// a brand new file *before* anything below treats it as this
		// post's file - every subsequent step (the unenc cache write, the
		// thumbnail poll, the file actually stored on the publication)
		// then operates on the compressed copy transparently, exactly as
		// if the owner had picked it directly.
		if shouldCompressForSocial(file.Mime, len(file.Content)) {
			compressed, cErr := sc.filesmanager.CompressVideoForSocial(file.Content)
			if cErr != nil {
				log.Error("error compressing oversized video for publication, publishing the original instead:", path, cErr)
			} else {
				socialPath := fmt.Sprintf("%s%s.mp4", cSocialVideoPath, uuid.New().String())
				compressedFile, upErr := sc.filesmanager.UploadFile(ses, socialPath, compressed, false, nil)
				if upErr != nil {
					log.Error("error storing compressed video for publication, publishing the original instead:", path, upErr)
				} else {
					log.Debug("Publishing compressed video instead of oversized original:", path, "->", socialPath, len(file.Content), "->", len(compressed))
					file = compressedFile
					// The compressed file is now what gets loaded below for
					// the unenc cache/thumbnail - GetFile fills in Content,
					// UploadFile's own return value doesn't.
					file, err = sc.filesmanager.GetFile(ses, socialPath)
					if err != nil {
						log.Error("Error loading compressed video:", err)
						return "", fmt.Errorf("error loading compressed video %q: %w", socialPath, err)
					}
				}
			}
		}

		files[i] = file
		unencPath := fmt.Sprintf("%s/%s", cfg.GetStr("otc", "unenc-storage-path"), file.Hash)
		err = os.WriteFile(unencPath, file.Content, 0644) // perms: rw-r--r--
		if err != nil {
			return "", err
		}

		// Issue #49: UploadFile writes the thumbnail from a background
		// goroutine (see files_manager.UploadFile), not before returning -
		// fine for the original flow (a post is always composed well
		// after a prior, separate sync finished), but a client that
		// uploads and immediately posts the same file (issue #49's
		// Phone-source picker) can race that goroutine and find no
		// thumbnail yet. Retried rather than failing the whole post over
		// what's normally a sub-second delay.
		var unEncThumb []byte
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
		unencPathThumb := fmt.Sprintf("%s/%s_thumbnail", cfg.GetStr("otc", "unenc-storage-path"), file.Hash)
		err = os.WriteFile(unencPathThumb, unEncThumb, 0644) // perms: rw-r--r--
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

	return pubUuID, sc.dao.NewSocialPublication(pubUuID, text, sc.profile.Domain, true, files)
}

func (sc *Social) GetEvents(pr *profile.Profile, since time.Time, total int32) (events []*pb.Event, err error) {
	events, err = sc.dao.GetEvents(since, total)
	if err != nil {
		log.Debug("error retriving events", err)
	}
	return
}

func (sc *Social) GetPublicationFiles(uuid string) (files []*pb.File, err error) {
	all, err := sc.dao.GetSocialPublicationFiles(uuid)
	if err != nil {
		return nil, err
	}

	files = make([]*pb.File, 0, len(all))
	for _, file := range all {
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
	publications, err = sc.dao.GetSocialPublications(since, total, ownOnly, exclude, pr.Name, pr.Text, pr.Image, pr.Domain)
	if err != nil {
		log.Debug("error retriving publications", err)
		return
	}

	// Populate the files content. A missing/corrupted thumbnail is skipped
	// rather than failing the whole feed - see GetPublicationFiles' doc
	// comment for why this used to be much worse than "this one photo is
	// missing from this one post".
	for _, pub := range publications.Publications {
		goodFiles := make([]*pb.File, 0, len(pub.Files))
		for _, file := range pub.Files {
			content, readErr := os.ReadFile(fmt.Sprintf("%s/%s_thumbnail", cfg.GetStr("otc", "unenc-storage-path"), file.Hash))
			if readErr != nil {
				log.Error("skipping missing/corrupted thumbnail in feed for publication", pub.Uuid, "hash", file.Hash, ":", readErr)
				continue
			}
			file.Content = content
			goodFiles = append(goodFiles, file)
		}
		pub.Files = goodFiles

		pub.Comments, err = sc.dao.GetSocialPublicationComments(pub.Uuid, pr.Domain)
		if err != nil {
			return nil, err
		}
	}

	return
}

// GetPublication (issue #78) is GetPublications' single-post equivalent -
// tapping a notification for a post outside whatever page of the feed
// happens to be loaded needs to fetch just that one directly. Populates
// thumbnails/comments the same way GetPublications does for its list.
func (sc *Social) GetPublication(pr *profile.Profile, pubUuid string) (pub *pb.SocialPublication, err error) {
	pub, err = sc.dao.GetSocialPublicationByUUID(pubUuid, pr.Name, pr.Text, pr.Image, pr.Domain)
	if err != nil {
		return nil, err
	}

	goodFiles := make([]*pb.File, 0, len(pub.Files))
	for _, file := range pub.Files {
		content, readErr := os.ReadFile(fmt.Sprintf("%s/%s_thumbnail", cfg.GetStr("otc", "unenc-storage-path"), file.Hash))
		if readErr != nil {
			log.Error("skipping missing/corrupted thumbnail for publication", pub.Uuid, "hash", file.Hash, ":", readErr)
			continue
		}
		file.Content = content
		goodFiles = append(goodFiles, file)
	}
	pub.Files = goodFiles

	pub.Comments, err = sc.dao.GetSocialPublicationComments(pub.Uuid, pr.Domain)
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
		if !ok || hash == "" {
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

func (sc *Social) connectToDevice(domain string) (conn *gorilla.Conn, err error) {
	if !isAllowedFriendDomain(domain) {
		return nil, fmt.Errorf("domain %q is not a %s address", domain, friendDomainTLD())
	}

	// If this is a bridge connection, we will retry to connect to the bridge
	u := url.URL{Scheme: "wss", Host: domain, Path: "/ws"}
	log.Debug("Connecting to external:", domain, u)
	h := http.Header{}
	h.Set("Sec-WebSocket-Protocol", "protobuf")
	conn, _, err = gorilla.DefaultDialer.Dial(u.String(), h)
	if err != nil {
		log.Error("dialing websocket:", err)
		return
	}
	log.Debug("Connected to external...")

	return
}

type friendship struct {
	conn *gorilla.Conn
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
		defer friend.conn.Close()

		err = friend.updateFriendshipStatus()
		if err != nil {
			log.Error("Error trying to update friendship status:", err)
			continue
		}

		if friend.data.Status == pb.FriendShipStatus_Accepted {
			log.Debug("Auth as friend:", friend.data.OriginProfile.Domain)
			err = friend.autAsFriend()
			if err != nil {
				log.Error("Error trying to auth as friend:", err)
				continue
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
				continue
			}
		}

		// Update friend timeline is the request is accepted
	}

	return
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
				Domain: fr.sc.settings.Domain, // We want to get our status, so our domain
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
				Domain: fr.sc.settings.Domain,
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
		return
	}

	_, data, err := fr.conn.ReadMessage()
	if err != nil {
		log.Error("read error trying to get publication files from friend:", fr.data.OriginProfile.Domain, err)
		return
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
		fr.sc.push.Notify(friendName, action)
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
		fr.sc.push.Notify(friendName, action)
	}
}

// cEventsSyncPageSize caps how many of a friend's events get replayed per
// sync cycle (120s) - also doubles as the "have we drained the whole
// backlog yet" signal updateFriendEvents uses below (issue #92): a page
// that comes back shorter than this means there's nothing older left
// queued behind it.
const cEventsSyncPageSize = 20

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
event_loop:
	for _, event := range resp.RespEvents.Events {
		switch event.Type {
		case PublicationEvent:
			var pubData Publication
			json.Unmarshal([]byte(event.Content), &pubData)

			files, err := fr.getPublicationFiles(pubData.Uuid)
			if err != nil {
				log.Error("Error getting publication:", err)
				continue event_loop
			}

			// Store the files in the local drive first
			for _, file := range files {
				sum := sha256.Sum256(file.Content)
				file.Hash = hex.EncodeToString(sum[:])

				unencPathThumb := fmt.Sprintf("%s/%s_thumbnail", cfg.GetStr("otc", "unenc-storage-path"), file.Hash)
				log.Debug("Storing file thumbnail in path:", unencPathThumb)
				err = os.WriteFile(unencPathThumb, file.Content, 0644) // perms: rw-r--r--
				if err != nil {
					log.Error("Error trying to write file from an external event")
					continue event_loop
				}
			}

			err = fr.dao.NewSocialPublication(pubData.Uuid, pubData.Text, fr.data.OriginProfile.Domain, false, files)
			if err != nil {
				log.Error("Error creating social publication for friend:", err)
				continue
			}

			// Issue #43: fr.data.LatestSync (advanced below, per event) means
			// ReqGetEvents{Since: LatestSync} never returns an
			// already-processed event again - every PublicationEvent
			// reaching this point is a genuinely new post, exactly once.
			// Issue #92: except during the one-time backlog catch-up,
			// where "genuinely new to us" still means "years old to the
			// friend who posted it" - not worth a push.
			if fr.sc.push != nil && !catchingUp {
				friendName := fr.data.OriginProfile.Name
				if friendName == "" {
					friendName = fr.data.OriginProfile.Domain
				}
				fr.sc.push.NotifyNewPost(friendName)
			}

		case LikeEvent:
			var like LikePublication
			json.Unmarshal([]byte(event.Content), &like)
			if err := fr.dao.NewLikePublication(like.Uuid, like.PubUUID, fr.data.OriginProfile.Domain); err == nil && !catchingUp {
				fr.notifyIfOwnPublication(like.PubUUID, "", "liked your post", pb.NotificationType_NotificationLikePublication)
			}

		case LikeCommentEvent:
			var like LikePublicationComment
			json.Unmarshal([]byte(event.Content), &like)
			if err := fr.dao.NewLikePublicationComment(like.Uuid, like.CommentUUID, fr.data.OriginProfile.Domain); err == nil && !catchingUp {
				fr.notifyIfOwnComment(like.CommentUUID, "liked your comment", pb.NotificationType_NotificationLikeComment)
			}

		case CommentEvent:
			var comment Comment
			json.Unmarshal([]byte(event.Content), &comment)
			// false: this is a friend's comment, synced in - see
			// NewSocialComment for the device owner's own-comment path.
			if err := fr.dao.NewComment(comment.Uuid, comment.PublisherName, comment.PubUUID, comment.Comment, false); err == nil && !catchingUp {
				fr.notifyIfOwnPublication(comment.PubUUID, comment.Uuid, "commented on your post", pb.NotificationType_NotificationNewComment)
			}

		case DelPublicationEvent:
			// issue #34: the owner deleted one of their posts — remove our
			// cached copy too.
			var del DelPublication
			json.Unmarshal([]byte(event.Content), &del)
			fr.dao.DeleteSocialPublication(del.PubUUID)

		case DelCommentEvent:
			// issue #35: the post owner deleted a comment — remove our
			// cached copy too.
			var del DelComment
			json.Unmarshal([]byte(event.Content), &del)
			fr.dao.DeleteSocialComment(del.CommentUUID)
		}

		err = fr.dao.UpdateLatestSync(fr.data.OriginProfile.Domain, event.Dt)
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

	return
}

func (sc *Social) GetRemoteProfile(domain string, conn *gorilla.Conn) (name, text string, image []byte, err error) {
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

	secret := uuid.New().String()

	// Store the remote data and then send the real request
	err = sc.dao.NewFriendship(domain, secret, remoteName, remoteText, remoteImg, true)
	log.Debug("Remote profile stored")
	if err != nil {
		return
	}
	msg := &pb.ReqEnvelope{
		Id: 1,
		Payload: &pb.ReqEnvelope_ReqFriendshipInterRequest{
			ReqFriendshipInterRequest: &pb.FriendshipInterRequest{
				Domain: sc.settings.Domain,
				Secret: secret,
				OriginProfile: &pb.Profile{
					Name:  sc.profile.Name,
					Image: sc.profile.Image,
					Text:  sc.profile.Text,
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
		return
	}

	log.Debug("Frienship request failed...", ackResp.RespAck.ErrorMsg)
	return errors.New(ackResp.RespAck.ErrorMsg)
}

func (sc *Social) ExternalFriendshipRequest(extDomain, secret, name, profileText string, image []byte) (err error) {
	log.Debug("Got an internal friendship req, checking foreign domain:", extDomain)
	// Check if the request came from the other side
	conn, err := sc.connectToDevice(extDomain)
	defer conn.Close()
	if err != nil {
		log.Error("Error connecting to external device:", err)
		return err
	}

	// Check if the other device sent the request
	msg := &pb.ReqEnvelope{
		Id: 1,
		Payload: &pb.ReqEnvelope_ReqDidSendFriendshipReq{
			ReqDidSendFriendshipReq: &pb.DidSendFriendshipReq{
				Domain: sc.settings.Domain,
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
		if err = sc.dao.NewFriendship(extDomain, secret, name, profileText, image, false); err != nil {
			return err
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
	alreadyLiked, err := sc.dao.HasLikedComment(commentUuid, pr.Domain)
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
		FriendDomain: pr.Domain,
	})
	if err != nil {
		return false, err
	}
	if err = sc.dao.NewEvent(LikeCommentEvent, eventPayload); err != nil {
		return false, err
	}

	if alreadyLiked {
		return false, sc.dao.DeleteLikePublicationComment(commentUuid, pr.Domain)
	}
	return true, sc.dao.NewLikePublicationComment(likeUuid, commentUuid, pr.Domain)
}

// NewLikePublication toggles pr's like of pubUuid: if pr hasn't liked it
// yet, it likes it; if pr already liked it, it undoes that like instead.
// Returns the resulting liked state.
func (sc *Social) NewLikePublication(pr *profile.Profile, pubUuid string) (liked bool, err error) {
	alreadyLiked, err := sc.dao.HasLikedPublication(pubUuid, pr.Domain)
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
		FriendDomain: pr.Domain,
	})
	if err != nil {
		return false, err
	}
	if err = sc.dao.NewEvent(LikeEvent, eventPayload); err != nil {
		return false, err
	}

	if alreadyLiked {
		return false, sc.dao.DeleteLikePublication(pubUuid, pr.Domain)
	}
	return true, sc.dao.NewLikePublication(likeUuid, pubUuid, pr.Domain)
}

// resolveLikerProfiles turns a list of liker domains (self or friends) into
// displayable profiles (issue #29). A domain matching our own is resolved
// to our own current profile; anything else is looked up in the friendship
// cache (kept fresh by issue #26's periodic refresh) and simply skipped if
// unknown, e.g. an ex-friend since removed.
func (sc *Social) resolveLikerProfiles(domains []string) (likers []*pb.Profile, err error) {
	likers = []*pb.Profile{}
	for _, domain := range domains {
		if domain == sc.settings.Domain {
			likers = append(likers, &pb.Profile{
				Name:   sc.profile.Name,
				Text:   sc.profile.Text,
				Image:  sc.profile.Image,
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
		PublisherName: pr.Name,
	})
	err = sc.dao.NewEvent(CommentEvent, json)
	if err != nil {
		return err
	}
	return sc.dao.NewComment(commentUuid, pr.Name, pubUuid, comment, true)
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

	return sc.dao.DeleteSocialPublication(pubUuid)
}

// DeleteComment removes commentUuid, provided it's on one of the device
// owner's own posts — issue #35: "even if the comment is not yours, but
// only when the comment is in one of your posts." Logs an event so
// friends who cached a copy of the comment remove theirs too.
func (sc *Social) DeleteComment(commentUuid string) (err error) {
	pubUuid, err := sc.dao.GetCommentPubUuid(commentUuid)
	if err != nil {
		return err
	}
	own, err := sc.dao.IsOwnPublication(pubUuid)
	if err != nil {
		return err
	}
	if !own {
		return errors.New("not a comment on one of your own publications")
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
