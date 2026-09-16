// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/alonsovidales/otc/cfg"
	imagestagger "github.com/alonsovidales/otc/images_tagger"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/push"
	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Dao struct {
	db *sql.DB
}

// NewWithDB builds a Dao around an already-open *sql.DB, bypassing Init's
// real MySQL dial. Exported for tests in other packages (e.g.
// websocket_test.go) that need a Dao backed by a mock/fake connection
// (github.com/DATA-DOG/go-sqlmock) rather than a live database, since
// Dao's db field itself isn't exported.
func NewWithDB(db *sql.DB) *Dao {
	return &Dao{db: db}
}

func Init() (dao *Dao) {
	dao = new(Dao)

	dsn := fmt.Sprintf(
		"%s:%s@tcp(127.0.0.1:%d)/%s?parseTime=true&charset=utf8mb4,utf8",
		cfg.GetStr("mysql", "user"),
		cfg.GetStr("mysql", "pass"),
		cfg.GetInt("mysql", "port"),
		cfg.GetStr("mysql", "db"))

	log.Debug("connecting to DB:", dsn)

	var err error
	dao.db, err = sql.Open("mysql", dsn)
	if err != nil {
		log.Fatal("error trying to open DB connection", err)
	}

	dao.db.SetMaxOpenConns(20)
	dao.db.SetMaxIdleConns(10)
	dao.db.SetConnMaxLifetime(30 * time.Minute)

	// A freshly-imaged device (issue #38) boots MariaDB and otc around the
	// same time — systemd's `After=mariadb.service` orders the units, but
	// "started" doesn't mean "accepting connections yet", and on a slow
	// first boot (creating its data directory, etc.) that gap can be
	// several seconds. This used to be log.Fatal, which crash-looped otc
	// for that whole window instead of just waiting it out — the DB isn't
	// needed for the process to *exist*, only for handling any actual
	// request, so waiting here is strictly better than dying and letting
	// systemd's RestartSec churn through the same crash repeatedly.
	for {
		if err = dao.db.Ping(); err == nil {
			break
		}
		log.Error("cannot reach the DB yet, retrying in 5s:", err)
		time.Sleep(5 * time.Second)
	}

	return
}

func (dao *Dao) Stop() {
	dao.db.Close()
}

func (dao *Dao) IsSecretDefined() (defined bool, err error) {
	log.Debug("Is session defined")
	err = dao.db.QueryRow("select count(*) from `vault`").Scan(&defined)

	return
}

func (dao *Dao) GetSecret() (encText []byte, err error) {
	err = dao.db.QueryRow("select `secret` from `vault`").Scan(&encText)
	log.Debug("Checking Auth", err)

	return
}

// GetSalt returns the vault's Argon2id salt, or a nil/empty slice for a
// vault created before salted key derivation existed (see session.New,
// which treats that as "migrate this install on next successful login").
func (dao *Dao) GetSalt() (salt []byte, err error) {
	err = dao.db.QueryRow("select `salt` from `vault`").Scan(&salt)
	return
}

func (dao *Dao) PersistSecret(encCheck []byte, salt []byte) (err error) {
	log.Debug("Creating Auth session:")
	_, err = dao.db.Exec("insert into `vault` (`secret`, `salt`) values (?, ?)", encCheck, salt)
	return
}

func (dao *Dao) GetSettings() (subDomain, deviceUuid, BridgeSecret string, err error) {
	err = dao.db.QueryRow("select `subdomain`, `device_uuid`, `bridge_secret` from `settings`").Scan(&subDomain, &deviceUuid, &BridgeSecret)

	return
}

func (dao *Dao) UpdateSettings(subdomain string) (err error) {
	_, err = dao.db.Exec("update `settings` set `subdomain` = ?", subdomain)
	return
}

// UpdateBridgeSecret changes the shared secret this device registers with
// the bridge relay (issue #40), independent of the subdomain — these used
// to be updated together, silently breaking bridge pairing on every plain
// domain rename.
func (dao *Dao) UpdateBridgeSecret(secret string) (err error) {
	_, err = dao.db.Exec("update `settings` set `bridge_secret` = ?", secret)
	return
}

func (dao *Dao) UpdateSecret(encCheck []byte, salt []byte) (err error) {
	_, err = dao.db.Exec("update `vault` set `secret` = ?, `salt` = ?", encCheck, salt)
	return
}

// GetVapidKeys returns this device's Web Push VAPID keypair, empty strings
// if none has been generated yet (see push.Init, which generates one on
// first use and calls SetVapidKeys).
func (dao *Dao) GetVapidKeys() (pub, priv string, err error) {
	var pubN, privN sql.NullString
	err = dao.db.QueryRow("select `vapid_public_key`, `vapid_private_key` from `settings`").Scan(&pubN, &privN)
	return pubN.String, privN.String, err
}

func (dao *Dao) SetVapidKeys(pub, priv string) (err error) {
	_, err = dao.db.Exec("update `settings` set `vapid_public_key` = ?, `vapid_private_key` = ?", pub, priv)
	return
}

func (dao *Dao) SaveWebPushSubscription(endpoint, p256dh, auth string) (err error) {
	_, err = dao.db.Exec(
		"insert into `web_push_subscriptions` (`endpoint`, `p256dh`, `auth`, `created`) values (?, ?, ?, now()) "+
			"on duplicate key update `p256dh` = values(`p256dh`), `auth` = values(`auth`)",
		endpoint, p256dh, auth)
	return
}

// ListWebPushSubscriptions returns push.WebPushSubscription (not a type of
// its own) so *Dao satisfies push.Storage directly, without an adapter -
// see push's own package doc for why that interface exists at all (issue
// #62: the bridge needs to run this same sending logic against its own,
// unrelated storage).
func (dao *Dao) ListWebPushSubscriptions() (subs []*push.WebPushSubscription, err error) {
	rows, err := dao.db.Query("select `endpoint`, `p256dh`, `auth` from `web_push_subscriptions`")
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		sub := &push.WebPushSubscription{}
		if err = rows.Scan(&sub.Endpoint, &sub.P256dh, &sub.Auth); err != nil {
			return
		}
		subs = append(subs, sub)
	}
	return
}

// DeleteWebPushSubscription removes a subscription the push service reports
// as gone (HTTP 404/410) - the browser unsubscribed, or the endpoint expired.
func (dao *Dao) DeleteWebPushSubscription(endpoint string) (err error) {
	_, err = dao.db.Exec("delete from `web_push_subscriptions` where `endpoint` = ?", endpoint)
	return
}

func (dao *Dao) SaveApnsToken(token string) (err error) {
	_, err = dao.db.Exec(
		"insert into `apns_tokens` (`token`, `created`) values (?, now()) on duplicate key update `token` = values(`token`)",
		token)
	return
}

func (dao *Dao) ListApnsTokens() (tokens []string, err error) {
	rows, err := dao.db.Query("select `token` from `apns_tokens`")
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		var token string
		if err = rows.Scan(&token); err != nil {
			return
		}
		tokens = append(tokens, token)
	}
	return
}

// DeleteApnsToken removes a token APNs reports as no longer valid
// (BadDeviceToken/Unregistered) - the app was uninstalled, or the token
// rotated.
func (dao *Dao) DeleteApnsToken(token string) (err error) {
	_, err = dao.db.Exec("delete from `apns_tokens` where `token` = ?", token)
	return
}

func (dao *Dao) AddTags(file *pb.File, tags []imagestagger.RAMTag) {
	for _, tag := range tags {
		// Upsert against (hash, tag)'s unique key rather than a plain
		// insert: a file getting reprocessed (a fix to the tagger, a
		// forced re-tag) re-runs this for a hash that may already have
		// these exact tags, and a plain insert would just fail on the
		// second pass instead of refreshing the score.
		_, err := dao.db.Exec(
			"insert into `file_tags` (`hash`, `tag`, `score`) values (?, ?, ?) "+
				"on duplicate key update `score` = values(`score`)",
			file.Hash, tag.Name, tag.Score)

		if err != nil {
			log.Error("Error inserting tag:", err)
		}
	}
}

