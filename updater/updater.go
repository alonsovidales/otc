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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/container"
	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/i18n"
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

	// cRequestPath is the trigger the device writes to ask for an update,
	// and cRootRunner the root-side script systemd's otc-update.path
	// starts in answer to it (scripts/update-runner/). The service runs
	// with NoNewPrivileges, so this is the only way it can get an update
	// run as root: it cannot sudo, whatever sudoers says. A device whose
	// installer predates the path unit has no runner, and falls back to
	// sudo below - which works only on a hand-set-up development box.
	cRequestPath = "/var/lib/otc/update.request"
	cRootRunner  = "/usr/local/bin/otc-update-runner"

	// cManifestTimeout keeps a check from hanging the RPC it was called
	// from when GitHub is slow or unreachable.
	cManifestTimeout = 15 * time.Second

	// cUpdateUnit runs every update cRootRunner starts.
	cUpdateUnit = "otc-update.service"
	// cRunLock is held, shared, by every scripts/update.sh for as long as
	// it runs - inside cUpdateUnit or started by hand outside it.
	cRunLock = "/run/otc-update.lock"
	// cInterruptedCode is the status code (dev.update.interrupted) of a
	// run read as interrupted: once its unit is gone while the file still
	// says "running" - the same code otc-update-stopped writes.
	cInterruptedCode = "interrupted"
)

// cInterrupted is that run's message, in English as the status file keeps
// it - the same words otc-update-stopped writes.
var cInterrupted = i18n.DevUpdateInterrupted().Render("en")

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
	// Issue #183: from RELEASES (kinds.go); minor and "" when unknown.
	Kind  string
	Label string
}

// Status is what the Settings screen shows. Read from the file the runner
// writes, so it survives the restart the runner performs.
type Status struct {
	State string `json:"state"`
	// Message is the English the runner wrote (what older devices show);
	// Text renders it in a request's language from Code and Args.
	Message string  `json:"message"`
	Code    string  `json:"code"`
	Args    rawArgs `json:"args"`
	Version string  `json:"version"`
	Updated string  `json:"updated"`
}

// Text is the status message in lang, the request's language: the
// runner's code and arguments rendered, or Message as written (English, a
// runner from before codes, a code this build doesn't know).
func (s Status) Text(lang string) string {
	return devtext.UpdateStatus(lang, s.Code, []byte(s.Args), s.Message)
}

// rawArgs is the status file's "args" object kept as its JSON, in a string
// so that Status stays comparable (reconcileStatus compares two reads).
type rawArgs string

func (a *rawArgs) UnmarshalJSON(b []byte) error {
	*a = rawArgs(b)
	return nil
}

// Info answers "is there anything to install, and what happened last
// time".
type Info struct {
	CurrentVersion int
	LatestVersion  int
	Pending        []Release
	Status         Status
	// Issue #183: the labels of the installed and the newest release.
	CurrentLabel string
	LatestLabel  string
	// KindsVerified: the kinds came from a signed RELEASES. Without it
	// every release reads as minor, which says nothing about whether a
	// major or critical one is pending (see nextAlert).
	KindsVerified bool
	// ImageCheckFailed: in a container, the registry could not be asked
	// whether a newer release's image is published (onlyPublishedImages),
	// so what can be installed is not known: nothing is pending, and
	// nextAlert treats the check as unsettled.
	ImageCheckFailed bool
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

// Pending reports whether an update was asked for and hasn't ended: its
// request still waits for the root runner (which removes it as it
// starts), or a run is going on. Settings > Wi-Fi refuses a change then.
func Pending() bool {
	if _, err := os.Lstat(cRequestPath); err == nil {
		return true
	}
	return CurrentStatus().State == "running"
}

// CurrentStatus reports on the last (or running) update. A missing file
// simply means no update has ever been started here.
func CurrentStatus() Status {
	return reconcileStatus(readStatus, updateUnitGone)
}

// readStatus reads the status file as the root scripts wrote it.
func readStatus() Status {
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

// reconcileStatus reports a "running" status whose run has stopped (see
// updateUnitGone) as failed. A power cut mid-update (no unit hook sees
// that one) otherwise left the file "running" for good, and Apply refuses
// while it says so - the Update button locked forever. Only reported,
// never written: the next run overwrites the file anyway, and it is root's.
func reconcileStatus(read func() Status, unitGone func() bool) Status {
	status := read()
	if status.State != "running" || !unitGone() {
		return status
	}
	// The run was seen stopped after that read. One that ended in between
	// has written its own failed (with the real reason) or done, and one
	// started since its own "running": only a file unchanged from before
	// the run was seen stopped belongs to a run that was cut off.
	if now := read(); now != status {
		return now
	}

	return Status{State: "failed", Message: cInterrupted, Code: cInterruptedCode, Version: status.Version, Updated: status.Updated}
}

// unitActiveState asks systemd for the update unit's ActiveState. `show`
// rather than `is-active`, whose exit code is non-zero for "activating" -
// what a running oneshot is.
func unitActiveState() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "show", "-p", "ActiveState", "--value", cUpdateUnit).Output()
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(out)), nil
}

