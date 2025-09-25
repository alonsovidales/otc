package dao

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/images_tagger"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
	"strings"
	"time"
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
	log.Debug("Checking Auth")
	err = dao.db.QueryRow("select `secret` from `vault`").Scan(&encText)

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

func (dao *Dao) GetFilesByPath(path string, addSubDirs bool) (files []*pb.File, err error) {
	if addSubDirs {
		pathFiles := "^" + path + "[^/]+$"

		// We add first the sub-directories that are actually subpaths of the existing files
		slashesInPath := strings.Count(path, "/")
		rowsDirs, err := dao.db.Query("select distinct(SUBSTRING_INDEX(path, '/', ?+1)) as path from files WHERE path LIKE ? and path not regexp ?;", slashesInPath, path+"%", pathFiles)
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
			log.Debug("appending file", file.Hash)
			files = append(files, file)
		}
		path = pathFiles
	} else {
		path = "^" + path
	}

	searchStr := "select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` regexp ?"
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
		log.Debug("appending file", file.Hash)
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

func (dao *Dao) NewSocialPublication(text string, hashes []string) (err error) {
	log.Debug("Creating SocialPublication")
	pubUuid := uuid.New()

	for i, hash := range hashes {
		_, err = dao.db.Exec("insert into `social_publications_files` (`hash`, `uuid`, `pos`) values (?, ?, ?)", hash, pubUuid, i)
		if err != nil {
			return
		}
	}

	_, err = dao.db.Exec("insert into `social_publications` (`uuid`, `dt`, `text`) values (?, now(), ?)", pubUuid, text)
	return
}