func (dao *Dao) StoreNewFile(file *pb.File) (duplicated bool, err error) {
	_, err = dao.db.Exec(
		"insert into `files` (`hash`, `mime`, `created`, `modified`, `path`, `size`) values (?, ?, ?, ?, ?, ?)",
		file.Hash, file.Mime, file.Created.AsTime(), file.Modified.AsTime(), file.Path, file.Size)

	if err != nil {
		if me, ok := err.(*mysql.MySQLError); ok && me.Number == 1062 {
			return true, nil
		}
	}

	return
}

func (dao *Dao) GetFileByHash(hash string) (file *pb.File, err error) {
	var created, modified time.Time
	log.Debug("Get file SQL:", hash)
	file = new(pb.File)
	err = dao.db.QueryRow(
		"select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `hash` = ?", hash).
		Scan(&file.Hash, &file.Mime, &created, &modified, &file.Path, &file.Size)

	file.Created = timestamppb.New(created)
	file.Modified = timestamppb.New(modified)

	return
}

func (dao *Dao) GetTags() (tags []string, err error) {
	rowsTags, err := dao.db.Query("select distinct(`tag`) as `tag_name` from `file_tags` order by `tag_name`")
	if err != nil {
		return nil, err
	}
	defer rowsTags.Close()
	for rowsTags.Next() {
		var tag string
		if err := rowsTags.Scan(&tag); err != nil {
			return nil, err
		}
		tags = append(tags, tag)
	}

	return
}

func (dao *Dao) GetFileByPath(path string) (file *pb.File, err error) {
	var created, modified time.Time
	log.Debug("Get file SQL:", path)
	file = new(pb.File)
	err = dao.db.QueryRow(
		"select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` = ?", path).
		Scan(&file.Hash, &file.Mime, &created, &modified, &file.Path, &file.Size)

	file.Created = timestamppb.New(created)
	file.Modified = timestamppb.New(modified)

	return
}

// DelFileByPath deletes the files row at path. Files are deduplicated on
// disk by hash (files_manager), so more than one path can share a hash —
// file_tags only gets cleaned up for that hash once this is the last path
// referencing it, and it all happens in one transaction so a delete either
// fully succeeds or leaves both tables untouched. Without this, deleting
// any tagged photo failed outright with a file_tags foreign-key violation
// (error 1451) the moment the RAM++ tagger had ever tagged it.
//
// `files` is the parent side of that foreign key, so file_tags has to be
// cleared (when this is the last reference to the hash) BEFORE deleting the
// files row, not after — deleting the parent first is exactly what MySQL's
// FK check rejects, no matter what cleanup happens afterward.
//
// The ref-count check below is a classic check-then-act, and the check
// alone isn't enough to make it safe once a connection's requests can run
// concurrently (see websocket.handleConnection on the device side): a
// batch delete of several duplicated files fires all of their DelFile
// calls at once now, and two of them targeting the same hash could each
// see "still >1 other reference" before either has deleted its own row,
// so neither cleans up file_tags — leaving zero files rows for that hash
// once both commit, while file_tags still references it, tripping this
// exact FK error on whichever commits last. `for update` locks every
// files row sharing this hash for the rest of this transaction, so a
// second transaction doing the same check for the same hash blocks until
// the first commits (and by then correctly recounts one fewer reference)
// instead of racing it.
func (dao *Dao) DelFileByPath(path string) (err error) {
	log.Debug("Del file SQL:", path)

	tx, err := dao.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var hash string
	if err = tx.QueryRow("select `hash` from `files` where `path` = ?", path).Scan(&hash); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}

	var refCount int
	if err = tx.QueryRow("select count(*) from `files` where `hash` = ? for update", hash).Scan(&refCount); err != nil {
		return err
	}
	if refCount <= 1 {
		if _, err = tx.Exec("delete from `file_tags` where `hash` = ?", hash); err != nil {
			return err
		}
	}

	if _, err = tx.Exec("delete from `files` where `path` = ?", path); err != nil {
		return err
	}

	return tx.Commit()
}

func (dao *Dao) GetFilesByPath(path string, recursive bool, imagesOnly bool) (files []*pb.File, err error) {
	log.Debug("Get Files by path initial:", path, recursive)
	if !recursive {
		pathFiles := "^" + path + "[^/]+$"

		// We add first the sub-directories that are actually subpaths of the existing files
		slashesInPath := strings.Count(path, "/")
		rowsDirs, err := dao.db.Query("select distinct(SUBSTRING_INDEX(path, '/', ?+1)) as path from files WHERE path LIKE ? and path not regexp ? order by `created` desc", slashesInPath, path+"%", pathFiles)
		if err != nil {
			return nil, err
		}
		defer rowsDirs.Close()
		for rowsDirs.Next() {
			file := &pb.File{
				Mime: "inode/directory",
			}
			if err := rowsDirs.Scan(&file.Path); err != nil {
				return nil, err
			}
			log.Debug("Slashes:", file.Path, strings.Count(file.Path, "/"), slashesInPath)
			if strings.Count(file.Path, "/") != slashesInPath {
				continue
			}
			files = append(files, file)
		}
		path = pathFiles
	} else {
		path = "^" + path
	}

	extrImgs := ""
	if imagesOnly {
		extrImgs = " and `mime` like 'image%' "
	}

	searchStr := "select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` regexp ? " + extrImgs + " order by `created` desc"
	log.Debug("Get Files by path:", path, searchStr)
	rows, err := dao.db.Query(searchStr, path)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		file := new(pb.File)
		var created, modified time.Time
		if err := rows.Scan(&file.Hash, &file.Mime, &created, &modified, &file.Path, &file.Size); err != nil {
			return nil, err
		}
		file.Created = timestamppb.New(created)
		file.Modified = timestamppb.New(modified)
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return
}

func (dao *Dao) GetProfile() (name, text string, image []byte, err error) {
	err = dao.db.QueryRow("select `name`, `image`, `text` from `profile`").Scan(&name, &image, &text)

	return
}

func (dao *Dao) UpdateProfile(name, text string, image []byte) (err error) {
	_, err = dao.db.Exec("update `profile` set `name` = ?, `image` = ?, `text` = ?", name, image, text)

	return
}

func (dao *Dao) InsertSharedLink(pathUuid string, size int) (err error) {
	log.Debug("Creating SharedLink")
	_, err = dao.db.Exec("insert into `shared_links` (`uuid`, `size`, `created`) values (?, ?, now())", pathUuid, size)

	return
}

// GetSharedLinkCreated returns the creation time of a shared link, so
// callers can decide whether it has expired. Returns sql.ErrNoRows if the
// link doesn't exist (already expired and swept, or never existed).
func (dao *Dao) GetSharedLinkCreated(pathUuid string) (created time.Time, err error) {
	err = dao.db.QueryRow("select `created` from `shared_links` where `uuid` = ?", pathUuid).Scan(&created)

	return
}

// GetExpiredSharedLinkUuids returns the uuids of every shared link created
// before cutoff, so the caller can remove their on-disk content and delete
// the rows.
func (dao *Dao) GetExpiredSharedLinkUuids(cutoff time.Time) (uuids []string, err error) {
	rows, err := dao.db.Query("select `uuid` from `shared_links` where `created` < ?", cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var uuid string
		if err = rows.Scan(&uuid); err != nil {
			return nil, err
		}
		uuids = append(uuids, uuid)
	}

	return uuids, rows.Err()
}

// DeleteSharedLink removes a shared link row, once its on-disk content has
// been removed.
func (dao *Dao) DeleteSharedLink(pathUuid string) (err error) {
	_, err = dao.db.Exec("delete from `shared_links` where `uuid` = ?", pathUuid)

	return
}

func (dao *Dao) UpdateLatestSync(domain string, latestSync *timestamppb.Timestamp) (err error) {
	_, err = dao.db.Exec("update `social_friendship` set `latest_sync` = ? where `domain` = ?", latestSync.AsTime(), domain)
	return
}

