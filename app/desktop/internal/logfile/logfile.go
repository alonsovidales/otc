// SPDX-License-Identifier: AGPL-3.0-or-later

// Package logfile keeps otc-sync's log in its config directory -
// otc-sync.log, and otc-sync.log.1 before it, 5 MiB each - besides
// standard error, which the Windows tray (a program without a console)
// sends nowhere: there it was the only place what went wrong was said.
// The tray's Show Log opens it; the Mac app logs to the unified log
// instead (Console.app).
package logfile

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// maxSize is how big the log grows before it starts over (the previous
// one kept as otc-sync.log.1).
const maxSize = 5 << 20

// Name is the log's file name in the config directory.
const Name = "otc-sync.log"

type rotating struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
}

// Start sends the standard logger to dir's otc-sync.log as well as to
// standard error, and returns the log's path.
func Start(dir string) (string, error) {
	w := &rotating{path: filepath.Join(dir, Name)}
	if err := w.open(); err != nil {
		return w.path, err
	}
	log.SetOutput(tee{file: w, std: os.Stderr})

	return w.path, nil
}

func (w *rotating) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) // perms: rw-------
	if err != nil {
		return err
	}
	w.f = f
	w.size = 0
	if fi, err := f.Stat(); err == nil {
		w.size = fi.Size()
	}

	return nil
}

func (w *rotating) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		if err := w.open(); err != nil {
			return 0, err
		}
	}
	if w.size > 0 && w.size+int64(len(b)) > maxSize {
		_ = w.f.Close()
		w.f = nil
		_ = os.Rename(w.path, w.path+".1")
		if err := w.open(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(b)
	w.size += int64(n)

	return n, err
}

// tee writes to the file and, best effort, to standard error: a failing
// standard error (no console) never stops the file's line.
type tee struct {
	file io.Writer
	std  io.Writer
}

func (t tee) Write(b []byte) (int, error) {
	_, _ = t.std.Write(b)

	return t.file.Write(b)
}
