// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"crypto/subtle"
	"database/sql"
	"fmt"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/push"
	_ "github.com/go-sql-driver/mysql"
	"time"
)

const (
	// cLogRetention is how long auth_events and device_metrics rows are
	// kept before PruneOldLogs deletes them - both tables are operational/
	// security logs (auth_events in particular holds remote IP addresses),
	// not data anyone needs kept indefinitely. Neither had any retention
	// policy before this.
	cLogRetention  = 90 * 24 * time.Hour
	cPruneInterval = 24 * time.Hour
)

type Dao struct {
	db            *sql.DB
	stopLogPruner chan struct{}
}

// NewWithDB builds a Dao around an already-open *sql.DB, bypassing Init's
// real MySQL dial and its log-pruner goroutine. Exported for tests that
// need a Dao backed by a mock/fake connection (github.com/DATA-DOG/go-
// sqlmock) rather than a live database.
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

	if err = dao.db.Ping(); err != nil {
		log.Fatal("it is not possible to ping the DB", err)
	}

	dao.stopLogPruner = make(chan struct{})
	dao.startLogPruner()

	return
}

func (dao *Dao) Stop() {
	if dao.stopLogPruner != nil {
		close(dao.stopLogPruner)
	}
	dao.db.Close()
}

// startLogPruner runs PruneOldLogs once immediately (so upgrading onto
// this catches up any backlog that built up before it existed, right away
// rather than waiting up to cPruneInterval) and then every cPruneInterval
// for as long as the process runs.
func (dao *Dao) startLogPruner() {
	go func() {
		prune := func() {
			if err := dao.PruneOldLogs(time.Now().Add(-cLogRetention)); err != nil {
				log.Error("error pruning old auth_events/device_metrics rows:", err)
			}
		}
		prune()

		ticker := time.NewTicker(cPruneInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				prune()
			case <-dao.stopLogPruner:
				return
			}
		}
	}()
}

// PruneOldLogs deletes auth_events and device_metrics rows older than
// before - see cLogRetention's doc comment for why these two tables in
// particular need one.
func (dao *Dao) PruneOldLogs(before time.Time) (err error) {
	if _, err = dao.db.Exec("delete from `auth_events` where `dt` < ?", before); err != nil {
		return fmt.Errorf("pruning auth_events: %w", err)
	}
	if _, err = dao.db.Exec("delete from `device_metrics` where `hour_bucket` < ?", before); err != nil {
		return fmt.Errorf("pruning device_metrics: %w", err)
	}
	return nil
}

func (dao *Dao) IsValidDevice(owner, domain, secret string) (defined, validSecret bool, err error) {
	log.Debug("Is valid device")
	var dbSecret, dbOwner string
	err = dao.db.QueryRow("select `owner_uuid`, `secret` from `devices` where `domain` = ?", domain).Scan(&dbOwner, &dbSecret)
	if err != nil {
		// if we have sql.ErrNoRows that means that the domain is free for grabs
		return err != sql.ErrNoRows, false, err
	}

	// Constant-time: this gates onto the bridge relay for a device, worth
	// the same care as the admin session-token check elsewhere in this
	// codebase rather than a plain == that leaks timing information about
	// how many leading bytes of the secret a guess got right.
	validSecret = subtle.ConstantTimeCompare([]byte(owner), []byte(dbOwner)) == 1 &&
		subtle.ConstantTimeCompare([]byte(secret), []byte(dbSecret)) == 1
	return true, validSecret, nil
}

func (dao *Dao) RegistreDevice(owner, uuid, secret string) (err error) {
	log.Debug("Register device")
	_, err = dao.db.Exec("insert into `devices` (`owner_uuid`, `domain`, `secret`) values (?, ?, ?)", owner, uuid, secret)
	return
}

