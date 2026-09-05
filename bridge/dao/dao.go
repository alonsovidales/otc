package dao

import (
	"database/sql"
	"fmt"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
	_ "github.com/go-sql-driver/mysql"
	"time"
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

	if err = dao.db.Ping(); err != nil {
		log.Fatal("it is not possible to ping the DB", err)
	}

	return
}

func (dao *Dao) Stop() {
	dao.db.Close()
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

// RotateSecret replaces a device's secret with newSecret, but only if
// oldSecret is exactly what's currently on record for owner+domain — an
// atomic compare-and-swap in the WHERE clause rather than a separate
// check-then-write, so knowing the current secret is both how this proves
// it's really that device asking, and the only thing that can trigger a
// replacement (self-service "Regenerate" in Settings, issue #40 follow-up).
func (dao *Dao) RotateSecret(owner, domain, oldSecret, newSecret string) (ok bool, err error) {
	log.Debug("Rotate device secret")
	res, err := dao.db.Exec(
		"update `devices` set `secret` = ? where `owner_uuid` = ? and `domain` = ? and `secret` = ?",
		newSecret, owner, domain, oldSecret,
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// Device is a row from the devices table, as returned to the admin panel.
// Secret is intentionally omitted: the panel never needs it back, and
// there's no reason to put it back on the wire once it's been set.
type Device struct {
	OwnerUuid string
	Domain    string
}

func (dao *Dao) ListDevices() (devices []Device, err error) {
	rows, err := dao.db.Query("select `owner_uuid`, `domain` from `devices` order by `domain`")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	devices = []Device{}
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.OwnerUuid, &d.Domain); err != nil {
			return nil, err
		}
		devices = append(devices, d)
	}

	return devices, rows.Err()
}

func (dao *Dao) DeleteDevice(domain string) (err error) {
	_, err = dao.db.Exec("delete from `devices` where `domain` = ?", domain)
	return
}

// GetAdminPasswordHash returns the bcrypt hash for username, and whether
// that account exists at all.
func (dao *Dao) GetAdminPasswordHash(username string) (passwordHash string, found bool, err error) {
	err = dao.db.QueryRow("select `password_hash` from `admin_users` where `username` = ?", username).Scan(&passwordHash)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}

	return passwordHash, true, nil
}

// SetAdminPassword creates or updates an admin account's password hash.
func (dao *Dao) SetAdminPassword(username, passwordHash string) (err error) {
	_, err = dao.db.Exec(
		"insert into `admin_users` (`username`, `password_hash`, `created`) values (?, ?, now()) "+
			"on duplicate key update `password_hash` = values(`password_hash`)",
		username, passwordHash)
	return
}

// RecordDeviceActivity adds one request's worth of traffic to the current
// hour's bucket for domain.
func (dao *Dao) RecordDeviceActivity(domain string, bytesIn, bytesOut int64) (err error) {
	_, err = dao.db.Exec(
		"insert into `device_metrics` (`domain`, `hour_bucket`, `requests`, `bytes_in`, `bytes_out`) "+
			"values (?, date_format(now(), '%Y-%m-%d %H:00:00'), 1, ?, ?) "+
			"on duplicate key update `requests` = `requests` + 1, `bytes_in` = `bytes_in` + values(`bytes_in`), `bytes_out` = `bytes_out` + values(`bytes_out`)",
		domain, bytesIn, bytesOut)
	return
}

// MetricBucket is one hour's aggregated activity for a device.
type MetricBucket struct {
	HourBucket time.Time
	Requests   int
	BytesIn    int64
	BytesOut   int64
}

// GetDeviceMetrics returns hourly buckets for domain since "since", oldest
// first. An empty domain returns totals across every device instead, so
// the panel can show an overview alongside per-device detail.
func (dao *Dao) GetDeviceMetrics(domain string, since time.Time) (buckets []MetricBucket, err error) {
	var rows *sql.Rows
	if domain == "" {
		rows, err = dao.db.Query(
			"select `hour_bucket`, sum(`requests`), sum(`bytes_in`), sum(`bytes_out`) from `device_metrics` "+
				"where `hour_bucket` >= ? group by `hour_bucket` order by `hour_bucket`", since)
	} else {
		rows, err = dao.db.Query(
			"select `hour_bucket`, `requests`, `bytes_in`, `bytes_out` from `device_metrics` "+
				"where `domain` = ? and `hour_bucket` >= ? order by `hour_bucket`", domain, since)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	buckets = []MetricBucket{}
	for rows.Next() {
		var b MetricBucket
		if err := rows.Scan(&b.HourBucket, &b.Requests, &b.BytesIn, &b.BytesOut); err != nil {
			return nil, err
		}
		buckets = append(buckets, b)
	}

	return buckets, rows.Err()
}

// LogAuthEvent records a failed/suspicious bridge-registration attempt.
func (dao *Dao) LogAuthEvent(uuid, domain, ownerUuidAttempted, remoteAddr, reason string) (err error) {
	_, err = dao.db.Exec(
		"insert into `auth_events` (`uuid`, `domain`, `owner_uuid_attempted`, `remote_addr`, `dt`, `reason`) values (?, ?, ?, ?, now(), ?)",
		uuid, domain, ownerUuidAttempted, remoteAddr, reason)
	return
}

// AuthEvent is a row from the auth_events table.
type AuthEvent struct {
	Domain             string
	OwnerUuidAttempted string
	RemoteAddr         string
	Dt                 time.Time
	Reason             string
}

// GetAuthEvents returns the most recent auth events, newest first, for
// domain (or across every device if domain is empty), capped at limit.
func (dao *Dao) GetAuthEvents(domain string, limit int) (events []AuthEvent, err error) {
	var rows *sql.Rows
	if domain == "" {
		rows, err = dao.db.Query("select `domain`, `owner_uuid_attempted`, `remote_addr`, `dt`, `reason` from `auth_events` order by `dt` desc limit ?", limit)
	} else {
		rows, err = dao.db.Query("select `domain`, `owner_uuid_attempted`, `remote_addr`, `dt`, `reason` from `auth_events` where `domain` = ? order by `dt` desc limit ?", domain, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events = []AuthEvent{}
	for rows.Next() {
		var e AuthEvent
		if err := rows.Scan(&e.Domain, &e.OwnerUuidAttempted, &e.RemoteAddr, &e.Dt, &e.Reason); err != nil {
			return nil, err
		}
		events = append(events, e)
	}

	return events, rows.Err()
}
