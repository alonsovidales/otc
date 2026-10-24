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

	"github.com/alonsovidales/otc/log"
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
// asks for it.
var registryClient = &http.Client{Timeout: 15 * time.Second}

// cMissingFor is how long "no image for N yet" is believed before the
// registry is asked again; an image found stays found.
const cMissingFor = 15 * time.Minute

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
}{found: map[int]bool{}, missing: map[int]time.Time{}}

// ImageAvailable reports whether release's image has been published
// (ghcr.io/alonsovidales/otc:<release>), asking the registry with an
// anonymous pull token and a HEAD of the tag's manifest - no image is
// downloaded. Answers are kept (see cMissingFor); a lookup that fails
// answers false and is not kept, so the next check asks again.
func ImageAvailable(release int) bool {
	if release <= 0 {
		return false
	}
	images.Lock()
	if images.found[release] {
		images.Unlock()
		return true
	}
	if at, ok := images.missing[release]; ok && time.Since(at) < cMissingFor {
		images.Unlock()
		return false
	}
	images.Unlock()

	found, err := imagePublished(release)
	if err != nil {
		log.Debug("could not ask the registry for release", release, "image:", err)
		return false
	}
	images.Lock()
	if found {
		images.found[release] = true
		delete(images.missing, release)
	} else {
		images.missing[release] = time.Now()
	}
	images.Unlock()
	return found
}

// imagePublished asks the registry whether release's tag exists.
func imagePublished(release int) (bool, error) {
	token, err := pullToken()
	if err != nil {
		return false, err
	}
	req, err := http.NewRequest(http.MethodHead, fmt.Sprintf("%s/v2/%s/manifests/%d", registry, cImageRepository, release), nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", strings.Join(manifestTypes, ", "))
	resp, err := registryClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	}
	return false, fmt.Errorf("the registry answered %s", resp.Status) // i18n-ignore: logged, never shown
}

// pullToken is an anonymous token to read the public repository with.
func pullToken() (string, error) {
	q := url.Values{"scope": {"repository:" + cImageRepository + ":pull"}, "service": {"ghcr.io"}}
	resp, err := registryClient.Get(registry + "/token?" + q.Encode())
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
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
