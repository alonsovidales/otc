// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
)

// Issue #141: files whose content is gone from the disk - a row pointing
// at a blob that isn't there - used to surface only as a download that
// came back empty, days later, on some other device. Once a day the
// device looks for them and tells the owner (Alerts, #64), once per
// change: the same set of missing files isn't reported again every day.

const (
	cIntegrityFirstRun = 10 * time.Minute
	cIntegrityEvery    = 24 * time.Hour
	// An upload's row is stored a moment before its content is written
	// (UploadFile's background write); a hash missing on the first look
	// is only reported if it is still missing this much later.
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

// missingContent returns the hashes (sorted) whose content exists() says
// is gone.
func missingContent(paths map[string][]string, exists func(hash string) bool) []string {
	var out []string
	for hash := range paths {
		if !exists(hash) {
			out = append(out, hash)
		}
	}
	sort.Strings(out)
	return out
}

// integrityReport is the alert for the missing hashes: a title with the
// count, and the affected paths (the first few) with what to do.
func integrityReport(missing []string, paths map[string][]string) (title, detail string) {
	var all []string
	for _, h := range missing {
		all = append(all, paths[h]...)
	}
	sort.Strings(all)
	n := len(all)
	if n == 1 {
		title = "1 file has lost its content"
	} else {
		title = fmt.Sprintf("%d files have lost their content", n)
	}
	shown := all
	if len(shown) > cIntegrityListed {
		shown = shown[:cIntegrityListed]
	}
	detail = strings.Join(shown, "; ")
	if n > len(shown) {
		detail += fmt.Sprintf("; and %d more", n-len(shown))
	}
	detail += ". Upload them again from where they came from - Sync All on a phone, or the desktop app's folder - and they are restored."
	return title, detail
}

func integrityDigest(missing []string) string {
	sum := sha256.Sum256([]byte(strings.Join(missing, ",")))
	return hex.EncodeToString(sum[:])
}

// CheckStorageIntegrity looks for files whose content is missing and
// raises one alert when that set has changed since the last report.
func (mg *Manager) CheckStorageIntegrity() error {
	paths, err := mg.dao.ContentPaths()
	if err != nil {
		return err
	}
	missing := missingContent(paths, mg.hasBlob)
	if len(missing) > 0 {
		time.Sleep(cIntegrityRecheck)
		still := map[string][]string{}
		for _, h := range missing {
			still[h] = paths[h]
		}
		missing = missingContent(still, mg.hasBlob)
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
	title, detail := integrityReport(missing, paths)
	log.Error("storage integrity check:", title, "-", detail)
	if err := mg.dao.AddErrorNotification(title, detail); err != nil {
		return err
	}
	return os.WriteFile(marker, []byte(digest+"\n"), 0o600) // perms: rw-------
}
