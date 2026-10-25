// SPDX-License-Identifier: AGPL-3.0-or-later

package raidwatch

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alonsovidales/otc/log"
)

// The storage log: scripts/raid_watch.py (root) writes what it sees and
// does - the disks the Pi sees with their USB port and speed, the mirror's
// state, why a disk wasn't added, a rebuild's progress, the kernel's lines
// about USB, the disks and md - to a root-owned file, since this service
// can't read the journal and root must not write in this service's own
// log directory. FollowLog copies each new line into this device's log as
// "storage: ...", so Settings > Logs > Device shows it in every app.
var storageLogPath = "/var/log/otc-storage/storage.log"

const (
	followEvery   = 5 * time.Second
	followFile    = ".storage-log-read"
	followChunk   = 256 << 10 // read per look at most; the rest comes next time
	followMaxLine = 4096
	followHeadMax = 200
)

// followPos is how far the storage log has been copied: the file, known by
// its first line (each starts with the time it was started), and the offset.
// Kept in <storage>/.storage-log-read, so a restart neither copies the
// lines again nor loses the ones written while the service was down.
type followPos struct {
	Head string `json:"head"`
	Off  int64  `json:"off"`
}

// FollowLog copies the storage log into this device's log, every 5 s.
// Main instance only, not in a container (where there is no watcher).
// Blocks.
func FollowLog(storagePath string) {
	pos := loadFollowPos(storagePath)
	for {
		next := followOnce(pos, func(line string) { log.Info("storage:", line) })
		if next != pos {
			saveFollowPos(storagePath, next)
			pos = next
		}
		time.Sleep(followEvery)
	}
}

// followOnce hands out what was added to the storage log since pos, one
// line at a time, and says where it got to. A log moved to .1 since gives
// the rest of .1 first; one that shrank is read from its start.
func followOnce(pos followPos, out func(string)) followPos {
	head, size, err := fileHead(storageLogPath)
	if err != nil || head == "" {
		return pos // no watcher, or nothing written yet
	}
	if head != pos.Head {
		if pos.Head != "" {
			if oldHead, oldSize, err := fileHead(storageLogPath + ".1"); err == nil && oldHead == pos.Head {
				copyLines(storageLogPath+".1", pos.Off, oldSize, out)
			}
		}
		pos = followPos{Head: head}
	}
	if size < pos.Off {
		pos.Off = 0
	}
	pos.Off = copyLines(storageLogPath, pos.Off, size, out)
	return pos
}

// fileHead is the file's first line (at most followHeadMax bytes, "" while
// it has none) and its size.
func fileHead(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	buf := make([]byte, followHeadMax)
	n, err := f.ReadAt(buf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", 0, err
	}
	i := bytes.IndexByte(buf[:n], '\n')
	if i < 0 && n < followHeadMax {
		return "", fi.Size(), nil // the first line isn't finished
	}
	if i < 0 {
		i = n
	}
	return string(buf[:i]), fi.Size(), nil
}

// copyLines hands out the whole lines in [off, size) - at most followChunk
// bytes of them - and returns the offset after the last one. A line longer
// than the chunk is handed out cut, so one can't hold the log up.
func copyLines(path string, off, size int64, out func(string)) int64 {
	if size <= off {
		return off
	}
	f, err := os.Open(path)
	if err != nil {
		return off
	}
	defer f.Close()
	buf := make([]byte, min(size-off, followChunk))
	n, err := f.ReadAt(buf, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return off
	}
	buf = buf[:n]
	end := bytes.LastIndexByte(buf, '\n') + 1
	if end == 0 {
		if int64(n) < followChunk {
			return off // a line still being written
		}
		end = n
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(buf[:end]), "\n"), "\n") {
		if len(line) > followMaxLine {
			line = line[:followMaxLine] + "…"
		}
		out(strings.ToValidUTF8(line, "�"))
	}
	return off + int64(end)
}

func loadFollowPos(storagePath string) followPos {
	var pos followPos
	if b, err := os.ReadFile(filepath.Join(storagePath, followFile)); err == nil {
		_ = json.Unmarshal(b, &pos)
	}
	return pos
}

func saveFollowPos(storagePath string, pos followPos) {
	b, err := json.Marshal(pos)
	if err != nil {
		return
	}
	tmp := filepath.Join(storagePath, followFile+".tmp")
	if os.WriteFile(tmp, b, 0o600) != nil || os.Rename(tmp, filepath.Join(storagePath, followFile)) != nil {
		_ = os.Remove(tmp) // kept in memory: a restart copies from the last saved place
	}
}
