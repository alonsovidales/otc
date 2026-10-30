// SPDX-License-Identifier: AGPL-3.0-or-later

// Package modelfetch downloads a model's files when the device first needs
// them, in the background and gently, instead of with every update: Smart
// search's model is about 415 MB, and an update that fetched it on every
// device - Smart search on or off - took the whole of a slow home link
// for a long while.
//
// Each file is pinned by size and SHA-256. It is written to a hidden
// ".<name>.part" file in the directory, hashed while it is written, and
// renamed to its name only when it matches its pin, so a file found under
// its name is whole. A part left by a try that failed (or a restart) is
// resumed with a Range request; a server that answers the whole file
// instead starts it again. The transfer is capped (Config.BytesPerSecond)
// so it never fills the link, cut when nothing arrives for two minutes,
// and refused before it starts when the files wouldn't fit (Config.Room).
// A try that fails is tried again by itself, a minute later, doubling up
// to an hour, for as long as someone wants the model (Config.Wanted).
package modelfetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/alonsovidales/otc/log"
)

// File is one file of a model: Name in the directory it is kept in, Asset
// its name under the base URL, and its pinned size and SHA-256 (hex).
type File struct {
	Name   string
	Asset  string
	Size   int64
	SHA256 string
}

// Why the last try failed (Problem.Kind).
const (
	// NoSpace: the files don't fit, the device's reserve kept: Need bytes
	// are still to be written and Left can be.
	NoSpace = "no_space"
	// Unreachable: the download didn't get through - no connection, a
	// transfer that stalled or was cut, the server's own error.
	Unreachable = "unreachable"
	// Failed: anything else - a file that didn't match its pin, one that
	// couldn't be written, a refusal.
	Failed = "failed"
)

// Problem is why the last try failed.
type Problem struct {
	Kind       string
	Need, Left int64
}

// State is where the download is.
type State struct {
	// Ready: every file is in place.
	Ready bool
	// Downloading: asked for and not ready yet - downloading, or waiting to
	// try again (Problem says why).
	Downloading bool
	// Done of Total bytes: Total is what was missing when this download
	// was asked for, Done what of it is on the disk.
	Done, Total int64
	// Problem is the last try's failure while it waits to try again; nil
	// otherwise.
	Problem *Problem
}

// Percent is how far the download is, 0-99 until every file is in place.
func (s State) Percent() int {
	if s.Ready {
		return 100
	}
	if s.Total <= 0 {
		return 0
	}
	return int(min(max(s.Done*100/s.Total, 0), 99))
}

// Config is what a Fetcher downloads, where, and how.
type Config struct {
	// Name is the model's, for the log ("smart search's model").
	Name string
	// Dir is where the files are kept; made when it isn't there.
	Dir string
	// BaseURL is where the files are, each at BaseURL + File.Asset.
	BaseURL string
	Files   []File
	// BytesPerSecond caps the transfer; 0: no cap.
	BytesPerSecond int64
	// Room is how many bytes can be written in dir, the device's reserve
	// kept; nil, or an error, is not checked.
	Room func(dir string) (int64, error)
	// Wanted is asked before each new try; false ends the download. nil:
	// always wanted, until Stop.
	Wanted func() bool
	// OnReady is called once every file is in place, from the download's
	// goroutine.
	OnReady func()
	// Client makes the requests; nil: one with timeouts on connecting and
	// on the answer's headers (never on the whole transfer).
	Client *http.Client
}

const (
	// cRetryFirst is the wait after a failed try, doubling up to cRetryMax;
	// a try that wrote at least cProgress starts again from cRetryFirst.
	cRetryFirst = time.Minute
	cRetryMax   = time.Hour
	cProgress   = 1 << 20
	// cStall: a transfer that receives nothing for this long is cut (as
	// update.sh's curl --speed-limit 1 --speed-time 120).
	cStall = 2 * time.Minute
	// cChunk is how much is read and written at a time.
	cChunk = 32 << 10
)

// Fetcher downloads one model's files on demand. Its methods are safe to
// call from any goroutine.
type Fetcher struct {
	c      Config
	client *http.Client

	// Tests' clock, cut and waits.
	now        func() time.Time
	sleep      func(ctx context.Context, d time.Duration) error
	stall      time.Duration
	retryFirst time.Duration
	retryMax   time.Duration

	// opMu: one Start or Stop at a time.
	opMu sync.Mutex

	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	done    chan struct{}
	ready   bool
	problem *Problem
	total   int64
	got     atomic.Int64
}

