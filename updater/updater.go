// SPDX-License-Identifier: AGPL-3.0-or-later

// Package updater backs issue #94: updating a device in place, from
// Settings, without anyone rebuilding or reinstalling anything.
//
// The model is deliberately small. A manifest on GitHub lists every
// release, oldest first, each with a script that carries only that
// release's changes. The device records which release it is on in
// /etc/otc/version and runs everything after it, in order - so a device
// two releases behind runs two scripts rather than a reinstall, and a
// schema change arrives as an idempotent ALTER instead of a re-import
// that would take the data with it.
//
// This package only decides *whether* to update and starts the run. The
// run itself is scripts/update.sh, on purpose: its final act is to
// restart the service, which kills the process that started it, so it has
// to outlive this one. Progress comes back through a status file the
// script writes, which is also what lets Settings report on an update
// that finished after the process that launched it was already gone.
package updater

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
)

const (
	// cDefaultRepoRaw is where releases are fetched from: this project's
	// own repository over HTTPS, the same trust model install.sh already
	// establishes since that is also fetched from here and run as root.
	//
	// Overridable via [otc] update-repo so a fork updates from its own
	// repository rather than silently taking code from upstream - which
	// would be a real problem, not a convenience, for anyone running a
	// modified build.
	cDefaultRepoRaw = "https://raw.githubusercontent.com/alonsovidales/otc/main"

	// cDefaultRepoGH is where a release's own artefacts live - the source
	// archive for its tag, and the prebuilt web assets attached to it.
	// Separate from the manifest above because that is read from main (so
	// a device learns about a release immediately) while these are
	// addressed by tag (so it installs exactly that version).
	cDefaultRepoGH = "https://github.com/alonsovidales/otc"

	// cVersionFile is what release this device is on. Absent on every
	// install made before this feature existed, which reads as version 0
	// - such a device runs the whole history, which is safe precisely
	// because every release script is required to be idempotent.
	cVersionFile = "/etc/otc/version"
	cStatusFile  = "/var/lib/otc/update-status.json"
	cRunnerPath  = "/var/lib/otc/update.sh"

	// cManifestTimeout keeps a check from hanging the RPC it was called
	// from when GitHub is slow or unreachable.
	cManifestTimeout = 15 * time.Second
)

// repoRaw is the base every update artefact is fetched from.
func repoRaw() string {
	if cfg.HasSection("otc") {
		if configured := cfg.GetStr("otc", "update-repo"); configured != "" {
			return strings.TrimSuffix(configured, "/")
		}
	}

	return cDefaultRepoRaw
}

// repoGH is the release host, overridable via [otc] update-releases for
// the same reason repoRaw is.
func repoGH() string {
	if cfg.HasSection("otc") {
		if configured := cfg.GetStr("otc", "update-releases"); configured != "" {
			return strings.TrimSuffix(configured, "/")
		}
	}

	return cDefaultRepoGH
}

func manifestURL() string     { return repoRaw() + "/scripts/updates/VERSIONS" }
func updateScriptURL() string { return repoRaw() + "/scripts/update.sh" }

// Release is one line of the manifest.
type Release struct {
	Version     int
	Description string
}

// Status is what the Settings screen shows. Read from the file the runner
// writes, so it survives the restart the runner performs.
type Status struct {
	State   string `json:"state"`
	Message string `json:"message"`
	Version string `json:"version"`
	Updated string `json:"updated"`
}

// Info answers "is there anything to install, and what happened last
// time".
type Info struct {
	CurrentVersion int
	LatestVersion  int
	Pending        []Release
	Status         Status
}

// InstalledVersion reads the release this device is on. A missing or
// unreadable file is version 0: every install predating this feature has
// no file, and the honest answer for those is "the beginning".
func InstalledVersion() int {
	raw, err := os.ReadFile(cVersionFile)
	if err != nil {
		return 0
	}
	version, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0
	}

	return version
}

