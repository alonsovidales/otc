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
}

type Push struct {
	storage Storage

	vapidPublicKey  string
	vapidPrivateKey string

	// nil until/unless [apns] is configured (see loadApns) - every send path
	// below treats a nil client as "APNs not set up yet", not an error.
	apnsClient   *apns2.Client
	apnsTopic    string
	subscriberID string

	// OnChange, if set, is called after a stale subscription/token is
	// pruned (see sendWebPush/sendApns) - i.e. whenever storage's
	// registration set actually changed as a side effect of sending. The
	// device wires this to re-sync its registrations to the bridge
	// (issue #62), so the bridge's copy doesn't accumulate dead
	// entries between explicit register/unregister calls. Never called
	// for any other reason, and nil is a valid no-op value (the bridge's
	// own Push instance has no further hop to sync to).
	OnChange func()
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

// loadApns wires up the APNs client from the [apns] config section, only if
// present - see the package doc for why this can't be generated on our own
// the way the VAPID keypair above is.
//
//	[apns]
//	key-path=/etc/otc/apns_auth_key.p8
//	key-id=ABCD1234EF
//	team-id=WXYZ9876AB
//	bundle-id=otc.OffTheCloud
//	production=1
func (p *Push) loadApns() {
	if !cfg.HasSection("apns") {
		log.Info("No [apns] config section - push notifications to iOS are registered but won't be delivered until it's configured")
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
	if cfg.GetBool("apns", "production") {
		client = client.Production()
	}

	p.apnsClient = client
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
func (p *Push) NotifyNewPost(friendName string) {
	p.Notify(friendName, "posted something new")
}

// NotifyFriendshipRequest tells every registered device that fromName sent
// a friend request (issue #43 follow-up) - fired once, from
// Social.ExternalFriendshipRequest, right after the inbound request is
// verified and persisted.
func (p *Push) NotifyFriendshipRequest(fromName string) {
	p.Notify(fromName, "sent you a friend request")
}

// NotifyFriendshipAccepted tells every registered device that friendName
// accepted a friend request this device sent - fired once, from
// friendship.updateFriendshipStatus, exactly on the Pending -> Accepted
// transition (never on every sync poll after that).
func (p *Push) NotifyFriendshipAccepted(friendName string) {
	p.Notify(friendName, "accepted your friend request")
}

// Notify fans a title/body out to every registered device - the generic
// entry point for social-interaction notifications (post liked/commented,
// comment liked - issue #43 follow-up) that don't need their own named
// wrapper. Best-effort per-device: one failing subscription/token doesn't
// stop the others, and errors are logged rather than returned - every
// caller runs from the background friend-sync loop
// (social.SyncWithFriends), which has nowhere useful to surface a
// push-delivery failure to.
func (p *Push) Notify(title, body string) {
	p.sendWebPush(title, body)
	p.sendApns(title, body)
}

func (p *Push) sendWebPush(title, body string) {
	subs, err := p.storage.ListWebPushSubscriptions()
	if err != nil {
		log.Error("could not list web push subscriptions:", err)
		return
	}
	if len(subs) == 0 {
		return
	}

	payloadBytes, err := json.Marshal(map[string]string{"title": title, "body": body})
	if err != nil {
		log.Error("could not marshal web push payload:", err)
		return
	}

	opts := &webpush.Options{
		Subscriber:      p.subscriberID,
		VAPIDPublicKey:  p.vapidPublicKey,
		VAPIDPrivateKey: p.vapidPrivateKey,
		TTL:             60,
	}

	for _, sub := range subs {
		resp, err := webpush.SendNotification(payloadBytes, &webpush.Subscription{
			Endpoint: sub.Endpoint,
			Keys:     webpush.Keys{P256dh: sub.P256dh, Auth: sub.Auth},
		}, opts)
		if err != nil {
			log.Error("web push send failed for", sub.Endpoint, ":", err)
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

func (p *Push) sendApns(title, body string) {
	if p.apnsClient == nil {
		return
	}

	tokens, err := p.storage.ListApnsTokens()
	if err != nil {
		log.Error("could not list APNs tokens:", err)
		return
	}

	pl := payload.NewPayload().AlertTitle(title).AlertBody(body).Sound("default")
	for _, t := range tokens {
		resp, err := p.apnsClient.Push(&apns2.Notification{
			DeviceToken: t,
			Topic:       p.apnsTopic,
			Payload:     pl,
		})
		if err != nil {
			log.Error("APNs send failed for token", t, ":", err)
			continue
		}
		if !resp.Sent() {
			log.Error("APNs rejected notification: status", resp.StatusCode, "reason", resp.Reason, "apnsID", resp.ApnsID)
			// BadDeviceToken (never valid for this topic) and Unregistered
			// (app uninstalled, or 410-equivalent) both mean this token
			// will never work again.
			if resp.Reason == "BadDeviceToken" || resp.Reason == "Unregistered" {
				if delErr := p.storage.DeleteApnsToken(t); delErr != nil {
					log.Error("could not remove stale APNs token:", delErr)
				} else if p.OnChange != nil {
					p.OnChange()
				}
			}
		}
	}
}
