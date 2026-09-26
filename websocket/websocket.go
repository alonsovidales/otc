// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	crand "crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/dao"
	facerecognition "github.com/alonsovidales/otc/face_recognition"
	filesmanager "github.com/alonsovidales/otc/files_manager"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/mediastream"
	"github.com/alonsovidales/otc/network"
	"github.com/alonsovidales/otc/profile"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/push"
	"github.com/alonsovidales/otc/session"
	"github.com/alonsovidales/otc/settings"
	"github.com/alonsovidales/otc/social"
	"github.com/alonsovidales/otc/staticassets"
	"github.com/alonsovidales/otc/status"
	"github.com/alonsovidales/otc/storage"
	"github.com/alonsovidales/otc/supervisor"
	"github.com/alonsovidales/otc/tailscalefunnel"
	"github.com/alonsovidales/otc/updater"
	"github.com/google/uuid"
	gorilla "github.com/gorilla/websocket"
	"github.com/shirou/gopsutil/v4/disk"
	"google.golang.org/protobuf/proto"
)

const (
	CEndpoint        = "/ws"
	cWorkerSleepSecs = 120

	// cCodeNotAuthenticated is Ack.code on the "you have no session here"
	// reply (issue #105). Clients key their sign-out handling off this
	// rather than the prose beside it.
	cCodeNotAuthenticated = "not_authenticated"

	// Bridge connection pool. A connection is consumed for as long as the
	// bridge-side client that picked it up keeps its socket open — see
	// bridge/websocket/websocket.go's default case, which pins one pool
	// connection to a client for that client's *entire* session (every
	// message after the first reuses the same pairing), not just its first
	// request. A previous version of this comment claimed each connection
	// was released after one request; that hasn't been true since the
	// bridge started pinning connections for a whole session, and sizing
	// the pool as if it were (5 ready, assuming they'd cycle back quickly)
	// is exactly what caused "No available connections in the pool" under
	// completely ordinary use — a phone app, a Mac app, and a browser tab
	// each hold one connection for as long as they're open, not just for
	// the length of one round trip.
	//
	// Rather than dialing a fixed number once at startup and hoping it's
	// enough, this device keeps cBridgePoolTarget() ready at all times:
	// whenever the count of still-open (not yet consumed) connections
	// drops to cBridgePoolLowWater(), it opens enough more to reach target
	// again (see ensureBridgePool).
	//
	// cDefaultBridgePoolTarget is deliberately generous given the
	// per-session (not per-request) consumption above — it only costs one
	// idle websocket per spare slot. [otc] bridge-pool-target overrides it
	// for a device that legitimately needs more concurrent sessions than
	// that (a household with several people/devices talking to it through
	// the bridge at once).
	cDefaultBridgePoolTarget = 20
	cBridgePoolRefillBatch   = 4
)

// bridgeConnPool tracks this device's own accounting of its bridge
// connections — not the bridge's, which is separate, server-side state.
// available is how many of the connections this device has open right now
// are just sitting idle in the bridge's pool, not yet consumed for a
// relay; pending is how many dial+auth attempts are currently in flight
// (counted toward the target too, so a slow dial doesn't cause a second,
// redundant refill to fire before the first one even finishes).
type bridgeConnPool struct {
	mu        sync.Mutex
	available int
	pending   int
	// consecutiveFailures drives the retry backoff below. Reset to zero
	// by every successful registration, so a device that is simply
	// reconnecting after a blip pays no penalty.
	consecutiveFailures int
}

// Retry backoff for failed bridge dials/registrations (see
// failedBridgeDial). This used to be a flat 0-3s retry, forever, with no
// regard for *why* the attempt failed - and some failures are permanent:
// a subdomain claimed by another registration answers "Invalid Secret"
// every time, and a device already at the bridge's per-device connection
// cap is refused every time. Retrying those ~1.5 times a second achieves
// nothing and actively hurts: one device in that state produced 2167
// rejected registrations in three minutes, which is what kept the bridge
// pinned at its cap and starved every other device - cala among them,
// which is how this was found.
//
// Exponential with jitter instead, so a transient failure still recovers
// in about a second while a permanent one settles into a background poke
// every few minutes.
var (
	cBridgeRetryBase = time.Second
	cBridgeRetryMax  = 5 * time.Minute
)

// bridgeRetryDelay is the wait before the nth consecutive retry, capped
// and jittered (jitter matters here because a device opens its whole pool
// at once - without it, 20 connections would fail and retry in lockstep
// forever).
func bridgeRetryDelay(consecutiveFailures int) time.Duration {
	delay := cBridgeRetryBase
	for i := 0; i < consecutiveFailures && delay < cBridgeRetryMax; i++ {
		delay *= 2
	}
	if delay > cBridgeRetryMax {
		delay = cBridgeRetryMax
	}
	// Full jitter over the computed window.
	return time.Duration(rand.Float64() * float64(delay))
}

// Manager Structure that provides HTTP access to manage all the different
// groups and shards on each grorup
type Manager struct {
	baseUrl      string
	dao          *dao.Dao
	upgrader     gorilla.Upgrader
	filesManager *filesmanager.Manager
	settings     *settings.Settings
	profile      *profile.Profile
	social       *social.Social
	push         *push.Push
	bridgePool   bridgeConnPool
	// sup is nil on a spawned child (see supervisor.ChildEnvVar) - only
	// the primary instance manages users, so every user-management RPC
	// below checks this is non-nil before doing anything (see
	// ReqGetInstanceRole's own doc comment for why that's not just a
	// cosmetic UI-side check).
	sup *supervisor.Supervisor
	// staticPath (issue #95) is this instance's own built web assets
	// directory - the same one api.API serves directly over plain HTTP,
	// now also reachable over this connection via ReqGetStaticAsset so the
	// bridge can fetch a device's current assets on a browser's behalf
	// instead of keeping its own separate, driftable copy. See
	// staticassets.Resolve for the actual path-safety guarantee.
	staticPath string
	// activeConns counts real inbound client connections only (Listen,
	// below) - NOT this device's own outbound bridge-pool relay
	// connections (openBridgeConn), which use the same handleConnection
	// but aren't "usage" in the sense issue #82's per-user metrics mean.
	// Backs the primary's Users panel's "active connections" figure via
	// api's /internal/metrics endpoint.
	activeConns atomic.Int64
	// Issue #110: mints and serves the short-lived tokens behind
	// /media/<token>, the URL a video player streams from.
	media *mediastream.Server
	// Guards the one-shot thumbnail backfill (startBackfillOnce).
	backfillOnce sync.Once
}

// startBackfillOnce kicks off the missing-thumbnail repair the first
// time anyone signs in on this process. It needs the owner's key, so it
// can't run at startup, and it only ever needs to run once per process -
// see BackfillMissingThumbnails for what it repairs and why it exists.
func (mg *Manager) startBackfillOnce(ses *session.Session) {
	mg.backfillOnce.Do(func() {
		go mg.filesManager.BackfillMissingThumbnails(ses)
	})
}

// Media is read by api's /media/{token} handler.
func (mg *Manager) Media() *mediastream.Server { return mg.media }

// ActiveConnections is read by api's /internal/metrics handler.
func (mg *Manager) ActiveConnections() int64 {
	return mg.activeConns.Load()
}