// New is a Fetcher for c, idle until Start.
func New(c Config) *Fetcher {
	if !strings.HasSuffix(c.BaseURL, "/") {
		c.BaseURL += "/"
	}
	client := c.Client
	if client == nil {
		client = defaultClient()
	}
	return &Fetcher{
		c:          c,
		client:     client,
		now:        time.Now,
		sleep:      sleepCtx,
		stall:      cStall,
		retryFirst: cRetryFirst,
		retryMax:   cRetryMax,
	}
}

// defaultClient answers within a minute of being asked, or fails; a
// transfer may take as long as it takes, unless it stalls (cStall).
func defaultClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: time.Minute,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (f *Fetcher) path(file File) string { return filepath.Join(f.c.Dir, file.Name) }

func (f *Fetcher) partPath(file File) string {
	return filepath.Join(f.c.Dir, "."+file.Name+".part")
}

// has is whether file is in place: a regular file of its pinned size
// (only one that matched its pin is ever renamed there).
func (f *Fetcher) has(file File) bool {
	fi, err := os.Lstat(f.path(file))
	return err == nil && fi.Mode().IsRegular() && fi.Size() == file.Size
}

// Ready is whether every file is in place.
func (f *Fetcher) Ready() bool {
	f.mu.Lock()
	ready := f.ready
	f.mu.Unlock()
	if ready {
		return true
	}
	for _, file := range f.c.Files {
		if !f.has(file) {
			return false
		}
	}
	f.mu.Lock()
	f.ready = true
	f.mu.Unlock()
	return true
}

