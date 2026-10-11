// SPDX-License-Identifier: AGPL-3.0-or-later

// Package push sends notifications to every device the owner has
// registered, over two independent channels:
//
//   - Web Push (browsers): fully self-hosted, no third-party account
//     needed. A device generates its own VAPID keypair on first use (see
//     Init) and talks directly to the browser vendor's push service using
//     it.
//   - APNs (iOS): genuinely can't be self-hosted - it needs an APNs Auth
//     Key (.p8) generated in the device owner's own Apple Developer
//     account, configured via the [apns] section below. Until that's
//     configured, Send just skips the APNs half and logs once - device
//     token registration itself still works and isn't lost.
//
// This started (issue #43) as "a friend posted" notifications sent by the
// OTC device itself, backed directly by its own *dao.Dao. Issue #62 (alert
// the owner if their device goes unreachable) needs the exact same sending
// logic to run from the bridge instead - the device is the thing that's
// offline, so it's the one party that provably can't send its own "I'm
// offline" push. Since the bridge has its own, unrelated *dao.Dao (it
// isn't part of the device module at all - see CLAUDE.md), Push is backed
// by the Storage interface below rather than a concrete dao type, so both
// a device's own dao.Dao and the bridge's own per-domain adapter (over
// whatever it was told to store for that one domain - see
// bridge/websocket's UpdatePushRegistrations handling) can satisfy it.
package push

import (
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/SherClockHolmes/webpush-go"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
	"github.com/sideshow/apns2"
	"github.com/sideshow/apns2/payload"
	"github.com/sideshow/apns2/token"
)

// WebPushSubscription mirrors a browser PushSubscription's fields, verbatim
// from subscription.toJSON() (issue #43).
type WebPushSubscription struct {
	Endpoint string
	P256dh   string
	Auth     string
}

// Storage is everything Push needs to persist/read registrations and the
// VAPID keypair - see the package doc for why this is an interface rather
// than a concrete dao type.
type Storage interface {
	GetVapidKeys() (pub, priv string, err error)
	SetVapidKeys(pub, priv string) error
	ListWebPushSubscriptions() ([]*WebPushSubscription, error)
	DeleteWebPushSubscription(endpoint string) error
	ListApnsTokens() ([]string, error)
	DeleteApnsToken(token string) error
	// Issue #125: the Android app instances (FCM registration tokens).
	ListFcmTokens() ([]string, error)
	DeleteFcmToken(token string) error
}

// Target is what a tap on a notification opens in the apps - the same
// place tapping it in the in-app Alerts list does. IDs only, never
// content (see NotifyNewPost's note on what a push may carry).
type Target struct {
	// Kind is "post" (a post, or a like/comment on one) or "friends"
	// (a friend request, sent or accepted); empty opens nothing special.
	Kind        string
	PubUUID     string
	CommentUUID string
}

const (
	TargetPost    = "post"
	TargetFriends = "friends"
)

// data is the Target as the key/value pairs a push payload carries.
func (t Target) data() map[string]string {
	d := map[string]string{"otc": "notification"}
	if t.Kind != "" {
		d["kind"] = t.Kind
	}
	if t.PubUUID != "" {
		d["pub_uuid"] = t.PubUUID
	}
	if t.CommentUUID != "" {
		d["comment_uuid"] = t.CommentUUID
	}
	return d
}

type Push struct {
	storage Storage

	vapidPublicKey  string
	vapidPrivateKey string

	// nil until/unless [apns] is configured (see loadApns) - every send path
	// below treats a nil client as "APNs not set up yet", not an error.
	apnsClient *apns2.Client
	// apnsFallback is the other APNs environment: a token the primary
	// rejects as BadDeviceToken is tried here before it's deleted. An app
	// run from Xcode registers for the development environment, an App
	// Store or TestFlight build for production, and the same key signs
	// for both - so one bridge serves either kind of install.
	apnsFallback *apns2.Client
	apnsTopic    string
	subscriberID string

	// nil until/unless [fcm] is configured (see loadFcm, issue #125).
	fcm *fcmSender

	// RelayMobile, if set, is asked to deliver a phone notification (iOS
	// and Android) instead of this process sending it to APNs/FCM itself.
	// The device sets it (see websocket.relayMobileToBridge): the APNs auth
	// key and the Firebase service account are the project's private keys
	// and must never be on a device, so a device hands the title/body to
	// the bridge, which holds the keys and this device's own tokens.
	// Returning false means "not relayed" (no bridge configured, or it
	// couldn't be reached) and the local clients, if any, are tried
	// instead - which on a device are normally nil, so the notification is
	// simply not delivered to phones. The bridge leaves this nil and sends
	// directly.
	RelayMobile func(title, body string, t Target) bool

	// OnChange, if set, is called after a stale subscription/token is
	// pruned (see sendWebPush/sendApns) - i.e. whenever storage's
	// registration set actually changed as a side effect of sending. The
	// device wires this to re-sync its registrations to the bridge
	// (issue #62), so the bridge's copy doesn't accumulate dead
	// entries between explicit register/unregister calls. Never called
	// for any other reason, and nil is a valid no-op value (the bridge's
	// own Push instance has no further hop to sync to).
	OnChange func()

	// Language, if set, answers the language pushes are written in (see
	// Lang). The device wires it to its settings (settings.PushLanguage);
	// the bridge leaves it nil.
	Language func() string

	// queue, once StartAsync has run, takes Notify's deliveries off the
	// caller; nil means Notify sends inline.
	queue     chan notifyJob
	queueOnce sync.Once

	// WebPushClient, if set, sends Web Push instead of webPushClient. The
	// bridge sets one that only reaches public addresses: its
	// subscriptions come from devices, which could name anything.
	WebPushClient *http.Client
}

