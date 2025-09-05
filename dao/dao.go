package dao

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/go-sql-driver/mysql"
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

func (dao *Dao) IsSessionDefined() (defined bool, err error) {
	log.Debug("Is session defined")
	err = dao.db.QueryRow("select count(*) from `auth_check`").Scan(&defined)

	return
}

func (dao *Dao) GetSessionCheck() (encText []byte, err error) {
	log.Debug("Checking Auth")
	err = dao.db.QueryRow("select `check` from `auth_check`").Scan(&encText)

	return
}

func (dao *Dao) CreateSession(encCheck string) (err error) {
	log.Debug("Creating Auth session:")
	_, err = dao.db.Exec("insert into `auth_check` (`check`) values (?)", encCheck)
	return
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

func (dao *Dao) GetFilesByPath(globbing bool, path string) (files []*pb.File, err error) {
	if globbing {
		path = strings.ReplaceAll(path, "%", "\\%")
		path = strings.ReplaceAll(path, "_", "\\_")
		path = strings.ReplaceAll(path, "*", "%")
		path = strings.ReplaceAll(path, "?", "_")
	} else {
		pathFiles := "^" + path + "[^/]+$"
		slashesInPath := strings.Count(path, "/")
		// We add first the sub-directories that are actually subpaths of the existing files
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
	}

	log.Debug("Get Files by path:", globbing, path)
	rows, err := dao.db.Query(
		"select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` regexp ?", path)

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