// Start starts downloading what is missing, or joins the download
// already going on; OnReady follows once every file is in place (at once
// when they all are already, and never again after that).
func (f *Fetcher) Start() {
	f.opMu.Lock()
	defer f.opMu.Unlock()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.running || f.ready {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.running, f.cancel, f.done, f.problem = true, cancel, make(chan struct{}), nil
	go f.run(ctx, f.done)
}

// Stop ends the download, waits for it to end, and removes what it had
// written of files not yet whole; the files in place stay.
func (f *Fetcher) Stop() {
	f.opMu.Lock()
	defer f.opMu.Unlock()
	f.mu.Lock()
	running, cancel, done := f.running, f.cancel, f.done
	f.mu.Unlock()
	if running {
		cancel()
		<-done
	}
	f.removeParts()
}

// removeParts removes the files' parts and forgets the progress.
func (f *Fetcher) removeParts() {
	for _, file := range f.c.Files {
		if err := os.Remove(f.partPath(file)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Error(f.c.Name+": could not remove a partial download:", err)
		}
	}
	f.mu.Lock()
	f.problem, f.total = nil, 0
	f.got.Store(0)
	f.mu.Unlock()
}

// State is where the download is now.
func (f *Fetcher) State() State {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := State{Ready: f.ready, Downloading: f.running && !f.ready, Done: f.got.Load(), Total: f.total}
	if s.Downloading && f.problem != nil {
		p := *f.problem
		s.Problem = &p
	}
	return s
}

// missing is the files not in place, the bytes they take, and what their
// parts already hold.
func (f *Fetcher) missing() (files []File, size, have int64) {
	for _, file := range f.c.Files {
		if f.has(file) {
			continue
		}
		files = append(files, file)
		size += file.Size
		if fi, err := os.Lstat(f.partPath(file)); err == nil && fi.Mode().IsRegular() && fi.Size() <= file.Size {
			have += fi.Size()
		}
	}
	return files, size, have
}

func (f *Fetcher) run(ctx context.Context, done chan struct{}) {
	defer func() {
		f.mu.Lock()
		f.running = false
		f.mu.Unlock()
		close(done)
	}()
	_, size, _ := f.missing()
	f.mu.Lock()
	f.total = size
	f.mu.Unlock()
	if size > 0 {
		speed := "uncapped" // i18n-ignore: a log line
		if f.c.BytesPerSecond > 0 {
			speed = fmt.Sprintf("at most %d KB/s", f.c.BytesPerSecond>>10) // i18n-ignore: a log line
		}
		log.Info(f.c.Name+": downloading", size>>20, "MB,", speed)
	}
	started := f.now()
	wait := f.retryFirst
	for {
		wrote, err := f.try(ctx)
		if err == nil {
			f.mu.Lock()
			f.ready, f.problem = true, nil
			f.mu.Unlock()
			if size > 0 {
				log.Info(f.c.Name+": downloaded in", f.now().Sub(started).Round(time.Second))
			}
			if f.c.OnReady != nil {
				f.c.OnReady()
			}
			return
		}
		if ctx.Err() != nil {
			log.Info(f.c.Name + ": the download stopped")
			return
		}
		p := classify(err)
		f.mu.Lock()
		f.problem = p
		f.mu.Unlock()
		if wrote >= cProgress {
			wait = f.retryFirst
		}
		log.Info(f.c.Name+": the download failed ("+short(err)+"), trying again in", wait)
		if f.sleep(ctx, wait) != nil {
			log.Info(f.c.Name + ": the download stopped")
			return
		}
		if f.c.Wanted != nil && !f.c.Wanted() {
			log.Info(f.c.Name + ": no longer wanted, the download stops")
			f.removeParts()
			return
		}
		wait = min(2*wait, f.retryMax)
	}
}

// spaceError is a download that wouldn't fit.
type spaceError struct{ need, left int64 }

func (e *spaceError) Error() string {
	return fmt.Sprintf("not enough space: %d MB needed, %d MB can be written", e.need>>20, e.left>>20) // i18n-ignore: a log line; the device's words are Problem's
}

// statusError is a server's answer other than the file.
type statusError struct{ code int }

func (e *statusError) Error() string { return "HTTP " + strconv.Itoa(e.code) } // i18n-ignore: a log line

var (
	errMismatch = errors.New("the file doesn't match its pin")               // i18n-ignore: a log line
	errTooLong  = errors.New("the server sent more than the file")           // i18n-ignore: a log line
	errShort    = errors.New("the transfer ended before the file did")       // i18n-ignore: a log line
	errStalled  = errors.New("nothing arrived for two minutes")              // i18n-ignore: a log line
	errRange    = errors.New("the server answered another part of the file") // i18n-ignore: a log line
)

// try downloads every file not in place, and is how many bytes it wrote.
func (f *Fetcher) try(ctx context.Context) (wrote int64, err error) {
	if err := os.MkdirAll(f.c.Dir, 0o755); err != nil { // perms: rwxr-xr-x, a model the service reads
		return 0, err
	}
	files, size, have := f.missing()
	f.mu.Lock()
	f.got.Store(max(0, f.total-size) + have)
	f.mu.Unlock()
	if len(files) == 0 {
		return 0, nil
	}
	need := size - have
	if f.c.Room != nil {
		if room, err := f.c.Room(f.c.Dir); err == nil && need > room {
			return 0, &spaceError{need: need, left: max(room, 0)}
		}
	}
	for _, file := range files {
		n, err := f.fetchFile(ctx, file)
		wrote += n
		if err != nil {
			if errors.Is(err, syscall.ENOSPC) {
				left := int64(0)
				if f.c.Room != nil {
					if room, rerr := f.c.Room(f.c.Dir); rerr == nil {
						left = max(room, 0)
					}
				}
				_, size, have := f.missing()
				return wrote, &spaceError{need: size - have, left: left}
			}
			return wrote, err
		}
	}
	return wrote, nil
}

// fetchFile downloads file into its part, resuming what the part holds,
// and renames it into place once it matches its pin. Wrote: bytes written.
func (f *Fetcher) fetchFile(ctx context.Context, file File) (wrote int64, err error) {
	part := f.partPath(file)
	fh, err := os.OpenFile(part, os.O_RDWR|os.O_CREATE, 0o644) // perms: rw-r--r--, a model the service reads
	if err != nil {
		return 0, err
	}
	defer func() {
		if fh != nil {
			fh.Close()
		}
	}()
	fi, err := fh.Stat()
	if err != nil {
		return 0, err
	}
	if !fi.Mode().IsRegular() {
		return 0, fmt.Errorf("%s is not a regular file", part) // i18n-ignore: a log line
	}
	off := fi.Size()
	h := sha256.New()
	if off > file.Size {
		if err := fh.Truncate(0); err != nil {
			return 0, err
		}
		off = 0
	}
	if off > 0 {
		if _, err := io.Copy(h, io.NewSectionReader(fh, 0, off)); err != nil {
			return 0, err
		}
	}
	if off < file.Size {
		off, wrote, err = f.download(ctx, file, fh, h, off)
		if err != nil {
			if errors.Is(err, errTooLong) {
				fh.Close()
				fh = nil
				os.Remove(part)
			}
			return wrote, err
		}
	}
	if off != file.Size {
		return wrote, errShort
	}
	if err := fh.Sync(); err != nil {
		return wrote, err
	}
	err = fh.Close()
	fh = nil
	if err != nil {
		return wrote, err
	}
	if hex.EncodeToString(h.Sum(nil)) != file.SHA256 {
		os.Remove(part)
		f.got.Add(-file.Size)
		return wrote, fmt.Errorf("%s: %w", file.Name, errMismatch)
	}
	if err := os.Rename(part, f.path(file)); err != nil {
		return wrote, err
	}
	if d, err := os.Open(f.c.Dir); err == nil {
		d.Sync()
		d.Close()
	}
	return wrote, nil
}

// download writes file's bytes from off on to fh, through h, and is
// where the part ends now and how much it wrote.
func (f *Fetcher) download(ctx context.Context, file File, fh *os.File, h hash.Hash, off int64) (end, wrote int64, err error) {
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var stalled atomic.Bool
	watchdog := time.AfterFunc(f.stall, func() {
		stalled.Store(true)
		cancel()
	})
	defer watchdog.Stop()
	// stallErr is err as the cut it was, when the watchdog made it.
	stallErr := func(err error) error {
		if stalled.Load() && ctx.Err() == nil {
			return errStalled
		}
		return err
	}

	req, err := http.NewRequestWithContext(rctx, http.MethodGet, f.c.BaseURL+file.Asset, nil)
	if err != nil {
		return off, 0, err
	}
	if off > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(off, 10)+"-")
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return off, 0, stallErr(err)
	}
	defer resp.Body.Close()
	switch {
	case off > 0 && resp.StatusCode == http.StatusPartialContent:
		if start, ok := rangeStart(resp.Header.Get("Content-Range")); !ok || start != off {
			// Start the part again next time.
			if err := fh.Truncate(0); err != nil {
				return off, 0, err
			}
			f.got.Add(-off)
			return 0, 0, errRange
		}
	case resp.StatusCode == http.StatusOK:
		// The whole file: a server that doesn't resume, or nothing to resume.
		if off > 0 {
			if err := fh.Truncate(0); err != nil {
				return off, 0, err
			}
			h.Reset()
			f.got.Add(-off)
			off = 0
		}
	default:
		return off, 0, &statusError{code: resp.StatusCode}
	}
	if _, err := fh.Seek(off, io.SeekStart); err != nil {
		return off, 0, err
	}
	lim := limiter{rate: f.c.BytesPerSecond, now: f.now, sleep: f.sleep}
	buf := make([]byte, cChunk)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			watchdog.Reset(f.stall)
			if off+int64(n) > file.Size {
				return off, wrote, errTooLong
			}
			if _, err := fh.Write(buf[:n]); err != nil {
				return off, wrote, err
			}
			h.Write(buf[:n])
			off += int64(n)
			wrote += int64(n)
			f.got.Add(int64(n))
			if err := lim.wait(rctx, n); err != nil {
				return off, wrote, stallErr(err)
			}
		}
		if rerr == io.EOF {
			return off, wrote, nil
		}
		if rerr != nil {
			return off, wrote, stallErr(rerr)
		}
	}
}