// CurrentStatus reports on the last (or running) update. A missing file
// simply means no update has ever been started here.
func CurrentStatus() Status {
	raw, err := os.ReadFile(cStatusFile)
	if err != nil {
		return Status{State: "idle"}
	}
	var status Status
	if err := json.Unmarshal(raw, &status); err != nil {
		return Status{State: "idle"}
	}

	return status
}

// Check fetches the manifest and works out what, if anything, this device
// is missing.
func Check() (*Info, error) {
	releases, err := fetchManifest()
	if err != nil {
		return nil, err
	}

	info := &Info{
		CurrentVersion: InstalledVersion(),
		Status:         CurrentStatus(),
	}
	for _, release := range releases {
		if release.Version > info.LatestVersion {
			info.LatestVersion = release.Version
		}
		if release.Version > info.CurrentVersion {
			info.Pending = append(info.Pending, release)
		}
	}

	return info, nil
}

// Apply downloads the runner and starts it, detached and as root, then
// returns immediately - the caller is answering an RPC, and the update
// takes minutes and ends by restarting this very process.
//
// Deliberately re-fetched rather than run from the copy already on disk:
// the source tree here is whatever the *last* update left behind, and an
// update has to be driven by the newest runner, not an old one that may
// not understand the newest manifest.
func Apply() error {
	if status := CurrentStatus(); status.State == "running" {
		return fmt.Errorf("an update is already running")
	}

	if err := download(updateScriptURL(), cRunnerPath); err != nil {
		return fmt.Errorf("downloading the updater: %w", err)
	}
	if err := os.Chmod(cRunnerPath, 0o755); err != nil { // perms: rwxr-xr-x
		return fmt.Errorf("making the updater executable: %w", err)
	}

	// setsid detaches it from this process's group, which is what lets it
	// survive the systemctl restart it performs at the end. Output goes
	// to the script's own log; nothing is piped back here, because there
	// will be no here to pipe it to.
	// The runner fetches the release scripts itself, so it has to be
	// pointed at the same place this build checks against.
	// Via env rather than "sudo VAR=value ...": sudo only accepts inline
	// assignments when its policy allows them, and refuses the whole
	// command when it doesn't.
	cmd := exec.Command(
		"setsid", "sudo", "-n", "/usr/bin/env",
		"OTC_REPO_RAW="+repoRaw(),
		"OTC_REPO_GH="+repoGH(),
		"/bin/bash", cRunnerPath,
	)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting the updater: %w", err)
	}
	// Released rather than waited on - this process may well be killed
	// before it finishes.
	if err := cmd.Process.Release(); err != nil {
		log.Error("could not release the updater process:", err)
	}

	log.Info("device update started")

	return nil
}

func fetchManifest() ([]Release, error) {
	client := &http.Client{Timeout: cManifestTimeout}
	resp, err := client.Get(manifestURL())
	if err != nil {
		return nil, fmt.Errorf("fetching the release manifest: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching the release manifest: %s", resp.Status)
	}

	return parseManifest(resp.Body)
}

// parseManifest reads the tab-separated release list. Anything it can't
// make sense of is skipped rather than fatal: a manifest written by a
// newer release may carry columns this build has never heard of, and
// refusing to update because of that would strand exactly the devices
// that most need to.
func parseManifest(r io.Reader) ([]Release, error) {
	var releases []Release
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// version, script sha, assets sha, summary - see the manifest's
		// own header. Only the version and the summary matter here; the
		// checksums are the runner's business, since it is the one that
		// downloads what they cover.
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			continue
		}
		version, err := strconv.Atoi(strings.TrimSpace(fields[0]))
		if err != nil {
			continue
		}
		description := ""
		if len(fields) >= 4 {
			description = strings.TrimSpace(fields[3])
		}
		releases = append(releases, Release{Version: version, Description: description})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading the release manifest: %w", err)
	}

	return releases, nil
}

func download(url, dest string) error {
	client := &http.Client{Timeout: cManifestTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}

	file, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := io.Copy(file, resp.Body); err != nil {
		return err
	}

	return nil
}