func Init(baseUrl string, dao *dao.Dao, filesManager *filesmanager.Manager, sup *supervisor.Supervisor, staticPath string) (mg *Manager) {
	log.Debug("Init Websocket")
	st, err := settings.Init(dao)
	if err != nil {
		log.Fatal("Error loading the settings", err)
	}
	pr, err := profile.Init(dao, st.Domain)
	if err != nil {
		log.Fatal("Error loading the profile", err)
	}
	ps, err := push.Init(dao)
	if err != nil {
		log.Fatal("Error initializing push notifications", err)
	}
	mg = &Manager{
		baseUrl:      baseUrl,
		dao:          dao,
		filesManager: filesManager,
		sup:          sup,
		staticPath:   staticPath,
		media: mediastream.NewServer(
			mediastream.NewStore(),
			cfg.GetStr("otc", "storage-path"),
			cfg.GetStr("otc", "unenc-storage-path"),
		),
		upgrader: gorilla.Upgrader{
			// In production, set a proper origin check!
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		settings: st,
		profile:  pr,
		push:     ps,
		social:   social.Init(dao, filesManager, st, pr, ps),
	}

	// Issue #62: whenever this device's own Push instance (used for social
	// notifications) prunes a stale subscription/token, re-sync the
	// bridge's copy too, so it doesn't keep a dead entry around between
	// explicit register/unregister calls. Async: sendWebPush/sendApns run
	// from the background friend-sync loop and shouldn't block on a
	// bridge round-trip.
	ps.OnChange = func() { go mg.syncPushRegistrationsToBridge() }
	ps.RelayAPNs = mg.relayAPNsToBridge

	// Issue #103: a local-only user's instance has no bridge-addr at all
	// (see supervisor.bridgeAddrFor), and dialing "wss:///ws" forever
	// would just be a hot retry loop against nothing. No relay configured
	// means no relay - the instance still serves normally on its own port.
	if !bridgeConfigured() {
		log.Info("no bridge-addr configured - this instance stays local-only")
	} else {
		mg.ensureBridgePool()
	}

	// Issue #62: re-sync on every startup too, not just on the next
	// register - the bridge's own copy of this device's push registrations
	// could otherwise stay stale forever after a bridge-side DB reset, or
	// simply never exist at all for a device that registered tokens before
	// this feature shipped.
	go mg.syncPushRegistrationsToBridge()

	rand.Seed(time.Now().UnixNano())

	go mg.backgroundWorker()

	return
}

// backgroundWorker Used to process all the tasks in background to sync with
// friend devices and so on
func (mg *Manager) backgroundWorker() {
	log.Debug("Background Worker")
	for true {
		// Update friendship status
		mg.social.SyncWithFriends()
		time.Sleep(time.Duration(cWorkerSleepSecs) * time.Second)
	}
}

func (mg *Manager) closeWithError(conn *gorilla.Conn, id int32, err error) {
	log.Error("closing socket with error:", err)
	// Acknoledge the authentication
	respAuth := &pb.RespEnvelope{
		Id: id,
		Payload: &pb.RespEnvelope_RespAck{
			RespAck: &pb.Ack{
				Ok:       false,
				ErrorMsg: fmt.Sprintf("Error: %s", err),
			},
		},
	}
	resp, _ := proto.Marshal(respAuth)
	if err := conn.WriteMessage(gorilla.BinaryMessage, resp); err != nil {
		log.Error("error responding, closing the connection:", err)
	}
	conn.Close()

}

// bridgePoolTarget reads [otc] bridge-pool-target, falling back to
// cDefaultBridgePoolTarget if that section/key is absent — deliberately
// optional config, not a required one, so existing deployments don't need
// an ini change just to pick up a new default. Reads the raw string and
// parses it here rather than going through cfg.GetInt: that logs an error
// on every call whenever the key is simply absent (the expected common
// case for an optional key), not only on a genuinely malformed value,
// which would otherwise spam the log every time ensureBridgePool runs.
func bridgePoolTarget() int {
	if cfg.HasSection("otc") {
		if s := cfg.GetStr("otc", "bridge-pool-target"); s != "" {
			if v, err := strconv.Atoi(s); err == nil && v > 0 {
				return v
			}
		}
	}
	return cDefaultBridgePoolTarget
}

// bridgePoolLowWater is how low (available+pending together) the pool has
// to drop before ensureBridgePool tops it back up — cBridgePoolRefillBatch
// short of the target, same reasoning as before sizing became configurable.
func bridgePoolLowWater() int {
	return bridgePoolTarget() - cBridgePoolRefillBatch
}

// ensureBridgePool tops the bridge connection pool back up to
// bridgePoolTarget() whenever it's dropped to bridgePoolLowWater() or below
// (available+pending together, so a dial already in flight counts toward
// the target and doesn't trigger a redundant second batch). Always opens
// exactly enough to reach the target, not a fixed cBridgePoolRefillBatch —
// which happens to be the same thing in the steady-state case this was
// designed around (one connection trickling down to the next at a time
// puts total at exactly the low water mark, cBridgePoolRefillBatch short
// of target), but matters at startup, where total starts at 0 and a fixed
// batch would leave the pool stuck below target forever (nothing re-checks
// until a connection already in the pool gets consumed - see
// openBridgeConn - which idle, never-consumed connections would never
// trigger on their own).
//
// Called once at startup, and again every time a connection is actually
// consumed for a relay (see openBridgeConn) — NOT on a failed dial/auth
// attempt, which schedules its own jittered retry instead (see
// failedBridgeDial) rather than competing with this for the same refill.
func (mg *Manager) ensureBridgePool() {
	target := bridgePoolTarget()
	lowWater := bridgePoolLowWater()

	mg.bridgePool.mu.Lock()
	total := mg.bridgePool.available + mg.bridgePool.pending
	var toOpen int
	if total <= lowWater {
		toOpen = target - total
		mg.bridgePool.pending += toOpen
	}
	mg.bridgePool.mu.Unlock()

	for i := 0; i < toOpen; i++ {
		go mg.openBridgeConn()
	}
}

// openBridgeConn dials, registers, and then serves exactly one bridge
// connection for its whole single-use lifetime — one client's entire
// session, not just one request (see the pool constants' doc comment
// above). The caller (ensureBridgePool, or failedBridgeDial's own
// retry) has already accounted for this attempt in pending before spawning
// it — this function's job is just to move that accounting forward
// correctly as the attempt succeeds, fails, or the connection is
// eventually consumed.
// bridgeConfigured reports whether this instance has a relay to talk to at
// all (issue #103). Empty for a local-only additional user - see
// supervisor.bridgeAddrFor - and every path that dials out has to check
// it, not just the connection pool: without this they each fail forever
// against "wss:///ws" ("dial tcp :443: connect: connection refused"),
// which is noise at best and a hot retry loop at worst.
func bridgeConfigured() bool {
	// HasSection first because cfg.GetStr is fatal when the config isn't
	// loaded at all - this predicate gets called from enough places
	// (including one that runs during Init) that it should answer "no
	// relay" rather than take the process down.
	if !cfg.HasSection("otc") {
		return false
	}
	return cfg.GetStr("otc", "bridge-addr") != ""
}

func (mg *Manager) openBridgeConn() {
	u := url.URL{Scheme: "wss", Host: cfg.GetStr("otc", "bridge-addr"), Path: "/ws"}
	log.Debug("Connecting to bridge:", cfg.GetStr("otc", "bridge-addr"), u)
	h := http.Header{}
	h.Set("Sec-WebSocket-Protocol", "protobuf")
	c, _, err := gorilla.DefaultDialer.Dial(u.String(), h)
	if err != nil {
		log.Error("dialing websocket:", err)
		mg.failedBridgeDial()
		return
	}
	log.Debug("Connected to bridge...")
	defer c.Close()

	// AUTH the connection
	msg := &pb.ReqEnvelope{
		Id: 1,
		Payload: &pb.ReqEnvelope_ReqBridgeRegister{
			ReqBridgeRegister: &pb.BridgeRegister{
				OwnerUuid: mg.settings.DeviceUuid,
				Domain:    mg.settings.Domain,
				Secret:    mg.settings.BridgeSecret,
			},
		},
	}
	b, _ := proto.Marshal(msg)
	if err := c.WriteMessage(gorilla.BinaryMessage, b); err != nil {
		log.Error("write:", err)
		mg.failedBridgeDial()
		return
	}

	// We should get back the Ack
	_, data, err := c.ReadMessage()
	if err != nil {
		log.Error("read:", err)
		mg.failedBridgeDial()
		return
	}

	var respAck pb.RespEnvelope
	if err := proto.Unmarshal(data, &respAck); err != nil {
		log.Error("error unmarshaling bridge register response:", err)
		mg.failedBridgeDial()
		return
	}

	// The bridge answers a rejected registration (e.g. a stale/mismatched
	// shared secret, or this device already at its connection cap - see
	// [bridge] max-connections-per-device on the bridge side) with
	// Error=true and no RespBridgeAckOnboard payload at all - asserting the
	// type unconditionally used to panic here and take the whole process
	// down with it (crash-looping every few seconds instead of just
	// backing off and retrying like every other failure path here).
	if respAck.Error {
		log.Error("bridge rejected registration:", respAck.ErrorMessage)
		mg.failedBridgeDial()
		return
	}
	onboard, ok := respAck.Payload.(*pb.RespEnvelope_RespBridgeAckOnboard)
	if !ok || onboard.RespBridgeAckOnboard == nil {
		log.Error("unexpected bridge register response:", respAck.Payload)
		mg.failedBridgeDial()
		return
	}
	if !onboard.RespBridgeAckOnboard.Ok {
		mg.failedBridgeDial()
		return
	}

	log.Debug("Authenticated in the bridge, waiting for messages...")
	mg.bridgePool.mu.Lock()
	mg.bridgePool.pending--
	mg.bridgePool.available++
	// A working registration clears the backoff, so the next transient
	// failure starts from a one-second retry again rather than inheriting
	// whatever penalty an earlier outage built up.
	mg.bridgePool.consecutiveFailures = 0
	mg.bridgePool.mu.Unlock()

	// Blocks for this connection's entire lifetime in the pool - returns
	// once the bridge has consumed it for its one relay (or the underlying
	// socket otherwise drops).
	mg.handleConnection(c, nil)

	mg.bridgePool.mu.Lock()
	mg.bridgePool.available--
	mg.bridgePool.mu.Unlock()
	// A connection actually being consumed is normal, expected traffic —
	// check immediately (no jitter) whether the pool needs topping up,
	// same as ensureBridgePool's other callers.
	mg.ensureBridgePool()
}

// failedBridgeDial accounts for one attempt that never became available
// (a dial error, a rejected registration, ...) and schedules its
// replacement after a random 0-3s backoff — keeping a persistent failure
// (e.g. a stale secret) from turning into a tight retry loop hammering the
// bridge, the same spirit as the jitter the old fixed-pool loop already
// had. This intentionally does NOT call ensureBridgePool: the retry below
// already re-adds exactly the one pending slot this attempt is giving up,
// so the pool's total stays correct without a second, competing refill
// decision.
func (mg *Manager) failedBridgeDial() {
	mg.bridgePool.mu.Lock()
	mg.bridgePool.pending--
	mg.bridgePool.consecutiveFailures++
	delay := bridgeRetryDelay(mg.bridgePool.consecutiveFailures)
	failures := mg.bridgePool.consecutiveFailures
	mg.bridgePool.mu.Unlock()

	if failures == 1 || failures%20 == 0 {
		log.Info("bridge dial failed (", failures, "in a row ), next retry in", delay.Round(time.Second))
	}

	go func() {
		time.Sleep(delay)
		mg.bridgePool.mu.Lock()
		mg.bridgePool.pending++
		mg.bridgePool.mu.Unlock()
		mg.openBridgeConn()
	}()
}

// regenerateBridgeSecret backs the Settings page's self-service
// "Regenerate" button (issue #40 follow-up): a short-lived, one-off dial to
// the bridge (unlike OpenBridge's long-lived pool connections) that proves
// ownership with the CURRENT secret and gets a fresh one back. The bridge
// only replaces it if the current one still matches its own record — see
// RotateSecret's compare-and-swap on the bridge side.
func (mg *Manager) regenerateBridgeSecret() (newSecret string, err error) {
	if !bridgeConfigured() {
		return "", errors.New("this instance has no bridge access, so there is no bridge secret to regenerate")
	}
	u := url.URL{Scheme: "wss", Host: cfg.GetStr("otc", "bridge-addr"), Path: "/ws"}
	h := http.Header{}
	h.Set("Sec-WebSocket-Protocol", "protobuf")
	c, _, err := gorilla.DefaultDialer.Dial(u.String(), h)
	if err != nil {
		return "", fmt.Errorf("dialing bridge: %w", err)
	}
	defer c.Close()

	msg := &pb.ReqEnvelope{
		Id: 1,
		Payload: &pb.ReqEnvelope_ReqRotateBridgeSecret{
			ReqRotateBridgeSecret: &pb.RotateBridgeSecret{
				OwnerUuid: mg.settings.DeviceUuid,
				Domain:    mg.settings.Domain,
				Secret:    mg.settings.BridgeSecret,
			},
		},
	}
	b, err := proto.Marshal(msg)
	if err != nil {
		return "", err
	}
	if err := c.WriteMessage(gorilla.BinaryMessage, b); err != nil {
		return "", fmt.Errorf("writing to bridge: %w", err)
	}

	_, data, err := c.ReadMessage()
	if err != nil {
		return "", fmt.Errorf("reading from bridge: %w", err)
	}
	var resp pb.RespEnvelope
	if err := proto.Unmarshal(data, &resp); err != nil {
		return "", err
	}
	if resp.Error {
		return "", errors.New(resp.ErrorMessage)
	}
	ack, ok := resp.Payload.(*pb.RespEnvelope_RespRotateBridgeSecretAck)
	if !ok || ack.RespRotateBridgeSecretAck == nil {
		return "", errors.New("unexpected bridge response")
	}

	return ack.RespRotateBridgeSecretAck.NewSecret, nil
}

// syncPushRegistrationsToBridge sends this device's current, full set of
// push registrations to the bridge (issue #62: alert the owner if this
// device goes unreachable) — see push's own package doc for why the bridge
// needs a copy of this at all, and its own doc comment on why it's always
// the complete current set rather than an incremental diff. A one-off
// connection, same pattern as regenerateBridgeSecret above, not a pooled
// relay connection — this has nothing to do with client traffic.
//
// Fire-and-forget: called from a goroutine by every caller below, since a
// failed sync isn't worth slowing down (or failing) whatever
// register/startup path triggered it, and there's nowhere better to
// surface the error to — the next successful sync (the very next
// register, or this device's own next restart) naturally catches the
// bridge back up.
// relayAPNsToBridge asks the bridge to deliver an iOS push to this device's
// own registered phones - see BridgeNotify's doc comment in messages.proto
// for why the key never lives here and why no tokens are sent. Same one-off
// connection pattern as syncPushRegistrationsToBridge below. Returns
// whether the bridge accepted it; false lets push fall back (to nothing,
// on a device without its own key - the intended state).
func (mg *Manager) relayAPNsToBridge(title, body string) bool {
	if !bridgeConfigured() {
		return false
	}
	u := url.URL{Scheme: "wss", Host: cfg.GetStr("otc", "bridge-addr"), Path: "/ws"}
	h := http.Header{}
	h.Set("Sec-WebSocket-Protocol", "protobuf")
	c, _, err := gorilla.DefaultDialer.Dial(u.String(), h)
	if err != nil {
		log.Error("error dialing bridge to relay a push:", err)
		return false
	}
	defer c.Close()

	b, err := proto.Marshal(&pb.ReqEnvelope{
		Id: 1,
		Payload: &pb.ReqEnvelope_ReqBridgeNotify{
			ReqBridgeNotify: &pb.BridgeNotify{
				OwnerUuid: mg.settings.DeviceUuid,
				Domain:    mg.settings.Domain,
				Secret:    mg.settings.BridgeSecret,
				Title:     title,
				Body:      body,
			},
		},
	})
	if err != nil {
		log.Error("error marshaling push relay:", err)
		return false
	}
	if err := c.WriteMessage(gorilla.BinaryMessage, b); err != nil {
		log.Error("error writing push relay to bridge:", err)
		return false
	}
	_, data, err := c.ReadMessage()
	if err != nil {
		log.Error("error reading push relay ack from bridge:", err)
		return false
	}
	var resp pb.RespEnvelope
	if err := proto.Unmarshal(data, &resp); err != nil {
		log.Error("error decoding push relay ack:", err)
		return false
	}
	if resp.Error {
		log.Error("bridge refused to relay a push:", resp.ErrorMessage)
		return false
	}
	ack, ok := resp.Payload.(*pb.RespEnvelope_RespBridgeNotifyAck)
	return ok && ack.RespBridgeNotifyAck.Ok
}

func (mg *Manager) syncPushRegistrationsToBridge() {
	// Nothing to sync push registrations *to* on a local-only instance,
	// and this runs on every push-subscription change - so without this it
	// logs a failed dial every time (caught live on a local-only user).
	if !bridgeConfigured() {
		return
	}
	apnsTokens, err := mg.dao.ListApnsTokens()
	if err != nil {
		log.Error("error listing APNs tokens for bridge push-registrations sync:", err)
		return
	}
	webPushSubs, err := mg.dao.ListWebPushSubscriptions()
	if err != nil {
		log.Error("error listing web push subscriptions for bridge push-registrations sync:", err)
		return
	}
	vapidPub, vapidPriv, err := mg.dao.GetVapidKeys()
	if err != nil {
		log.Error("error loading VAPID keys for bridge push-registrations sync:", err)
		return
	}

	pbSubs := make([]*pb.WebPushSub, len(webPushSubs))
	for i, s := range webPushSubs {
		pbSubs[i] = &pb.WebPushSub{Endpoint: s.Endpoint, P256Dh: s.P256dh, Auth: s.Auth}
	}

	u := url.URL{Scheme: "wss", Host: cfg.GetStr("otc", "bridge-addr"), Path: "/ws"}
	h := http.Header{}
	h.Set("Sec-WebSocket-Protocol", "protobuf")
	c, _, err := gorilla.DefaultDialer.Dial(u.String(), h)
	if err != nil {
		log.Error("error dialing bridge for push-registrations sync:", err)
		return
	}
	defer c.Close()

	msg := &pb.ReqEnvelope{
		Id: 1,
		Payload: &pb.ReqEnvelope_ReqUpdatePushRegistrations{
			ReqUpdatePushRegistrations: &pb.UpdatePushRegistrations{
				OwnerUuid:       mg.settings.DeviceUuid,
				Domain:          mg.settings.Domain,
				Secret:          mg.settings.BridgeSecret,
				ApnsTokens:      apnsTokens,
				WebPushSubs:     pbSubs,
				VapidPublicKey:  vapidPub,
				VapidPrivateKey: vapidPriv,
			},
		},
	}
	b, err := proto.Marshal(msg)
	if err != nil {
		log.Error("error marshaling push-registrations sync:", err)
		return
	}
	if err := c.WriteMessage(gorilla.BinaryMessage, b); err != nil {
		log.Error("error writing push-registrations sync to bridge:", err)
		return
	}

	_, data, err := c.ReadMessage()
	if err != nil {
		log.Error("error reading push-registrations sync ack from bridge:", err)
		return
	}
	var resp pb.RespEnvelope
	if err := proto.Unmarshal(data, &resp); err != nil {
		log.Error("error unmarshaling push-registrations sync ack:", err)
		return
	}
	if resp.Error {
		log.Error("bridge rejected push-registrations sync:", resp.ErrorMessage)
	}
}

func (mg *Manager) Listen(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")

	conn, err := mg.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error("error trying to upgrade the websocket:", err)
		return
	}

	defer conn.Close()

	mg.activeConns.Add(1)
	defer mg.activeConns.Add(-1)

	mg.handleConnection(conn, r)
}

// connHandler holds one WebSocket connection's per-connection state.
// Requests on a connection are now processed concurrently (see
// handleConnection) rather than one at a time, so every field below that
// gets set during the connection's lifetime (as opposed to fixed at
// construction, like mg) is guarded by mu — a request reading ch.session
// while the Auth request that sets it is still in flight is a real
// possibility now, not just a theoretical one, and needs an actual
// happens-before relationship (not just "it'll usually already be set by
// the time this runs"), or it's a data race regardless of how unlikely to
// misbehave in practice.
type connHandler struct {
	mg *Manager

	// Issue #117: who is on the other end, for the password-attempt limit.
	// For a direct connection, the peer's address. For one this device
	// dialled out to the bridge, it starts empty and is filled in by the
	// bridge's BridgeClientInfo once a client has been paired with it -
	// which is the only way it can be set from the wire (fromBridge).
	remoteAddr string
	fromBridge bool

	mu            sync.RWMutex
	session       *session.Session
	friendProfile *profile.Profile
	// privKey is an ephemeral RSA keypair generated per WebSocket connection.
	// Its public half is handed out via GetPubKey/PubKey so the client can
	// RSA-OAEP encrypt the password before it crosses the bridge, which only
	// ever sees the already-encrypted ciphertext.
	privKey *rsa.PrivateKey
}