type notifyJob struct {
	title, body string
	t           Target
}

// StartAsync makes Notify hand its deliveries to one background worker,
// which keeps their order, instead of sending them on the caller. On the
// device every caller is the friend sync (or a friend request waiting for
// its answer), which used to wait on each push service and a bridge dial
// per notification, holding up every friend after it. A full queue sends
// inline, so nothing is dropped. The bridge never calls this: its Push
// instances live for one request, and BridgeNotify answers after sending.
func (p *Push) StartAsync(size int) {
	p.queueOnce.Do(func() {
		q := make(chan notifyJob, size)
		go func() {
			for j := range q {
				p.deliver(j.title, j.body, j.t)
			}
		}()
		p.queue = q
	})
}

// Init loads (generating on first use - see loadOrGenerateVapidKeys) this
// device's VAPID keypair, and loads APNs config if the optional [apns]
// section is present. The bridge, sending on an already-registered
// device's behalf, never hits the "generate" path: it always has real
// keys already, reported by that device (see loadOrGenerateVapidKeys' own
// doc comment).
func Init(s Storage) (p *Push, err error) {
	p = &Push{storage: s, subscriberID: "mailto:otc@localhost"}

	if err = p.loadOrGenerateVapidKeys(); err != nil {
		return nil, err
	}
	p.loadApns()
	p.loadFcm()

	return p, nil
}

// loadOrGenerateVapidKeys loads whatever keypair storage already has, or -
// only when storage is a device's own, and it's truly the first time
// (nothing generated yet) - generates and persists a new one. The bridge's
// own Storage adapter (issue #62) always has real keys by the time it's
// asked, reported by the device itself in UpdatePushRegistrations, so this
// generate path never actually runs there in practice; it's kept
// unconditional rather than split into a separate device-only method so
// there's exactly one code path for "make sure the keys are loaded",
// regardless of which Storage is behind it.
func (p *Push) loadOrGenerateVapidKeys() (err error) {
	pub, priv, err := p.storage.GetVapidKeys()
	if err != nil {
		return err
	}
	if pub != "" && priv != "" {
		p.vapidPublicKey, p.vapidPrivateKey = pub, priv
		return nil
	}

	log.Info("Generating this device's VAPID keypair for Web Push (first use)")
	priv, pub, err = webpush.GenerateVAPIDKeys()
	if err != nil {
		return fmt.Errorf("generating VAPID keys: %w", err)
	}
	if err = p.storage.SetVapidKeys(pub, priv); err != nil {
		return fmt.Errorf("persisting VAPID keys: %w", err)
	}
	p.vapidPublicKey, p.vapidPrivateKey = pub, priv
	return nil
}

// The phone senders are built once per process and shared by every Push:
// the bridge calls Init for each notification it relays, and used to open
// a new APNs HTTP/2 connection (never closed) with a freshly signed JWT,
// and fetch a new FCM access token, for every one of them. Only a
// successful load is kept, so a missing or broken key is still retried
// and logged on every push. Replacing a key on disk now takes a restart.
var (
	mobileMu   sync.Mutex
	apnsShared *apnsSenders
	fcmShared  *fcmSender
)

type apnsSenders struct {
	client, fallback *apns2.Client
	topic            string
}

