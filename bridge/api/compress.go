// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"compress/gzip"
	"container/list"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Gzip for what the bridge sends besides its own pages (writeCompressed,
// site.go, renders those): the files of its site's static directory
// (fonts.css, the privacy and terms pages, sw.js). Pictures, fonts and
// video are compressed already and go as they are, and so does an answer
// to a Range request.

const (
	// cGzipMin is the smallest body worth it: below this, gzip's own
	// header and trailer eat most of what it saves.
	cGzipMin = 512
	// cGzipCacheBytes is the most the cache of gzipped bodies holds, and
	// cGzipCacheEntry the largest body it keeps.
	cGzipCacheBytes = 32 << 20
	cGzipCacheEntry = 4 << 20
)

// compressible reports whether a body of this Content-Type is text that
// gzip shrinks: HTML, CSS, scripts, JSON, a web app manifest, SVG, XML,
// plain text.
func compressible(contentType string) bool {
	mt, _, _ := strings.Cut(contentType, ";")
	switch mt = strings.ToLower(strings.TrimSpace(mt)); mt {
	case "application/javascript", "application/x-javascript", "application/json",
		"application/manifest+json", "application/xml", "image/svg+xml":
		return true
	}
	return strings.HasPrefix(mt, "text/")
}

// gzipWriters are reused: each holds about a megabyte of compressor state.
var gzipWriters = sync.Pool{New: func() any { return gzip.NewWriter(nil) }}

// gzipBytes is b gzipped.
func gzipBytes(b []byte) ([]byte, error) {
	var out bytes.Buffer
	zw := gzipWriters.Get().(*gzip.Writer)
	defer gzipWriters.Put(zw)
	zw.Reset(&out)
	if _, err := zw.Write(b); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// gzipCache keeps gzipped bodies the bridge sends again and again, so
// each is compressed once: a static file by its path, size and time. The
// least recently used go first once the bodies pass max bytes.
type gzipCache struct {
	mu    sync.Mutex
	max   int
	size  int
	order *list.List // of *gzipEntry, the most recently used first
	items map[string]*list.Element
}

type gzipEntry struct {
	key  string
	body []byte
}

func newGzipCache(max int) *gzipCache {
	return &gzipCache{max: max, order: list.New(), items: map[string]*list.Element{}}
}

// gzipped is the bridge's cache: shared by every request, bounded by
// cGzipCacheBytes.
var gzipped = newGzipCache(cGzipCacheBytes)

// get is the gzipped body kept under key, if there is one.
func (c *gzipCache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*gzipEntry).body, true
}

// put keeps body under key - unless it is over cGzipCacheEntry or the
// whole budget - and drops the least recently used bodies past max.
func (c *gzipCache) put(key string, body []byte) {
	if len(body) > cGzipCacheEntry || len(body) > c.max {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.order.MoveToFront(el)
		return
	}
	c.items[key] = c.order.PushFront(&gzipEntry{key, body})
	c.size += len(body)
	for c.size > c.max {
		last := c.order.Back()
		e := last.Value.(*gzipEntry)
		c.order.Remove(last)
		delete(c.items, e.key)
		c.size -= len(e.body)
	}
}

// serveStaticGzipped answers r with the static file at path gzipped, when
// it is text a client that takes gzip asked for whole, and reports whether
// it did; otherwise http.ServeFile sends it as it is. Its Last-Modified and
// If-Modified-Since work as ServeFile's do, so a page kept "no-cache" is
// still a 304 when it hasn't changed.
func serveStaticGzipped(w http.ResponseWriter, r *http.Request, path string) bool {
	ct := mime.TypeByExtension(filepath.Ext(path))
	if !compressible(ct) || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		return false
	}
	w.Header().Add("Vary", "Accept-Encoding")
	if r.Header.Get("Range") != "" || !acceptsGzip(r.Header.Get("Accept-Encoding")) {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < cGzipMin || info.Size() > cGzipCacheEntry {
		return false
	}
	mod := info.ModTime()
	key := "file:" + path + ":" + strconv.FormatInt(info.Size(), 10) + ":" + strconv.FormatInt(mod.UnixNano(), 10)
	body, ok := gzipped.get(key)
	if !ok {
		raw, err := os.ReadFile(path)
		if err != nil || int64(len(raw)) != info.Size() {
			return false // changed under us: ServeFile sends what is there now
		}
		if body, err = gzipBytes(raw); err != nil {
			return false
		}
		gzipped.put(key, body)
	}
	h := w.Header()
	h.Set("Last-Modified", mod.UTC().Format(http.TimeFormat))
	if since, err := http.ParseTime(r.Header.Get("If-Modified-Since")); err == nil && r.Header.Get("If-None-Match") == "" &&
		!mod.Truncate(time.Second).After(since) {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	h.Set("Content-Type", ct)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Encoding", "gzip")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
	return true
}
