package dao

import (
	"context"
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
	db     *sql.DB
	cancel context.CancelFunc
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

	if err = dao.db.Ping(); err != nil {
		log.Fatal("it is not possible to ping the DB", err)
	}

	return
}

func (dao *Dao) Stop() {
	defer dao.db.Close()
	defer dao.cancel()
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
	_, err = dao.db.Exec("update `settings` set `subdomain` = ?, `bridge_secret` = ?", subdomain, uuid.New())
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

func (dao *Dao) DelFileByPath(path string) (err error) {
	log.Debug("Del file SQL:", path)
	_, err = dao.db.Exec("delete from `files` where `path` = ?", path)

	return
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

func (dao *Dao) GetSocialPublicationComments(pubUuid string) (comments []*pb.Comment, err error) {
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

func (dao *Dao) GetSocialPublications(since time.Time, total int32, ownOnly bool, exclude []string, prName, prText string, prImage []byte) (pubs *pb.SocialPublications, err error) {
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

func (dao *Dao) ChangeFriendStatus(domain string, status pb.FriendShipStatus) (err error) {
	_, err = dao.db.Exec("update `social_friendship` set `status` = ? where `domain` = ?", dao.pbToStatus(status), domain)
	return
}

func (dao *Dao) NewEvent(eventType string, data []byte) (err error) {
	log.Debug("Creating new event", eventType, data)
	_, err = dao.db.Exec("insert into `events` (`uuid`, `dt`, `type`, `content`) values (?, now(), ?, ?)", uuid.New(), eventType, data)

	return err
}
