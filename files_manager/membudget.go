// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// A download holds its whole file in memory several times over - the
// encrypted blob, the decrypted content, and the reply packed for the wire
// - and a sync client fetches several files at once. A Mac re-downloading
// a large folder took a device from 3.8 to 7 GB in ten seconds and the
// kernel killed it. memBudget bounds the file content in flight: a download
// that doesn't fit waits for others to finish instead of adding to them.
type memBudget struct {
	mu   sync.Mutex
	cond *sync.Cond
	used int64
	max  int64
}

// cDownloadCopies is how many copies of a file's content a download holds
// at its peak (see memBudget).
const cDownloadCopies = 3

// cHeicDecodeFactor is roughly how many bytes of memory decoding a HEIC to
// JPEG takes per byte of the file (see ReserveForDownload).
const cHeicDecodeFactor = 36

func newMemBudget(max int64) *memBudget {
	b := &memBudget{max: max}
	b.cond = sync.NewCond(&b.mu)
	return b
}

// acquire reserves n bytes, waiting while they don't fit, and returns the
// release. A request larger than the whole budget still runs - alone - so
// no file is ever too big to download.
func (b *memBudget) acquire(n int64) func() {
	if n <= 0 {
		return func() {}
	}
	if n > b.max {
		n = b.max
	}
	b.mu.Lock()
	for b.used > 0 && b.used+n > b.max {
		b.cond.Wait()
	}
	b.used += n
	b.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			b.used -= n
			b.mu.Unlock()
			b.cond.Broadcast()
		})
	}
}

// contentBudgetBytes is a fifth of the machine's memory (1.6 GB on an 8 GB
// Raspberry Pi), at least 256 MB.
func contentBudgetBytes() int64 {
	const floor = 256 << 20
	total := memTotalBytes()
	if total <= 0 {
		return 1 << 30
	}
	if b := total / 5; b > floor {
		return b
	}
	return floor
}

// MemTotalBytes is MemTotal from /proc/meminfo, or 0 off Linux.
func MemTotalBytes() int64 { return memTotalBytes() }

// memTotalBytes is MemTotal from /proc/meminfo, or 0 off Linux.
func memTotalBytes() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kb, err := strconv.ParseInt(fields[1], 10, 64)
			if err == nil {
				return kb << 10
			}
		}
	}
	return 0
}

// ReserveBytes holds n bytes of the content budget, waiting for room.
// Never nil.
func (mg *Manager) ReserveBytes(n int64) func() {
	if mg.contentBudget == nil {
		return func() {}
	}
	return mg.contentBudget.acquire(n)
}

// ReservePublicationMedia holds the content budget for serving a post's
// media file (issue #166: read whole, for the owner's feed and friends).
func (mg *Manager) ReservePublicationMedia(hash string) func() {
	if !dao.IsContentHash(hash) {
		return func() {}
	}
	info, err := os.Stat(filepath.Join(cfg.GetStr("otc", "unenc-storage-path"), hash))
	if err != nil {
		return func() {}
	}
	return mg.ReserveBytes(info.Size() * cDownloadCopies)
}

// ReserveForDownload holds a share of the content budget for serving the
// file at path (a version, with versionHash), waiting if the device is
// already holding as much file content as it should. Call the returned
// release once the reply has been sent - not before, the reply is one of
// the copies. Never nil.
func (mg *Manager) ReserveForDownload(path, versionHash string) func() {
	if mg.contentBudget == nil {
		return func() {}
	}
	var file *pb.File
	var err error
	if versionHash != "" {
		file, err = mg.dao.GetFileVersion(path, versionHash)
	} else {
		file, err = mg.dao.GetFileByPath(path)
	}
	if err != nil || file == nil {
		return func() {}
	}
	need := int64(file.Size) * cDownloadCopies
	// Issue #168: a HEIC is served converted to JPEG, and the decode is what
	// costs memory, not the file: ~1.3 bits a pixel, so a 2 MB iPhone photo
	// is 12 MP - ~48 MB as RGBA, plus the decoder's YCbCr and the JPEG being
	// built. The sync clients read originals with ReadFile, never this.
	if isHeicFile(file.Path, file.Mime) {
		need += int64(file.Size) * cHeicDecodeFactor
	}
	if need > mg.contentBudget.max/2 {
		log.Debug("download of", path, "waits for", need>>20, "MB of the", mg.contentBudget.max>>20, "MB content budget")
	}
	return mg.contentBudget.acquire(need)
}
