package dao

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
	_ "github.com/go-sql-driver/mysql"
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

func (dao *Dao) IsValidDevice(owner, domain, secret string) (defined, validSecret bool, err error) {
	log.Debug("Is valid device")
	var dbSecret, dbOwner string
	err = dao.db.QueryRow("select `owner_uuid`, `secret` from `devices` where `domain` = ?", domain).Scan(&dbOwner, &dbSecret)
	if err != nil {
		// if we have sql.ErrNoRows that means that the domain is free for grabs
		return err != sql.ErrNoRows, false, err
	}

	return true, owner == dbOwner && dbSecret == secret, nil
}

func (dao *Dao) RegistreDevice(owner, uuid, secret string) (err error) {
	log.Debug("Register device")
	_, err = dao.db.Exec("insert into `devices` (`owner_uuid`, `domain`, `secret`) values (?, ?, ?)", owner, uuid, secret)
	return
}
