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
	"strings"
	"sync"
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

	// cNameHold is how long a released device name stays reserved for the
	// account that held it: whoever took it next used to inherit that
	// device's friendships (friends' devices accepted the "same" domain).
	cNameHold = 30 * 24 * time.Hour
)

// NameHeld reports whether domain was released less than cNameHold ago
// by an account other than accountID (or by no account) - and so can't be
// taken by accountID yet.
func (dao *Dao) NameHeld(domain, accountID string) (bool, error) {
	var n int
	err := dao.db.QueryRow(
		"select count(*) from `released_domains` where `domain` = ? and `released_at` > ? "+
			"and (`account_id` is null or `account_id` <> ?)",
		domain, time.Now().Add(-cNameHold), accountID,
	).Scan(&n)
	return n > 0, err
}

// recordRelease holds domain for accountID (empty: for no one) from now.
func (dao *Dao) recordRelease(domain, accountID string) error {
	var acc any
	if accountID != "" {
		acc = accountID
	}
	_, err := dao.db.Exec(
		"insert into `released_domains` (`domain`, `account_id`, `released_at`) values (?, ?, ?) "+
			"on duplicate key update `account_id` = values(`account_id`), `released_at` = values(`released_at`)",
		domain, acc, time.Now())
	return err
}

type Dao struct {
	db            *sql.DB
	stopLogPruner chan struct{}

	// Issue #139: when devices.last_client_at was last written per
	// domain, so relayed traffic touches that row at most once a minute.
	lastClientMu sync.Mutex
	lastClient   map[string]time.Time
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
		// time_zone UTC: now() in SQL follows the session's zone,
		// the system one otherwise - Europe/London on the Pi image - while
		// parseTime reads every DATETIME back as UTC, so whatever SQL
		// stamped came out an hour ahead ("in 49 min" on a new alert).
		"%s:%s@tcp(127.0.0.1:%d)/%s?parseTime=true&charset=utf8mb4,utf8&time_zone=%%27%%2B00%%3A00%%27",
		cfg.GetStr("mysql", "user"),
		cfg.GetStr("mysql", "pass"),
		cfg.GetInt("mysql", "port"),
		cfg.GetStr("mysql", "db"))

	// Never the DSN itself: it carries the database password, and the
	// log is read by more than whoever holds that password.
	log.Debug("connecting to DB:", strings.Replace(dsn, ":"+cfg.GetStr("mysql", "pass")+"@", ":***@", 1))

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
	if _, err = dao.db.Exec("delete from `released_domains` where `released_at` < ?", time.Now().Add(-cNameHold)); err != nil {
		return fmt.Errorf("pruning released_domains: %w", err)
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

// IsDomainRegistered reports whether domain is already claimed here
// (issue #103). "Claimed" means a row exists at all, regardless of which
// owner or whether it's disabled: any existing registration is enough to
// stop a different device from ever registering that subdomain (see
// IsValidDevice - a domain that's defined with a different owner/secret is
// rejected, not adopted), so for "can a new user take this name?" the
// answer is simply whether anything is here.
func (dao *Dao) IsDomainRegistered(domain string) (registered bool, err error) {
	var one int
	err = dao.db.QueryRow("select 1 from `devices` where `domain` = ?", domain).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
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

// DeleteDevice removes a device by domain - the admin panel's delete. An
// admin freeing a name means it's free now: no 30-day hold (that is for a
// name an account releases itself, see DeleteAccountDomain), and any hold
// already on it is lifted.
func (dao *Dao) DeleteDevice(domain string) (err error) {
	if _, err = dao.db.Exec("delete from `devices` where `domain` = ?", domain); err != nil {
		return err
	}
	if _, err := dao.db.Exec("delete from `released_domains` where `domain` = ?", domain); err != nil {
		log.Error("could not lift the hold on", domain, ":", err)
	}
	return nil
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
	dao.touchLastClient(domain)
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

// cSetupBeaconTTLMinutes is how long a setup hand-off stays answerable.
const cSetupBeaconTTLMinutes = 10

// SetSetupBeacon records (or refreshes) where a device being set up can be
// reached on its LAN, under the wizard's one-time token (issue #38), and
// drops every expired hand-off while it is at it.
func (dao *Dao) SetSetupBeacon(token, addr string) (err error) {
	if _, err = dao.db.Exec("delete from `setup_beacons` where `created` < now() - interval ? minute", cSetupBeaconTTLMinutes); err != nil {
		return
	}
	_, err = dao.db.Exec(
		"insert into `setup_beacons` (`token`, `addr`, `created`) values (?, ?, now()) on duplicate key update `addr` = values(`addr`), `created` = now()",
		token, addr)
	return
}

// GetSetupBeacon answers the wizard page's poll: the address reported
// under token within the last ten minutes, or found=false.
func (dao *Dao) GetSetupBeacon(token string) (addr string, found bool, err error) {
	err = dao.db.QueryRow(
		"select `addr` from `setup_beacons` where `token` = ? and `created` >= now() - interval ? minute",
		token, cSetupBeaconTTLMinutes).Scan(&addr)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return addr, true, nil
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

// ListContactRequests returns a page of contact requests, newest first,
// matching q in the name, email, reason or message, how many match in all,
// and how many are unread overall (the tab's badge).
func (dao *Dao) ListContactRequests(q string, limit, offset int) (requests []ContactRequest, total, unread int, err error) {
	where := ""
	args := []any{}
	if q != "" {
		where = " where (`name` like ? or `email` like ? or `reason` like ? or `message` like ?)"
		l := likeArg(q)
		args = append(args, l, l, l, l)
	}
	if err = dao.db.QueryRow("select count(*) from `contact_requests`"+where, args...).Scan(&total); err != nil {
		return nil, 0, 0, err
	}
	if err = dao.db.QueryRow("select count(*) from `contact_requests` where not `is_read`").Scan(&unread); err != nil {
		return nil, 0, 0, err
	}
	tail, targs := pageClause(limit, offset)
	rows, err := dao.db.Query("select `id`, `name`, `email`, `reason`, `message`, `created`, `is_read` from `contact_requests`"+where+" order by `created` desc"+tail, append(args, targs...)...)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()

	requests = []ContactRequest{}
	for rows.Next() {
		var c ContactRequest
		if err := rows.Scan(&c.Id, &c.Name, &c.Email, &c.Reason, &c.Message, &c.Created, &c.IsRead); err != nil {
			return nil, 0, 0, err
		}
		requests = append(requests, c)
	}

	return requests, total, unread, rows.Err()
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

// GetAuthEvents returns a page of auth events, newest first, for domain
// (or every device if empty) matching q in the domain, attempted owner
// UUID, remote address or reason, and how many match in all.
func (dao *Dao) GetAuthEvents(domain, q string, limit, offset int) (events []AuthEvent, total int, err error) {
	where := " where 1=1"
	args := []any{}
	if domain != "" {
		where += " and `domain` = ?"
		args = append(args, domain)
	}
	if q != "" {
		where += " and (`domain` like ? or `owner_uuid_attempted` like ? or `remote_addr` like ? or `reason` like ?)"
		l := likeArg(q)
		args = append(args, l, l, l, l)
	}
	if err = dao.db.QueryRow("select count(*) from `auth_events`"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	tail, targs := pageClause(limit, offset)
	rows, err := dao.db.Query("select `domain`, `owner_uuid_attempted`, `remote_addr`, `dt`, `reason` from `auth_events`"+where+" order by `dt` desc"+tail, append(args, targs...)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	events = []AuthEvent{}
	for rows.Next() {
		var e AuthEvent
		if err := rows.Scan(&e.Domain, &e.OwnerUuidAttempted, &e.RemoteAddr, &e.Dt, &e.Reason); err != nil {
			return nil, 0, err
		}
		events = append(events, e)
	}

	return events, total, rows.Err()
}

// SetPushRegistrations replaces the full push-registration snapshot for
// domain - this device's current VAPID keypair, APNs tokens, and Web Push
// subscriptions, as reported by ReqUpdatePushRegistrations (issue #62). See
// push_registrations/push_apns_tokens/push_web_subs' shared doc comment in
// db.sql for why this is delete-all-then-reinsert rather than a row-by-row
// reconcile. All in one transaction so a client of ListWebPushSubscriptions-
// ForDomain/ListApnsTokensForDomain never observes a half-replaced set.
func (dao *Dao) SetPushRegistrations(domain, vapidPub, vapidPriv string, apnsTokens, fcmTokens []string, webSubs []push.WebPushSubscription) (err error) {
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

	if _, err = tx.Exec("delete from `push_fcm_tokens` where `domain` = ?", domain); err != nil {
		return fmt.Errorf("clearing fcm tokens: %w", err)
	}
	for _, t := range fcmTokens {
		if _, err = tx.Exec("insert into `push_fcm_tokens` (`domain`, `token`) values (?, ?)", domain, t); err != nil {
			return fmt.Errorf("inserting fcm token: %w", err)
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

// ListFcmTokensForDomain and DeleteFcmTokenForDomain: issue #125's Android
// tokens, as the APNs pair above.
func (dao *Dao) ListFcmTokensForDomain(domain string) (tokens []string, err error) {
	rows, err := dao.db.Query("select `token` from `push_fcm_tokens` where `domain` = ?", domain)
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

func (dao *Dao) DeleteFcmTokenForDomain(domain, token string) (err error) {
	_, err = dao.db.Exec("delete from `push_fcm_tokens` where `domain` = ? and `token` = ?", domain, token)
	return
}

// ---------------------------------------------------------------------
// Issue #124: user accounts and the domains they own
// ---------------------------------------------------------------------

// Account is a row of the accounts table; PasswordHash is empty for an
// account that only ever signed in with a provider.
type Account struct {
	ID           string
	Email        string
	Name         string
	Surname      string
	Country      string
	PasswordHash string
	Created      time.Time
	LastSeen     time.Time
	FreeUntil    time.Time
}

// AccountDomain is one of an account's registered domains, as the account
// page lists them.
type AccountDomain struct {
	Domain   string
	Created  time.Time
	Disabled bool
}

func (dao *Dao) CreateAccount(a Account) error {
	_, err := dao.db.Exec(
		"insert into `accounts` (`id`, `email`, `name`, `surname`, `country`, `password_hash`, `created`, `last_seen`, `free_until`) values (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		a.ID, a.Email, a.Name, a.Surname, a.Country, sql.NullString{String: a.PasswordHash, Valid: a.PasswordHash != ""}, a.Created, a.LastSeen, a.FreeUntil)

	return err
}

func (dao *Dao) scanAccount(row *sql.Row) (*Account, error) {
	a := &Account{}
	var hash sql.NullString
	err := row.Scan(&a.ID, &a.Email, &a.Name, &a.Surname, &a.Country, &hash, &a.Created, &a.LastSeen, &a.FreeUntil)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a.PasswordHash = hash.String

	return a, nil
}

const cAccountColumns = "`id`, `email`, `name`, `surname`, `country`, `password_hash`, `created`, `last_seen`, `free_until`"

// GetAccount is nil, nil for an id nobody has.
func (dao *Dao) GetAccount(id string) (*Account, error) {
	return dao.scanAccount(dao.db.QueryRow("select "+cAccountColumns+" from `accounts` where `id` = ?", id))
}

// GetAccountByEmail is nil, nil for an email nobody has.
func (dao *Dao) GetAccountByEmail(email string) (*Account, error) {
	return dao.scanAccount(dao.db.QueryRow("select "+cAccountColumns+" from `accounts` where `email` = ?", email))
}

// GetAccountByLogin is the account a provider identity is linked to, or
// nil, nil.
func (dao *Dao) GetAccountByLogin(provider, subject string) (*Account, error) {
	return dao.scanAccount(dao.db.QueryRow("select "+cAccountColumns+" from `accounts` where `id` = (select `account_id` from `account_logins` where `provider` = ? and `subject` = ?)", provider, subject))
}

func (dao *Dao) LinkAccountLogin(provider, subject, accountID string) error {
	_, err := dao.db.Exec("insert ignore into `account_logins` (`provider`, `subject`, `account_id`) values (?, ?, ?)", provider, subject, accountID)

	return err
}

func (dao *Dao) UpdateAccountProfile(id, name, surname, country string) error {
	_, err := dao.db.Exec("update `accounts` set `name` = ?, `surname` = ?, `country` = ? where `id` = ?", name, surname, country, id)

	return err
}

func (dao *Dao) SetAccountPassword(id, passwordHash string) error {
	_, err := dao.db.Exec("update `accounts` set `password_hash` = ? where `id` = ?", passwordHash, id)

	return err
}

// TouchAccount records activity (the terms release an account after six
// months without any).
func (dao *Dao) TouchAccount(id string) error {
	_, err := dao.db.Exec("update `accounts` set `last_seen` = ? where `id` = ?", time.Now(), id)

	return err
}

// SaveAccountToken stores a one-time token (see account_tokens in db.sql).
func (dao *Dao) SaveAccountToken(token, accountID, purpose string, expires time.Time) error {
	_, err := dao.db.Exec("insert into `account_tokens` (`token`, `account_id`, `purpose`, `expires`) values (?, ?, ?, ?)", token, accountID, purpose, expires)

	return err
}

// AccountForToken is the account a live token of that purpose belongs to;
// found is false for an unknown or expired token. Not consumed: a setup
// token stays usable for its whole life, so a claim that fails on a taken
// name can be retried with another name.
func (dao *Dao) AccountForToken(token, purpose string) (accountID string, found bool, err error) {
	err = dao.db.QueryRow("select `account_id` from `account_tokens` where `token` = ? and `purpose` = ? and `expires` > ?", token, purpose, time.Now()).Scan(&accountID)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}

	return accountID, true, nil
}

func (dao *Dao) PruneAccountTokens() error {
	if _, err := dao.db.Exec("delete from `account_tokens` where `expires` < ?", time.Now()); err != nil {
		return err
	}
	_, err := dao.db.Exec("delete from `oauth_states` where `created` < ?", time.Now().Add(-time.Hour))

	return err
}

func (dao *Dao) SaveOAuthState(state, returnURL string) error {
	_, err := dao.db.Exec("insert into `oauth_states` (`state`, `return_url`, `created`) values (?, ?, ?)", state, returnURL, time.Now())

	return err
}

// ConsumeOAuthState takes a state out and returns where its sign-in
// should return to; found is false for an unknown or stale one.
func (dao *Dao) ConsumeOAuthState(state string) (returnURL string, found bool, err error) {
	var created time.Time
	err = dao.db.QueryRow("select `return_url`, `created` from `oauth_states` where `state` = ?", state).Scan(&returnURL, &created)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if _, err := dao.db.Exec("delete from `oauth_states` where `state` = ?", state); err != nil {
		return "", false, err
	}
	if time.Since(created) > 15*time.Minute {
		return "", false, nil
	}

	return returnURL, true, nil
}

func (dao *Dao) ListAccountDomains(accountID string) (domains []AccountDomain, err error) {
	rows, err := dao.db.Query("select `domain`, `created`, `disabled` from `devices` where `account_id` = ? order by `created`, `domain`", accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	domains = []AccountDomain{}
	for rows.Next() {
		var d AccountDomain
		var created sql.NullTime
		if err := rows.Scan(&d.Domain, &created, &d.Disabled); err != nil {
			return nil, err
		}
		d.Created = created.Time
		domains = append(domains, d)
	}

	return domains, rows.Err()
}

func (dao *Dao) CountAccountDomains(accountID string) (n int, err error) {
	err = dao.db.QueryRow("select count(*) from `devices` where `account_id` = ?", accountID).Scan(&n)

	return
}

// DomainAccount is who owns a registered domain: registered is false for
// a free name, accountID is empty for a domain that predates accounts.
func (dao *Dao) DomainAccount(domain string) (accountID string, registered bool, err error) {
	var id sql.NullString
	err = dao.db.QueryRow("select `account_id` from `devices` where `domain` = ?", domain).Scan(&id)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}

	return id.String, true, nil
}

// RegisterAccountDevice is RegistreDevice for a domain that belongs to an
// account (issue #124).
func (dao *Dao) RegisterAccountDevice(accountID, owner, domain, secret string) error {
	_, err := dao.db.Exec("insert into `devices` (`owner_uuid`, `domain`, `secret`, `account_id`, `created`) values (?, ?, ?, ?, ?)", owner, domain, secret, accountID, time.Now())

	return err
}

// ReplaceDeviceIdentity hands a domain to a new device: the owner uuid
// and secret the old one presented stop working the moment this commits.
// Only the owning account gets here (a lost device replaced by running
// setup again, or "new identity" on the account page).
func (dao *Dao) ReplaceDeviceIdentity(accountID, domain, owner, secret string) (ok bool, err error) {
	res, err := dao.db.Exec("update `devices` set `owner_uuid` = ?, `secret` = ? where `domain` = ? and `account_id` = ?", owner, secret, domain, accountID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()

	return n > 0, err
}

// DeleteAccountDomain releases a domain, but only the owning account's.
func (dao *Dao) DeleteAccountDomain(accountID, domain string) (ok bool, err error) {
	res, err := dao.db.Exec("delete from `devices` where `domain` = ? and `account_id` = ?", domain, accountID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err == nil && n > 0 {
		if relErr := dao.recordRelease(domain, accountID); relErr != nil {
			log.Error("could not hold released name", domain, ":", relErr)
		}
	}

	return n > 0, err
}

// cLastClientEvery is how often, at most, relayed traffic updates a
// device's last_client_at (issue #139).
const cLastClientEvery = time.Minute

// touchLastClient records that somebody reached domain through the bridge
// just now - the admin panel's "last client" column. Throttled per domain
// in memory, best effort like the traffic metrics themselves.
func (dao *Dao) touchLastClient(domain string) {
	now := time.Now()
	dao.lastClientMu.Lock()
	if dao.lastClient == nil {
		dao.lastClient = map[string]time.Time{}
	}
	if now.Sub(dao.lastClient[domain]) < cLastClientEvery {
		dao.lastClientMu.Unlock()
		return
	}
	dao.lastClient[domain] = now
	dao.lastClientMu.Unlock()
	// Written from Go in UTC, like every other time the bridge stores: the
	// driver reads datetimes back as UTC, and the database's own now() is
	// the server's local time, which need not be.
	if _, err := dao.db.Exec("update `devices` set `last_client_at` = ? where `domain` = ?", now.UTC(), domain); err != nil {
		log.Error("error recording the last client of", domain, err)
	}
}

// AdminDevice is one row of the admin panel's device list (issue #139):
// the device, who owns it, when a client last reached it through the
// bridge and how much traffic it relayed. Online is filled in by the
// admin package from the live relay pool.
type AdminDevice struct {
	Domain       string
	OwnerUuid    string
	AccountID    string
	AccountEmail string
	AccountName  string
	Disabled     bool
	Created      *time.Time
	LastClient   *time.Time
	Online       bool
	// In + out bytes: the last full clock hour plus the current one (the
	// metrics are hourly buckets), the last 24 hours and the last 30 days.
	BytesHour  int64
	BytesDay   int64
	BytesMonth int64
}

// ListAdminDevices returns a page of devices (all of accountID's when it
// is not empty) matching q in the domain, owner UUID or the owner's email
// or name, and how many match in all.
func (dao *Dao) ListAdminDevices(accountID, q string, limit, offset int) (devices []AdminDevice, total int, err error) {
	where := " where 1=1"
	args := []any{}
	if accountID != "" {
		where += " and d.`account_id` = ?"
		args = append(args, accountID)
	}
	if q != "" {
		where += " and (d.`domain` like ? or d.`owner_uuid` like ? or a.`email` like ? or concat(coalesce(a.`name`, ''), ' ', coalesce(a.`surname`, '')) like ?)"
		l := likeArg(q)
		args = append(args, l, l, l, l)
	}
	if err = dao.db.QueryRow("select count(*) from `devices` d left join `accounts` a on a.`id` = d.`account_id`"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := "select d.`domain`, d.`owner_uuid`, coalesce(d.`account_id`, ''), coalesce(a.`email`, ''), " +
		"trim(concat(coalesce(a.`name`, ''), ' ', coalesce(a.`surname`, ''))), d.`disabled`, d.`created`, d.`last_client_at`, " +
		"coalesce(sum(case when m.`hour_bucket` >= date_format(now() - interval 1 hour, '%Y-%m-%d %H:00:00') then m.`bytes_in` + m.`bytes_out` end), 0), " +
		"coalesce(sum(case when m.`hour_bucket` >= now() - interval 1 day then m.`bytes_in` + m.`bytes_out` end), 0), " +
		"coalesce(sum(m.`bytes_in` + m.`bytes_out`), 0) " +
		"from `devices` d left join `accounts` a on a.`id` = d.`account_id` " +
		"left join `device_metrics` m on m.`domain` = d.`domain` and m.`hour_bucket` >= now() - interval 30 day" +
		where +
		" group by d.`domain`, d.`owner_uuid`, d.`account_id`, a.`email`, a.`name`, a.`surname`, d.`disabled`, d.`created`, d.`last_client_at`" +
		" order by d.`domain`"
	tail, targs := pageClause(limit, offset)
	rows, err := dao.db.Query(query+tail, append(args, targs...)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	devices = []AdminDevice{}
	for rows.Next() {
		var d AdminDevice
		var created, lastClient sql.NullTime
		if err := rows.Scan(&d.Domain, &d.OwnerUuid, &d.AccountID, &d.AccountEmail, &d.AccountName, &d.Disabled,
			&created, &lastClient, &d.BytesHour, &d.BytesDay, &d.BytesMonth); err != nil {
			return nil, 0, err
		}
		if created.Valid {
			d.Created = &created.Time
		}
		if lastClient.Valid {
			d.LastClient = &lastClient.Time
		}
		devices = append(devices, d)
	}

	return devices, total, rows.Err()
}

// AllDomains lists every registered domain, for the panel's device pickers.
func (dao *Dao) AllDomains() (domains []string, err error) {
	rows, err := dao.db.Query("select `domain` from `devices` order by `domain`")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	domains = []string{}
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		domains = append(domains, d)
	}
	return domains, rows.Err()
}

// AdminAccount is one row of the admin panel's accounts list (issue #139).
// Never the password hash - only whether there is one.
type AdminAccount struct {
	ID          string
	Email       string
	Name        string
	Surname     string
	Country     string
	HasPassword bool
	Providers   []string
	Created     time.Time
	LastSeen    time.Time
	FreeUntil   time.Time
	Domains     int
}

// ListAdminAccounts returns a page of accounts, newest first, matching q
// in the name, email, country or id - or only the one with id when it is
// not empty - and how many match in all.
func (dao *Dao) ListAdminAccounts(id, q string, limit, offset int) (accounts []AdminAccount, total int, err error) {
	where := " where 1=1"
	args := []any{}
	if id != "" {
		where += " and a.`id` = ?"
		args = append(args, id)
	}
	if q != "" {
		where += " and (concat(a.`name`, ' ', a.`surname`) like ? or a.`email` like ? or a.`country` like ? or a.`id` like ?)"
		l := likeArg(q)
		args = append(args, l, l, l, l)
	}
	if err = dao.db.QueryRow("select count(*) from `accounts` a"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := "select a.`id`, a.`email`, a.`name`, a.`surname`, a.`country`, a.`password_hash` is not null, " +
		"a.`created`, a.`last_seen`, a.`free_until`, count(distinct d.`domain`), coalesce(group_concat(distinct l.`provider` order by l.`provider`), '') " +
		"from `accounts` a left join `devices` d on d.`account_id` = a.`id` left join `account_logins` l on l.`account_id` = a.`id`" +
		where +
		" group by a.`id`, a.`email`, a.`name`, a.`surname`, a.`country`, a.`password_hash`, a.`created`, a.`last_seen`, a.`free_until`" +
		" order by a.`created` desc"
	tail, targs := pageClause(limit, offset)
	rows, err := dao.db.Query(query+tail, append(args, targs...)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	accounts = []AdminAccount{}
	for rows.Next() {
		var a AdminAccount
		var providers string
		if err := rows.Scan(&a.ID, &a.Email, &a.Name, &a.Surname, &a.Country, &a.HasPassword,
			&a.Created, &a.LastSeen, &a.FreeUntil, &a.Domains, &providers); err != nil {
			return nil, 0, err
		}
		a.Providers = []string{}
		if providers != "" {
			a.Providers = strings.Split(providers, ",")
		}
		accounts = append(accounts, a)
	}

	return accounts, total, rows.Err()
}

// Issue #143: the admin panel's lists are paged and searchable.

// likeArg turns a search box's text into a LIKE pattern that matches it
// anywhere, with LIKE's own wildcards escaped so they match literally.
func likeArg(q string) string {
	r := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`)
	return "%" + r.Replace(q) + "%"
}

// pageClause is the limit/offset tail of a paged query; limit <= 0 means
// everything (an account's own devices, which are few).
func pageClause(limit, offset int) (string, []any) {
	if limit <= 0 {
		return "", nil
	}
	return " limit ? offset ?", []any{limit, offset}
}
