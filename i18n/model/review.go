// SPDX-License-Identifier: AGPL-3.0-or-later

package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// A review-required key ("review": "required": consent, legal, security
// and destructive text) needs a "reviewed" value in English and in every
// translation, "<who> <YYYY-MM-DD> #<fingerprint>". The reviewer writes
// "<who> <date>" and runs make i18n, whose canonical rewrite appends the
// fingerprint of the text as it is then. Any later change to the text -
// in English, or in a translation's text or the English it came from -
// changes the fingerprint, and the review counts as stale until someone
// writes a new "<who> <date>".

// Fingerprint identifies the texts a review covers: 8 hex digits of a
// SHA-256 over their exact content.
func Fingerprint(texts ...Text) string {
	h := sha256.New()
	for _, t := range texts {
		b, _ := json.Marshal([]any{t.Plural, t.One, t.Other})
		h.Write(b)
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))[:8]
}

// Review is a parsed "reviewed" value.
type Review struct {
	Who         string
	Date        time.Time
	Fingerprint string // "" when not recorded yet
}

var fingerprintPattern = regexp.MustCompile(`^#[0-9a-f]{8}$`)

// ParseReview reads "<who> <YYYY-MM-DD>[ #<fingerprint>]".
func ParseReview(s string) (Review, error) {
	fields := strings.Fields(s)
	if strings.Join(fields, " ") != s {
		return Review{}, fmt.Errorf("%q has extra spaces", s)
	}
	var r Review
	if n := len(fields); n > 0 && fingerprintPattern.MatchString(fields[n-1]) {
		r.Fingerprint = fields[n-1][1:]
		fields = fields[:n-1]
	}
	if len(fields) < 2 {
		return Review{}, fmt.Errorf("%q is not \"<who> <YYYY-MM-DD>\"", s)
	}
	d, err := time.Parse("2006-01-02", fields[len(fields)-1])
	if err != nil {
		return Review{}, fmt.Errorf("%q: the date must be YYYY-MM-DD", s)
	}
	r.Date = d
	r.Who = strings.Join(fields[:len(fields)-1], " ")
	return r, nil
}

// withFingerprint appends fp to a review that has none yet; anything else
// (a recorded review, or a value ParseReview refuses) stays as it is.
func withFingerprint(reviewed, fp string) string {
	r, err := ParseReview(reviewed)
	if err != nil || r.Fingerprint != "" {
		return reviewed
	}
	return reviewed + " #" + fp
}

// reviewCurrent reports whether a reviewed value covers texts as they are.
func reviewCurrent(reviewed string, texts ...Text) bool {
	r, err := ParseReview(reviewed)
	return err == nil && r.Fingerprint == Fingerprint(texts...)
}
