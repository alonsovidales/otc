// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

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
	mu      sync.Mutex
	closed  bool
}

// NewWatcher starts watching root recursively. Hidden directories (a
// leading dot) are skipped, the same as the file enumeration.
func NewWatcher(root string, onEvent func(path string, isDir bool, removed bool)) (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{w: fw, root: root, onEvent: onEvent}
	if err := w.addTree(root); err != nil {
		_ = fw.Close()

		return nil, err
	}
	go w.loop()

	return w, nil
}

func (w *Watcher) addTree(dir string) error {
	return filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable subtree is skipped, not fatal
		}
		if !d.IsDir() {
			return nil
		}
		if p != dir && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}

		return w.w.Add(p)
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
		case _, ok := <-w.w.Errors:
			if !ok {
				return
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