// rangeStart is where a Content-Range ("bytes 100-199/200") starts.
func rangeStart(v string) (int64, bool) {
	v, ok := strings.CutPrefix(v, "bytes ")
	if !ok {
		return 0, false
	}
	start, _, ok := strings.Cut(v, "-")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(start, 10, 64)
	return n, err == nil
}

// limiter keeps a transfer at rate bytes a second on average: after each
// read it waits until the time that many bytes take at that rate, so the
// connection's window fills and the sender slows down. No credit builds up
// while the link is slower than the cap.
type limiter struct {
	rate  int64
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
	next  time.Time
}

func (l *limiter) wait(ctx context.Context, n int) error {
	if l.rate <= 0 {
		return nil
	}
	now := l.now()
	if l.next.Before(now) {
		l.next = now
	}
	l.next = l.next.Add(time.Duration(int64(n) * int64(time.Second) / l.rate))
	if d := l.next.Sub(now); d > 0 {
		return l.sleep(ctx, d)
	}
	return nil
}

// classify is why a try failed.
func classify(err error) *Problem {
	var se *spaceError
	if errors.As(err, &se) {
		return &Problem{Kind: NoSpace, Need: se.need, Left: se.left}
	}
	var st *statusError
	if errors.As(err, &st) {
		if st.code >= 500 || st.code == http.StatusTooManyRequests || st.code == http.StatusRequestTimeout {
			return &Problem{Kind: Unreachable}
		}
		return &Problem{Kind: Failed}
	}
	var pe *fs.PathError
	if errors.Is(err, errMismatch) || errors.Is(err, errTooLong) || errors.As(err, &pe) {
		return &Problem{Kind: Failed}
	}
	// The connection's: refused, reset, timed out, stalled, cut short.
	return &Problem{Kind: Unreachable}
}

// short is err for the log, without the request's address.
func short(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return err.Error()
}