// loadApns wires up the APNs client from the [apns] config section, only if
// present - see the package doc for why this can't be generated on our own
// the way the VAPID keypair above is.
//
//	[apns]
//	key-path=/etc/otc/apns_auth_key.p8
//	key-id=ABCD1234EF
//	team-id=WXYZ9876AB
//	bundle-id=cloud.off-the.OffTheCloud
//	production=1
func (p *Push) loadApns() {
	mobileMu.Lock()
	defer mobileMu.Unlock()
	if apnsShared != nil {
		p.apnsClient, p.apnsFallback, p.apnsTopic = apnsShared.client, apnsShared.fallback, apnsShared.topic
		return
	}
	if !cfg.HasSection("apns") {
		// Expected on a device: iOS pushes go through the bridge (see
		// RelayMobile). Only the bridge itself carries an [apns] section.
		log.Info("No [apns] config section - iOS pushes will be relayed through the bridge if one is configured")
		return
	}

	keyPath := cfg.GetStr("apns", "key-path")
	keyID := cfg.GetStr("apns", "key-id")
	teamID := cfg.GetStr("apns", "team-id")
	bundleID := cfg.GetStr("apns", "bundle-id")
	if keyPath == "" || keyID == "" || teamID == "" || bundleID == "" {
		log.Error("[apns] section is missing key-path/key-id/team-id/bundle-id - iOS push notifications won't be delivered")
		return
	}

	var authKey *ecdsa.PrivateKey
	authKey, err := token.AuthKeyFromFile(keyPath)
	if err != nil {
		log.Error("could not load APNs auth key from", keyPath, ":", err)
		return
	}

	tok := &token.Token{AuthKey: authKey, KeyID: keyID, TeamID: teamID}
	client := apns2.NewTokenClient(tok)
	fallback := apns2.NewTokenClient(tok)
	if cfg.GetBool("apns", "production") {
		client = client.Production()
		fallback = fallback.Development()
	} else {
		client = client.Development()
		fallback = fallback.Production()
	}

	apnsShared = &apnsSenders{client: client, fallback: fallback, topic: bundleID}
	p.apnsClient = client
	p.apnsFallback = fallback
	p.apnsTopic = bundleID
	log.Info("APNs configured (bundle", bundleID, ") - iOS push notifications are live")
}

func (p *Push) VapidPublicKey() string { return p.vapidPublicKey }

// NotifyNewPost tells every registered device that friendName just posted -
// friendName only, deliberately never the post's own text or photo: this
// goes out over APNs too, not just Web Push, and unlike Web Push's
// mandatory end-to-end payload encryption, a standard APNs payload is
// readable by Apple's own infrastructure in transit. Keeping every push
// notification's body to a fixed, generic string regardless of channel -
// friend identity only, never a friend's actual words, comment, or which
// photo a like/comment landed on - means there's nothing there for it to
// read even in principle, and one rule to keep instead of two different
// ones per channel.
func (p *Push) NotifyNewPost(friendName, pubUUID string) {
	p.Notify(friendName, "posted something new", Target{Kind: TargetPost, PubUUID: pubUUID})
}

// NotifyFriendshipRequest tells every registered device that fromName sent
// a friend request (issue #43 follow-up) - fired once, from
// Social.ExternalFriendshipRequest, right after the inbound request is
// verified and persisted.
func (p *Push) NotifyFriendshipRequest(fromName string) {
	p.Notify(fromName, "sent you a friend request", Target{Kind: TargetFriends})
}

// NotifyFriendshipAccepted tells every registered device that friendName
// accepted a friend request this device sent - fired once, from
// friendship.updateFriendshipStatus, exactly on the Pending -> Accepted
// transition (never on every sync poll after that).
func (p *Push) NotifyFriendshipAccepted(friendName string) {
	p.Notify(friendName, "accepted your friend request", Target{Kind: TargetFriends})
}

// Notify fans a title/body out to every registered device - the generic
// entry point for social-interaction notifications (post liked/commented,
// comment liked - issue #43 follow-up) that don't need their own named
// wrapper. Best-effort per-device: one failing subscription/token doesn't
// stop the others, and errors are logged rather than returned - every
// caller runs from the background friend-sync loop
// (social.SyncWithFriends), which has nowhere useful to surface a
// push-delivery failure to.
func (p *Push) Notify(title, body string, t Target) {
	if p.queue != nil {
		select {
		case p.queue <- notifyJob{title: title, body: body, t: t}:
			return
		default:
			log.Error("push queue full, sending inline")
		}
	}
	p.deliver(title, body, t)
}

func (p *Push) deliver(title, body string, t Target) {
	p.sendWebPush(title, body, t)
	p.sendMobile(title, body, t)
}