// updateUnitGone reports whether the run behind a "running" status has
// definitely stopped. The runner and the update.sh it starts write
// "running" inside the update unit, but an update.sh started by hand (the
// README's console line, the legacy sudo path in Apply, a shell on a dev
// box) writes it from outside, with the unit inactive throughout. Any
// doubt (no runner - a legacy sudo device -, systemctl missing or slow, an
// unexpected answer, a lock that can't be tested) is false, which keeps
// the status as written.
func updateUnitGone() bool {
	if _, err := os.Stat(cRootRunner); err != nil {
		return false
	}

	return runStopped(func() bool { return runLockHeld(cRunLock) }, unitActiveState)
}

// runStopped: no update.sh holds cRunLock, and the unit reads as stopped.
// The lock first: it costs no fork, and answers for most live runs.
func runStopped(scriptRunning func() bool, activeState func() (string, error)) bool {
	if scriptRunning() {
		return false
	}
	state, err := activeState()
	if err != nil {
		return false
	}

	return unitStopped(state)
}

// unitStopped: activating, active, deactivating, reloading or "" are a
// run still going (or unknown).
func unitStopped(activeState string) bool {
	return activeState == "inactive" || activeState == "failed"
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
	// Issue #183: kinds and labels, when the signed file can be had; without
	// it every release counts as minor and shows by number.
	kinds, err := fetchKinds()
	if err != nil {
		log.Debug("release kinds unavailable:", err)
	}
	info.KindsVerified = err == nil
	for _, release := range releases {
		if m, ok := kinds[release.Version]; ok {
			release.Kind, release.Label = m.kind, m.label
		} else {
			release.Kind = KindMinor
		}
		if release.Version > info.LatestVersion {
			info.LatestVersion = release.Version
			info.LatestLabel = release.Label
		}
		if release.Version == info.CurrentVersion {
			info.CurrentLabel = release.Label
		}
		if release.Version > info.CurrentVersion {
			info.Pending = append(info.Pending, release)
		}
	}
	if container.Enabled() {
		onlyPublishedImages(info, newImageLookup())
	}

	return info, nil
}

// newImageLookup is one check's question to the registry: one token for
// every release it asks about (container.ImageLookup). A var for tests.
var newImageLookup = func() func(int) (bool, error) {
	return new(container.ImageLookup).Available
}

// cImageLookups is how many of the newest pending releases one check asks
// the registry about.
const cImageLookups = 5

// onlyPublishedImages narrows info, in a container, to the releases it can
// be updated to: those up to the newest whose image is published
// (available). A container is updated by pulling its new image, which is
// built and published some minutes after the release; until then the
// release is not offered, not alerted and not pushed. With none published,
// nothing is pending and the latest is the installed release. A lookup
// that fails ends the asking - the registry is out of reach, and asking
// about older releases would only wait for it again - and marks the check
// ImageCheckFailed.
func onlyPublishedImages(info *Info, available func(int) (bool, error)) {
	if len(info.Pending) == 0 {
		return
	}
	newest := Release{}
	byVersion := slices.Clone(info.Pending)
	slices.SortFunc(byVersion, func(a, b Release) int { return b.Version - a.Version })
	for i, r := range byVersion {
		if i >= cImageLookups {
			break
		}
		published, err := available(r.Version)
		if err != nil {
			log.Debug("could not ask the registry whether release", r.Version, "has its image:", err)
			info.ImageCheckFailed = true
			break
		}
		if published {
			newest = r
			break
		}
	}
	var kept []Release
	for _, r := range info.Pending {
		if r.Version <= newest.Version {
			kept = append(kept, r)
		}
	}
	info.Pending = kept
	if newest.Version == 0 {
		info.LatestVersion, info.LatestLabel = info.CurrentVersion, info.CurrentLabel
		return
	}
	info.LatestVersion, info.LatestLabel = newest.Version, newest.Label
}