func (dao *Dao) NewSocialPublication(pubUuid, text, originDomain string, ownPublication bool, files []*pb.File) (err error) {
	log.Debug("Creating SocialPublication")
	_, err = dao.db.Exec("insert into `social_publications` (`uuid`, `dt`, `text`, `own_publication`, `friend_domain`) values (?, now(), ?, ?, ?)", pubUuid, text, ownPublication, originDomain)
	if err != nil {
		log.Debug("Error trying to create a new social publicaton", err)
		return
	}

	for i, file := range files {
		log.Debug("Inserting file in publication", file.Hash)
		_, err = dao.db.Exec(
			"insert into `social_publications_files` (`pos`, `uuid`, `hash`, `mime`, `created`, `modified`, `size`) values (?, ?, ?, ?, ?, ?, ?)",
			i, pubUuid, file.Hash, file.Mime, file.Created.AsTime(), file.Modified.AsTime(), file.Size)
		if err != nil {
			return
		}
	}

	return
}

func (dao *Dao) NewLikePublication(uuid, pubUuid string, friendDomain string) (err error) {
	log.Debug("Creating New LikePublication:", uuid, "PubUUID:", pubUuid, friendDomain)
	_, err = dao.db.Exec("insert into `social_publication_likes` (`uuid`, `pub_uuid`, `dt`, `friend_domain`) values (?, ?, now(), ?)", uuid, pubUuid, friendDomain)
	if err != nil {
		log.Error("Error trying to create a new like publication", err)
		return
	}
	_, err = dao.db.Exec("update `social_publications` set `likes` = `likes` + 1 where `uuid` = ?", pubUuid)
	return
}

// HasLikedPublication reports whether likerDomain has already liked pubUuid.
func (dao *Dao) HasLikedPublication(pubUuid, likerDomain string) (liked bool, err error) {
	var exists int
	err = dao.db.QueryRow("select 1 from `social_publication_likes` where `pub_uuid` = ? and `friend_domain` = ? limit 1", pubUuid, likerDomain).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	return true, nil
}

// DeleteLikePublication removes likerDomain's like of pubUuid, if any, and
// decrements the publication's like counter to match. A no-op (nil error)
// if likerDomain hadn't liked it.
func (dao *Dao) DeleteLikePublication(pubUuid, likerDomain string) (err error) {
	res, err := dao.db.Exec("delete from `social_publication_likes` where `pub_uuid` = ? and `friend_domain` = ?", pubUuid, likerDomain)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}

	_, err = dao.db.Exec("update `social_publications` set `likes` = `likes` - 1 where `uuid` = ? and `likes` > 0", pubUuid)
	return
}

func (dao *Dao) GetEvents(since time.Time, total int32) (events []*pb.Event, err error) {
	log.Debug("Get Events")
	rows, err := dao.db.Query("select `uuid`, `dt`, `type`, `content` from `events` where `dt` > ? order by `dt` asc limit ?", since, total)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events = []*pb.Event{}
	for rows.Next() {
		event := &pb.Event{}
		var dt time.Time
		if err := rows.Scan(&event.Uuid, &dt, &event.Type, &event.Content); err != nil {
			return nil, err
		}
		event.Dt = timestamppb.New(dt)
		events = append(events, event)
	}

	return
}

func (dao *Dao) NewLikePublicationComment(uuid, commentUuid string, friendDomain string) (err error) {
	log.Debug("Creating New PublicationComment Like", uuid, commentUuid, friendDomain)
	_, err = dao.db.Exec("insert into `social_publication_comment_likes` (`uuid`, `comment_uuid`, `dt`, `friend_domain`) values (?, ?, now(), ?)", uuid, commentUuid, friendDomain)
	if err != nil {
		log.Error("Error trying to create a new like publication", err)
		return
	}
	_, err = dao.db.Exec("update `social_publications_comments` set `likes` = `likes` + 1 where `uuid` = ?", commentUuid)
	return
}

// HasLikedComment reports whether likerDomain has already liked commentUuid.
func (dao *Dao) HasLikedComment(commentUuid, likerDomain string) (liked bool, err error) {
	var exists int
	err = dao.db.QueryRow("select 1 from `social_publication_comment_likes` where `comment_uuid` = ? and `friend_domain` = ? limit 1", commentUuid, likerDomain).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	return true, nil
}

// DeleteLikePublicationComment removes likerDomain's like of commentUuid, if
// any, and decrements the comment's like counter to match. A no-op (nil
// error) if likerDomain hadn't liked it.
func (dao *Dao) DeleteLikePublicationComment(commentUuid, likerDomain string) (err error) {
	res, err := dao.db.Exec("delete from `social_publication_comment_likes` where `comment_uuid` = ? and `friend_domain` = ?", commentUuid, likerDomain)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}

	_, err = dao.db.Exec("update `social_publications_comments` set `likes` = `likes` - 1 where `uuid` = ? and `likes` > 0", commentUuid)
	return
}

