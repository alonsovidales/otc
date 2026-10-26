// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// The storage log: the same file and format as the Raspberry Pi's
// scripts/raid_watch.py, so the device's raidwatch.FollowLog copies it into
// Settings > Logs > Device as it does there. Root writes it in a root-owned
// directory and keeps it small: past storageLogMax it moves to .1 and starts
// again, with a line of its own first (FollowLog knows a file by it).
const (
	storageLogPath = "/var/log/otc-storage/storage.log"
	storageLogMax  = 512 << 10
	logTime        = "2006-01-02T15:04:05-0700"
)

type storageLog struct {
	path   string
	max    int64
	now    func() time.Time
	stderr io.Writer
	failed bool // said once on stderr, until a line is written again
}

// write appends each line of text with the time.
func (l *storageLog) write(text string) {
	if l == nil {
		return
	}
	for _, line := range strings.Split(text, "\n") {
		if line != "" {
			l.append(l.now().Format(logTime) + " " + line + "\n")
		}
	}
}

func (l *storageLog) append(s string) {
	err := func() error {
		if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
			return err
		}
		if fi, err := os.Lstat(l.path); err == nil && fi.Size()+int64(len(s)) > l.max {
			if err := os.Rename(l.path, l.path+".1"); err != nil {
				return err
			}
		}
		f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE|unix.O_NOFOLLOW, 0o644)
		if err != nil {
			return err
		}
		_, err = f.WriteString(s)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		return err
	}()
	if err != nil && !l.failed {
		fmt.Fprintf(l.stderr, "storage log not written: %v\n", err)
	}
	l.failed = err != nil
}