// SetDeviceDisabled records whether domain's owning process is currently
// disabled (issue #93) - see ReqSetDeviceDisabled's own doc comment for
// why the bridge, not the device, ends up being the source of truth for
// this while that process is stopped. A no-op (not an error) if domain
// isn't registered at all: this is called reactively from the primary
// disabling/re-enabling one of its own users, and racing that against the
// user's own first-ever ReqBridgeRegister isn't worth failing over -
// RegistreDevice's own default (disabled=0) is correct either way for a
// user being enabled, and one being created is never disabled on arrival.
func (dao *Dao) SetDeviceDisabled(domain string, disabled bool) (err error) {
	_, err = dao.db.Exec("update `devices` set `disabled` = ? where `domain` = ?", disabled, domain)
	return
}

// IsDeviceDisabled reports whether domain has been marked disabled - false
// for a domain not registered at all (its own connection attempt will
// already fail for that reason, without needing an "account disabled"
// message that would be actively misleading).
func (dao *Dao) IsDeviceDisabled(domain string) (disabled bool, err error) {
	err = dao.db.QueryRow("select `disabled` from `devices` where `domain` = ?", domain).Scan(&disabled)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return disabled, err
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

// NewContactRequest stores one submission of the public site's contact
// form (issue #57) - a general enquiry or a request for bridge access.
func (dao *Dao) NewContactRequest(name, email, reason, message string) (err error) {
	_, err = dao.db.Exec(
		"insert into `contact_requests` (`name`, `email`, `reason`, `message`, `created`) values (?, ?, ?, ?, now())",
		name, email, reason, message)
	return
}

// ContactRequest is a row from the contact_requests table.
type ContactRequest struct {
	Id      int
	Name    string
	Email   string
	Reason  string
	Message string
	Created time.Time
	IsRead  bool
}

// ListContactRequests returns the most recent contact requests, newest
// first, capped at limit.
func (dao *Dao) ListContactRequests(limit int) (requests []ContactRequest, err error) {
	rows, err := dao.db.Query(
		"select `id`, `name`, `email`, `reason`, `message`, `created`, `is_read` from `contact_requests` order by `created` desc limit ?",
		limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	requests = []ContactRequest{}
	for rows.Next() {
		var c ContactRequest
		if err := rows.Scan(&c.Id, &c.Name, &c.Email, &c.Reason, &c.Message, &c.Created, &c.IsRead); err != nil {
			return nil, err
		}
		requests = append(requests, c)
	}

	return requests, rows.Err()
}

// SetContactRequestRead marks a contact request read/unread.
func (dao *Dao) SetContactRequestRead(id int, isRead bool) (err error) {
	_, err = dao.db.Exec("update `contact_requests` set `is_read` = ? where `id` = ?", isRead, id)
	return
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

// SetPushRegistrations replaces the full push-registration snapshot for
// domain - this device's current VAPID keypair, APNs tokens, and Web Push
// subscriptions, as reported by ReqUpdatePushRegistrations (issue #62). See
// push_registrations/push_apns_tokens/push_web_subs' shared doc comment in
// db.sql for why this is delete-all-then-reinsert rather than a row-by-row
// reconcile. All in one transaction so a client of ListWebPushSubscriptions-
// ForDomain/ListApnsTokensForDomain never observes a half-replaced set.
func (dao *Dao) SetPushRegistrations(domain, vapidPub, vapidPriv string, apnsTokens []string, webSubs []push.WebPushSubscription) (err error) {
	tx, err := dao.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err = tx.Exec(
		"insert into `push_registrations` (`domain`, `vapid_public_key`, `vapid_private_key`) values (?, ?, ?) "+
			"on duplicate key update `vapid_public_key` = values(`vapid_public_key`), `vapid_private_key` = values(`vapid_private_key`)",
		domain, vapidPub, vapidPriv); err != nil {
		return fmt.Errorf("upserting vapid keys: %w", err)
	}

	if _, err = tx.Exec("delete from `push_apns_tokens` where `domain` = ?", domain); err != nil {
		return fmt.Errorf("clearing apns tokens: %w", err)
	}
	for _, t := range apnsTokens {
		if _, err = tx.Exec("insert into `push_apns_tokens` (`domain`, `token`) values (?, ?)", domain, t); err != nil {
			return fmt.Errorf("inserting apns token: %w", err)
		}
	}

	if _, err = tx.Exec("delete from `push_web_subs` where `domain` = ?", domain); err != nil {
		return fmt.Errorf("clearing web push subs: %w", err)
	}
	for _, s := range webSubs {
		if _, err = tx.Exec(
			"insert into `push_web_subs` (`domain`, `endpoint`, `p256dh`, `auth`) values (?, ?, ?, ?)",
			domain, s.Endpoint, s.P256dh, s.Auth); err != nil {
			return fmt.Errorf("inserting web push sub: %w", err)
		}
	}

	return tx.Commit()
}

// GetVapidKeysForDomain, SetVapidKeysForDomain, ListWebPushSubscriptions-
// ForDomain, DeleteWebPushSubscriptionForDomain, ListApnsTokensForDomain and
// DeleteApnsTokenForDomain below are the domain-scoped equivalents of
// push.Storage's methods - a device's own dao.Dao only ever holds one
// device's worth of registrations, but the bridge holds every domain's, so
// each of these needs to say which one. See websocket.domainPushStorage,
// the small per-domain adapter that lets *Dao back a push.Push the same way
// a device's own *dao.Dao does.

func (dao *Dao) GetVapidKeysForDomain(domain string) (pub, priv string, err error) {
	err = dao.db.QueryRow("select `vapid_public_key`, `vapid_private_key` from `push_registrations` where `domain` = ?", domain).Scan(&pub, &priv)
	if err == sql.ErrNoRows {
		return "", "", nil
	}
	return pub, priv, err
}

// SetVapidKeysForDomain exists only to satisfy push.Storage - in practice
// never called for a bridge-side adapter, since the device always reports
// its own real keys before the bridge ever needs them (see
// push.loadOrGenerateVapidKeys' doc comment).
func (dao *Dao) SetVapidKeysForDomain(domain, pub, priv string) (err error) {
	_, err = dao.db.Exec(
		"insert into `push_registrations` (`domain`, `vapid_public_key`, `vapid_private_key`) values (?, ?, ?) "+
			"on duplicate key update `vapid_public_key` = values(`vapid_public_key`), `vapid_private_key` = values(`vapid_private_key`)",
		domain, pub, priv)
	return
}

func (dao *Dao) ListWebPushSubscriptionsForDomain(domain string) (subs []*push.WebPushSubscription, err error) {
	rows, err := dao.db.Query("select `endpoint`, `p256dh`, `auth` from `push_web_subs` where `domain` = ?", domain)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	subs = []*push.WebPushSubscription{}
	for rows.Next() {
		var s push.WebPushSubscription
		if err := rows.Scan(&s.Endpoint, &s.P256dh, &s.Auth); err != nil {
			return nil, err
		}
		subs = append(subs, &s)
	}

	return subs, rows.Err()
}

func (dao *Dao) DeleteWebPushSubscriptionForDomain(domain, endpoint string) (err error) {
	_, err = dao.db.Exec("delete from `push_web_subs` where `domain` = ? and `endpoint` = ?", domain, endpoint)
	return
}

func (dao *Dao) ListApnsTokensForDomain(domain string) (tokens []string, err error) {
	rows, err := dao.db.Query("select `token` from `push_apns_tokens` where `domain` = ?", domain)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tokens = []string{}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		tokens = append(tokens, t)
	}

	return tokens, rows.Err()
}

func (dao *Dao) DeleteApnsTokenForDomain(domain, token string) (err error) {
	_, err = dao.db.Exec("delete from `push_apns_tokens` where `domain` = ? and `token` = ?", domain, token)
	return
}
