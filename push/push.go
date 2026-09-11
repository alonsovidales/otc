// Package push sends "a friend posted" notifications (issue #43) to every
// device the owner has registered, over two independent channels:
//
//   - Web Push (browsers): fully self-hosted, no third-party account
//     needed. This device generates its own VAPID keypair on first use
//     (see Init) and talks directly to the browser vendor's push service
//     using it.
//   - APNs (iOS): genuinely can't be self-hosted - it needs an APNs Auth
//     Key (.p8) generated in the device owner's own Apple Developer
//     account, configured via the [apns] section below. Until that's
//     configured, Send just skips the APNs half and logs once - device
//     token registration itself still works and isn't lost.
package push

import (
	"crypto/ecdsa"
	"encoding/json"
	"fmt"

	"github.com/SherClockHolmes/webpush-go"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/log"
	"github.com/sideshow/apns2"
	"github.com/sideshow/apns2/payload"
	"github.com/sideshow/apns2/token"
)

// webPushBodyLimit keeps the notification body short - this is a summary,
// not the post itself, and Web Push messages have their own encrypted-size
// ceiling (webpush.MaxRecordSize) besides.
const webPushBodyLimit = 200

type Push struct {
	dao *dao.Dao

	vapidPublicKey  string
	vapidPrivateKey string

	// nil until/unless [apns] is configured (see loadApns) - every send path
	// below treats a nil client as "APNs not set up yet", not an error.
	apnsClient   *apns2.Client
	apnsTopic    string
	subscriberID string
}

// Init loads (generating on first use) this device's VAPID keypair, and
// loads APNs config if the optional [apns] section is present.
func Init(d *dao.Dao) (p *Push, err error) {
	p = &Push{dao: d, subscriberID: "mailto:otc@localhost"}

	if err = p.loadOrGenerateVapidKeys(); err != nil {
		return nil, err
	}
	p.loadApns()

	return p, nil
}

func (p *Push) loadOrGenerateVapidKeys() (err error) {
	pub, priv, err := p.dao.GetVapidKeys()
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
	if err = p.dao.SetVapidKeys(pub, priv); err != nil {
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

func (p *Push) RegisterWebPush(endpoint, p256dh, auth string) error {
	return p.dao.SaveWebPushSubscription(endpoint, p256dh, auth)
}

func (p *Push) RegisterApnsToken(t string) error {
	return p.dao.SaveApnsToken(t)
}

// NotifyNewPost tells every registered device that friendName just posted.
func (p *Push) NotifyNewPost(friendName, text string) {
	body := text
	if len(body) > webPushBodyLimit {
		body = body[:webPushBodyLimit] + "…"
	}
	p.Notify(friendName, body)
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
	subs, err := p.dao.ListWebPushSubscriptions()
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
			if delErr := p.dao.DeleteWebPushSubscription(sub.Endpoint); delErr != nil {
				log.Error("could not remove stale web push subscription:", delErr)
			}
		}
	}
}

func (p *Push) sendApns(title, body string) {
	if p.apnsClient == nil {
		return
	}

	tokens, err := p.dao.ListApnsTokens()
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
				if delErr := p.dao.DeleteApnsToken(t); delErr != nil {
					log.Error("could not remove stale APNs token:", delErr)
				}
			}
		}
	}
}
