// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"

	"github.com/fsnotify/fsnotify"
)

// Watcher is FolderWatcher.swift for Linux and Windows. FSEvents watches a
// tree; inotify and ReadDirectoryChangesW (as fsnotify exposes them) watch
// one directory each, so every subdirectory is registered up front and
// any directory created later is added as it appears.
type Watcher struct {
	w       *fsnotify.Watcher
	root    string
	onEvent func(path string, isDir bool, removed bool)
	// onLost runs when the system dropped events (a burst that overflowed
	// its buffer): the folder is to be looked at as a whole.
	onLost  func()
	mu      sync.Mutex
	closed  bool
	// full: a directory could not be watched because the system's limit
	// was reached (said once, in the log); the rest of the tree is still
	// watched, and the periodic passes find changes in what isn't.
	full bool
}

// watchBufferSize is how much each directory's watch buffers on Windows
// (fsnotify's least). Its default, 64 KiB, is allocated per directory: a
// folder of 10,000 directories held 640 MiB. A burst that overflows the
// smaller buffer is reported (ErrEventOverflow) and the folder is checked
// as a whole, as for any change.
const watchBufferSize = 4096

// NewWatcher starts watching root recursively. Hidden directories (a
// leading dot; on Windows also the Hidden and System attributes) are
// skipped, the same as the file enumeration. onLost (may be nil) runs when
// events were lost.
func NewWatcher(root string, onEvent func(path string, isDir bool, removed bool), onLost func()) (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{w: fw, root: root, onEvent: onEvent, onLost: onLost}
	if err := w.addTree(root); err != nil {
		_ = fw.Close()

		return nil, err
	}
	go w.loop()

	return w, nil
}

// addTree watches dir and every directory below it. Only dir itself not
// being watchable is an error: a subdirectory that can't be read, or the
// system's limit on watches reached (Linux: fs.inotify.max_user_watches),
// leaves the rest watched - the whole watch used to fail, and with it
// every change made between the periodic passes.
func (w *Watcher) addTree(dir string) error {
	return filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if p == dir {
				return err
			}

			return nil // an unreadable subtree is skipped, not fatal
		}
		if !d.IsDir() {
			return nil
		}
		if p != dir {
			if strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			if runtime.GOOS == "windows" {
				if fi, err := d.Info(); err == nil && hiddenAttr(fi) {
					return filepath.SkipDir
				}
			}
		}
		if err := w.w.AddWith(p, fsnotify.WithBufferSize(watchBufferSize)); err != nil {
			if p == w.root {
				return err
			}
			if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EMFILE) {
				w.mu.Lock()
				first := !w.full
				w.full = true
				w.mu.Unlock()
				if first {
					log.Printf("can't watch every folder in %s: the system's limit on watched folders is reached (on Linux, raise fs.inotify.max_user_watches) - changes in the rest are found by the periodic check", w.root)
				}

				return filepath.SkipAll
			}
			log.Printf("can't watch %s: %v - its changes are found by the periodic check", p, err)
		}

		return nil
	})
}

func (w *Watcher) loop() {
	for {
		select {
		case ev, ok := <-w.w.Events:
			if !ok {
				return
			}
			removed := ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename)
			isDir := false
			if !removed {
				if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
					isDir = true
					if ev.Has(fsnotify.Create) {
						// Files written into the new directory before this
						// watch existed are reported here so they aren't
						// missed until the next periodic reconcile.
						_ = w.addTree(ev.Name)
						_ = filepath.WalkDir(ev.Name, func(p string, d os.DirEntry, err error) error {
							if err == nil && !d.IsDir() {
								w.onEvent(p, false, false)
							}

							return nil
						})
					}
				}
			}
			w.onEvent(ev.Name, isDir, removed)
		case err, ok := <-w.w.Errors:
			if !ok {
				return
			}
			if errors.Is(err, fsnotify.ErrEventOverflow) && w.onLost != nil {
				// Events were lost: the folder is looked at as a whole.
				log.Printf("too many changes at once in %s - looking at the whole folder", w.root)
				w.onLost()
			}
		}
	}
}

// Stop ends the watch.
func (w *Watcher) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.closed = true
	_ = w.w.Close()
}