func (ch *connHandler) getSession() *session.Session {
	ch.mu.RLock()
	defer ch.mu.RUnlock()
	return ch.session
}

func (ch *connHandler) setSession(s *session.Session) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	ch.session = s
}

func (ch *connHandler) getFriendProfile() *profile.Profile {
	ch.mu.RLock()
	defer ch.mu.RUnlock()
	return ch.friendProfile
}

func (ch *connHandler) setFriendProfile(p *profile.Profile) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	ch.friendProfile = p
}

func (ch *connHandler) getPrivKey() *rsa.PrivateKey {
	ch.mu.RLock()
	defer ch.mu.RUnlock()
	return ch.privKey
}

// getOrCreatePrivKey returns this connection's keypair, generating it on
// first call. The check-then-generate-then-set has to happen under one
// lock, not as separate getPrivKey/setPrivKey calls, since two GetPubKey
// requests racing each other would otherwise both see nil and each
// generate their own keypair, with whichever sets ch.privKey last silently
// winning — the client that got the other one back would then fail every
// decrypt on this connection.
func (ch *connHandler) getOrCreatePrivKey() (*rsa.PrivateKey, error) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	if ch.privKey == nil {
		key, err := rsa.GenerateKey(crand.Reader, 2048)
		if err != nil {
			return nil, err
		}
		ch.privKey = key
	}
	return ch.privKey, nil
}

// decryptSecret decrypts an RSA-OAEP(SHA-256) ciphertext produced by a
// client using the public key returned from GetPubKey on this same
// connection. It's used for Auth.key and ChangeKey.old_key/new_key.
func (ch *connHandler) decryptSecret(ciphertext []byte) (string, error) {
	privKey := ch.getPrivKey()
	if privKey == nil {
		return "", errors.New("no public key was requested for this connection")
	}
	plain, err := rsa.DecryptOAEP(sha256.New(), crand.Reader, privKey, ciphertext, nil)
	if err != nil {
		return "", errors.New("unable to decrypt key material")
	}
	return string(plain), nil
}

// issueMediaURL backs issue #110's ReqGetMediaURL: it authorizes the
// request against this connection, then mints a short-lived token for
// exactly the file that was asked for.
//
// An empty url is a real answer, not a failure: it means "this one isn't
// worth streaming, fetch it the old way". Below MinStreamableSize the old
// way is genuinely faster - the whole file already arrives in one socket
// round trip, where streaming would spend this call as an extra round
// trip before a single byte moved.
func (ch *connHandler) issueMediaURL(req *pb.ReqGetMediaURL) (url string, size int64, mime string, expiresAtMs int64, err error) {
	res := mediastream.Resource{}

	switch {
	case req.PubUuid != "" && req.Hash != "":
		// Authorized the same way ReqGetPublicationMedia is: the
		// publication has to actually carry this hash. Without that
		// check a token could be minted for any file on the device by
		// naming its hash next to a publication the caller can see.
		pubMime, found, mErr := ch.mg.dao.PublicationFileMime(req.PubUuid, req.Hash)
		if mErr != nil || !found {
			return "", 0, "", 0, fmt.Errorf("media not found")
		}
		info, sErr := os.Stat(fmt.Sprintf("%s/%s", cfg.GetStr("otc", "unenc-storage-path"), req.Hash))
		if sErr != nil {
			return "", 0, "", 0, fmt.Errorf("media not found")
		}
		res = mediastream.Resource{
			Kind:    mediastream.KindPublicationMedia,
			PubUuid: req.PubUuid,
			Hash:    req.Hash,
			Mime:    pubMime,
			Size:    info.Size(),
		}

	case req.Path != "":
		// A library path is the owner's own file - a friend reading the
		// feed has no business reaching one, and only a real session can
		// decrypt it anyway.
		ses := ch.getSession()
		if ses == nil {
			return "", 0, "", 0, fmt.Errorf("not authenticated")
		}
		file, fErr := ch.mg.dao.GetFileByPath(req.Path)
		if fErr != nil {
			return "", 0, "", 0, fmt.Errorf("file not found")
		}
		res = mediastream.Resource{
			Kind:    mediastream.KindLibraryFile,
			Path:    req.Path,
			Hash:    file.Hash,
			Mime:    file.Mime,
			Size:    int64(file.Size),
			Decrypt: ses.Decrypt,
		}

	default:
		return "", 0, "", 0, fmt.Errorf("nothing to stream")
	}

	// Both of these answer "don't stream this, fetch it the usual way",
	// which every client already handles - an empty url is a normal
	// reply, not an error.
	if !mediastream.IsStreamable(res.Mime) {
		return "", res.Size, res.Mime, 0, nil
	}
	if res.Size < mediastream.MinStreamableSize {
		return "", res.Size, res.Mime, 0, nil
	}

	token, expiresAt, tErr := ch.mg.media.Store().Issue(res)
	if tErr != nil {
		log.Error("error issuing a media token:", tErr)
		return "", 0, "", 0, fmt.Errorf("could not prepare the stream")
	}

	// Relative on purpose: the browser is already on the device's own
	// origin (directly on the LAN, or the device's subdomain through the
	// bridge), and a relative URL is correct in both without this code
	// having to know which one it's talking to. The native apps resolve
	// it against the address they're already connected to.
	return "/media/" + token, res.Size, res.Mime, expiresAt.UnixMilli(), nil
}

func tailscaleStatusResponse(state tailscalefunnel.State, errMessage string) *pb.RespTailscaleStatus {
	return &pb.RespTailscaleStatus{
		Installed: state.Installed,
		LoggedIn:  state.LoggedIn,
		FunnelOn:  state.FunnelOn,
		PublicUrl: state.PublicURL,
		LoginUrl:  state.LoginURL,
		Error:     errMessage,
	}
}

// buildUpdateInfo answers issue #94's "is there a new version" question.
//
// A manifest that can't be reached is reported as check_error rather than
// as an RPC error: the device's own installed version and the outcome of
// any previous update are still worth showing, and "GitHub was
// unreachable just now" is a different thing from "this failed".
func buildUpdateInfo() *pb.RespUpdateInfo {
	status := updater.CurrentStatus()
	out := &pb.RespUpdateInfo{
		CurrentVersion: int32(updater.InstalledVersion()),
		State:          status.State,
		Message:        status.Message,
		LastUpdated:    status.Updated,
	}

	info, err := updater.Check()
	if err != nil {
		log.Debug("could not check for updates:", err)
		out.CheckError = err.Error()
		return out
	}

	out.LatestVersion = int32(info.LatestVersion)
	for _, release := range info.Pending {
		out.Pending = append(out.Pending, &pb.UpdateRelease{
			Version: int32(release.Version),
			Summary: release.Description,
		})
	}

	return out
}

func (ch *connHandler) processNonAuthRequest(env *pb.ReqEnvelope) (resp *pb.RespEnvelope, closeConn bool) {
	resp = &pb.RespEnvelope{
		Id: env.Id,
	}

	switch p := env.Payload.(type) {
	// Issue #95: the bridge fetches this device's own static web assets
	// through here now (over the same tunnel any other request already
	// uses) instead of keeping its own separate, driftable copy - see
	// staticassets.Resolve for the actual security guarantee. Answers
	// with the exact same generic "not found" either way (a genuinely
	// missing file or a rejected path), never anything path- or
	// filesystem-shaped, to a request that - reached through the bridge -
	// could be coming from anyone on the internet, not just this device's
	// own signed-in owner.
	case *pb.ReqEnvelope_ReqGetMediaRange:
		// Issue #110: how the bridge reads a span of a device's media on
		// a browser's behalf. Unauthenticated at this tier on purpose,
		// exactly like ReqGetStaticAsset below - the token in the
		// request IS the credential, minted over an authenticated socket
		// for one specific file and expiring on its own (see
		// mediastream). Nothing here can name a file: a caller can only
		// present a token and get back what that token was minted for.
		req := p.ReqGetMediaRange
		content, total, mime, rErr := ch.mg.media.Range(req.Token, req.Offset, req.Length)
		if rErr != nil {
			// Same flat answer for an expired token and a bad offset -
			// there's nothing an unauthenticated caller should learn
			// from the difference.
			if !errors.Is(rErr, mediastream.ErrUnknownToken) {
				log.Error("error reading media range:", rErr)
			}
			resp.Error = true
			resp.ErrorMessage = "media not found"
			break
		}
		resp.Payload = &pb.RespEnvelope_RespMediaRange{
			RespMediaRange: &pb.RespMediaRange{
				Content:   content,
				Offset:    req.Offset,
				TotalSize: total,
				Mime:      mime,
			},
		}

	case *pb.ReqEnvelope_ReqGetStaticAsset:
		path, rErr := staticassets.Resolve(ch.mg.staticPath, p.ReqGetStaticAsset.Path)
		if rErr != nil {
			resp.Error = true
			resp.ErrorMessage = "asset not found"
			break
		}
		content, rErr := os.ReadFile(path)
		if rErr != nil {
			log.Error("error reading resolved static asset:", rErr)
			resp.Error = true
			resp.ErrorMessage = "asset not found"
			break
		}
		contentType := mime.TypeByExtension(filepath.Ext(path))
		if contentType == "" {
			contentType = http.DetectContentType(content)
		}
		resp.Payload = &pb.RespEnvelope_RespStaticAsset{
			RespStaticAsset: &pb.RespStaticAsset{
				Content:     content,
				ContentType: contentType,
			},
		}

	case *pb.ReqEnvelope_ReqGetPubKey:
		privKey, err := ch.getOrCreatePrivKey()
		if err != nil {
			log.Error("error generating connection keypair:", err)
			resp.Error = true
			resp.ErrorMessage = "error generating keypair"
			break
		}

		pubDER, err := x509.MarshalPKIXPublicKey(&privKey.PublicKey)
		if err != nil {
			log.Error("error marshaling public key:", err)
			resp.Error = true
			resp.ErrorMessage = "error marshaling public key"
			break
		}

		secretDefined, err := ch.mg.dao.IsSecretDefined()
		if err != nil {
			log.Error("error checking if secret is defined:", err)
			resp.Error = true
			resp.ErrorMessage = "error checking device state"
			break
		}

		resp.Payload = &pb.RespEnvelope_RespPubKey{
			RespPubKey: &pb.PubKey{
				PublicKey:   pubDER,
				IsNewDevice: !secretDefined,
				// Issue #85: lets the pre-auth setup wizard skip the
				// storage/WiFi steps on an additional user's instance -
				// see the field's own comment in messages.proto.
				IsPrimary: ch.mg.sup != nil,
			},
		}

	case *pb.ReqEnvelope_ReqGetFriendshipStatus:
		log.Info("Getting friendship status", p.ReqGetFriendshipStatus.Domain, p.ReqGetFriendshipStatus.Secret)
		fr, err := ch.mg.social.GetFriendship(p.ReqGetFriendshipStatus.Domain, p.ReqGetFriendshipStatus.Secret)
		log.Info("Getting friendship status err:", err)
		if errors.Is(err, sql.ErrNoRows) {
			// Issue #25: "we hold nothing for you" is an answer, not a
			// failure - see FriendshipStatus.not_found.
			resp.Payload = &pb.RespEnvelope_RespFriendshipStatus{
				RespFriendshipStatus: &pb.FriendshipStatusReply{NotFound: true},
			}
		} else if err != nil {
			resp.Error = true
			resp.ErrorMessage = fmt.Sprintf("error retreiving friendship: %s", err)
		} else {
			resp.Payload = &pb.RespEnvelope_RespFriendshipStatus{
				RespFriendshipStatus: &pb.FriendshipStatusReply{
					Status: fr.Status,
				},
			}
		}

	case *pb.ReqEnvelope_ReqAuthAsFriend:
		var err error
		friendship, err := ch.mg.social.GetFriendship(
			p.ReqAuthAsFriend.Domain,
			p.ReqAuthAsFriend.Secret)

		if err != nil || friendship == nil || friendship.Status != pb.FriendShipStatus_Accepted {
			resp.Payload = &pb.RespEnvelope_RespAck{
				RespAck: &pb.Ack{
					Ok:       false,
					ErrorMsg: fmt.Sprintf("Friendship not accepted"),
				},
			}
			return resp, true
		}
		log.Info("Authenticated as friend")
		ch.setFriendProfile(profile.InitFromPb(ch.mg.dao, friendship.OriginProfile))

		resp.Payload = &pb.RespEnvelope_RespAck{
			RespAck: &pb.Ack{
				Ok: true,
			},
		}

	case *pb.ReqEnvelope_ReqFriendshipInterDelete:
		// Issue #25: the other device deleted the friendship; its secret is
		// what authorises dropping our copy (see ExternalFriendshipDelete).
		log.Info("Friendship deleted by the other side:", p.ReqFriendshipInterDelete.Domain)
		if err := ch.mg.social.ExternalFriendshipDelete(p.ReqFriendshipInterDelete.Domain, p.ReqFriendshipInterDelete.Secret); err != nil {
			resp.Payload = &pb.RespEnvelope_RespAck{
				RespAck: &pb.Ack{Ok: false, ErrorMsg: fmt.Sprintf("Error: %s", err)},
			}
			return resp, true
		}
		resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}

	case *pb.ReqEnvelope_ReqDidSendFriendshipReq:
		var err error
		friendship, err := ch.mg.social.GetFriendship(
			p.ReqDidSendFriendshipReq.Domain,
			p.ReqDidSendFriendshipReq.Secret)

		// Issue #102: this is the anti-spoofing check behind
		// ExternalFriendshipRequest - a friend's device calls back here to
		// confirm we actually hold a friendship record for its domain+secret
		// before it accepts a request claiming to be from us. The previous
		// `&&` only rejected when BOTH err was set and friendship was nil;
		// GetFriendship never actually returns any other combination today,
		// but that made the check accidentally rely on that implementation
		// detail rather than being correct on its own terms. Use `||` like
		// the equivalent ReqAuthAsFriend check above.
		if err != nil || friendship == nil {
			errMsg := "Friendship not found"
			if err != nil {
				errMsg = fmt.Sprintf("Error: %s", err)
			}
			resp.Payload = &pb.RespEnvelope_RespAck{
				RespAck: &pb.Ack{
					Ok:       false,
					ErrorMsg: errMsg,
				},
			}
			return resp, true
		}
		log.Info("Authenticated session")

		resp.Payload = &pb.RespEnvelope_RespAck{
			RespAck: &pb.Ack{
				Ok: true,
			},
		}

	case *pb.ReqEnvelope_ReqFriendshipInterRequest:
		var err error
		err = ch.mg.social.ExternalFriendshipRequest(
			p.ReqFriendshipInterRequest.Domain,
			p.ReqFriendshipInterRequest.Secret,
			p.ReqFriendshipInterRequest.OriginProfile.Name,
			p.ReqFriendshipInterRequest.OriginProfile.Text,
			p.ReqFriendshipInterRequest.OriginProfile.Image)

		if err != nil {
			resp.Payload = &pb.RespEnvelope_RespAck{
				RespAck: &pb.Ack{
					Ok:       false,
					ErrorMsg: fmt.Sprintf("Error: %s", err),
				},
			}
			return resp, true
		}
		log.Info("Authenticated session")

		resp.Payload = &pb.RespEnvelope_RespAck{
			RespAck: &pb.Ack{
				Ok: true,
			},
		}

	case *pb.ReqEnvelope_ReqAuth:
		// Issue #117: refused outright while this address is locked out,
		// before the password is even looked at.
		if retry, blocked := session.Attempts.Blocked(ch.remoteAddr); blocked {
			secs := int32(retry.Seconds() + 0.999)
			log.Info("password attempt refused, address locked out:", ch.remoteAddr, "for", retry.Round(time.Second))
			resp.Payload = &pb.RespEnvelope_RespAck{
				RespAck: &pb.Ack{
					Ok:                false,
					Code:              "too_many_attempts",
					ErrorMsg:          fmt.Sprintf("Too many attempts. Try again in %d seconds.", secs),
					RetryAfterSeconds: secs,
				},
			}
			return resp, true
		}

		key, err := ch.decryptSecret(p.ReqAuth.Key)
		if err == nil {
			var ses *session.Session
			ses, err = session.New(p.ReqAuth.Uuid, key, p.ReqAuth.Create, ch.mg.dao)
			if err == nil {
				ch.setSession(ses)
				ch.mg.startBackfillOnce(ses)
			}
		}

		if err != nil {
			// Deliberate delay on a failed auth attempt: was `time.Sleep(1)`,
			// which is 1 *nanosecond* (time.Sleep takes a Duration, i.e.
			// nanoseconds) — no throttling at all against repeated guesses.
			time.Sleep(time.Second)
			ack := &pb.Ack{Ok: false, ErrorMsg: fmt.Sprintf("Error: %s", err)}
			// Issue #117: the attempt that spends the allowance is answered
			// with the lockout itself, so the client can say when to retry.
			if locked := session.Attempts.Fail(ch.remoteAddr); locked > 0 {
				log.Info("too many failed password attempts from", ch.remoteAddr, "- locked out for", locked)
				ack.Code = "too_many_attempts"
				ack.RetryAfterSeconds = int32(locked.Seconds())
				ack.ErrorMsg = fmt.Sprintf("Too many attempts. Try again in %d seconds.", ack.RetryAfterSeconds)
			}
			resp.Payload = &pb.RespEnvelope_RespAck{RespAck: ack}
			return resp, true
		}
		session.Attempts.Reset(ch.remoteAddr)
		log.Info("Authenticated session")

		// One-off self-healing sweep for any face row written before
		// encryption-at-rest was added for it - see MigrateLegacyFace
		// Encryption's doc comment. Backgrounded: it only touches leftover
		// plaintext rows (a no-op most logins) and must never delay the
		// auth response.
		go ch.mg.filesManager.MigrateLegacyFaceEncryption(ch.getSession())

		resp.Payload = &pb.RespEnvelope_RespAck{
			RespAck: &pb.Ack{
				Ok: true,
			},
		}

	// Issue #117: the bridge telling this relay who it now serves. Only from
	// a connection this device opened to the bridge - a direct client
	// naming an address for itself is ignored, and told nothing.
	case *pb.ReqEnvelope_ReqBridgeClientInfo:
		if ch.fromBridge {
			ch.remoteAddr = p.ReqBridgeClientInfo.RemoteAddr
		} else {
			log.Info("ignoring BridgeClientInfo on a direct connection from", ch.remoteAddr)
		}
		resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}

	case *pb.ReqEnvelope_ReqAuthWithToken:
		// Issue #101: the token-redemption counterpart to ReqAuth above -
		// see session/tokens.go's doc comment for why a browser holds one
		// of these instead of the real password. RedeemToken hands back
		// the exact *Session a real password login already built, so
		// there's no vault/Argon2id work to redo here at all.
		ses, ok := session.RedeemToken(p.ReqAuthWithToken.Token)
		if !ok {
			// Deliberately does NOT close the connection, unlike ReqAuth's
			// own failure path above: an expired token is the completely
			// ordinary case here (every tab that sits untouched for an
			// hour), and the client's very next move is to fall back to
			// the sign-in form - which needs this connection to ask
			// GetPubKey/IsNewDevice over. Closing it out from under those
			// left them waiting on a response that could never arrive, and
			// the app rendered an empty page instead of the sign-in form
			// (caught live on pit). There's nothing to throttle here
			// either: unlike a guessable password, these are 256 random
			// bits.
			resp.Payload = &pb.RespEnvelope_RespAck{
				RespAck: &pb.Ack{
					Ok:       false,
					ErrorMsg: "Session expired or invalid, please sign in again.",
				},
			}
			break
		}
		ch.setSession(ses)
		ch.mg.startBackfillOnce(ses)
		log.Info("Authenticated session via token")

		resp.Payload = &pb.RespEnvelope_RespAck{
			RespAck: &pb.Ack{
				Ok: true,
			},
		}

	case *pb.ReqEnvelope_ReqDownloadSharedLink:
		log.Info("Download link")

		fileContent, err := ch.mg.filesManager.OpenSharedLink(p.ReqDownloadSharedLink.Uuid, p.ReqDownloadSharedLink.Secret)

		if err != nil {
			resp.Error = true
			resp.ErrorMessage = fmt.Sprintf("error trying to download file: %s", err)
		} else {
			resp.Payload = &pb.RespEnvelope_RespSharedFiles{
				RespSharedFiles: &pb.SharedFiles{
					Content: fileContent,
				},
			}
		}

	case *pb.ReqEnvelope_ReqGetProfile:
		log.Info("Get profile")

		resp.Payload = &pb.RespEnvelope_RespProfile{
			RespProfile: &pb.Profile{
				Name:  ch.mg.profile.Name,
				Image: ch.mg.profile.Image,
				Text:  ch.mg.profile.Text,
			},
		}

	default:
		return nil, false
	}

	return
}

