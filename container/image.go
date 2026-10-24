// SPDX-License-Identifier: AGPL-3.0-or-later

package container

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// A container is updated by pulling a new image, which the release
// workflow builds and publishes some minutes after the release itself. Until
// ghcr.io has release N's image, telling the owner about N would only send
// them to pull an image that isn't there yet.

// cImageRepository is the image's repository on the registry: tags are
// release numbers (and beta).
const cImageRepository = "alonsovidales/otc"

// registry is the registry's address: ghcr.io but in tests.
var registry = "https://ghcr.io"

// registryClient keeps a lookup from holding up the update check that
// asks for it - Settings > Device version waits for that check - when the
// registry is slow or out of reach.
var registryClient = &http.Client{Timeout: 5 * time.Second}

// cMissingFor is how long "no image for N yet" is believed before the
// registry is asked again; an image found stays found. cFailedFor is how
// long a registry that could not be asked is left alone: checks in that
// time fail at once instead of waiting for it again.
const (
	cMissingFor = 15 * time.Minute
	cFailedFor  = 5 * time.Minute
)

// manifestTypes: a release's tag is a multi-architecture index; the single
// manifest types are accepted too.
var manifestTypes = []string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}

var images = struct {
	sync.Mutex
	found   map[int]bool
	missing map[int]time.Time
	// The last lookup that failed, and when.
	failure  error
	failedAt time.Time
}{found: map[int]bool{}, missing: map[int]time.Time{}}

// ImageLookup asks the registry whether releases' images have been
// published (ghcr.io/alonsovidales/otc:<release>), for one update check:
// one anonymous pull token serves every release it is asked about. The
// zero value is ready; not for concurrent use.
type ImageLookup struct {
	token string
	// denied: the registry gave no token for the repository - it has no
	// public image at all.
	denied bool
}

// ImageAvailable is Available with a lookup of its own.
func ImageAvailable(release int) (bool, error) {
	return new(ImageLookup).Available(release)
}

// Available reports whether release's image has been published, with a
// HEAD of its tag's manifest - no image is downloaded. Answers are kept
// (see cMissingFor). The registry turning the lookup away (401, 403,
// 404) means not published: that is how ghcr.io answers for a package
// that doesn't exist or isn't public. An error is a lookup that could not
// be made - the registry out of reach or failing - which says nothing
// about release; it is kept for cFailedFor, and every lookup in that time
// returns it without asking.
func (l *ImageLookup) Available(release int) (bool, error) {
	if release <= 0 {
		return false, nil
	}
	images.Lock()
	switch at, missing := images.missing[release]; {
	case images.found[release]:
		images.Unlock()
		return true, nil
	case missing && time.Since(at) < cMissingFor:
		images.Unlock()
		return false, nil
	case images.failure != nil && time.Since(images.failedAt) < cFailedFor:
		err := images.failure
		images.Unlock()
		return false, err
	}
	images.Unlock()

	found, err := l.published(release)
	images.Lock()
	defer images.Unlock()
	if err != nil {
		images.failure, images.failedAt = err, time.Now()
		return false, err
	}
	images.failure = nil
	if found {
		images.found[release] = true
		delete(images.missing, release)
	} else {
		images.missing[release] = time.Now()
	}
	return found, nil
}

// published asks the registry whether release's tag exists.
func (l *ImageLookup) published(release int) (bool, error) {
	if l.token == "" && !l.denied {
		token, err := pullToken()
		if err != nil {
			return false, err
		}
		l.token, l.denied = token, token == ""
	}
	if l.denied {
		return false, nil
	}
	req, err := http.NewRequest(http.MethodHead, fmt.Sprintf("%s/v2/%s/manifests/%d", registry, cImageRepository, release), nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+l.token)
	req.Header.Set("Accept", strings.Join(manifestTypes, ", "))
	resp, err := registryClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		return true, nil
	case turnedAway(resp.StatusCode):
		return false, nil
	}
	return false, fmt.Errorf("the registry answered %s", resp.Status) // i18n-ignore: logged, never shown
}

// turnedAway: the registry's answer for an image it won't name - none
// with that tag, or no such public package.
func turnedAway(status int) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusNotFound
}

// pullToken is an anonymous token to read the public repository with, or
// "" (and no error) when the registry turns the request away.
func pullToken() (string, error) {
	q := url.Values{"scope": {"repository:" + cImageRepository + ":pull"}, "service": {"ghcr.io"}}
	resp, err := registryClient.Get(registry + "/token?" + q.Encode())
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if turnedAway(resp.StatusCode) {
		return "", nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the registry's token answered %s", resp.Status) // i18n-ignore: logged, never shown
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err != nil {
		return "", err
	}
	if body.Token == "" {
		return "", fmt.Errorf("the registry gave no token") // i18n-ignore: logged, never shown
	}
	return body.Token, nil
}
