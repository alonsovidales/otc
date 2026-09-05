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
	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Dao struct {
	db *sql.DB
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

func (dao *Dao) PersistSecret(encCheck []byte) (err error) {
	log.Debug("Creating Auth session:")
	_, err = dao.db.Exec("insert into `vault` (`secret`) values (?)", encCheck)
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

func (dao *Dao) UpdateSecret(encCheck []byte) (err error) {
	_, err = dao.db.Exec("update `vault` set `secret` = ?", encCheck)
	return
}

func (dao *Dao) AddTags(file *pb.File, tags []imagestagger.RAMTag) {
	for _, tag := range tags {
		_, err := dao.db.Exec(
			"insert into `file_tags` (`hash`, `tag`, `score`) values (?, ?, ?)",
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
	if err = tx.QueryRow("select count(*) from `files` where `hash` = ?", hash).Scan(&refCount); err != nil {
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

func (dao *Dao) SearchByTags(path string, tags []string) (files []*pb.File, err error) {
	var pathSearch string

	if path != "" {
		// We want to search only in this directory
		pathSearch = " `f`.`path` like ? and "
		path = "^" + path + "[^/]+$"
	}

	ph := strings.Repeat("?,", len(tags))
	ph = ph[:len(ph)-1]

	searchStr := "select " +
		"`f`.`hash`, `f`.`mime`, `f`.`created`, `f`.`modified`, `f`.`path`, `f`.`size`, sum(`tg`.`score`) as `score`, count(`tg`.`tag`) as `total_tags` " +
		"from `file_tags` as `tg` left join `files` as `f` on `tg`.`hash` = `f`.`hash` " +
		"where " + pathSearch + " `tg`.`tag` in (" + ph + ") " +
		"group by `f`.`hash` " +
		"order by `score` desc"
		//"limit " + fmt.Sprintf("%d", cfg.GetInt("tagger", "max-images-search"))

	argsLen := len(tags)
	if path != "" {
		argsLen += 1
	}
	args := make([]any, argsLen)
	if path != "" {
		args[0] = path
	}
	for i, t := range tags {
		if path != "" {
			args[i+1] = t
		} else {
			args[i] = t
		}
	}
	rows, err := dao.db.Query(searchStr, args...)
	log.Debug("Search Query:", searchStr, args)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		file := new(pb.File)
		var created, modified time.Time
		var score float32
		var totalTags int
		if err := rows.Scan(&file.Hash, &file.Mime, &created, &modified, &file.Path, &file.Size, &score, &totalTags); err != nil {
			return nil, err
		}
		log.Debug("Img:", file.Hash, "Tags:", totalTags, "Score:", score)
		file.Created = timestamppb.New(created)
		file.Modified = timestamppb.New(modified)
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return
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

func (dao *Dao) NewComment(commentUuid, pubName, pubUuid, comment string) (err error) {
	log.Debug("Creating new comment")
	_, err = dao.db.Exec("insert into `social_publications_comments` (`uuid`, `pub_uuid`, `dt`, `comment`, `publisher_name`) values (?, ?, now(), ?, ?)", commentUuid, pubUuid, comment, pubName)

	return err
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