func (ch *connHandler) processAuthAsFriendRequest(env *pb.ReqEnvelope) (resp *pb.RespEnvelope, closeConn bool) {
	resp = &pb.RespEnvelope{
		Id: env.Id,
	}

	switch p := env.Payload.(type) {

	case *pb.ReqEnvelope_ReqGetEvents:
		log.Info("Getting events")
		events, err := ch.mg.social.GetEvents(ch.mg.profile, p.ReqGetEvents.Since.AsTime(), p.ReqGetEvents.Total)
		if err != nil {
			resp.Error = true
			resp.ErrorMessage = fmt.Sprintf("error trying to retrieve events: %s", err)
		} else {
			resp.Payload = &pb.RespEnvelope_RespEvents{
				RespEvents: &pb.Events{
					Events: events,
				},
			}
		}

	case *pb.ReqEnvelope_ReqGetMediaUrl:
		// Issue #110: hands back a URL a video player can stream from,
		// instead of the whole file. Same tier as ReqGetPublicationMedia
		// below, because a friend watching a video in the feed needs
		// this exactly as much as the owner does - but a library path is
		// the owner's own file, so that half is session-only (checked
		// inside).
		url, size, mime, expiresAt, err := ch.issueMediaURL(p.ReqGetMediaUrl)
		if err != nil {
			resp.Error = true
			resp.ErrorMessage = err.Error()
			break
		}
		resp.Payload = &pb.RespEnvelope_RespMediaUrl{
			RespMediaUrl: &pb.RespMediaURL{
				Url:             url,
				TotalSize:       size,
				Mime:            mime,
				ExpiresAtUnixMs: expiresAt,
			},
		}

	case *pb.ReqEnvelope_ReqGetPublicationMedia:
		// Issue #107: the bytes a feed client needs to actually play a
		// video (or show a full-size image) from the timeline. Same tier
		// as ReqGetSocialPublicationFiles below - a friend reading the
		// feed needs this exactly as much as the owner does.
		req := p.ReqGetPublicationMedia
		content, mime, err := ch.mg.social.GetPublicationMedia(req.PubUuid, req.Hash)
		if err != nil {
			log.Error("error reading publication media:", err)
			resp.Error = true
			resp.ErrorMessage = "media not available"
		} else {
			resp.Payload = &pb.RespEnvelope_RespFile{
				RespFile: &pb.File{Hash: req.Hash, Mime: mime, Content: content, Size: int32(len(content))},
			}
		}

	case *pb.ReqEnvelope_ReqGetSocialPublicationFiles:
		log.Info("Getting social publication")
		files, err := ch.mg.social.GetPublicationFiles(p.ReqGetSocialPublicationFiles.Uuid)
		if err != nil {
			resp.Error = true
			resp.ErrorMessage = fmt.Sprintf("error trying to collect publications: %s", err)
		} else {
			resp.Payload = &pb.RespEnvelope_RespSocialPublicationFiles{
				RespSocialPublicationFiles: &pb.SocialPublicationFiles{
					Files: files,
				},
			}
		}

	case *pb.ReqEnvelope_ReqGetSocialPublications:
		log.Info("Getting social publications")
		publications, err := ch.mg.social.GetPublications(ch.mg.profile, p.ReqGetSocialPublications.Since.AsTime(), p.ReqGetSocialPublications.Total, ch.getFriendProfile() != nil, p.ReqGetSocialPublications.ExcludeUuids)
		if err != nil {
			resp.Error = true
			resp.ErrorMessage = fmt.Sprintf("error trying to collect publications: %s", err)
		} else {
			resp.Payload = &pb.RespEnvelope_RespSocialPublications{
				RespSocialPublications: publications,
			}
		}

	default:
		return nil, false
	}

	return
}