// GetPublicationLikerDomains lists who liked pubUuid, most recent first
// (issue #29). Each entry is either the owner's own domain (a self-like) or
// a friend's domain — callers resolve those to a display name/photo.
func (dao *Dao) GetPublicationLikerDomains(pubUuid string) (domains []string, err error) {
	rows, err := dao.db.Query("select `friend_domain` from `social_publication_likes` where `pub_uuid` = ? order by `dt` desc", pubUuid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	domains = []string{}
	for rows.Next() {
		var domain string
		if err := rows.Scan(&domain); err != nil {
			return nil, err
		}
		domains = append(domains, domain)
	}
	return
}

// GetCommentLikerDomains lists who liked commentUuid, most recent first
// (issue #29). See GetPublicationLikerDomains.
func (dao *Dao) GetCommentLikerDomains(commentUuid string) (domains []string, err error) {
	rows, err := dao.db.Query("select `friend_domain` from `social_publication_comment_likes` where `comment_uuid` = ? order by `dt` desc", commentUuid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	domains = []string{}
	for rows.Next() {
		var domain string
		if err := rows.Scan(&domain); err != nil {
			return nil, err
		}
		domains = append(domains, domain)
	}
	return
}

func (dao *Dao) GetSocialPublicationComments(pubUuid, viewerDomain string) (comments []*pb.Comment, err error) {
	log.Debug("Get SocialPublication Comments")
	rowComms, err := dao.db.Query("select `uuid`, `dt`, `comment`, `publisher_name`, `likes` from `social_publications_comments` where `pub_uuid` = ? order by `dt` desc", pubUuid)
	if err != nil {
		return nil, err
	}
	defer rowComms.Close()
	comments = []*pb.Comment{}
	for rowComms.Next() {
		comment := &pb.Comment{
			PubUuid: pubUuid,
		}
		var dt time.Time
		if err := rowComms.Scan(&comment.CommentUuid, &dt, &comment.Comment, &comment.Publisher, &comment.Likes); err != nil {
			return nil, err
		}

		comment.DateTime = timestamppb.New(dt)
		if comment.Liked, err = dao.HasLikedComment(comment.CommentUuid, viewerDomain); err != nil {
			return nil, err
		}
		comments = append(comments, comment)
	}

	return
}

func (dao *Dao) GetSocialPublicationFiles(uuid string) (files []*pb.File, err error) {
	// TODO: Populate owner and other stuff
	rowFiles, err := dao.db.Query("select `hash`, `mime`, `created`, `modified`, `size` from `social_publications_files` where `uuid` = ? order by `pos`", uuid)
	if err != nil {
		return nil, err
	}
	defer rowFiles.Close()
	for rowFiles.Next() {
		spFile := new(pb.File)
		var created, modified time.Time
		if err := rowFiles.Scan(&spFile.Hash, &spFile.Mime, &created, &modified, &spFile.Size); err != nil {
			return nil, err
		}
		spFile.Created = timestamppb.New(created)
		spFile.Modified = timestamppb.New(modified)
		files = append(files, spFile)
	}

	return
}

func (dao *Dao) GetSocialPublications(since time.Time, total int32, ownOnly bool, exclude []string, prName, prText string, prImage []byte, viewerDomain string) (pubs *pb.SocialPublications, err error) {
	log.Debug("Get SocialPublications")
	if len(exclude) == 0 {
		exclude = []string{""}
	}
	exPh := strings.Repeat("?,", len(exclude))
	args := make([]any, len(exclude)+1)
	for i := 0; i < len(exclude); i++ {
		args[i] = exclude[i]
	}
	args[len(exclude)] = total
	ownClaus := ""
	if ownOnly {
		ownClaus = " and `own_publication` = true "
	}
	log.Debug("select `friend_domain`, `uuid`, `dt`, `text`, `own_publication`, `likes` from `social_publications` where uuid not in ("+exPh[:len(exPh)-1]+") "+ownClaus+" order by `dt` desc limit ?", args)
	rowPubs, err := dao.db.Query("select `friend_domain`, `uuid`, `dt`, `text`, `own_publication`, `likes` from `social_publications` where uuid not in ("+exPh[:len(exPh)-1]+") "+ownClaus+" order by `dt` desc limit ?", args...)
	if err != nil {
		return nil, err
	}
	defer rowPubs.Close()
	pubs = &pb.SocialPublications{
		Publications: []*pb.SocialPublication{},
	}
	for rowPubs.Next() {
		sp := new(pb.SocialPublication)
		var dt time.Time
		var friendDomain string
		var ownPub bool
		if err := rowPubs.Scan(&friendDomain, &sp.Uuid, &dt, &sp.Text, &ownPub, &sp.Likes); err != nil {
			return nil, err
		}
		// issues #34/#35: let clients know when to offer delete-post /
		// delete-any-comment actions.
		sp.Own = ownPub
		sp.DateTime = timestamppb.New(dt)

		if ownPub {
			log.Debug("Own publication populating own data")
			sp.Publisher = &pb.Profile{
				Name:  prName,
				Image: prImage,
				Text:  prText,
			}
		} else {
			_, name, text, image, _, err := dao.getFriendshipByDomain(friendDomain)
			if err != nil {
				log.Error("Error trying to retreive friend profile")
				continue
			}
			// Get friend profile
			sp.Publisher = &pb.Profile{
				Domain: friendDomain,
				Name:   name,
				Image:  image,
				Text:   text,
			}
		}

		// TODO: Populate owner and other stuff
		files, err := dao.GetSocialPublicationFiles(sp.Uuid)
		if err != nil {
			log.Error("Error trying to retrieve publication files")
			continue
		}
		sp.Files = files

		if sp.Liked, err = dao.HasLikedPublication(sp.Uuid, viewerDomain); err != nil {
			log.Error("Error trying to check like state for publication")
			continue
		}

		pubs.Since = timestamppb.New(dt)
		pubs.Publications = append(pubs.Publications, sp)
	}

	log.Debug("Publications to return:", len(pubs.Publications))

	return
}

// GetSocialPublicationByUUID is GetSocialPublications' single-row
// equivalent (issue #78): tapping a notification for a post that isn't
// among whatever page of the feed happens to be loaded needs to fetch just
// that one post directly, rather than paging through everything since it.
// Mirrors that function's own row-to-proto mapping exactly, just against
// one row instead of a result set.
func (dao *Dao) GetSocialPublicationByUUID(pubUuid, prName, prText string, prImage []byte, viewerDomain string) (sp *pb.SocialPublication, err error) {
	sp = new(pb.SocialPublication)
	var dt time.Time
	var friendDomain string
	var ownPub bool
	err = dao.db.QueryRow(
		"select `friend_domain`, `uuid`, `dt`, `text`, `own_publication`, `likes` from `social_publications` where `uuid` = ?",
		pubUuid,
	).Scan(&friendDomain, &sp.Uuid, &dt, &sp.Text, &ownPub, &sp.Likes)
	if err != nil {
		return nil, err
	}

	sp.Own = ownPub
	sp.DateTime = timestamppb.New(dt)

	if ownPub {
		sp.Publisher = &pb.Profile{
			Name:  prName,
			Image: prImage,
			Text:  prText,
		}
	} else {
		_, name, text, image, _, err := dao.getFriendshipByDomain(friendDomain)
		if err != nil {
			return nil, err
		}
		sp.Publisher = &pb.Profile{
			Domain: friendDomain,
			Name:   name,
			Image:  image,
			Text:   text,
		}
	}

	if sp.Files, err = dao.GetSocialPublicationFiles(sp.Uuid); err != nil {
		return nil, err
	}
	if sp.Liked, err = dao.HasLikedPublication(sp.Uuid, viewerDomain); err != nil {
		return nil, err
	}

	return sp, nil
}

func (dao *Dao) NewFriendship(domain, secret, name, text string, image []byte, sent bool) (err error) {
	log.Debug("Creating new friendship")
	_, err = dao.db.Exec("insert into `social_friendship` (`domain`, `status`, `name`, `image`, `text`, `secret`, `sent`) values (?, 'pending', ?, ?, ?, ?, ?)", domain, name, image, text, secret, sent)

	return err
}

func (dao *Dao) GetFriendship(domain, secret string) (status, name, text string, image []byte, sent bool, err error) {
	err = dao.db.QueryRow("select `status`, `name`, `image`, `text`, `sent` from `social_friendship` where `domain` = ? and `secret` = ?", domain, secret).Scan(&status, &name, &image, &text, &sent)

	return
}

func (dao *Dao) getFriendshipByDomain(domain string) (status, name, text string, image []byte, sent bool, err error) {
	err = dao.db.QueryRow("select `status`, `name`, `image`, `text`, `sent` from `social_friendship` where `domain` = ?", domain).Scan(&status, &name, &image, &text, &sent)

	return
}

// GetFriendProfile is the exported equivalent of getFriendshipByDomain for
// callers outside this package that just want the cached name/bio/photo for
// a friend's domain (issue #29's "who liked this" resolves liker domains
// this way).
func (dao *Dao) GetFriendProfile(domain string) (name, text string, image []byte, err error) {
	_, name, text, image, _, err = dao.getFriendshipByDomain(domain)
	return
}

func (dao *Dao) GetFriendships() (friendships []*pb.Friendship, err error) {
	rowFriendships, err := dao.db.Query("select `status`, `name`, `image`, `text`, `sent`, `domain`, `secret`, `latest_sync` from `social_friendship`")
	if err != nil {
		return nil, err
	}
	defer rowFriendships.Close()
	for rowFriendships.Next() {
		friendship := &pb.Friendship{
			OriginProfile: new(pb.Profile),
		}
		var status string
		var latestSync sql.NullTime
		if err := rowFriendships.Scan(&status, &friendship.OriginProfile.Name, &friendship.OriginProfile.Image, &friendship.OriginProfile.Text, &friendship.Sent, &friendship.OriginProfile.Domain, &friendship.Secret, &latestSync); err != nil {
			return nil, err
		}
		if latestSync.Valid {
			friendship.LatestSync = timestamppb.New(latestSync.Time)
		}
		friendship.Status = dao.statusToPb(status)

		friendships = append(friendships, friendship)
	}

	return
}

func (dao *Dao) statusToPb(status string) (pbStatus pb.FriendShipStatus) {
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

func (dao *Dao) pbToStatus(pbStatus pb.FriendShipStatus) (status string) {
	switch pbStatus {
	case pb.FriendShipStatus_Pending:
		return "pending"
	case pb.FriendShipStatus_Accepted:
		return "accepted"
	case pb.FriendShipStatus_Blocked:
		return "blocked"
	}

	return
}

func (dao *Dao) NewComment(commentUuid, pubName, pubUuid, comment string, ownComment bool) (err error) {
	log.Debug("Creating new comment")
	_, err = dao.db.Exec(
		"insert into `social_publications_comments` (`uuid`, `pub_uuid`, `dt`, `comment`, `publisher_name`, `own_comment`) values (?, ?, now(), ?, ?, ?)",
		commentUuid, pubUuid, comment, pubName, ownComment)

	return err
}

// IsOwnComment reports whether commentUuid is one the device owner wrote,
// as opposed to one synced in from a friend (issue #43 follow-up: whether
// a like on it is worth notifying the owner about).
func (dao *Dao) IsOwnComment(commentUuid string) (own bool, err error) {
	err = dao.db.QueryRow("select `own_comment` from `social_publications_comments` where `uuid` = ?", commentUuid).Scan(&own)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return
}

// IsOwnPublication reports whether pubUuid is one of the device owner's own
// publications, as opposed to one synced in from a friend (issue #34/#35:
// deleting a post, or any comment on it, is restricted to the owner).
func (dao *Dao) IsOwnPublication(pubUuid string) (own bool, err error) {
	err = dao.db.QueryRow("select `own_publication` from `social_publications` where `uuid` = ?", pubUuid).Scan(&own)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return
}

// GetCommentPubUuid looks up which publication a comment belongs to, so
// callers can check that publication's own_publication flag before
// allowing a delete (issue #35).
func (dao *Dao) GetCommentPubUuid(commentUuid string) (pubUuid string, err error) {
	err = dao.db.QueryRow("select `pub_uuid` from `social_publications_comments` where `uuid` = ?", commentUuid).Scan(&pubUuid)
	return
}

// DeleteSocialPublication removes pubUuid and everything attached to it —
// its files, comments, comment likes, and publication likes — in one
// transaction (issue #34). Children have to go before the parent, same
// reasoning as DelFileByPath above: social_publications is the referenced
// side of several foreign keys.
func (dao *Dao) DeleteSocialPublication(pubUuid string) (err error) {
	tx, err := dao.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err = tx.Exec(
		"delete from `social_publication_comment_likes` where `comment_uuid` in (select `uuid` from `social_publications_comments` where `pub_uuid` = ?)",
		pubUuid,
	); err != nil {
		return err
	}
	if _, err = tx.Exec("delete from `social_publications_comments` where `pub_uuid` = ?", pubUuid); err != nil {
		return err
	}
	if _, err = tx.Exec("delete from `social_publication_likes` where `pub_uuid` = ?", pubUuid); err != nil {
		return err
	}
	if _, err = tx.Exec("delete from `social_publications_files` where `uuid` = ?", pubUuid); err != nil {
		return err
	}
	if _, err = tx.Exec("delete from `social_publications` where `uuid` = ?", pubUuid); err != nil {
		return err
	}

	return tx.Commit()
}

// DeleteSocialComment removes commentUuid and its likes (issue #35).
func (dao *Dao) DeleteSocialComment(commentUuid string) (err error) {
	tx, err := dao.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err = tx.Exec("delete from `social_publication_comment_likes` where `comment_uuid` = ?", commentUuid); err != nil {
		return err
	}
	if _, err = tx.Exec("delete from `social_publications_comments` where `uuid` = ?", commentUuid); err != nil {
		return err
	}

	return tx.Commit()
}

func (dao *Dao) ChangeFriendStatus(domain string, status pb.FriendShipStatus) (err error) {
	_, err = dao.db.Exec("update `social_friendship` set `status` = ? where `domain` = ?", dao.pbToStatus(status), domain)
	return
}

// UpdateFriendshipProfile refreshes the locally cached snapshot of a
// friend's name/image/bio (issue #26): the friendship row stores a copy of
// the friend's profile taken when the request was accepted, and it's never
// touched again on its own, so a friend renaming themselves or changing
// their photo would otherwise never show up here.
func (dao *Dao) UpdateFriendshipProfile(domain, name, text string, image []byte) (err error) {
	_, err = dao.db.Exec("update `social_friendship` set `name` = ?, `text` = ?, `image` = ? where `domain` = ?", name, text, image, domain)
	return
}

func (dao *Dao) NewEvent(eventType string, data []byte) (err error) {
	log.Debug("Creating new event", eventType, data)
	_, err = dao.db.Exec("insert into `events` (`uuid`, `dt`, `type`, `content`) values (?, now(), ?, ?)", uuid.New(), eventType, data)

	return err
}

// notificationTypeToStr/strToNotificationType mirror pbToStatus/
// statusToPb's own explicit-switch style above, rather than relying on the
// generated enum's String() (whose output isn't a contract this table's
// stored values should be tied to).
func (dao *Dao) notificationTypeToStr(t pb.NotificationType) string {
	switch t {
	case pb.NotificationType_NotificationLikePublication:
		return "LikePublication"
	case pb.NotificationType_NotificationLikeComment:
		return "LikeComment"
	case pb.NotificationType_NotificationNewComment:
		return "NewComment"
	case pb.NotificationType_NotificationFriendRequest:
		return "FriendRequest"
	case pb.NotificationType_NotificationFriendAccepted:
		return "FriendAccepted"
	}
	return ""
}

func (dao *Dao) strToNotificationType(s string) pb.NotificationType {
	switch s {
	case "LikePublication":
		return pb.NotificationType_NotificationLikePublication
	case "LikeComment":
		return pb.NotificationType_NotificationLikeComment
	case "NewComment":
		return pb.NotificationType_NotificationNewComment
	case "FriendRequest":
		return pb.NotificationType_NotificationFriendRequest
	case "FriendAccepted":
		return pb.NotificationType_NotificationFriendAccepted
	}
	return pb.NotificationType_NotificationLikePublication
}

// NewNotification (issue #78) records one row in the owner-facing
// notification timeline - see notifications' own doc comment in db.sql for
// why this is a distinct thing from NewEvent's write-log above. pubUuid/
// commentUuid are passed through as-is (empty string when not applicable
// to notifType) rather than as *string, matching this file's existing
// convention of plain strings for optional-but-usually-present columns
// (e.g. GetCommentPubUuid's own pubUuid return).
func (dao *Dao) NewNotification(notifType pb.NotificationType, actorName, actorDomain, pubUuid, commentUuid string) (err error) {
	_, err = dao.db.Exec(
		"insert into `notifications` (`uuid`, `dt`, `type`, `actor_name`, `actor_domain`, `pub_uuid`, `comment_uuid`) values (?, now(), ?, ?, ?, ?, ?)",
		uuid.New(), dao.notificationTypeToStr(notifType), actorName, actorDomain,
		sql.NullString{String: pubUuid, Valid: pubUuid != ""},
		sql.NullString{String: commentUuid, Valid: commentUuid != ""},
	)
	return err
}

// ListNotifications returns the most recent notifications, newest first.
func (dao *Dao) ListNotifications(limit int) (notifications []*pb.Notification, err error) {
	rows, err := dao.db.Query(
		"select `uuid`, `dt`, `type`, `actor_name`, `actor_domain`, `pub_uuid`, `comment_uuid`, `acknowledged` from `notifications` order by `dt` desc limit ?",
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	notifications = []*pb.Notification{}
	for rows.Next() {
		n := new(pb.Notification)
		var dt time.Time
		var typeStr string
		var pubUuid, commentUuid sql.NullString
		if err := rows.Scan(&n.Uuid, &dt, &typeStr, &n.ActorName, &n.ActorDomain, &pubUuid, &commentUuid, &n.Acknowledged); err != nil {
			return nil, err
		}
		n.Dt = timestamppb.New(dt)
		n.Type = dao.strToNotificationType(typeStr)
		n.PubUuid = pubUuid.String
		n.CommentUuid = commentUuid.String
		notifications = append(notifications, n)
	}
	return notifications, rows.Err()
}

// UnacknowledgedNotificationCount backs the bell icon's badge/highlight.
func (dao *Dao) UnacknowledgedNotificationCount() (count int, err error) {
	err = dao.db.QueryRow("select count(*) from `notifications` where `acknowledged` = 0").Scan(&count)
	return
}

// MarkAllNotificationsAcknowledged is called the moment the owner opens the
// notifications panel/sheet (issue #78: "when the user opens the section
// all the notifications will change to acknowledged") - a single bulk
// update rather than acknowledging one at a time, since the whole list is
// always shown at once.
func (dao *Dao) MarkAllNotificationsAcknowledged() (err error) {
	_, err = dao.db.Exec("update `notifications` set `acknowledged` = 1 where `acknowledged` = 0")
	return
}

// GetFaceRecognitionEnabled/SetFaceRecognitionEnabled back issue #52's
// settings toggle - see db.sql's own doc comment on `settings.face_
// recognition_enabled` for why enabling it never retroactively processes
// anything already uploaded.
func (dao *Dao) GetFaceRecognitionEnabled() (enabled bool, err error) {
	err = dao.db.QueryRow("select `face_recognition_enabled` from `settings`").Scan(&enabled)
	return
}

func (dao *Dao) SetFaceRecognitionEnabled(enabled bool) (err error) {
	_, err = dao.db.Exec("update `settings` set `face_recognition_enabled` = ?", enabled)
	return
}

// CreatePerson inserts a brand new, still-unnamed person (issue #52) -
// every detected face either matches an existing one (see
// ListFaceEmbeddings) or gets one of these created for it.
func (dao *Dao) CreatePerson(id string) (err error) {
	_, err = dao.db.Exec("insert into `people` (`id`, `name`, `created`) values (?, '', now())", id)
	return
}

// SetPersonCoverFace records which of a person's faces is their current
// medoid (see face_recognition.MedoidAndCohesion) plus that medoid's
// cohesion score - both recomputed by files_manager.processFaces every
// time a new face is added to them, so ListPeople's cover thumbnail can
// only get more representative over time, and its ordering (see
// ListPeople's own doc comment) can tell a real, recurring person from
// faces chained together by nearest-neighbor matching.
func (dao *Dao) SetPersonCoverFace(personID, faceID string, cohesion float32) (err error) {
	_, err = dao.db.Exec("update `people` set `cover_face_id` = ?, `cohesion` = ? where `id` = ?", faceID, cohesion, personID)
	return
}

// AddFace stores one detected face (issue #52), already resolved to
// whichever person it was matched to (see CreatePerson). embedding is the
// raw feature vector, encoded via face_recognition.EncodeEmbedding -
// opaque to this layer, never interpreted here.
func (dao *Dao) AddFace(id, hash, personID string, x, y, w, h int, embedding, thumbnail []byte) (err error) {
	_, err = dao.db.Exec(
		"insert into `faces` (`id`, `hash`, `person_id`, `bbox_x`, `bbox_y`, `bbox_w`, `bbox_h`, `embedding`, `thumbnail`, `created`) "+
			"values (?, ?, ?, ?, ?, ?, ?, ?, ?, now())",
		id, hash, personID, x, y, w, h, embedding, thumbnail)
	return
}

// RawFace is one face row's id plus its two encrypted-at-rest blobs, for
// files_manager.MigrateLegacyFaceEncryption - see that function's doc
// comment for why this one-off read/rewrite exists.
type RawFace struct {
	ID        string
	Embedding []byte
	Thumbnail []byte
}

// ListRawFaces returns every stored face's id/embedding/thumbnail, opaque
// to this layer - see RawFace.
func (dao *Dao) ListRawFaces() (faces []RawFace, err error) {
	rows, err := dao.db.Query("select `id`, `embedding`, `thumbnail` from `faces`")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var f RawFace
		if err := rows.Scan(&f.ID, &f.Embedding, &f.Thumbnail); err != nil {
			return nil, err
		}
		faces = append(faces, f)
	}
	return faces, rows.Err()
}

// UpdateFaceEncryption overwrites a face row's embedding/thumbnail in
// place - the write side of MigrateLegacyFaceEncryption's re-encryption
// pass, never called from the normal detect-and-store path (AddFace owns
// that).
func (dao *Dao) UpdateFaceEncryption(id string, embedding, thumbnail []byte) (err error) {
	_, err = dao.db.Exec("update `faces` set `embedding` = ?, `thumbnail` = ? where `id` = ?", embedding, thumbnail, id)
	return
}

// FaceEmbedding is one stored face's identity, for matching a newly
// detected face against (issue #52) - see ListFaceEmbeddings. ID is also
// used to recompute a person's medoid cover face whenever a new one is
// added (see files_manager.processFaces and face_recognition.MedoidFaceID).
type FaceEmbedding struct {
	ID        string
	PersonID  string
	Embedding []byte
}

// ListFaceEmbeddings returns every stored face's id/person/raw embedding -
// the full set a newly detected face gets compared against (see
// face_recognition.CosineSimilarity/IsSamePersonScore) to decide whether
// it matches an existing person or needs a new one. A full-table read
// rather than anything indexed/ANN-based: a personal photo library's face
// count is small enough (thousands, not millions) for a linear scan on
// every new upload to be trivial, and it's more accurate than maintaining
// per-person centroids would be (a person's different angles/lighting/
// expressions are better matched against their nearest individual face
// than an average of all of them).
func (dao *Dao) ListFaceEmbeddings() (faces []FaceEmbedding, err error) {
	rows, err := dao.db.Query("select `id`, `person_id`, `embedding` from `faces`")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var f FaceEmbedding
		if err := rows.Scan(&f.ID, &f.PersonID, &f.Embedding); err != nil {
			return nil, err
		}
		faces = append(faces, f)
	}
	return faces, rows.Err()
}

// ListPeople returns every recognized person (issue #52), legitimate and
// well-photographed people first, each with its face count and one
// representative face thumbnail - their medoid face (see
// face_recognition.MedoidAndCohesion's own doc comment: the most
// "typical" example of their face already on file, kept up to date
// incrementally by files_manager.processFaces) once one's been computed,
// falling back to their oldest detected face for a person added before
// this existed.
//
// Ordering is (1) cohesion at or above cohesionThreshold (or not yet
// computed - a brand new person, or one that predates this column) ahead
// of anyone below it, then (2) most-photographed first within each group
// (issue #75), then (3) newest first as a final tie-break. The cohesion
// split exists because raw face count alone isn't a legitimacy signal:
// matchOrNewPerson clusters by nearest-neighbor, so a person can rack up
// a high count purely by chaining through marginal matches - reproduced
// live as one someone renamed "Nope" (a mix of unrelated junk, one frame
// literally a dog) out-ranking real, recurring people under a face-count-
// only sort. cohesionThreshold is a parameter (not a dao-owned constant)
// so this low-level, CGO/OpenCV-free-by-design package doesn't need to
// import face_recognition itself - pass face_recognition.
// SamePersonThreshold. See face_recognition.MedoidAndCohesion for the
// full mechanics.
func (dao *Dao) ListPeople(cohesionThreshold float64) (people []*pb.Person, err error) {
	rows, err := dao.db.Query(
		"select `p`.`id`, `p`.`name`, count(`f`.`id`) as `face_count`, `p`.`cover_face_id` "+
			"from `people` as `p` left join `faces` as `f` on `f`.`person_id` = `p`.`id` "+
			"group by `p`.`id`, `p`.`name`, `p`.`cover_face_id`, `p`.`cohesion` "+
			"order by (`p`.`cohesion` is null or `p`.`cohesion` >= ?) desc, `face_count` desc, `p`.`created` desc",
		cohesionThreshold)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// coverFaceIDs[i] pairs with people[i] - kept alongside rather than on
	// pb.Person itself, since cover_face_id is server-side bookkeeping a
	// client never needs to see.
	var coverFaceIDs []sql.NullString
	for rows.Next() {
		p := new(pb.Person)
		var coverFaceID sql.NullString
		if err := rows.Scan(&p.Id, &p.Name, &p.FaceCount, &coverFaceID); err != nil {
			return nil, err
		}
		people = append(people, p)
		coverFaceIDs = append(coverFaceIDs, coverFaceID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i, p := range people {
		var thumb []byte
		if coverFaceIDs[i].Valid && coverFaceIDs[i].String != "" {
			err = dao.db.QueryRow("select `thumbnail` from `faces` where `id` = ?", coverFaceIDs[i].String).Scan(&thumb)
		} else {
			err = dao.db.QueryRow("select `thumbnail` from `faces` where `person_id` = ? order by `created` asc limit 1", p.Id).Scan(&thumb)
		}
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		p.CoverThumbnail = thumb
	}

	return people, nil
}

// RenamePerson also *creates* the name for a still-unnamed person - see
// the RenamePerson proto message's own doc comment.
func (dao *Dao) RenamePerson(id, name string) (err error) {
	_, err = dao.db.Exec("update `people` set `name` = ? where `id` = ?", name, id)
	return
}

// DeletePerson removes this person and every face detection matched to
// them (issue #52: "just click on delete the individual") - see the
// `faces` table's own doc comment in db.sql for why both are deleted
// together rather than leaving orphaned face rows behind.
func (dao *Dao) DeletePerson(id string) (err error) {
	tx, err := dao.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err = tx.Exec("delete from `faces` where `person_id` = ?", id); err != nil {
		return fmt.Errorf("deleting person's faces: %w", err)
	}
	if _, err = tx.Exec("delete from `people` where `id` = ?", id); err != nil {
		return fmt.Errorf("deleting person: %w", err)
	}
	return tx.Commit()
}

// MergePeople folds every sourceIDs person into targetID (issue #74) - see
// the MergePeople proto message's own doc comment for why this exists and
// what it does to targetID vs. the sources. targetID itself is filtered
// out of sourceIDs (merging a person into themselves is a no-op, not an
// error) so a client doesn't need to de-duplicate before calling this.
func (dao *Dao) MergePeople(targetID string, sourceIDs []string) (err error) {
	ids := make([]string, 0, len(sourceIDs))
	for _, id := range sourceIDs {
		if id != targetID {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}

	tx, err := dao.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	ph := strings.Repeat("?,", len(ids))
	ph = ph[:len(ph)-1]
	args := make([]any, 0, len(ids)+1)
	args = append(args, targetID)
	for _, id := range ids {
		args = append(args, id)
	}

	if _, err = tx.Exec("update `faces` set `person_id` = ? where `person_id` in ("+ph+")", args...); err != nil {
		return fmt.Errorf("reassigning merged faces: %w", err)
	}
	if _, err = tx.Exec("delete from `people` where `id` in ("+ph+")", args[1:]...); err != nil {
		return fmt.Errorf("deleting merged people: %w", err)
	}
	return tx.Commit()
}

// SearchMedia is the Photo Gallery's one general search, composing
// whichever filters were actually given (issue #52 follow-up: "next to the
// search bar you can filter by people but also add tags... select multiple
// people so it will search for images that contain more than one person").
// tags keep their existing behavior: any file tagged with at least one of
// them, ranked by summed score across however many matched - unchanged
// from the old SearchByTags. personIDs is AND, not OR: a file must have a
// face matched to *every* person listed (a photo of just one of two
// selected people doesn't qualify) - combining both narrows further still,
// e.g. tags=["dogs"] + personIDs=[alice] means "photos of a dog that alice
// is also in", not "either". imagesOnly is ignored whenever tags or
// personIDs are given, same as the old SearchByTags/SearchByPerson never
// filtered by mime either - it only applies to the plain "browse
// everything, no filters" case, matching GetFilesByPath's own contract.
// searchMediaClauses builds the FROM/WHERE/GROUP BY/HAVING/ORDER BY shared
// by SearchMedia and SearchMediaDateBuckets - both filter the same `files`
// rows by tags/personIDs/imagesOnly (and, for SearchMedia's "jump to date"
// use, an optional created-before cutoff), they just select something
// different off the result. Kept as one function so the two queries can't
// silently drift apart on what counts as a match.
func searchMediaClauses(path string, tags []string, personIDs []string, imagesOnly bool, before *time.Time) (from, where, groupBy, having, orderBy, selectExtra string, args []any) {
	from = "from `files` as `f`"
	orderBy = " order by `f`.`created` desc"

	if len(tags) > 0 {
		ph := strings.Repeat("?,", len(tags))
		ph = ph[:len(ph)-1]
		from += " join `file_tags` as `tg` on `tg`.`hash` = `f`.`hash` and `tg`.`tag` in (" + ph + ")"
		for _, t := range tags {
			args = append(args, t)
		}
		selectExtra = ", sum(`tg`.`score`) as `score`"
		groupBy = " group by `f`.`hash`"
		orderBy = " order by `score` desc"
	}

	if len(personIDs) > 0 {
		ph := strings.Repeat("?,", len(personIDs))
		ph = ph[:len(ph)-1]
		from += " join `faces` as `fc` on `fc`.`hash` = `f`.`hash` and `fc`.`person_id` in (" + ph + ")"
		for _, p := range personIDs {
			args = append(args, p)
		}
		groupBy = " group by `f`.`hash`"
		having = fmt.Sprintf(" having count(distinct `fc`.`person_id`) = %d", len(personIDs))
	}

	var whereParts []string
	if path != "" {
		whereParts = append(whereParts, "`f`.`path` regexp ?")
		// The WHERE clause comes after the FROM/JOIN clauses above in the
		// final query text, so its placeholder's arg must be appended
		// after theirs, not before - args must stay in the exact
		// left-to-right order the ?s appear in the assembled query.
		args = append(args, "^"+path+"[^/]+$")
	}
	if len(tags) == 0 && len(personIDs) == 0 && imagesOnly {
		whereParts = append(whereParts, "`f`.`mime` like 'image%'")
	}
	// Issue #77: the date scrubber's "jump to date" - same left-to-right
	// arg-ordering rule as above, this is the last WHERE part so its arg
	// goes last regardless of which of the filters above are also active.
	if before != nil {
		whereParts = append(whereParts, "`f`.`created` <= ?")
		args = append(args, *before)
	}
	if len(whereParts) > 0 {
		where = " where " + strings.Join(whereParts, " and ")
	}
	return
}

func (dao *Dao) SearchMedia(path string, tags []string, personIDs []string, imagesOnly bool, before *time.Time) (files []*pb.File, err error) {
	from, where, groupBy, having, orderBy, selectExtra, args := searchMediaClauses(path, tags, personIDs, imagesOnly, before)

	query := "select `f`.`hash`, `f`.`mime`, `f`.`created`, `f`.`modified`, `f`.`path`, `f`.`size`" + selectExtra + " " +
		from + where + groupBy + having + orderBy

	rows, err := dao.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		file := new(pb.File)
		var created, modified time.Time
		dest := []any{&file.Hash, &file.Mime, &created, &modified, &file.Path, &file.Size}
		if len(tags) > 0 {
			var score float64
			dest = append(dest, &score)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		file.Created = timestamppb.New(created)
		file.Modified = timestamppb.New(modified)
		files = append(files, file)
	}
	return files, rows.Err()
}

// DateBucket is one month's photo count (issue #77's date scrubber) -
// internal bookkeeping like ReprocessState below, translated to a pb type
// only at the websocket layer.
type DateBucket struct {
	Month string // "2022-06"
	Count int
}

// SearchMediaDateBuckets answers "how many photos per month" for the same
// filters SearchMedia would use (minus imagesOnly's path-browsing case,
// which the scrubber never needs) - used to size/position the date
// scrubber and to know how many placeholder squares to draw for a month
// that hasn't been paged in yet. Shares searchMediaClauses with SearchMedia
// so the two can't drift on what counts as a match; ignores whatever
// SearchMedia's own orderBy would be (tag search's relevance order) since
// buckets are always reported newest-month-first regardless of chip state -
// callers are expected to hide the scrubber entirely when tags are active,
// per its own doc comment on the ReqPhotoDateBuckets proto message.
func (dao *Dao) SearchMediaDateBuckets(tags []string, personIDs []string, imagesOnly bool) (buckets []DateBucket, err error) {
	from, where, groupBy, having, _, _, args := searchMediaClauses("", tags, personIDs, imagesOnly, nil)

	// groupBy/having (by `f`.`hash`, when personIDs are given) enforce the
	// "matches every requested person on the SAME file" AND semantics -
	// that has to resolve to one row per matching file *before* bucketing
	// by month, or a month with two files each matching only one of two
	// requested people would wrongly get counted as if both had matched a
	// single file. Hence the subquery: settle "which files match" first,
	// exactly like SearchMedia does, then bucket that file set by month.
	query := "select date_format(`month_src`.`created`, '%Y-%m') as `bucket`, count(*) from (" +
		"select `f`.`hash`, `f`.`created` " + from + where + groupBy + having +
		") as `month_src` group by `bucket` order by `bucket` desc"

	rows, err := dao.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var b DateBucket
		if err := rows.Scan(&b.Month, &b.Count); err != nil {
			return nil, err
		}
		buckets = append(buckets, b)
	}
	return buckets, rows.Err()
}

// ReprocessState mirrors the `reprocess_state` singleton row (issue #73) -
// deliberately not a pb type, same reasoning as FaceEmbedding: it's
// internal bookkeeping for files_manager.Reprocess, translated to
// pb.ReprocessStatus only at the websocket layer for whichever subset a
// client actually needs.
type ReprocessState struct {
	Status    string
	Total     int
	Processed int
	LastHash  string
}

// GetReprocessState reads the current (singleton) reprocess row.
func (dao *Dao) GetReprocessState() (state ReprocessState, err error) {
	err = dao.db.QueryRow(
		"select `status`, `total`, `processed`, `last_hash` from `reprocess_state` where `id` = 1",
	).Scan(&state.Status, &state.Total, &state.Processed, &state.LastHash)
	return
}

// StartReprocess marks a brand new run: status becomes 'running', progress
// and the resume checkpoint both reset to zero/empty. Only called for a
// *fresh* start (see files_manager.Reprocess) - resuming an interrupted
// run instead updates status in place via ResumeReprocess, keeping
// whatever total/processed/last_hash it already had.
func (dao *Dao) StartReprocess(total int) (err error) {
	_, err = dao.db.Exec(
		"update `reprocess_state` set `status` = 'running', `total` = ?, `processed` = 0, `last_hash` = '', `started` = now(), `updated` = now() where `id` = 1",
		total)
	return
}

// ResumeReprocess flips a stale 'running' row (left behind by a server
// restart mid-run - see the table's own doc comment in db.sql) back to
// 'running' without touching total/processed/last_hash, so the next batch
// picks up exactly where the interrupted run left off.
func (dao *Dao) ResumeReprocess() (err error) {
	_, err = dao.db.Exec("update `reprocess_state` set `status` = 'running', `updated` = now() where `id` = 1")
	return
}

// UpdateReprocessProgress advances the resume checkpoint after each file -
// frequent, deliberately: the whole point of last_hash is to lose as
// little work as possible to an interruption.
func (dao *Dao) UpdateReprocessProgress(processed int, lastHash string) (err error) {
	_, err = dao.db.Exec(
		"update `reprocess_state` set `processed` = ?, `last_hash` = ?, `updated` = now() where `id` = 1",
		processed, lastHash)
	return
}

// FinishReprocess marks the run's terminal state - "completed" when every
// file was walked, "failed" if the run had to give up outright (as
// opposed to skipping one bad file and continuing, which doesn't fail the
// run - see files_manager.Reprocess).
func (dao *Dao) FinishReprocess(status string) (err error) {
	_, err = dao.db.Exec("update `reprocess_state` set `status` = ?, `updated` = now() where `id` = 1", status)
	return
}

// MarkStaleReprocessStopped flips a leftover "running" row to "stopped" -
// called once at startup (see files_manager.Init). A process that just
// started can't possibly have a reprocess goroutine still going (nothing
// survives a restart), so a "running" status found at boot is always
// stale: the previous run was killed outright (a deploy, a crash) rather
// than cleanly cancelled, and nothing was left behind to ever notice and
// update it - reported live as a Cancel button that appeared to do
// nothing, because there was really nothing left running to cancel in
// the first place, and the status never moved off "running" to say so.
// A no-op (WHERE excludes every other status) if there's nothing to fix.
func (dao *Dao) MarkStaleReprocessStopped() (err error) {
	_, err = dao.db.Exec("update `reprocess_state` set `status` = 'stopped', `updated` = now() where `id` = 1 and `status` = 'running'")
	return
}

// WipeTagsAndFaces deletes every file_tags/faces/people row, in one
// transaction - the "strip pre-existing data so it can be recalculated"
// half of issue #73, run once at the start of a *fresh* reprocess (never
// on resume - see Reprocess's own doc comment). people goes too, not just
// faces: a stale person cluster from a since-fixed detection bug (a false
// positive on a pet, a garbled crop) is worse than useless, and every
// person gets freshly re-clustered as reprocessing goes rather than
// reconciled against old, possibly-wrong ones.
func (dao *Dao) WipeTagsAndFaces() (err error) {
	tx, err := dao.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err = tx.Exec("delete from `file_tags`"); err != nil {
		return fmt.Errorf("wiping file_tags: %w", err)
	}
	if _, err = tx.Exec("delete from `faces`"); err != nil {
		return fmt.Errorf("wiping faces: %w", err)
	}
	if _, err = tx.Exec("delete from `people`"); err != nil {
		return fmt.Errorf("wiping people: %w", err)
	}
	return tx.Commit()
}

// CountMediaFiles returns how many *distinct* pieces of content (images and
// videos - everything files_manager.processMediaContent actually knows how
// to reprocess) a fresh reprocess run has ahead of it. Distinct hashes, not
// file rows: files.hash is deliberately non-unique (see file_tags' own doc
// comment - dedup means several paths can legitimately share one hash),
// and tags/faces are keyed by hash, so two paths pointing at identical
// content need reprocessing exactly once between them, not twice. This
// count must match what ListMediaForReprocess actually walks, or the
// progress bar's denominator lies.
func (dao *Dao) CountMediaFiles() (count int, err error) {
	err = dao.db.QueryRow("select count(distinct `hash`) from `files` where `mime` like 'image%' or `mime` like 'video%'").Scan(&count)
	return
}

// ListMediaForReprocess returns the next batch of distinct-content media
// files after afterHash, ordered by hash ascending - a stable walk order
// that doubles as the resume checkpoint (see reprocess_state's own doc
// comment in db.sql). afterHash is "" for the very first batch.
//
// Grouped by hash (not one row per path) for the same reason as
// CountMediaFiles: besides being redundant work, walking one row per path
// would make `hash > afterHash` pagination itself unsafe - two paths
// sharing a hash could straddle a batch boundary, and advancing the
// checkpoint past that hash after only one of them was seen would skip
// the other forever. Grouping first means every hash the walk will ever
// see appears exactly once, so that can't happen. Which path/mime
// represents the group doesn't matter to processMediaContent (it only
// needs *a* valid path/mime for this content, not it consulting every
// duplicate) - min() picks one deterministically.
func (dao *Dao) ListMediaForReprocess(afterHash string, limit int) (files []*pb.File, err error) {
	rows, err := dao.db.Query(
		"select `hash`, min(`mime`), min(`created`), min(`modified`), min(`path`), min(`size`) from `files` "+
			"where (`mime` like 'image%' or `mime` like 'video%') and `hash` > ? "+
			"group by `hash` order by `hash` asc limit ?",
		afterHash, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		file := new(pb.File)
		var created, modified time.Time
		if err := rows.Scan(&file.Hash, &file.Mime, &created, &modified, &file.Path, &file.Size); err != nil {
			return nil, err
		}
		file.Created = timestamppb.New(created)
		file.Modified = timestamppb.New(modified)
		files = append(files, file)
	}
	return files, rows.Err()
}
