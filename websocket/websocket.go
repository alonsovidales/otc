// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	crand "crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/dao"
	facerecognition "github.com/alonsovidales/otc/face_recognition"
	filesmanager "github.com/alonsovidales/otc/files_manager"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/network"
	"github.com/alonsovidales/otc/profile"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/push"
	"github.com/alonsovidales/otc/session"
	"github.com/alonsovidales/otc/settings"
	"github.com/alonsovidales/otc/social"
	"github.com/alonsovidales/otc/status"
	"github.com/alonsovidales/otc/storage"
	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

const (
	CEndpoint        = "/ws"
	cWorkerSleepSecs = 120

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
}

func Init(baseUrl string, dao *dao.Dao, filesManager *filesmanager.Manager) (mg *Manager) {
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

	mg.ensureBridgePool()

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
	mg.bridgePool.mu.Unlock()

	go func() {
		d := (3 * rand.Float64()) * float64(time.Second)
		time.Sleep(time.Duration(d))
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
func (mg *Manager) syncPushRegistrationsToBridge() {
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

func (ch *connHandler) processNonAuthRequest(env *pb.ReqEnvelope) (resp *pb.RespEnvelope, closeConn bool) {
	resp = &pb.RespEnvelope{
		Id: env.Id,
	}

	switch p := env.Payload.(type) {
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
			},
		}

	case *pb.ReqEnvelope_ReqGetFriendshipStatus:
		log.Info("Getting friendship status", p.ReqGetFriendshipStatus.Domain, p.ReqGetFriendshipStatus.Secret)
		fr, err := ch.mg.social.GetFriendship(p.ReqGetFriendshipStatus.Domain, p.ReqGetFriendshipStatus.Secret)
		log.Info("Getting friendship status err:", err)
		if err != nil {
			resp.Error = true
			resp.ErrorMessage = fmt.Sprintf("error retreiving friendship: %s", err)
		} else {
			resp.Payload = &pb.RespEnvelope_RespFriendshipStatus{
				RespFriendshipStatus: &pb.FriendshipStatus{
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

	case *pb.ReqEnvelope_ReqDidSendFriendshipReq:
		var err error
		friendship, err := ch.mg.social.GetFriendship(
			p.ReqDidSendFriendshipReq.Domain,
			p.ReqDidSendFriendshipReq.Secret)

		if err != nil && friendship == nil {
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
		key, err := ch.decryptSecret(p.ReqAuth.Key)
		if err == nil {
			var ses *session.Session
			ses, err = session.New(p.ReqAuth.Uuid, key, p.ReqAuth.Create, ch.mg.dao)
			if err == nil {
				ch.setSession(ses)
			}
		}

		if err != nil {
			// Deliberate delay on a failed auth attempt: was `time.Sleep(1)`,
			// which is 1 *nanosecond* (time.Sleep takes a Duration, i.e.
			// nanoseconds) — no throttling at all against repeated guesses.
			time.Sleep(time.Second)
			resp.Payload = &pb.RespEnvelope_RespAck{
				RespAck: &pb.Ack{
					Ok:       false,
					ErrorMsg: fmt.Sprintf("Error: %s", err),
				},
			}
			return resp, true
		}
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

		uuid, err := ch.mg.social.NewPublication(ses, p.ReqNewSocialPublication.Text, p.ReqNewSocialPublication.Paths)

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
		pbFile, err := ch.mg.filesManager.UploadFile(ses, p.ReqUploadFile.Path, p.ReqUploadFile.Content, p.ReqUploadFile.ForceOverride, p.ReqUploadFile.Created)
		if err != nil {
			resp.Error = true
			resp.ErrorMessage = fmt.Sprintf("error trying to upload file: %s", err)
		} else {
			resp.Payload = &pb.RespEnvelope_RespFile{
				RespFile: pbFile,
			}
		}

	case *pb.ReqEnvelope_ReqHasFile:
		exists, err := ch.mg.filesManager.HasFile(p.ReqHasFile.Hash)
		if err != nil {
			resp.Error = true
			resp.ErrorMessage = fmt.Sprintf("error checking file hash: %s", err)
		} else {
			resp.Payload = &pb.RespEnvelope_RespFileExists{
				RespFileExists: &pb.FileExists{Exists: exists},
			}
		}

	case *pb.ReqEnvelope_ReqLinkFile:
		log.Info("Linking file with path:", p.ReqLinkFile.Path, "to hash:", p.ReqLinkFile.Hash)
		pbFile, err := ch.mg.filesManager.LinkFile(ses, p.ReqLinkFile.Path, p.ReqLinkFile.Hash, p.ReqLinkFile.ForceOverride, p.ReqLinkFile.Created)
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
		pbFile, err := ch.mg.filesManager.GetFile(ses, p.ReqGetFile.Path)
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
		err := ch.mg.filesManager.DelFile(ses, p.ReqDelFile.Path)
		if err != nil {
			// This used to only ever reach the client, never the server's
			// own log — tracking down a real "Delete failed" report meant
			// searching the log for an error that was never actually
			// written there, only sent over the wire.
			log.Error("error trying to delete file:", p.ReqDelFile.Path, err)
			resp.Error = true
			resp.ErrorMessage = fmt.Sprintf("error trying to delete file: %s", err)
		} else {
			log.Info("Deleted file by path:", p.ReqDelFile.Path)
			// Acknoledge the Deletion
			resp.Payload = &pb.RespEnvelope_RespAck{
				RespAck: &pb.Ack{
					Ok: true,
				},
			}
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
		files, token, err := ch.mg.filesManager.ImageSearch(ses, "", p.ReqSearchPhotos.Tags, p.ReqSearchPhotos.Token, p.ReqSearchPhotos.IncludeVideos, p.ReqSearchPhotos.PersonIds, before)
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
		buckets, err := ch.mg.filesManager.PhotoDateBuckets(p.ReqPhotoDateBuckets.Tags, p.ReqPhotoDateBuckets.PersonIds, p.ReqPhotoDateBuckets.IncludeVideos)
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
		notifications, err := ch.mg.dao.ListNotifications(limit)
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

	case *pb.ReqEnvelope_ReqSetupStorage:
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
		log.Error("unknown payload:", p)
		resp.Error = true
		resp.ErrorMessage = "unknown payload"
	}

	return
}

// processMessage dispatches one request to the pre-auth, friend-auth, or
// owner-auth handler as appropriate, recovering from any panic along the
// way. Without this, a bug in any single request handler (a nil dereference
// on a malformed/unexpected request, say) would crash this goroutine
// unrecovered — which takes down the entire process, for every connected
// client, not just this one. Recovering here contains that to "this one
// connection gets closed", regardless of what request type or handler bug
// causes it in the future.
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
func (mg *Manager) handleConnection(conn *gorilla.Conn, r *http.Request) {
	ch := &connHandler{
		mg: mg,
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
				resp = &pb.RespEnvelope{
					Id:           env.Id,
					Error:        true,
					ErrorMessage: "not authenticated",
				}
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