func (ch *connHandler) processAuthRequest(env *pb.ReqEnvelope) (resp *pb.RespEnvelope, closeConn bool) {
	resp = &pb.RespEnvelope{
		Id: env.Id,
	}
	// Read once, up front: requests on a connection are now processed
	// concurrently (see handleConnection), so ses could otherwise be
	// read multiple times as this function runs, each call independently
	// locking - fine for correctness (it's not going to change again once
	// set), but there's no reason to pay for repeated locking within a
	// single request's handling.
	ses := ch.getSession()

	switch p := env.Payload.(type) {
	case *pb.ReqEnvelope_ReqNewSocialPublication:
		log.Info("New social publication")

		uuid, err := ch.mg.social.NewPublication(ses, p.ReqNewSocialPublication.Text, p.ReqNewSocialPublication.Paths, p.ReqNewSocialPublication.Trims)

		if err != nil {
			resp.Error = true
			resp.ErrorMessage = fmt.Sprintf("error trying to create publication: %s", err)
		} else {
			resp.Payload = &pb.RespEnvelope_RespNewSocial{
				RespNewSocial: &pb.NewSocial{
					Uuid: uuid,
				},
			}
		}

	case *pb.ReqEnvelope_ReqNewSocialComment:
		log.Info("Getting social publications")
		err := ch.mg.social.NewSocialComment(ch.mg.profile, p.ReqNewSocialComment.PubUuid, p.ReqNewSocialComment.Comment)
		if err != nil {
			resp.Error = true
			resp.ErrorMessage = fmt.Sprintf("error publishing comment: %s", err)
		} else {
			resp.Payload = &pb.RespEnvelope_RespAck{
				RespAck: &pb.Ack{
					Ok: true,
				},
			}
		}

	case *pb.ReqEnvelope_ReqDelSocialPublication:
		log.Info("Deleting social publication:", p.ReqDelSocialPublication.PubUuid)
		err := ch.mg.social.DeletePublication(p.ReqDelSocialPublication.PubUuid)
		if err != nil {
			resp.Error = true
			resp.ErrorMessage = fmt.Sprintf("error deleting publication: %s", err)
		} else {
			resp.Payload = &pb.RespEnvelope_RespAck{
				RespAck: &pb.Ack{
					Ok: true,
				},
			}
		}

	case *pb.ReqEnvelope_ReqDelSocialComment:
		log.Info("Deleting social comment:", p.ReqDelSocialComment.CommentUuid)
		err := ch.mg.social.DeleteComment(p.ReqDelSocialComment.CommentUuid)
		if err != nil {
			resp.Error = true
			resp.ErrorMessage = fmt.Sprintf("error deleting comment: %s", err)
		} else {
			resp.Payload = &pb.RespEnvelope_RespAck{
				RespAck: &pb.Ack{
					Ok: true,
				},
			}
		}

	case *pb.ReqEnvelope_ReqDeleteFriendship:
		// Issue #25.
		log.Info("Delete friendship:", p.ReqDeleteFriendship.Domain)
		if err := ch.mg.social.DeleteFriendship(p.ReqDeleteFriendship.Domain); err != nil {
			resp.Error = true
			resp.ErrorMessage = fmt.Sprintf("Error trying to delete the friendship: %s", err)
		} else {
			resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}
		}

	case *pb.ReqEnvelope_ReqChangeFriendStatus:
		log.Info("Change friend status:", p.ReqChangeFriendStatus.Domain, p.ReqChangeFriendStatus.Status)
		err := ch.mg.social.ChangeFriendStatus(p.ReqChangeFriendStatus.Domain, p.ReqChangeFriendStatus.Status)
		if err != nil {
			resp.Error = true
			resp.ErrorMessage = fmt.Sprintf("Error trying to change friend status: %s", err)
		} else {
			resp.Payload = &pb.RespEnvelope_RespAck{
				RespAck: &pb.Ack{
					Ok: true,
				},
			}
		}

	case *pb.ReqEnvelope_ReqLikePublication:
		log.Info("Liking publication", ch.mg.profile.Domain, "-", p.ReqLikePublication.PubUuid)
		_, err := ch.mg.social.NewLikePublication(ch.mg.profile, p.ReqLikePublication.PubUuid)
		if err != nil {
			resp.Error = true
			resp.ErrorMessage = fmt.Sprintf("error liking publication: %s", err)
		} else {
			resp.Payload = &pb.RespEnvelope_RespAck{
				RespAck: &pb.Ack{
					Ok: true,
				},
			}
		}

	case *pb.ReqEnvelope_ReqLikeComment:
		log.Info("Liking comment", ch.mg.profile.Domain, "-", p.ReqLikeComment.CommentUuid)
		_, err := ch.mg.social.NewLikePublicationComment(ch.mg.profile, p.ReqLikeComment.CommentUuid)
		if err != nil {
			resp.Error = true
			resp.ErrorMessage = fmt.Sprintf("error liking publication comment: %s", err)
		} else {
			resp.Payload = &pb.RespEnvelope_RespAck{
				RespAck: &pb.Ack{
					Ok: true,
				},
			}
		}

	case *pb.ReqEnvelope_ReqGetPublicationLikers:
		log.Info("Getting publication likers", p.ReqGetPublicationLikers.PubUuid)
		likers, err := ch.mg.social.GetPublicationLikers(p.ReqGetPublicationLikers.PubUuid)
		if err != nil {
			resp.Error = true
			resp.ErrorMessage = fmt.Sprintf("error getting publication likers: %s", err)
		} else {
			resp.Payload = &pb.RespEnvelope_RespLikers{
				RespLikers: &pb.Likers{
					Likers: likers,
				},
			}
		}

	case *pb.ReqEnvelope_ReqGetCommentLikers:
		log.Info("Getting comment likers", p.ReqGetCommentLikers.CommentUuid)
		likers, err := ch.mg.social.GetCommentLikers(p.ReqGetCommentLikers.CommentUuid)
		if err != nil {
			resp.Error = true
			resp.ErrorMessage = fmt.Sprintf("error getting comment likers: %s", err)
		} else {
			resp.Payload = &pb.RespEnvelope_RespLikers{
				RespLikers: &pb.Likers{
					Likers: likers,
				},
			}
		}

	case *pb.ReqEnvelope_ReqFriendshipsList:
		log.Info("Friendship list request")
		friendships, err := ch.mg.social.GetFriendships()
		if err != nil {
			resp.Error = true
			resp.ErrorMessage = fmt.Sprintf("Error reading friendships: %s", err)
		} else {
			log.Debug("Sending back list of frnedships", len(friendships))
			resp.Payload = &pb.RespEnvelope_RespFriendships{
				RespFriendships: &pb.Friendships{
					Friendships: friendships,
				},
			}
		}

	case *pb.ReqEnvelope_ReqFriendshipRequest:
		log.Info("Friendship request:", p.ReqFriendshipRequest.Domain)
		err := ch.mg.social.SendFriendshipReq(p.ReqFriendshipRequest.Domain)
		if err != nil {
			resp.Error = true
			resp.ErrorMessage = fmt.Sprintf("Error requesting friendship: %s", err)
		} else {
			resp.Payload = &pb.RespEnvelope_RespAck{
				RespAck: &pb.Ack{
					Ok: true,
				},
			}
		}

	case *pb.ReqEnvelope_ReqShareFilesLink:
		log.Info("Sharing files with path:", p.ReqShareFilesLink.Paths)
		link, err := ch.mg.filesManager.GetSharedLink(ses, p.ReqShareFilesLink.Paths, ch.mg.settings.Domain)
		if err != nil {
			resp.Error = true
			resp.ErrorMessage = fmt.Sprintf("error creating files share link: %s", err)
		} else {
			resp.Payload = &pb.RespEnvelope_RespShareLink{
				RespShareLink: &pb.ShareLink{
					Link: link,
				},
			}
		}

	case *pb.ReqEnvelope_ReqUploadFile:
		log.Info("Uploading file with path:", p.ReqUploadFile.Path)
		pbFile, err := ch.mg.filesManager.UploadFile(ses, p.ReqUploadFile.Path, p.ReqUploadFile.Content, p.ReqUploadFile.ForceOverride, p.ReqUploadFile.Created, p.ReqUploadFile.Modified, p.ReqUploadFile.CloudId)
		if err != nil {
			resp.Error = true
			resp.ErrorMessage = fmt.Sprintf("error trying to upload file: %s", err)
		} else {
			resp.Payload = &pb.RespEnvelope_RespFile{
				RespFile: pbFile,
			}
		}

	case *pb.ReqEnvelope_ReqHasFile:
		exists, err := ch.mg.filesManager.HasFile(p.ReqHasFile.Hash, p.ReqHasFile.CloudId)
		if err != nil {
			resp.Error = true
			resp.ErrorMessage = fmt.Sprintf("error checking file hash: %s", err)
		} else {
			resp.Payload = &pb.RespEnvelope_RespFileExists{
				RespFileExists: &pb.FileExists{Exists: exists},
			}
		}

	case *pb.ReqEnvelope_ReqHasCloudIds:
		found, err := ch.mg.filesManager.HasCloudIDs(p.ReqHasCloudIds.CloudIds)
		if err != nil {
			resp.Error = true
			resp.ErrorMessage = fmt.Sprintf("error checking cloud ids: %s", err)
		} else {
			out := &pb.CloudIdsFound{}
			for id, hash := range found {
				out.Files = append(out.Files, &pb.CloudIdFile{CloudId: id, Hash: hash})
			}
			resp.Payload = &pb.RespEnvelope_RespCloudIdsFound{RespCloudIdsFound: out}
		}

	case *pb.ReqEnvelope_ReqLinkFile:
		log.Info("Linking file with path:", p.ReqLinkFile.Path, "to hash:", p.ReqLinkFile.Hash)
		pbFile, err := ch.mg.filesManager.LinkFile(ses, p.ReqLinkFile.Path, p.ReqLinkFile.Hash, p.ReqLinkFile.ForceOverride, p.ReqLinkFile.Created, p.ReqLinkFile.Modified, p.ReqLinkFile.CloudId)
		if err != nil {
			resp.Error = true
			resp.ErrorMessage = fmt.Sprintf("error trying to link file: %s", err)
		} else {
			resp.Payload = &pb.RespEnvelope_RespFile{
				RespFile: pbFile,
			}
		}

	case *pb.ReqEnvelope_ReqGetFile:
		log.Info("Get file with path:", p.ReqGetFile.Path)
		pbFile, err := ch.mg.filesManager.GetFile(ses, p.ReqGetFile.Path, p.ReqGetFile.Hash)
		if err != nil {
			resp.Error = true
			resp.ErrorMessage = fmt.Sprintf("error trying to retrieve file: %s", err)
		} else {
			resp.Payload = &pb.RespEnvelope_RespFile{
				RespFile: pbFile,
			}
		}

	// Issue #41: "More info" in the photo gallery — camera/EXIF metadata
	// computed live from the file's own bytes, nothing persisted.
	case *pb.ReqEnvelope_ReqGetFileInfo:
		log.Info("Get file info with path:", p.ReqGetFileInfo.Path)
		info, err := ch.mg.filesManager.GetFileInfo(ses, p.ReqGetFileInfo.Path)
		if err != nil {
			resp.Error = true
			resp.ErrorMessage = fmt.Sprintf("error trying to retrieve file info: %s", err)
		} else {
			resp.Payload = &pb.RespEnvelope_RespFileInfo{
				RespFileInfo: info,
			}
		}

	case *pb.ReqEnvelope_ReqDelFile:
		log.Info("Del file by path:", p.ReqDelFile.Path)
		// Issue #116: a directory path deletes everything under it.
		err := ch.mg.filesManager.DelPath(ses, p.ReqDelFile.Path)
		if err != nil {
			// This used to only ever reach the client, never the server's
			// own log — tracking down a real "Delete failed" report meant
			// searching the log for an error that was never actually
			// written there, only sent over the wire.
			log.Error("error trying to delete file:", p.ReqDelFile.Path, err)
			resp.Error = true
			resp.ErrorMessage = fmt.Sprintf("error trying to delete file: %s", err)
			if errors.Is(err, filesmanager.ErrUploadOnly) {
				resp.ErrorCode = "upload_only"
				resp.ErrorMessage = err.Error()
			}
		} else {
			log.Info("Deleted file by path:", p.ReqDelFile.Path)
			// Acknoledge the Deletion
			resp.Payload = &pb.RespEnvelope_RespAck{
				RespAck: &pb.Ack{
					Ok: true,
				},
			}
		}

	// Issue #132: upload-only folders and the versions they keep.
	case *pb.ReqEnvelope_ReqSetUploadOnly:
		log.Info("Set upload only:", p.ReqSetUploadOnly.Path, p.ReqSetUploadOnly.UploadOnly)
		if err := ch.mg.filesManager.SetUploadOnly(p.ReqSetUploadOnly.Path, p.ReqSetUploadOnly.UploadOnly); err != nil {
			resp.Error = true
			resp.ErrorMessage = fmt.Sprintf("error updating the folder: %s", err)
		} else {
			resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}
		}

	case *pb.ReqEnvelope_ReqListFileVersions:
		versions, err := ch.mg.filesManager.FileVersions(p.ReqListFileVersions.Path)
		if err != nil {
			resp.Error = true
			resp.ErrorMessage = fmt.Sprintf("error listing the versions: %s", err)
		} else {
			resp.Payload = &pb.RespEnvelope_RespFileVersions{RespFileVersions: &pb.FileVersions{Versions: versions}}
		}

	case *pb.ReqEnvelope_ReqListFiles:
		log.Info("List of file by path:", p.ReqListFiles.Path, p.ReqListFiles.Recursive)
		files, err := ch.mg.filesManager.ListFiles(ses, p.ReqListFiles.Path, p.ReqListFiles.Recursive)
		if err != nil {
			log.Error("error trying to list files:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			log.Debug("Files to return:", len(files))

			resp.Payload = &pb.RespEnvelope_RespListOfFiles{
				RespListOfFiles: &pb.ListOfFiles{
					Files: files,
				},
			}
		}

	case *pb.ReqEnvelope_ReqGetTags:
		log.Info("Get Tags")
		tags, err := ch.mg.dao.GetTags()
		if err != nil {
			log.Error("error trying to get tags:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			log.Debug("Available tags:", len(tags))

			resp.Payload = &pb.RespEnvelope_RespTagsList{
				RespTagsList: &pb.TagsList{
					Tags: tags,
				},
			}
		}

	case *pb.ReqEnvelope_ReqSearchPhotos:
		log.Info("Search by text:", p.ReqSearchPhotos.Tags)
		// Issue #77: the date scrubber's "jump to date" - Before is only
		// set while dragging the scrubber, nil the rest of the time.
		var before *time.Time
		if p.ReqSearchPhotos.Before != nil {
			t := p.ReqSearchPhotos.Before.AsTime()
			before = &t
		}
		files, token, err := ch.mg.filesManager.ImageSearch(ses, "", p.ReqSearchPhotos.Tags, p.ReqSearchPhotos.Token, p.ReqSearchPhotos.IncludeVideos, p.ReqSearchPhotos.PersonIds, p.ReqSearchPhotos.GroupId, before, p.ReqSearchPhotos.Have)
		if err != nil {
			log.Error("error trying to list files:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			log.Debug("Files to return:", len(files))

			resp.Payload = &pb.RespEnvelope_RespListOfFiles{
				RespListOfFiles: &pb.ListOfFiles{
					Files: files,
					Token: token,
				},
			}
		}

	case *pb.ReqEnvelope_ReqPhotoDateBuckets:
		buckets, err := ch.mg.filesManager.PhotoDateBuckets(p.ReqPhotoDateBuckets.Tags, p.ReqPhotoDateBuckets.PersonIds, p.ReqPhotoDateBuckets.GroupId, p.ReqPhotoDateBuckets.IncludeVideos)
		if err != nil {
			log.Error("error trying to compute photo date buckets:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			pbBuckets := make([]*pb.PhotoDateBucket, len(buckets))
			for i, b := range buckets {
				pbBuckets[i] = &pb.PhotoDateBucket{Month: b.Month, Count: int32(b.Count)}
			}
			resp.Payload = &pb.RespEnvelope_RespPhotoDateBuckets{
				RespPhotoDateBuckets: &pb.RespPhotoDateBuckets{Buckets: pbBuckets},
			}
		}

	// Issue #78: notifications.
	case *pb.ReqEnvelope_ReqListNotifications:
		limit := int(p.ReqListNotifications.Limit)
		if limit <= 0 {
			limit = 50
		}
		notifications, err := ch.mg.social.ListNotifications(limit)
		if err != nil {
			log.Error("error listing notifications:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			resp.Payload = &pb.RespEnvelope_RespNotifications{
				RespNotifications: &pb.RespNotifications{Notifications: notifications},
			}
		}

	case *pb.ReqEnvelope_ReqGetNotificationCount:
		count, err := ch.mg.dao.UnacknowledgedNotificationCount()
		if err != nil {
			log.Error("error counting unacknowledged notifications:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			resp.Payload = &pb.RespEnvelope_RespNotificationCount{
				RespNotificationCount: &pb.RespNotificationCount{UnacknowledgedCount: int32(count)},
			}
		}

	case *pb.ReqEnvelope_ReqMarkNotificationsAcknowledged:
		if err := ch.mg.dao.MarkAllNotificationsAcknowledged(); err != nil {
			log.Error("error acknowledging notifications:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}
		}

	case *pb.ReqEnvelope_ReqGetPublication:
		pub, err := ch.mg.social.GetPublication(ch.mg.profile, p.ReqGetPublication.PubUuid)
		if err != nil {
			log.Error("error fetching publication:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			resp.Payload = &pb.RespEnvelope_RespPublication{
				RespPublication: &pb.RespPublication{Publication: pub},
			}
		}

	// Issue #82: multiple users on one device - every case below except
	// ReqGetInstanceRole refuses to act unless ch.mg.sup is set (this is
	// the primary instance). See supervisor's own package doc for why a
	// child never even attempts to run one of these itself.

	case *pb.ReqEnvelope_ReqGetInstanceRole:
		resp.Payload = &pb.RespEnvelope_RespInstanceRole{
			RespInstanceRole: &pb.RespInstanceRole{IsPrimary: ch.mg.sup != nil},
		}

	case *pb.ReqEnvelope_ReqListUsers:
		if ch.mg.sup == nil {
			resp.Error = true
			resp.ErrorMessage = "not available on this instance"
			break
		}
		users, err := ch.mg.dao.ListUsers()
		if err != nil {
			log.Error("error listing users:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			resp.Payload = &pb.RespEnvelope_RespUsers{RespUsers: &pb.RespUsers{Users: users}}
		}

	case *pb.ReqEnvelope_ReqCreateUser:
		if ch.mg.sup == nil {
			resp.Error = true
			resp.ErrorMessage = "not available on this instance"
			break
		}
		users, err := ch.createUser(p.ReqCreateUser)
		if err != nil {
			log.Error("error creating user:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			resp.Payload = &pb.RespEnvelope_RespUsers{RespUsers: &pb.RespUsers{Users: users}}
		}

	case *pb.ReqEnvelope_ReqDeleteUser:
		if ch.mg.sup == nil {
			resp.Error = true
			resp.ErrorMessage = "not available on this instance"
			break
		}
		if err := ch.deleteUser(p.ReqDeleteUser); err != nil {
			log.Error("error deleting user:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}
		}

	case *pb.ReqEnvelope_ReqSetUserActive:
		if ch.mg.sup == nil {
			resp.Error = true
			resp.ErrorMessage = "not available on this instance"
			break
		}
		if err := ch.setUserActive(p.ReqSetUserActive); err != nil {
			log.Error("error setting user active flag:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}
		}

	case *pb.ReqEnvelope_ReqGetUserMetrics:
		if ch.mg.sup == nil {
			resp.Error = true
			resp.ErrorMessage = "not available on this instance"
			break
		}
		metrics, err := ch.getUserMetrics(p.ReqGetUserMetrics.Uuid)
		if err != nil {
			log.Error("error fetching user metrics:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			resp.Payload = &pb.RespEnvelope_RespUserMetrics{RespUserMetrics: metrics}
		}

	case *pb.ReqEnvelope_ReqGetStatus:
		log.Info(fmt.Sprintf("Requested status %v", p))
		st, err := status.GetStatus()

		if err != nil {
			log.Error("error trying to retrive status:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			log.Debug("Current status:", st)

			resp.Payload = &pb.RespEnvelope_RespStatus{
				RespStatus: st,
			}
		}

	case *pb.ReqEnvelope_ReqChangeKey:
		log.Info(fmt.Sprintf("Change key %v", p))
		oldKey, err := ch.decryptSecret(p.ReqChangeKey.OldKey)
		if err == nil {
			var newKey string
			newKey, err = ch.decryptSecret(p.ReqChangeKey.NewKey)
			if err == nil {
				err = ses.ChangeKey(oldKey, newKey)
			}
		}

		if err != nil {
			log.Error("error trying to change secret key:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			resp.Payload = &pb.RespEnvelope_RespAck{
				RespAck: &pb.Ack{
					Ok: true,
				},
			}
		}

	case *pb.ReqEnvelope_ReqIssueSessionToken:
		// Issue #101: called right after a password-based Auth, after a
		// ChangeKey (ses itself is unaffected by a password change - it
		// re-wraps the same vault secret, see Session.ChangeKey - so the
		// existing token remains just as valid, but the client mints a
		// fresh one anyway since ChangeKey is exactly when the old
		// savePersistedKey(newKey) used to run), and after redeeming a
		// token, to keep rotating a fresh one into the client's storage.
		token, expiresAt, err := session.IssueToken(ses)
		if err != nil {
			log.Error("error issuing session token:", err)
			resp.Error = true
			resp.ErrorMessage = "error issuing session token"
			break
		}
		resp.Payload = &pb.RespEnvelope_RespSessionToken{
			RespSessionToken: &pb.RespSessionToken{
				SessionToken: &pb.SessionToken{
					Token:           token,
					ExpiresAtUnixMs: expiresAt.UnixMilli(),
				},
			},
		}

	case *pb.ReqEnvelope_ReqRevokeSessionToken:
		session.RevokeAllTokensFor(ses)
		resp.Payload = &pb.RespEnvelope_RespAck{
			RespAck: &pb.Ack{
				Ok: true,
			},
		}

	case *pb.ReqEnvelope_ReqSetSettings:
		log.Info("Set settings")
		err := ch.mg.settings.SetSettings(p.ReqSetSettings.Domain)

		if err != nil {
			log.Error("error trying to update domain:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			resp.Payload = &pb.RespEnvelope_RespAck{
				RespAck: &pb.Ack{
					Ok: true,
				},
			}
		}

	// Issue #40: update the bridge shared secret from the setup section,
	// independent of the domain (see SetBridgeSecret's proto comment for
	// why these two used to be — and no longer are — coupled).
	case *pb.ReqEnvelope_ReqSetBridgeSecret:
		log.Info("Set bridge secret")
		secret := p.ReqSetBridgeSecret.Secret
		if secret == "" {
			resp.Error = true
			resp.ErrorMessage = "secret cannot be empty"
			break
		}
		if err := ch.mg.settings.SetBridgeSecret(secret); err != nil {
			log.Error("error trying to update bridge secret:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			resp.Payload = &pb.RespEnvelope_RespAck{
				RespAck: &pb.Ack{
					Ok: true,
				},
			}
		}

	// Self-service "Regenerate": asks the bridge itself for a fresh secret
	// (authenticated by the current one — see regenerateBridgeSecret) and
	// persists it, rather than the device inventing one locally that the
	// bridge would just reject.
	case *pb.ReqEnvelope_ReqRegenerateBridgeSecret:
		log.Info("Regenerate bridge secret")
		newSecret, err := ch.mg.regenerateBridgeSecret()
		if err != nil {
			log.Error("error regenerating bridge secret:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
			break
		}
		if err := ch.mg.settings.SetBridgeSecret(newSecret); err != nil {
			log.Error("error persisting regenerated bridge secret:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
			break
		}
		resp.Payload = &pb.RespEnvelope_RespSettings{
			RespSettings: &pb.Settings{
				Domain:                 ch.mg.settings.Domain,
				BridgeSecret:           ch.mg.settings.BridgeSecret,
				FaceRecognitionEnabled: ch.mg.settings.FaceRecognitionEnabled,
			},
		}

	case *pb.ReqEnvelope_ReqSetProfile:
		log.Info("Set profile")
		err := ch.mg.profile.SetProfile(p.ReqSetProfile.Name, p.ReqSetProfile.Image, p.ReqSetProfile.Text)

		if err != nil {
			log.Error("error trying to get serrings:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			resp.Payload = &pb.RespEnvelope_RespAck{
				RespAck: &pb.Ack{
					Ok: true,
				},
			}
		}

	case *pb.ReqEnvelope_ReqGetSettings:
		log.Info("Get settings")

		resp.Payload = &pb.RespEnvelope_RespSettings{
			RespSettings: &pb.Settings{
				Domain:                 ch.mg.settings.Domain,
				BridgeSecret:           ch.mg.settings.BridgeSecret,
				FaceRecognitionEnabled: ch.mg.settings.FaceRecognitionEnabled,
			},
		}

	// Issue #52: face recognition ("People" search), humans only.
	case *pb.ReqEnvelope_ReqSetFaceRecognitionEnabled:
		log.Info("Set face recognition enabled:", p.ReqSetFaceRecognitionEnabled.Enabled)
		if err := ch.mg.settings.SetFaceRecognitionEnabled(p.ReqSetFaceRecognitionEnabled.Enabled); err != nil {
			log.Error("error trying to update face_recognition_enabled:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			resp.Payload = &pb.RespEnvelope_RespAck{
				RespAck: &pb.Ack{Ok: true},
			}
		}

	case *pb.ReqEnvelope_ReqListPeople:
		people, err := ch.mg.dao.ListPeople(facerecognition.SamePersonThreshold)
		if err != nil {
			log.Error("error listing people:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			// CoverThumbnail comes back from the DB encrypted at rest (see
			// files_manager.processFaces) - decrypt it here, same as
			// GetThumbnail does for a regular file, before it ever reaches
			// the wire.
			for _, person := range people {
				if len(person.CoverThumbnail) == 0 {
					continue
				}
				plain, err := ses.Decrypt(person.CoverThumbnail)
				if err != nil {
					log.Error("error decrypting a person's cover thumbnail:", err)
					person.CoverThumbnail = nil
					continue
				}
				person.CoverThumbnail = plain
			}
			resp.Payload = &pb.RespEnvelope_RespPeople{
				RespPeople: &pb.People{People: people},
			}
		}

	// Issue #115: image groups (albums).
	case *pb.ReqEnvelope_ReqListImageGroups:
		groups, err := ch.mg.filesManager.ListImageGroups(ses)
		if err != nil {
			log.Error("error listing image groups:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			resp.Payload = &pb.RespEnvelope_RespImageGroups{
				RespImageGroups: &pb.ImageGroups{Groups: groups},
			}
		}

	case *pb.ReqEnvelope_ReqCreateImageGroup:
		name := strings.TrimSpace(p.ReqCreateImageGroup.Name)
		log.Info("Create image group:", name)
		if name == "" {
			resp.Error = true
			resp.ErrorMessage = "a group needs a name"
			break
		}
		id, err := ch.mg.dao.CreateImageGroup(name)
		if err == nil {
			err = ch.mg.dao.AddPathsToImageGroup(id, p.ReqCreateImageGroup.Paths)
		}
		if err != nil {
			log.Error("error creating image group:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			// Answered with the group as the list would show it, so a
			// client can drop it straight into its own list.
			groups, _ := ch.mg.filesManager.ListImageGroups(ses)
			created := &pb.ImageGroup{Id: id, Name: name, FileCount: int32(len(p.ReqCreateImageGroup.Paths))}
			for _, g := range groups {
				if g.Id == id {
					created = g
					break
				}
			}
			resp.Payload = &pb.RespEnvelope_RespImageGroup{
				RespImageGroup: &pb.RespImageGroup{Group: created},
			}
		}

	case *pb.ReqEnvelope_ReqAddToImageGroup:
		log.Info("Add to image group:", p.ReqAddToImageGroup.GroupId, len(p.ReqAddToImageGroup.Paths))
		if err := ch.mg.dao.AddPathsToImageGroup(p.ReqAddToImageGroup.GroupId, p.ReqAddToImageGroup.Paths); err != nil {
			log.Error("error adding to image group:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}
		}

	case *pb.ReqEnvelope_ReqRenameImageGroup:
		name := strings.TrimSpace(p.ReqRenameImageGroup.Name)
		log.Info("Rename image group:", p.ReqRenameImageGroup.Id)
		if name == "" {
			resp.Error = true
			resp.ErrorMessage = "a group needs a name"
			break
		}
		if err := ch.mg.dao.RenameImageGroup(p.ReqRenameImageGroup.Id, name); err != nil {
			log.Error("error renaming image group:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}
		}

	case *pb.ReqEnvelope_ReqDeleteImageGroup:
		log.Info("Delete image group:", p.ReqDeleteImageGroup.Id)
		if err := ch.mg.dao.DeleteImageGroup(p.ReqDeleteImageGroup.Id); err != nil {
			log.Error("error deleting image group:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}
		}

	case *pb.ReqEnvelope_ReqRenamePerson:
		log.Info("Rename person:", p.ReqRenamePerson.Id)
		if err := ch.mg.dao.RenamePerson(p.ReqRenamePerson.Id, p.ReqRenamePerson.Name); err != nil {
			log.Error("error renaming person:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			resp.Payload = &pb.RespEnvelope_RespAck{
				RespAck: &pb.Ack{Ok: true},
			}
		}

	case *pb.ReqEnvelope_ReqDeletePerson:
		log.Info("Delete person:", p.ReqDeletePerson.Id)
		if err := ch.mg.dao.DeletePerson(p.ReqDeletePerson.Id); err != nil {
			log.Error("error deleting person:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			resp.Payload = &pb.RespEnvelope_RespAck{
				RespAck: &pb.Ack{Ok: true},
			}
		}

	case *pb.ReqEnvelope_ReqMergePeople:
		log.Info("Merge people:", p.ReqMergePeople.SourceIds, "into", p.ReqMergePeople.TargetId)
		if err := ch.mg.dao.MergePeople(p.ReqMergePeople.TargetId, p.ReqMergePeople.SourceIds); err != nil {
			log.Error("error merging people:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			resp.Payload = &pb.RespEnvelope_RespAck{
				RespAck: &pb.Ack{Ok: true},
			}
		}

	case *pb.ReqEnvelope_ReqStartReprocess:
		log.Info("Start reprocess")
		if err := ch.mg.filesManager.Reprocess(ses, p.ReqStartReprocess.ForceRestart); err != nil {
			log.Error("error starting reprocess:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			resp.Payload = &pb.RespEnvelope_RespAck{
				RespAck: &pb.Ack{Ok: true},
			}
		}

	case *pb.ReqEnvelope_ReqGetReprocessStatus:
		status, total, processed, err := ch.mg.filesManager.ReprocessStatus()
		if err != nil {
			log.Error("error reading reprocess status:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			resp.Payload = &pb.RespEnvelope_RespReprocessStatus{
				RespReprocessStatus: &pb.ReprocessStatus{
					Status:    status,
					Total:     total,
					Processed: processed,
				},
			}
		}

	case *pb.ReqEnvelope_ReqStopReprocess:
		log.Info("Stop reprocess")
		ch.mg.filesManager.CancelReprocess()
		resp.Payload = &pb.RespEnvelope_RespAck{
			RespAck: &pb.Ack{Ok: true},
		}

	case *pb.ReqEnvelope_ReqListStorageDevices:
		// Issue #85: primary-only, like the setup RPCs it feeds - a
		// child instance has no business enumerating the machine's
		// hardware, and its wizard never asks.
		if ch.mg.sup == nil {
			resp.Error = true
			resp.ErrorMessage = "not available on this instance"
			break
		}
		// Issue #38/#39: read-only disk enumeration, safe to run directly —
		// see storage.ListDevices's doc comment for why the actual
		// (destructive) setup step isn't wired up here yet.
		devices, err := storage.ListDevices()
		if err != nil {
			log.Error("error listing storage devices:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
			break
		}

		pbDevices := make([]*pb.StorageDevice, len(devices))
		for i, d := range devices {
			pbDevices[i] = &pb.StorageDevice{
				Path:      d.Path,
				SizeBytes: d.SizeBytes,
				Model:     d.Model,
			}
		}
		resp.Payload = &pb.RespEnvelope_RespStorageDevices{
			RespStorageDevices: &pb.StorageDevices{
				Devices: pbDevices,
			},
		}

	case *pb.ReqEnvelope_ReqGetTailscaleStatus:
		// Issue #80: machine-level like the update and storage RPCs
		// around it - Funnel publishes this whole machine, which is not
		// an additional user's (issue #82) to turn on. It is also the
		// reason Funnel mode is single-user: one machine, one public
		// name, no subdomains to hand out.
		if ch.mg.sup == nil {
			resp.Error = true
			resp.ErrorMessage = "not available on this instance"
			break
		}
		resp.Payload = &pb.RespEnvelope_RespTailscaleStatus{
			RespTailscaleStatus: tailscaleStatusResponse(tailscalefunnel.Status(), ""),
		}

	case *pb.ReqEnvelope_ReqSetupTailscale:
		if ch.mg.sup == nil {
			resp.Error = true
			resp.ErrorMessage = "not available on this instance"
			break
		}
		var state tailscalefunnel.State
		var err error
		if p.ReqSetupTailscale.Enable {
			state, err = tailscalefunnel.Enable(p.ReqSetupTailscale.AuthKey)
		} else {
			err = tailscalefunnel.Disable()
			state = tailscalefunnel.Status()
		}
		errMessage := ""
		if err != nil {
			log.Error("error configuring tailscale funnel:", err)
			errMessage = err.Error()
		}
		resp.Payload = &pb.RespEnvelope_RespTailscaleStatus{
			RespTailscaleStatus: tailscaleStatusResponse(state, errMessage),
		}

	case *pb.ReqEnvelope_ReqCheckUpdate:
		// Issue #94: machine-level, so primary-only for the same reason
		// ReqSetupStorage below is - an additional user (issue #82) is a
		// separate process sharing this one machine, and the binary and
		// schema it runs on are not that user's to replace.
		if ch.mg.sup == nil {
			resp.Error = true
			resp.ErrorMessage = "not available on this instance"
			break
		}
		resp.Payload = &pb.RespEnvelope_RespUpdateInfo{RespUpdateInfo: buildUpdateInfo()}

	case *pb.ReqEnvelope_ReqApplyUpdate:
		if ch.mg.sup == nil {
			resp.Error = true
			resp.ErrorMessage = "not available on this instance"
			break
		}
		if err := updater.Apply(); err != nil {
			log.Error("error starting the device update:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
			break
		}
		resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}

	case *pb.ReqEnvelope_ReqSetupStorage:
		// Issue #85: machine-level, so primary-only. An additional user
		// (issue #82) is a separate process sharing this one physical
		// machine - its disks, its network - none of which is that user's
		// to touch. The wizard that calls these is hidden on a child
		// instance now, but hiding UI is not a control: these are
		// authenticated RPCs any signed-in sub-user could send directly.
		// ReqSetupStorage wipes and reformats the selected disks, and
		// ReqSetWifi drops whatever connection the machine currently has -
		// so an unguarded pair let any account the owner handed out
		// destroy the owner's storage or take the device off the network.
		if ch.mg.sup == nil {
			resp.Error = true
			resp.ErrorMessage = "not available on this instance"
			break
		}
		// DESTRUCTIVE on anything selected (wipes/formats those disks) once
		// applied — but this service can't do that itself (see storage
		// package doc comment on why it's unprivileged), so this just hands
		// the request off to scripts/raid_watch.py's root-owned service,
		// which picks it up and does the actual work in the background.
		log.Info("Setup storage:", p.ReqSetupStorage.DevicePaths)
		err := storage.RequestSetup(p.ReqSetupStorage.DevicePaths)
		if err != nil {
			log.Error("error requesting storage setup:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			resp.Payload = &pb.RespEnvelope_RespAck{
				RespAck: &pb.Ack{
					Ok: true,
				},
			}
		}

	case *pb.ReqEnvelope_ReqListWifiNetworks:
		// Issue #85: primary-only, like the setup RPCs it feeds - a
		// child instance has no business enumerating the machine's
		// hardware, and its wizard never asks.
		if ch.mg.sup == nil {
			resp.Error = true
			resp.ErrorMessage = "not available on this instance"
			break
		}
		// Issue #38: safe, read-only scan — see network.ListNetworks's
		// doc comment for why joining is handled differently.
		networks, err := network.ListNetworks()
		if err != nil {
			log.Error("error listing wifi networks:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
			break
		}
		pbNetworks := make([]*pb.WifiNetwork, len(networks))
		for i, n := range networks {
			pbNetworks[i] = &pb.WifiNetwork{
				Ssid:    n.SSID,
				Signal:  n.Signal,
				Secured: n.Secured,
			}
		}
		resp.Payload = &pb.RespEnvelope_RespWifiNetworks{
			RespWifiNetworks: &pb.WifiNetworks{
				Networks: pbNetworks,
			},
		}

	case *pb.ReqEnvelope_ReqSetWifi:
		// Issue #85: machine-level, so primary-only. An additional user
		// (issue #82) is a separate process sharing this one physical
		// machine - its disks, its network - none of which is that user's
		// to touch. The wizard that calls these is hidden on a child
		// instance now, but hiding UI is not a control: these are
		// authenticated RPCs any signed-in sub-user could send directly.
		// ReqSetupStorage wipes and reformats the selected disks, and
		// ReqSetWifi drops whatever connection the machine currently has -
		// so an unguarded pair let any account the owner handed out
		// destroy the owner's storage or take the device off the network.
		if ch.mg.sup == nil {
			resp.Error = true
			resp.ErrorMessage = "not available on this instance"
			break
		}
		// DESTRUCTIVE to whatever network connection this device currently
		// has — joining a new WiFi network drops any existing one. Handed
		// off to network_setup.py the same way storage setup is; see
		// network.RequestJoin's doc comment for why.
		log.Info("Request wifi join:", p.ReqSetWifi.Ssid)
		err := network.RequestJoin(p.ReqSetWifi.Ssid, p.ReqSetWifi.Password)
		if err != nil {
			log.Error("error requesting wifi join:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			resp.Payload = &pb.RespEnvelope_RespAck{
				RespAck: &pb.Ack{
					Ok: true,
				},
			}
		}

	case *pb.ReqEnvelope_ReqGetVapidPublicKey:
		resp.Payload = &pb.RespEnvelope_RespVapidPublicKey{
			RespVapidPublicKey: &pb.VapidPublicKey{
				Key: ch.mg.push.VapidPublicKey(),
			},
		}

	case *pb.ReqEnvelope_ReqRegisterWebPush:
		log.Info("Register web push subscription")
		err := ch.mg.dao.SaveWebPushSubscription(
			p.ReqRegisterWebPush.Endpoint,
			p.ReqRegisterWebPush.P256Dh,
			p.ReqRegisterWebPush.Auth,
		)
		if err != nil {
			log.Error("error registering web push subscription:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}
			// Issue #62: the bridge needs its own copy of this to be able
			// to alert the owner if this device ever goes unreachable -
			// see syncPushRegistrationsToBridge's own doc comment.
			go ch.mg.syncPushRegistrationsToBridge()
		}

	// Issue #131: Log Out / Sign Out forget the device, so the device (and
	// the bridge, through the sync) forgets the client.
	case *pb.ReqEnvelope_ReqUnregisterApnsToken:
		log.Info("Unregister APNs token")
		if err := ch.mg.dao.DeleteApnsToken(p.ReqUnregisterApnsToken.Token); err != nil {
			log.Error("error unregistering APNs token:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}
			go ch.mg.syncPushRegistrationsToBridge()
		}

	case *pb.ReqEnvelope_ReqUnregisterWebPush:
		log.Info("Unregister web push subscription")
		if err := ch.mg.dao.DeleteWebPushSubscription(p.ReqUnregisterWebPush.Endpoint); err != nil {
			log.Error("error unregistering web push subscription:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}
			go ch.mg.syncPushRegistrationsToBridge()
		}

	case *pb.ReqEnvelope_ReqRegisterApnsToken:
		log.Info("Register APNs token")
		err := ch.mg.dao.SaveApnsToken(p.ReqRegisterApnsToken.Token)
		if err != nil {
			log.Error("error registering APNs token:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}
			go ch.mg.syncPushRegistrationsToBridge()
		}

	default:
		// A request this build has no case for is, in practice, an app
		// newer than the device: the proto field is unknown here, so the
		// oneof decodes as nothing. Say so in words the owner can act on -
		// the fix is the Update button - rather than a bare "unknown
		// payload" that reads like a bug.
		log.Error("unknown payload (the client is probably newer than this device):", p)
		resp.Error = true
		resp.ErrorCode = "unknown_payload"
		resp.ErrorMessage = "This device does not understand that request: its software is older than the app. Check for updates in Settings."
	}

	return
}

// usernameRe matches scripts/install.sh's own subdomain validation
// (^[a-z0-9-]+$) - a new user's username doubles as its bridge subdomain
// prefix (see createUser), so it has to satisfy the same constraint.
var usernameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// createUser (issue #82) orchestrates provisioning a brand new user end
// to end: validate, generate identity, create its database (dao.
// ProvisionUserDatabase also seeds its settings row), create its storage
// directory, record the row, and spawn it immediately (ch.mg.sup.SpawnNow)
// so the admin sees it come up live rather than waiting for the next
// restart. Returns the full updated user list on success, mirroring how
// most "created a resource" RPCs in this file reply with the resource
// itself rather than a bare Ack.
func (ch *connHandler) createUser(req *pb.ReqCreateUser) ([]*pb.User, error) {
	username := req.Username
	if !usernameRe.MatchString(username) {
		return nil, errors.New("username must be lowercase letters, digits, or hyphens")
	}
	if taken, err := ch.mg.dao.IsUsernameTaken(username); err != nil {
		return nil, err
	} else if taken {
		return nil, errors.New("username already taken")
	}

	port := int(req.Port)
	if port == 0 {
		var err error
		if port, err = ch.mg.dao.NextFreePort(8081); err != nil {
			return nil, err
		}
	} else if inUse, err := ch.mg.dao.IsPortInUse(port); err != nil {
		return nil, err
	} else if inUse {
		return nil, fmt.Errorf("port %d is already in use by another user", port)
	}

	id := uuid.New().String()
	dbName := "otc_" + strings.ReplaceAll(id, "-", "")
	dbPass, err := supervisor.GenerateSecret(24)
	if err != nil {
		return nil, err
	}
	bridgeSecret, err := supervisor.GenerateSecret(24)
	if err != nil {
		return nil, err
	}
	supervisorToken, err := supervisor.GenerateSecret(24)
	if err != nil {
		return nil, err
	}
	storagePath := "/mnt/storage/user_" + id
	subdomain := username + "." + cfg.GetStr("otc", "bridge-addr")

	// Issue #103: ask the bridge whether this subdomain is actually
	// claimable *before* provisioning a database, a storage directory and
	// a process for it. A name already registered by anything else can
	// never be taken by this user (the bridge rejects a mismatched
	// owner/secret rather than adopting it), so without this the panel
	// reports a cheerful "user created" for an account that is silently
	// unreachable forever - which is exactly how this was found on a live
	// device. Refusing up front also leaves nothing to clean up.
	if req.RequestBridgeAccess {
		free, err := isSubdomainFreeOnBridge(ch.mg.dao, subdomain)
		if err != nil {
			return nil, fmt.Errorf("could not reach the bridge to check %s, so this user was not created - try again in a moment, or uncheck \"request bridge access\" to create a local-only account: %w", subdomain, err)
		}
		if !free {
			// Both suggestions have to be things the person reading this
			// can actually do. "Free that subdomain first" was neither:
			// releasing a registration needs access to the bridge's own
			// admin panel, which a device owner doesn't have - so it read
			// as "your fault, unfixable". Choosing another name, or
			// creating the account without bridge access, are both
			// entirely in their hands.
			return nil, fmt.Errorf("%s is already taken on the bridge - pick a different username, or uncheck \"request bridge access\" to create this user for your own network only", subdomain)
		}
	}

	if err := dao.ProvisionUserDatabase(dbName, dbName, dbPass, id, subdomain, bridgeSecret); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(storagePath+"/unencrypted", 0750); err != nil {
		return nil, fmt.Errorf("creating storage directory: %w", err)
	}

	u := dao.User{
		Uuid: id, Username: username, Port: port,
		DbName: dbName, DbPass: dbPass,
		StoragePath: storagePath, Subdomain: subdomain,
		BridgeSecret: bridgeSecret, SupervisorToken: supervisorToken,
		Active: true,
		// Issue #103: persisted, because it decides whether this user's
		// own config gets a bridge-addr on every respawn - not just once
		// at creation (see supervisor.renderUserConfig).
		BridgeAccess: req.RequestBridgeAccess,
	}
	if err := ch.mg.dao.CreateUser(u); err != nil {
		return nil, err
	}

	internal, err := ch.mg.dao.GetUserInternal(id)
	if err != nil {
		log.Error("created user", username, "but could not immediately spawn it:", err)
	} else {
		ch.mg.sup.SpawnNow(internal)
	}

	return ch.mg.dao.ListUsers()
}

// deleteUser (issue #82) requires confirm_username to match the target
// user's actual username server-side too - never trusts the client-side
// confirmation UI alone, since this deletes a whole database and storage
// directory with no way back. Stops the process FIRST and waits for it to
// actually exit (ch.mg.sup.Stop blocks) before touching its database or
// files, so nothing is still holding either open mid-delete.
func (ch *connHandler) deleteUser(req *pb.ReqDeleteUser) error {
	u, err := ch.mg.dao.GetUserInternal(req.Uuid)
	if err != nil {
		return err
	}
	if req.ConfirmUsername != u.Username {
		return errors.New("confirmation username does not match")
	}

	if err := ch.mg.sup.Stop(u.Uuid); err != nil {
		return fmt.Errorf("could not stop %s's process: %w", u.Username, err)
	}
	if err := dao.DropUserDatabase(u.DbName, u.DbName); err != nil {
		return err
	}
	if err := os.RemoveAll(u.StoragePath); err != nil {
		log.Error("could not remove storage directory for", u.Username, ":", err)
	}
	if err := os.RemoveAll(supervisor.UserHome(u.Uuid)); err != nil {
		log.Error("could not remove config directory for", u.Username, ":", err)
	}
	return ch.mg.dao.DeleteUserRow(u.Uuid)
}

// setUserActive (issue #90) is deleteUser's reversible sibling - disabling
// stops that user's process right away (same supervisor.Stop as delete
// uses), rather than just flipping a flag the supervisor would only ever
// notice next time that process happened to restart on its own. Enabling
// spawns a fresh process for it immediately.
func (ch *connHandler) setUserActive(req *pb.ReqSetUserActive) error {
	if req.Active {
		if err := ch.mg.dao.ReactivateUser(req.Uuid); err != nil {
			return err
		}
		u, err := ch.mg.dao.GetUserInternal(req.Uuid)
		if err != nil {
			return err
		}
		ch.mg.sup.SpawnNow(u)
		notifyBridgeDisabled(u, false)
		return nil
	}

	u, err := ch.mg.dao.GetUserInternal(req.Uuid)
	if err != nil {
		return err
	}
	if err := ch.mg.sup.Stop(u.Uuid); err != nil {
		return fmt.Errorf("could not stop %s's process: %w", u.Username, err)
	}
	if err := ch.mg.dao.DeactivateUser(req.Uuid); err != nil {
		return err
	}
	notifyBridgeDisabled(u, true)
	return nil
}

// notifyBridgeDisabled (issue #93) tells the bridge whether u's own domain
// should be treated as disabled - a one-off dial using u's own identity
// (owner_uuid/domain/secret - what u's own process would use to register
// with the bridge itself), not this primary's, same shape as
// regenerateBridgeSecret/syncPushRegistrationsToBridge above. Best-effort:
// logged, never returned to the Settings caller - the enable/disable
// itself already fully succeeded locally (the process really is stopped/
// started) by the time this runs, and failing the whole request over a
// bridge notification alone would be worse than a stale bridge-side flag,
// which self-corrects the next time this is called either way.
// isSubdomainFreeOnBridge asks the bridge whether candidate is still
// unclaimed (issue #103), authenticating as *this* device with its own
// registration - the only credentials the primary has, and the reason the
// bridge will answer at all (see ReqIsDomainAvailable's doc comment).
//
// Returns an error rather than a bare false whenever the answer isn't
// known - the bridge being unreachable, a malformed reply, a rejected
// secret. The caller refuses to create the user in that case: the entire
// point is never to hand someone an account that looks fine and silently
// isn't, and "I could not check" is not "it is free".
func isSubdomainFreeOnBridge(d *dao.Dao, candidate string) (bool, error) {
	subDomain, deviceUuid, bridgeSecret, err := d.GetSettings()
	if err != nil {
		return false, fmt.Errorf("reading this device's own bridge identity: %w", err)
	}

	addr := url.URL{Scheme: "wss", Host: cfg.GetStr("otc", "bridge-addr"), Path: "/ws"}
	h := http.Header{}
	h.Set("Sec-WebSocket-Protocol", "protobuf")
	c, _, err := gorilla.DefaultDialer.Dial(addr.String(), h)
	if err != nil {
		return false, fmt.Errorf("could not reach the bridge: %w", err)
	}
	defer c.Close()

	msg := &pb.ReqEnvelope{
		Id: 1,
		Payload: &pb.ReqEnvelope_ReqIsDomainAvailable{
			ReqIsDomainAvailable: &pb.ReqIsDomainAvailable{
				OwnerUuid:       deviceUuid,
				Domain:          subDomain,
				Secret:          bridgeSecret,
				CandidateDomain: candidate,
			},
		},
	}
	b, err := proto.Marshal(msg)
	if err != nil {
		return false, err
	}
	if err := c.WriteMessage(gorilla.BinaryMessage, b); err != nil {
		return false, fmt.Errorf("asking the bridge: %w", err)
	}

	_, data, err := c.ReadMessage()
	if err != nil {
		return false, fmt.Errorf("reading the bridge's answer: %w", err)
	}
	var resp pb.RespEnvelope
	if err := proto.Unmarshal(data, &resp); err != nil {
		return false, fmt.Errorf("unreadable answer from the bridge: %w", err)
	}
	if resp.Error {
		return false, errors.New(resp.ErrorMessage)
	}
	avail, ok := resp.Payload.(*pb.RespEnvelope_RespDomainAvailable)
	if !ok {
		return false, errors.New("unexpected answer from the bridge")
	}
	return avail.RespDomainAvailable.Available, nil
}

func notifyBridgeDisabled(u *dao.UserInternal, disabled bool) {
	addr := url.URL{Scheme: "wss", Host: cfg.GetStr("otc", "bridge-addr"), Path: "/ws"}
	h := http.Header{}
	h.Set("Sec-WebSocket-Protocol", "protobuf")
	c, _, err := gorilla.DefaultDialer.Dial(addr.String(), h)
	if err != nil {
		log.Error("error dialing bridge to update disabled state for", u.Subdomain, ":", err)
		return
	}
	defer c.Close()

	msg := &pb.ReqEnvelope{
		Id: 1,
		Payload: &pb.ReqEnvelope_ReqSetDeviceDisabled{
			ReqSetDeviceDisabled: &pb.ReqSetDeviceDisabled{
				OwnerUuid: u.Uuid,
				Domain:    u.Subdomain,
				Secret:    u.BridgeSecret,
				Disabled:  disabled,
			},
		},
	}
	b, err := proto.Marshal(msg)
	if err != nil {
		log.Error("error marshaling disabled-state update:", err)
		return
	}
	if err := c.WriteMessage(gorilla.BinaryMessage, b); err != nil {
		log.Error("error writing disabled-state update to bridge:", err)
		return
	}

	_, data, err := c.ReadMessage()
	if err != nil {
		log.Error("error reading disabled-state update response:", err)
		return
	}
	var resp pb.RespEnvelope
	if err := proto.Unmarshal(data, &resp); err != nil {
		log.Error("bad proto from bridge for disabled-state update:", err)
		return
	}
	if resp.Error {
		log.Error("bridge rejected disabled-state update for", u.Subdomain, ":", resp.ErrorMessage)
	}
}

// getUserMetrics (issue #82) combines a direct DB read (storage - each
// user's own files_manager already tracks file sizes, no filesystem walk
// needed) with a local-only HTTP call to that user's own running process
// (active connections - see api's /internal/metrics, gated on this user's
// own supervisor_token). A user that isn't currently running just answers
// with 0 active connections rather than an error - storage usage is still
// meaningful for an inactive/stopped user.
func (ch *connHandler) getUserMetrics(uuidStr string) (*pb.RespUserMetrics, error) {
	u, err := ch.mg.dao.GetUserInternal(uuidStr)
	if err != nil {
		return nil, err
	}

	mb, err := dao.UserStorageUsageMB(u.DbName)
	if err != nil {
		return nil, err
	}

	total, err := disk.Usage(cfg.GetStr("otc", "storage-path"))
	var pct float64
	if err == nil && total.Total > 0 {
		pct = (mb * 1024 * 1024) / float64(total.Total) * 100
	}

	active := int32(0)
	client := http.Client{Timeout: 2 * time.Second}
	httpReq, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/internal/metrics", u.Port), nil)
	httpReq.Header.Set("X-Supervisor-Token", u.SupervisorToken)
	if httpResp, err := client.Do(httpReq); err == nil {
		defer httpResp.Body.Close()
		var body struct {
			ActiveConnections int32 `json:"active_connections"`
		}
		if json.NewDecoder(httpResp.Body).Decode(&body) == nil {
			active = body.ActiveConnections
		}
	}

	return &pb.RespUserMetrics{StorageMb: mb, StoragePct: pct, ActiveConnections: active}, nil
}

// processMessage dispatches one request to the pre-auth, friend-auth, or
// owner-auth handler as appropriate, recovering from any panic along the
// way. Without this, a bug in any single request handler (a nil dereference
// on a malformed/unexpected request, say) would crash this goroutine
// unrecovered — which takes down the entire process, for every connected
// client, not just this one. Recovering here contains that to "this one
// connection gets closed", regardless of what request type or handler bug
// causes it in the future.
// notAuthenticatedResponse is what a request gets when no handler in the
// chain would take it because this connection has no session.
//
// The Ack payload carries the same weight as issue #56's
// unreachable/disabled answers: a bare top-level error is dropped by both
// clients' handshake handling, and this one has to be *acted on* rather
// than printed - it's how a browser finds out mid-session that its session
// is gone (the device restarted, discarding every session token - see
// session/tokens.go). Without it the app sits on a view whose data will
// now never load, saying nothing at all (issue #105).
func notAuthenticatedResponse(id int32) *pb.RespEnvelope {
	return &pb.RespEnvelope{
		Id:           id,
		Error:        true,
		ErrorMessage: "not authenticated",
		Payload: &pb.RespEnvelope_RespAck{
			RespAck: &pb.Ack{Ok: false, ErrorMsg: "not authenticated", Code: cCodeNotAuthenticated},
		},
	}
}

func (ch *connHandler) processMessage(env *pb.ReqEnvelope) (resp *pb.RespEnvelope, closeConn bool) {
	defer func() {
		if r := recover(); r != nil {
			log.Error("recovered from panic handling request:", r, string(debug.Stack()))
			resp = &pb.RespEnvelope{
				Id:           env.Id,
				Error:        true,
				ErrorMessage: "internal error",
			}
			closeConn = true
		}
	}()

	resp, closeConn = ch.processNonAuthRequest(env)

	// Read once rather than call ch.getSession()/ch.getFriendProfile()
	// separately in each condition below — both would still be internally
	// consistent (neither is ever cleared once set), but there's no reason
	// to lock twice over what's logically one check.
	ses := ch.getSession()
	friendProfile := ch.getFriendProfile()

	if resp == nil && (ses != nil || friendProfile != nil) {
		resp, closeConn = ch.processAuthAsFriendRequest(env)
	}

	if resp == nil && ses != nil {
		resp, closeConn = ch.processAuthRequest(env)
	}

	return
}

// handleConnection reads one connection's requests in a single loop (a
// gorilla *websocket.Conn only tolerates one concurrent reader) but hands
// each one off to its own goroutine for actual processing and replying, so
// one slow request (a big GetFile needing a HEIC decode/re-encode, a large
// ListFiles, ...) can't stall unrelated ones behind it - the earlier
// strictly-one-at-a-time version meant something as cheap as GetFileInfo
// could sit queued behind whatever slower request happened to arrive just
// before it, on the very same connection, entirely unrelated in practice.
// Responses can therefore come back in a different order than their
// requests were sent, which is fine: every response carries the request's
// own id, and every client already correlates by id rather than by order.
//
// Writes are serialized with writeMu regardless (gorilla tolerates only one
// concurrent writer too, same as one reader); closeOnce+closeConn ensure
// whichever goroutine first decides the connection should end is the only
// one that actually closes it, so a second goroutine finishing around the
// same time doesn't double-close or race the first's close against its own
// write; wg makes sure this function doesn't return - and so doesn't let a
// caller's deferred conn.Close() run out from under a still-writing
// goroutine - until every in-flight request has actually finished.
// clientAddr is the address a direct connection is limited by (issue
// #117). X-Forwarded-For is believed only when the connection itself comes
// from this machine - Tailscale Funnel terminates TLS in tailscaled and
// forwards from loopback with the real client in that header - and never
// from a LAN peer, which could otherwise pick a fresh "address" per guess.
func clientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if fwd := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); fwd != "" {
			return fwd
		}
	}
	return host
}

func (mg *Manager) handleConnection(conn *gorilla.Conn, r *http.Request) {
	ch := &connHandler{
		mg:         mg,
		fromBridge: r == nil,
	}
	if r != nil {
		ch.remoteAddr = clientAddr(r)
	}

	var writeMu sync.Mutex
	var wg sync.WaitGroup
	var closeOnce sync.Once
	closeConn := func() {
		closeOnce.Do(func() {
			conn.Close()
		})
	}
	// However this loop exits, wait for every goroutine it started before
	// returning - handleConnection returning is what lets a caller's own
	// deferred conn.Close() (Listen) or its equivalent (openBridgeConn)
	// run, and this connection needs to stay open and writable until then.
	defer wg.Wait()

	for {
		log.Debug("Waiting for messages")
		_, frame, err := conn.ReadMessage()
		if err != nil {
			log.Error("error processing message:", err)
			return
		}

		var env pb.ReqEnvelope
		if err := proto.Unmarshal(frame, &env); err != nil {
			log.Error("bad proto:", err)
			return
		}

		wg.Add(1)
		// Pass env by pointer, not value: pb.ReqEnvelope embeds a
		// protobuf-runtime sync.Mutex (its lazy-marshal state), and copying
		// that by value into the goroutine's argument is exactly the kind
		// of lock-copying go vet's copylocks check exists to catch. env
		// itself is a fresh local per loop iteration, so &env is safe to
		// hand off - no aliasing with the next iteration's env.
		go func(env *pb.ReqEnvelope) {
			defer wg.Done()

			resp, doClose := ch.processMessage(env)

			// A request needing a session this connection doesn't have (not
			// authenticated, or authenticated as the wrong kind of session
			// for this request) used to fall through here with resp still
			// nil, which marshals to an empty message. Clients have no way
			// to tell that apart from "no response yet" — the request
			// they're awaiting just hangs forever instead of failing. Send
			// a real error instead.
			if resp == nil {
				resp = notAuthenticatedResponse(env.Id)
			}

			respBin, _ := proto.Marshal(resp)

			writeMu.Lock()
			writeErr := conn.WriteMessage(gorilla.BinaryMessage, respBin)
			writeMu.Unlock()

			if writeErr != nil {
				log.Error("error responding, closing the connection:", writeErr)
				closeConn()
				return
			}
			if doClose {
				closeConn()
			}
		}(&env)
	}
}
