// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/alonsovidales/otc/alerttext"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
)

// Issue #141: files whose content is gone from the disk - a row pointing
// at a blob that isn't there - used to surface only as a download that
// came back empty, days later, on some other device. Once a day the
// device looks for them and tells the owner (Alerts, #64), once per
// change: the same set of missing files isn't reported again every day.

const (
	// Issue #173: soon after start - this run is also what fills in which
	// blobs are missing for listings (missingblobs.go).
	cIntegrityFirstRun = time.Minute
	cIntegrityEvery    = 24 * time.Hour
	// A hash missing on the first look is only reported if it is still
	// missing this much later (a DelFile and an upload of the same
	// content crossing; uploads write the content before the row now).
	cIntegrityRecheck = time.Minute
	cIntegrityListed  = 15
)

func (mg *Manager) integrityChecker() {
	time.Sleep(cIntegrityFirstRun)
	for {
		if err := mg.CheckStorageIntegrity(); err != nil {
			log.Error("storage integrity check:", err)
		}
		time.Sleep(cIntegrityEvery)
	}
}

// cIntegrityPage is how many hashes the check reads at a time.
const cIntegrityPage = 5000

// hashPager is a page of the distinct hashes the files (versions false)
// or the kept versions use, after after: dao.HashesPage.
type hashPager func(versions bool, after string, limit int) ([]string, error)

// missingHashes visits every hash the files and kept versions use, a page
// at a time, and returns those exists() says are gone, sorted, once each.
// Only those are kept: every (hash, path) pair of the library used to be
// loaded first, ~50 MB at 200k files, for a check that finds none missing
// almost every day.
func missingHashes(page hashPager, exists func(hash string) bool) ([]string, error) {
	gone := map[string]bool{}
	for _, versions := range []bool{false, true} {
		after := ""
		for {
			hashes, err := page(versions, after, cIntegrityPage)
			if err != nil {
				return nil, err
			}
			for _, h := range hashes {
				if !gone[h] && !exists(h) {
					gone[h] = true
				}
			}
			if len(hashes) < cIntegrityPage {
				break
			}
			after = hashes[len(hashes)-1]
		}
	}
	out := make([]string, 0, len(gone))
	for h := range gone {
		out = append(out, h)
	}
	sort.Strings(out)
	return out, nil
}

// stillMissing is the hashes of missing exists() still says are gone.
func stillMissing(missing []string, exists func(hash string) bool) []string {
	var out []string
	for _, h := range missing {
		if !exists(h) {
			out = append(out, h)
		}
	}
	return out
}

// integrityReport is integrityAlert's English: its title and detail.
func integrityReport(missing []string, paths map[string][]string) (title, detail string) {
	a := integrityAlert(missing, paths)
	return a.Title, a.Detail
}

// integrityAlert is the alert for the missing hashes: a title with the
// count, and the affected paths (the first few) with what to do.
func integrityAlert(missing []string, paths map[string][]string) alerttext.Alert {
	var all []string
	for _, h := range missing {
		all = append(all, paths[h]...)
	}
	sort.Strings(all)
	n := len(all)
	shown := all
	if len(shown) > cIntegrityListed {
		shown = shown[:cIntegrityListed]
	}
	listed := strings.Join(shown, "; ")
	detail := i18n.DevAlertIntegrityDetail(listed)
	if n > len(shown) {
		detail = i18n.DevAlertIntegrityDetailMore(listed, n-len(shown))
	}
	return alerttext.New(i18n.DevAlertIntegrityLost(n), detail)
}

func integrityDigest(missing []string) string {
	sum := sha256.Sum256([]byte(strings.Join(missing, ",")))
	return hex.EncodeToString(sum[:])
}

// CheckStorageIntegrity looks for files whose content is missing and
// raises one alert when that set has changed since the last report.
func (mg *Manager) CheckStorageIntegrity() error {
	// Every hash is looked at (hasBlob also tells listings which blobs are
	// missing, missingblobs.go); only the missing ones' paths are read.
	missing, err := missingHashes(mg.dao.HashesPage, mg.hasBlob)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		time.Sleep(cIntegrityRecheck)
		missing = stillMissing(missing, mg.hasBlob)
	}
	var paths map[string][]string
	if len(missing) > 0 {
		if paths, err = mg.dao.ContentPathsOf(missing); err != nil {
			return err
		}
		// A row deleted meanwhile, content and all: nothing lost.
		missing = slices.DeleteFunc(missing, func(h string) bool { return len(paths[h]) == 0 })
	}

	marker := filepath.Join(cfg.GetStr("otc", "storage-path"), ".integrity-reported")
	if len(missing) == 0 {
		os.Remove(marker)
		log.Info("storage integrity check: every file's content is on disk")
		return nil
	}
	digest := integrityDigest(missing)
	if prev, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(prev)) == digest {
		log.Info("storage integrity check:", len(missing), "missing blobs, already reported")
		return nil
	}
	alert := integrityAlert(missing, paths)
	// The paths go to the owner's Alerts only: at Error the log names
	// hashes, never paths, and "Send logs" mails it off the device. Capped
	// like the detail, so a RAID loss doesn't write one line of many KB.
	shown := missing
	if len(shown) > cIntegrityListed {
		shown = shown[:cIntegrityListed]
	}
	log.Error("storage integrity check:", alert.Title, "-", len(missing), "missing blobs:", strings.Join(shown, ","))
	log.Debug("storage integrity check:", alert.Detail)
	if err := mg.dao.AddErrorNotification(alert); err != nil {
		return err
	}
	return os.WriteFile(marker, []byte(digest+"\n"), 0o600) // perms: rw-------
}
