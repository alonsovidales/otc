// SPDX-License-Identifier: AGPL-3.0-or-later

package push

// Issue #125: Android pushes through Firebase Cloud Messaging, the APNs
// counterpart. Like the APNs key, the Firebase service account's private
// key is the project's own and lives only on the bridge (the [fcm] config
// section); a device relays through the bridge (see RelayMobile).
//
// FCM's HTTP v1 API, called directly: a service-account JWT is exchanged
// for an OAuth access token (cached until shortly before it expires), and
// each notification is one POST per token. No Firebase SDK - the same few
// requests it would make, with the JWT library this module already has.

import (
	"bytes"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
	"github.com/golang-jwt/jwt/v5"
)

const (
	cFcmScope     = "https://www.googleapis.com/auth/firebase.messaging"
	cFcmSendURL   = "https://fcm.googleapis.com/v1/projects/%s/messages:send"
	cGoogleToken  = "https://oauth2.googleapis.com/token"
	cFcmChannelID = "social"
)

// errFcmTokenGone is a token FCM says will never work again.
var errFcmTokenGone = errors.New("fcm token is no longer registered")

type fcmSender struct {
	projectID   string
	clientEmail string
	tokenURI    string
	key         *rsa.PrivateKey
	http        *http.Client

	mu          sync.Mutex
	accessToken string
	expires     time.Time
}

// loadFcm reads [fcm] - only the bridge has it:
//
//	[fcm]
//	credentials-path=/etc/otc/fcm-service-account.json
//	project-id=off-the-cloud-ad49f   ; optional, the file's own by default
func (p *Push) loadFcm() {
	// Shared like the APNs clients (see mobileMu): its access token is
	// then fetched about once an hour, not once per push.
	mobileMu.Lock()
	defer mobileMu.Unlock()
	if fcmShared != nil {
		p.fcm = fcmShared
		return
	}
	if !cfg.HasSection("fcm") {
		log.Info("No [fcm] config section - Android pushes will be relayed through the bridge if one is configured")
		return
	}
	path := cfg.GetStr("fcm", "credentials-path")
	if path == "" {
		log.Error("[fcm] section has no credentials-path - Android push notifications won't be delivered")
		return
	}
	sender, err := newFcmSender(path, cfg.GetStr("fcm", "project-id"))
	if err != nil {
		log.Error("could not load the FCM service account from", path, ":", err)
		return
	}
	fcmShared = sender
	p.fcm = sender
	log.Info("FCM configured (project", sender.projectID, ") - Android push notifications are live")
}

func newFcmSender(path, projectID string) (*fcmSender, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var sa struct {
		Type        string `json:"type"`
		ProjectID   string `json:"project_id"`
		PrivateKey  string `json:"private_key"`
		ClientEmail string `json:"client_email"`
		TokenURI    string `json:"token_uri"`
	}
	if err := json.Unmarshal(raw, &sa); err != nil {
		return nil, err
	}
	if sa.Type != "service_account" || sa.PrivateKey == "" || sa.ClientEmail == "" {
		return nil, errors.New("not a service account key (Project settings → Service accounts → Generate new private key)")
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(sa.PrivateKey))
	if err != nil {
		return nil, err
	}
	if projectID == "" {
		projectID = sa.ProjectID
	}
	if projectID == "" {
		return nil, errors.New("no project id")
	}
	if sa.TokenURI == "" {
		sa.TokenURI = cGoogleToken
	}

	return &fcmSender{
		projectID: projectID, clientEmail: sa.ClientEmail, tokenURI: sa.TokenURI, key: key,
		http: &http.Client{Timeout: 15 * time.Second},
	}, nil
}

// token is the OAuth access token FCM calls carry, fetched again a few
// minutes before it expires.
func (f *fcmSender) token() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.accessToken != "" && time.Now().Before(f.expires) {
		return f.accessToken, nil
	}
	now := time.Now()
	assertion, err := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss":   f.clientEmail,
		"scope": cFcmScope,
		"aud":   f.tokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}).SignedString(f.key)
	if err != nil {
		return "", err
	}
	resp, err := f.http.PostForm(f.tokenURI, url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error_description"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out)
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16)) // so the connection is reused
	if resp.StatusCode != http.StatusOK || out.AccessToken == "" {
		return "", fmt.Errorf("google token endpoint: %d %s", resp.StatusCode, out.Error)
	}
	if out.ExpiresIn <= 0 {
		out.ExpiresIn = 3600
	}
	f.accessToken = out.AccessToken
	f.expires = now.Add(time.Duration(out.ExpiresIn)*time.Second - 5*time.Minute)

	return f.accessToken, nil
}

// send delivers one notification to one app instance. The data key is
// what the app looks for to open its notifications list on a tap.
func (f *fcmSender) send(deviceToken, title, body string, t Target) error {
	access, err := f.token()
	if err != nil {
		return err
	}
	msg := map[string]any{"message": map[string]any{
		"token":        deviceToken,
		"notification": map[string]string{"title": title, "body": body},
		"data":         t.data(),
		"android": map[string]any{
			"priority":     "high",
			"notification": map[string]string{"channel_id": cFcmChannelID},
		},
	}}
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf(cFcmSendURL, f.projectID), bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		// Read to the end, or the connection can't be reused.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if fcmTokenGone(resp.StatusCode, b) {
		return errFcmTokenGone
	}
	if resp.StatusCode == http.StatusUnauthorized {
		// A revoked or expired access token: fetch a new one next time.
		f.mu.Lock()
		f.accessToken = ""
		f.mu.Unlock()
	}

	return fmt.Errorf("fcm: %d %s", resp.StatusCode, strings.TrimSpace(string(b)))
}

// fcmTokenGone reads FCM's answer for "this app instance is gone" -
// UNREGISTERED (uninstalled, or the token was rotated), or a token that was
// never valid - as opposed to a failure worth trying again.
func fcmTokenGone(status int, body []byte) bool {
	var e struct {
		Error struct {
			Status  string `json:"status"`
			Message string `json:"message"`
			Details []struct {
				ErrorCode string `json:"errorCode"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil {
		return false
	}
	for _, d := range e.Error.Details {
		if d.ErrorCode == "UNREGISTERED" {
			return true
		}
		if d.ErrorCode == "INVALID_ARGUMENT" && strings.Contains(strings.ToLower(e.Error.Message), "registration token") {
			return true
		}
	}

	return status == http.StatusNotFound && e.Error.Status == "NOT_FOUND"
}

func (p *Push) sendFcm(title, body string, target Target) {
	if p.fcm == nil {
		return
	}
	tokens, err := p.storage.ListFcmTokens()
	if err != nil {
		log.Error("could not list FCM tokens:", err)
		return
	}
	for _, t := range tokens {
		err := p.fcm.send(t, title, body, target)
		if errors.Is(err, errFcmTokenGone) {
			if delErr := p.storage.DeleteFcmToken(t); delErr != nil {
				log.Error("could not remove stale FCM token:", delErr)
			} else if p.OnChange != nil {
				p.OnChange()
			}
			continue
		}
		if err != nil {
			log.Error("FCM send failed:", err)
		}
	}
}