func (p *Push) sendWebPush(title, body string, t Target) {
	subs, err := p.storage.ListWebPushSubscriptions()
	if err != nil {
		log.Error("could not list web push subscriptions:", err)
		return
	}
	if len(subs) == 0 {
		return
	}

	msg := t.data()
	msg["title"], msg["body"] = title, body
	payloadBytes, err := json.Marshal(msg)
	if err != nil {
		log.Error("could not marshal web push payload:", err)
		return
	}

	client := webPushClient
	if p.WebPushClient != nil {
		client = p.WebPushClient
	}
	opts := &webpush.Options{
		Subscriber:      p.subscriberID,
		VAPIDPublicKey:  p.vapidPublicKey,
		VAPIDPrivateKey: p.vapidPrivateKey,
		TTL:             60,
		// Issue #169: the library's default client has no timeout, and a
		// push service that never answers held up the friend sync that
		// raised the notification.
		HTTPClient: client,
	}

	for _, sub := range subs {
		resp, err := webpush.SendNotification(payloadBytes, &webpush.Subscription{
			Endpoint: sub.Endpoint,
			Keys:     webpush.Keys{P256dh: sub.P256dh, Auth: sub.Auth},
		}, opts)
		if err != nil {
			log.Error("web push send failed for", shortID(sub.Endpoint), ":", err)
			continue
		}
		resp.Body.Close()
		// 404/410: the push service says this subscription no longer
		// exists (browser unsubscribed, or the endpoint expired) - stop
		// retrying it forever.
		if resp.StatusCode == 404 || resp.StatusCode == 410 {
			if delErr := p.storage.DeleteWebPushSubscription(sub.Endpoint); delErr != nil {
				log.Error("could not remove stale web push subscription:", delErr)
			} else if p.OnChange != nil {
				p.OnChange()
			}
		}
	}
}

// NotifyMobile sends to phones only (iOS and Android) - what the bridge
// does on a device's behalf (see BridgeNotify in messages.proto), Web Push
// having stayed with the device that owns the VAPID keypair.
var webPushClient = &http.Client{Timeout: 15 * time.Second}

func (p *Push) NotifyMobile(title, body string, t Target) { p.sendMobile(title, body, t) }

func (p *Push) sendMobile(title, body string, t Target) {
	if p.RelayMobile != nil {
		// No phone registered here: the bridge only has the tokens this
		// device gave it, so relaying would dial it to deliver nothing. An
		// error (an older schema without fcm_tokens) still relays.
		apns, errA := p.storage.ListApnsTokens()
		fcm, errF := p.storage.ListFcmTokens()
		if errA == nil && errF == nil && len(apns) == 0 && len(fcm) == 0 {
			return
		}
		if p.RelayMobile(title, body, t) {
			return
		}
	}
	p.sendApns(title, body, t)
	p.sendFcm(title, body, t)
}

func (p *Push) sendApns(title, body string, t Target) {
	if p.apnsClient == nil {
		return
	}

	tokens, err := p.storage.ListApnsTokens()
	if err != nil {
		log.Error("could not list APNs tokens:", err)
		return
	}

	pl := payload.NewPayload().AlertTitle(title).AlertBody(body).Sound("default")
	for k, v := range t.data() {
		pl.Custom(k, v)
	}
	for _, tok := range tokens {
		n := &apns2.Notification{DeviceToken: tok, Topic: p.apnsTopic, Payload: pl}
		resp, err := p.apnsClient.Push(n)
		if err != nil {
			log.Error("APNs send failed for token", shortID(tok), ":", err)
			continue
		}
		// A token from the other environment (an app run from Xcode is
		// on development, an App Store build on production) is refused as
		// BadDeviceToken - try it there before calling it dead.
		if !resp.Sent() && resp.Reason == "BadDeviceToken" && p.apnsFallback != nil {
			if fb, fbErr := p.apnsFallback.Push(n); fbErr == nil {
				resp = fb
			}
		}
		if !resp.Sent() {
			log.Error("APNs rejected notification: status", resp.StatusCode, "reason", resp.Reason, "apnsID", resp.ApnsID)
			// BadDeviceToken (valid in neither environment) and
			// Unregistered (app uninstalled, or 410-equivalent) both mean
			// this token will never work again.
			if resp.Reason == "BadDeviceToken" || resp.Reason == "Unregistered" {
				if delErr := p.storage.DeleteApnsToken(tok); delErr != nil {
					log.Error("could not remove stale APNs token:", delErr)
				} else if p.OnChange != nil {
					p.OnChange()
				}
			}
		}
	}
}

// shortID is enough of a push token or endpoint to tell two apart in a log,
// without writing the whole address of someone's phone or browser there
// (issue #162).
func shortID(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:6] + "…" + s[len(s)-4:]
}