// Apply downloads the runner and starts it, detached and as root, then
// returns immediately - the caller is answering an RPC, and the update
// takes minutes and ends by restarting this very process.
//
// Deliberately re-fetched rather than run from the copy already on disk:
// the source tree here is whatever the *last* update left behind, and an
// update has to be driven by the newest runner, not an old one that may
// not understand the newest manifest.
//
// In a container nothing is run: the device is updated by pulling its new
// image on the server, which the error says.
func Apply() error {
	if container.Enabled() {
		return devtext.New(i18n.DevUpdateContainerPull())
	}
	if status := CurrentStatus(); status.State == "running" {
		return devtext.New(i18n.DevUpdateAlreadyRunning())
	}

	// The normal path: hand the run to systemd. The trigger carries no
	// instructions on purpose - the root side reads the repository from
	// the root-owned config, so nothing writable by this user decides
	// what gets run as root. See scripts/update-runner/otc-update-runner.sh.
	if _, err := os.Stat(cRootRunner); err == nil {
		stamp := time.Now().UTC().Format(time.RFC3339) + "\n"
		if err := os.WriteFile(cRequestPath, []byte(stamp), 0o644); err != nil { // perms: rw-r--r--
			return devtext.Wrap(i18n.DevUpdateRequestFailed(err.Error()), err)
		}
		log.Info("device update requested")

		return nil
	}

	// Legacy path, for a device set up by hand before the path unit
	// existed: run the script ourselves under sudo.
	if err := download(updateScriptURL(), cRunnerPath); err != nil {
		return devtext.Wrap(i18n.DevUpdateRunnerDownloadFailed(err.Error()), err)
	}
	if err := os.Chmod(cRunnerPath, 0o755); err != nil { // perms: rwxr-xr-x
		return devtext.Wrap(i18n.DevUpdateRunnerChmodFailed(err.Error()), err)
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
		return devtext.Wrap(i18n.DevUpdateRunnerStartFailed(err.Error()), err)
	}
	// Released rather than waited on - this process may well be killed
	// before it finishes.
	if err := cmd.Process.Release(); err != nil {
		log.Error("could not release the updater process:", err)
	}

	log.Info("device update started")

	return nil
}

// The last manifest that verified, for the few minutes after a release
// when the CDN may serve the new VERSIONS next to the old VERSIONS.sig.
var (
	verifiedMu       sync.Mutex
	verifiedReleases []Release
)

// fetchManifest reads VERSIONS only when it carries the release key's
// signature, as the root runner does: unsigned, anyone able to change the
// branch could put any text into a real critical banner (its summary comes
// from here) or list made-up releases - and the body was read unbounded.
func fetchManifest() ([]Release, error) {
	body, err := fetchSigned(manifestURL())
	if err != nil {
		if errors.Is(err, errNotSigned) {
			verifiedMu.Lock()
			cached := verifiedReleases
			verifiedMu.Unlock()
			if cached != nil {
				log.Debug("release manifest not verified, showing the last one that was:", err)
				return cached, nil
			}
		}
		return nil, fmt.Errorf("fetching the release manifest: %w", err) // i18n-ignore: check_error, which the apps only test for, never show
	}
	releases, err := parseManifest(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	verifiedMu.Lock()
	verifiedReleases = releases
	verifiedMu.Unlock()

	return releases, nil
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
		return nil, fmt.Errorf("reading the release manifest: %w", err) // i18n-ignore: check_error, which the apps only test for, never show
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
