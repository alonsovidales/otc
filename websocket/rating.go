// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"errors"
	"math"
	"sync"
	"time"

	"github.com/alonsovidales/otc/alerttext"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/devtext"
	filesmanager "github.com/alonsovidales/otc/files_manager"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/push"
)

// Collection Rating (release 163): the requests' answers. The rules and
// the sealing are files_manager/rating.go's.

const (
	// cCodeRatingOff is RespEnvelope.error_code (and Ack.code) for a
	// rating in a collection whose Rating is off, or through a link that
	// doesn't take ratings (any more).
	cCodeRatingOff = "rating_off"
	// cCodeNotInCollection: the photo isn't in the collection (any more).
	cCodeNotInCollection = "not_in_collection"
)

// ratingQuery is SearchPhotos' rating filter and order as the device
// searches by them; nil when the request has neither.
func ratingQuery(req *pb.SearchPhotos) *dao.RatingQuery {
	q := &dao.RatingQuery{ByRating: req.GetByRating()}
	if f := req.GetRating(); f != nil {
		q.MinStars = int(min(max(f.MinStars, 0), 5))
		q.Unrated = f.Unrated
		q.WithComment = f.WithComment
	}
	if !q.Active() {
		return nil
	}
	return q
}

// ratingCode is the error_code for a refused rating; "" for any other
// failure.
func ratingCode(err error) string {
	switch {
	case errors.Is(err, filesmanager.ErrRatingOff), errors.Is(err, filesmanager.ErrRatingLinkOff):
		return cCodeRatingOff
	case errors.Is(err, filesmanager.ErrNotInCollection):
		return cCodeNotInCollection
	}
	return ""
}

// ratingRefused reports whether err is one of the ratings' own refusals,
// answered in their words (and logged at Debug), not a failure.
func ratingRefused(err error) bool {
	for _, e := range []error{filesmanager.ErrRatingOff, filesmanager.ErrRatingLinkOff, filesmanager.ErrNotInCollection,
		filesmanager.ErrNoSuchCollection, filesmanager.ErrRatingStars, filesmanager.ErrRatingComment} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// refuseRating answers resp with err: on the envelope (error, code,
// message) and, for a request that answers with the generic Ack, as one.
func refuseRating(resp *pb.RespEnvelope, lang string, err error, ack bool) {
	code := ratingCode(err)
	msg := errText(lang, err)
	resp.Error = true
	resp.ErrorCode = code
	resp.ErrorMessage = msg
	if ack {
		resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: false, ErrorMsg: msg, Code: code}}
	}
}

// ratingAlert is the alert for ratings left through a collection's link.
func ratingAlert(n *filesmanager.RatingNotice) alerttext.Alert {
	detail := i18n.DevAlertRatingNewDetail(n.GroupName)
	if n.Favourites {
		detail = i18n.DevAlertRatingNewDetailFavourites()
	}
	return alerttext.New(i18n.DevAlertRatingNew(), detail)
}

// notifyRatings tells the owner that ratings came through a link: an alert
// in Alerts and a push (which names no collection).
func (mg *Manager) notifyRatings(n *filesmanager.RatingNotice) {
	if n == nil {
		return
	}
	if mg.dao != nil {
		if err := mg.dao.AddAlertNotification(ratingAlert(n)); err != nil {
			log.Error("could not add the ratings alert:", err)
		}
	}
	if mg.push != nil {
		mg.push.NotifyText(push.LinkRatings(), push.Target{})
	}
}

// Visitors' ratings (RateSharedGalleryItem) come from anyone with a link,
// through the bridge: each address gets cRatingBurst at once, refilled at
// cRatingPerSecond - a person rating photos one by one never meets it -
// and the device takes at most cRatingDeviceBurst at once, refilled at
// cRatingDevicePerSecond, whatever the address. Process-local, like the
// password-attempt limit (session/ratelimit.go).
const (
	cRatingBurst           = 30
	cRatingPerSecond       = 1.0
	cRatingDeviceBurst     = 120
	cRatingDevicePerSecond = 10.0
	cRatingSweep           = time.Minute
)

type tokenBucket struct {
	tokens float64
	at     time.Time
}

// take refills b at rate up to burst and takes a token if there is one;
// otherwise how long until there is.
func (b *tokenBucket) take(now time.Time, rate, burst float64, consume bool) (time.Duration, bool) {
	b.tokens = math.Min(burst, b.tokens+now.Sub(b.at).Seconds()*rate)
	b.at = now
	if b.tokens < 1 {
		return time.Duration((1 - b.tokens) / rate * float64(time.Second)), false
	}
	if consume {
		b.tokens--
	}
	return 0, true
}

type ratingLimiter struct {
	mu     sync.Mutex
	by     map[string]*tokenBucket
	device tokenBucket
	swept  time.Time
	now    func() time.Time
}

func newRatingLimiter() *ratingLimiter {
	return &ratingLimiter{by: map[string]*tokenBucket{}, device: tokenBucket{tokens: cRatingDeviceBurst}, now: time.Now}
}

// visitorRatings is the process' limit on visitors' ratings.
var visitorRatings = newRatingLimiter()

// allow takes a rating from addr ("" shares one bucket), or says how long
// to wait. An address gets a bucket only once the device's limit lets its
// rating through, so a flood of addresses can't grow the map past what
// the device's limit lets in between sweeps.
func (l *ratingLimiter) allow(addr string) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.swept) > cRatingSweep {
		for k, b := range l.by {
			if b.tokens+now.Sub(b.at).Seconds()*cRatingPerSecond >= cRatingBurst {
				delete(l.by, k)
			}
		}
		l.swept = now
	}
	if wait, ok := l.device.take(now, cRatingDevicePerSecond, cRatingDeviceBurst, false); !ok {
		return wait, false
	}
	b := l.by[addr]
	if b == nil {
		b = &tokenBucket{tokens: cRatingBurst, at: now}
		l.by[addr] = b
	}
	if wait, ok := b.take(now, cRatingPerSecond, cRatingBurst, true); !ok {
		return wait, false
	}
	l.device.take(now, cRatingDevicePerSecond, cRatingDeviceBurst, true)
	return 0, true
}

// rateSharedGalleryItem answers a link visitor's rating (public).
func (ch *connHandler) rateSharedGalleryItem(r *pb.RateSharedGalleryItem, resp *pb.RespEnvelope, lang string) {
	if wait, ok := visitorRatings.allow(ch.addr()); !ok {
		log.Debug("a visitor's rating refused: too many from its address")
		ack := tooManyAttemptsAck(int32(math.Ceil(wait.Seconds())), lang)
		resp.Error, resp.ErrorCode, resp.ErrorMessage = true, ack.Code, ack.ErrorMsg
		resp.Payload = &pb.RespEnvelope_RespAck{RespAck: ack}
		return
	}
	notice, err := ch.mg.filesManager.RateSharedGalleryItem(r.Uuid, r.Secret, int(r.Index), r.Stars, r.Comment)
	if errors.Is(err, filesmanager.ErrNoSuchGallery) {
		// The shared gallery's one answer to a link that doesn't open.
		resp.Error, resp.ErrorMessage = true, devtext.Text(lang, filesmanager.ErrNoSuchGallery)
		return
	}
	if err != nil {
		if ratingRefused(err) {
			log.Debug("a visitor's rating refused:", ratingCode(err))
		} else {
			log.Error("error storing a visitor's rating:", err)
		}
		refuseRating(resp, lang, err, true)
		return
	}
	resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}
	ch.mg.notifyRatings(notice)
}
