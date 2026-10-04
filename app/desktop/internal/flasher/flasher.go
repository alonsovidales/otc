// SPDX-License-Identifier: AGPL-3.0-or-later

// Package flasher is the desktop setup wizard's SD card writer (issue
// #184): it downloads the device image, checks it against the SHA-256 the
// project signs with its release key, lists the removable disks only, and
// writes the image to one of them - decompressing as it writes, reading it
// back to verify, then ejecting it. Writing a raw disk needs administrator
// rights, so the wizard runs this same program elevated (pkexec on Linux,
// UAC on Windows) as `otc-sync flash-device`, which reports its progress
// through a status file and re-checks everything itself: the disk must
// still be a removable one, and the image must match the signed hash.
package flasher

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alonsovidales/otc/app/desktop/internal/selfupdate"
	"github.com/ulikunitz/xz"
)

const (
	ImageName = "off-the-cloud-rpi-lite-arm64.img.xz"
	imageBase = "https://github.com/alonsovidales/otc/releases/download/image/"

	// MinCardSize: the image is about 3 GB, but the system grows on the
	// card as it installs - an "8 GB" card (sold sizes are a little less).
	MinCardSize = 7_500_000_000

	cChunk     = 4 << 20 // a multiple of every sector size
	cFirstPart = 1 << 20 // written last, so nothing mounts a half-written card
)

// ErrCancelled is returned when the person declines the administrator
// prompt.
var ErrCancelled = errors.New("cancelled")

// Disk is a removable disk the image can be written to.
type Disk struct {
	ID   string // /dev/sdb, \\.\PhysicalDrive2
	Name string // vendor and model
	Size int64
}

func (d Disk) Label() string {
	name := d.Name
	if name == "" {
		name = "Unnamed disk"
	}
	return fmt.Sprintf("%s · %s (%s)", name, HumanSize(d.Size), d.ShortID())
}

// ShortID names the disk briefly for the dialogs: sdb, disk 2.
func (d Disk) ShortID() string {
	if n, ok := strings.CutPrefix(d.ID, `\\.\PhysicalDrive`); ok {
		return "disk " + n
	}
	return filepath.Base(d.ID)
}

func HumanSize(n int64) string {
	if n >= 1e12 {
		return fmt.Sprintf("%.1f TB", float64(n)/1e12)
	}
	return fmt.Sprintf("%.1f GB", float64(n)/1e9)
}

// ImageURL is where the image is published (overridable for tests with
// OTC_IMAGE_BASE).
func ImageURL() string {
	if b := os.Getenv("OTC_IMAGE_BASE"); b != "" {
		return strings.TrimSuffix(b, "/") + "/" + ImageName
	}
	return imageBase + ImageName
}

// SignedHash is the image's SHA-256, from the .sha256 file published next
// to it, trusted only with a valid release-key signature.
func SignedHash() (string, error) {
	body, err := selfupdate.FetchSigned(ImageURL()+".sha256", 4096)
	if err != nil {
		return "", err
	}
	f := strings.Fields(string(body))
	if len(f) == 0 || len(f[0]) != 64 {
		return "", errors.New("the image's checksum file is malformed")
	}
	return strings.ToLower(f[0]), nil
}

// CachePath is where the download is kept until the card is written.
func CachePath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "OffTheCloud")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, ImageName), nil
}

func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Download fetches the image to CachePath and checks it against the signed
// hash; an earlier download that still matches is used as it is. progress
// gets the bytes so far and the total (-1 when unknown).
func Download(ctx context.Context, progress func(done, total int64)) (string, error) {
	want, err := SignedHash()
	if err != nil {
		return "", err
	}
	path, err := CachePath()
	if err != nil {
		return "", err
	}
	if got, err := fileHash(path); err == nil && got == want {
		return path, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ImageURL(), nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading the image: %s", resp.Status)
	}
	part := path + ".part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer os.Remove(part)
	h := sha256.New()
	cr := &countReader{r: resp.Body, every: 250 * time.Millisecond, cb: func(n int64) { progress(n, resp.ContentLength) }}
	_, err = io.Copy(io.MultiWriter(f, h), io.LimitReader(cr, 4<<30))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return "", errors.New("the downloaded image does not match its signed checksum - try again")
	}
	if err := os.Rename(part, path); err != nil {
		return "", err
	}
	return path, nil
}

type countReader struct {
	r     io.Reader
	n     int64
	every time.Duration
	last  time.Time
	cb    func(int64)
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if c.cb != nil && (time.Since(c.last) >= c.every || err != nil) {
		c.last = time.Now()
		c.cb(c.n)
	}
	return n, err
}

// Progress is one line of the writer's status file.
type Progress struct {
	Phase string `json:"phase"` // check, prepare, write, verify, eject, done, error
	Done  int64  `json:"done,omitempty"`
	Total int64  `json:"total,omitempty"`
	Error string `json:"error,omitempty"`
}

// Text is the step and its own percentage (for a terminal).
func (p Progress) Text() string {
	if p.Total > 0 && (p.Phase == "write" || p.Phase == "verify") {
		return fmt.Sprintf("%s %d%%", p.Step(), p.Done*100/p.Total)
	}
	return p.Step()
}

// Step names the step (for a dialog whose bar shows Percent).
func (p Progress) Step() string {
	switch p.Phase {
	case "check":
		return "Checking the image…"
	case "prepare":
		return "Preparing the card…"
	case "write":
		return "Writing the card…"
	case "verify":
		return "Checking what was written…"
	case "eject":
		return "Ejecting the card…"
	case "done":
		return "Done."
	}
	return p.Phase
}

// Percent maps the phases onto one bar: writing is most of it.
func (p Progress) Percent() int {
	frac := 0.0
	if p.Total > 0 {
		frac = float64(p.Done) / float64(p.Total)
	}
	switch p.Phase {
	case "check":
		return int(3 * frac)
	case "prepare":
		return 3
	case "write":
		return 3 + int(72*frac)
	case "verify":
		return 75 + int(23*frac)
	case "eject":
		return 98
	case "done":
		return 100
	}
	return 0
}

// WriteElevated writes image to disk through an elevated copy of this
// program, calling onProgress as it goes.
func WriteElevated(image string, disk Disk, onProgress func(Progress)) error {
	exe, err := selfupdate.Executable()
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "otc-flash-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	status := filepath.Join(dir, "status")
	if err := os.WriteFile(status, nil, 0o600); err != nil {
		return err
	}
	wait, err := elevate(exe, []string{"flash-device", "--image", image, "--disk", disk.ID, "--status", status})
	if err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- wait() }()
	var off int64
	var last Progress
	read := func() {
		f, err := os.Open(status)
		if err != nil {
			return
		}
		defer f.Close()
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			return
		}
		r := bufio.NewReader(f)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return // a partial line is read again next time
			}
			off += int64(len(line))
			var p Progress
			if json.Unmarshal([]byte(line), &p) == nil {
				last = p
				onProgress(p)
			}
		}
	}
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			read()
		case werr := <-exited:
			read()
			switch {
			case last.Phase == "done":
				return nil
			case last.Phase == "error":
				return errors.New(last.Error)
			case errors.Is(werr, ErrCancelled):
				return ErrCancelled
			case werr != nil:
				return fmt.Errorf("the card writer stopped: %w", werr)
			}
			return errors.New("the card writer stopped before it finished")
		}
	}
}

// HelperMain is `otc-sync flash-device`, run as administrator.
func HelperMain(args []string) error {
	fs := flag.NewFlagSet("flash-device", flag.ContinueOnError)
	image := fs.String("image", "", "")
	diskID := fs.String("disk", "", "")
	status := fs.String("status", "", "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var rep reporter
	if *status != "" {
		f, err := openStatus(*status)
		if err != nil {
			return err
		}
		defer f.Close()
		rep.w = f
	} else {
		rep.w = os.Stdout
	}
	err := Write(*image, *diskID, rep.report)
	if err != nil {
		rep.final(Progress{Phase: "error", Error: err.Error()})
		return err
	}
	rep.final(Progress{Phase: "done"})
	return nil
}

type reporter struct {
	w     io.Writer
	last  time.Time
	phase string
}

func (r *reporter) report(p Progress) {
	if p.Phase == r.phase && time.Since(r.last) < 250*time.Millisecond {
		return
	}
	r.final(p)
}

func (r *reporter) final(p Progress) {
	r.phase, r.last = p.Phase, time.Now()
	b, _ := json.Marshal(p)
	_, _ = r.w.Write(append(b, '\n'))
}

// Write puts the image on the disk; it must run as administrator.
func Write(image, diskID string, report func(Progress)) error {
	disks, err := ListDisks()
	if err != nil {
		return err
	}
	var disk *Disk
	for i := range disks {
		if disks[i].ID == diskID {
			disk = &disks[i]
		}
	}
	if disk == nil {
		return fmt.Errorf("%s is not a removable disk (or is no longer connected)", diskID)
	}
	if disk.Size < MinCardSize {
		return fmt.Errorf("the card is too small (%s) - it needs 8 GB or more", HumanSize(disk.Size))
	}

	// The image must be the signed one: checked here, by the administrator
	// process itself, and again as it is written.
	report(Progress{Phase: "check"})
	want, err := SignedHash()
	if err != nil {
		return err
	}
	f, err := os.Open(image)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(h, &countReader{r: f, every: 250 * time.Millisecond, cb: func(n int64) { report(Progress{Phase: "check", Done: n, Total: st.Size()}) }}); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != want {
		return errors.New("the image does not match its signed checksum - download it again")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}

	report(Progress{Phase: "prepare"})
	dev, err := openDisk(*disk)
	if err != nil {
		return err
	}
	defer dev.Close()

	// Blank the start first, so a card whose writing is interrupted is
	// never taken for a bootable one.
	zero := alignedBuf(cFirstPart)
	if _, err := dev.WriteAt(zero, 0); err != nil {
		return fmt.Errorf("writing the card: %w", err)
	}

	compHash := sha256.New()
	in := &countReader{r: io.TeeReader(f, compHash), every: 250 * time.Millisecond, cb: func(n int64) { report(Progress{Phase: "write", Done: n, Total: st.Size()}) }}
	xr, err := xz.NewReader(bufio.NewReaderSize(in, 1<<20))
	if err != nil {
		return err
	}
	imgHash := sha256.New()
	buf := alignedBuf(cChunk)
	first := alignedBuf(cChunk)
	var firstLen int
	var written int64
	for {
		n, rerr := io.ReadFull(xr, buf)
		if n > 0 {
			imgHash.Write(buf[:n])
			if written+int64(n) > disk.Size {
				return errors.New("the image is larger than the card")
			}
			if written == 0 {
				copy(first, buf[:n])
				firstLen = n
			} else if _, err := dev.WriteAt(padded(buf, n), written); err != nil {
				return fmt.Errorf("writing the card: %w", err)
			}
			written += int64(n)
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			return fmt.Errorf("decompressing the image: %w", rerr)
		}
	}
	// Whatever was read must still be the signed image.
	if _, err := io.Copy(io.Discard, in); err != nil {
		return err
	}
	if hex.EncodeToString(compHash.Sum(nil)) != want {
		return errors.New("the image changed while it was written - download it again")
	}
	if _, err := dev.WriteAt(padded(first, firstLen), 0); err != nil {
		return fmt.Errorf("writing the card: %w", err)
	}
	if err := dev.Sync(); err != nil {
		return fmt.Errorf("writing the card: %w", err)
	}
	dropCache(dev)

	// Read it back.
	back := sha256.New()
	var read int64
	lastReport := time.Time{}
	for read < written {
		n := int(min(int64(cChunk), written-read))
		if _, err := dev.ReadAt(buf[:roundUp(n)], read); err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("reading the card back: %w", err)
		}
		back.Write(buf[:n])
		read += int64(n)
		if time.Since(lastReport) > 250*time.Millisecond {
			lastReport = time.Now()
			report(Progress{Phase: "verify", Done: read, Total: written})
		}
	}
	if !equalHash(back, imgHash) {
		return errors.New("the card does not read back what was written - it may be faulty; try another card")
	}
	report(Progress{Phase: "eject"})
	dev.Close()
	eject(*disk)
	return nil
}

func equalHash(a, b interface{ Sum([]byte) []byte }) bool {
	return hex.EncodeToString(a.Sum(nil)) == hex.EncodeToString(b.Sum(nil))
}

func roundUp(n int) int { return (n + 511) &^ 511 }

// padded is buf[:n] rounded up to whole sectors, with zeros: raw disks
// take whole sectors only.
func padded(buf []byte, n int) []byte {
	r := roundUp(n)
	for i := n; i < r; i++ {
		buf[i] = 0
	}
	return buf[:r]
}

// alignedBuf is a page-aligned buffer (raw, unbuffered disk I/O on Windows
// needs sector-aligned memory).
func alignedBuf(size int) []byte {
	b := make([]byte, size+4096)
	off := int(4096 - uintptrOf(b)%4096)
	if off == 4096 {
		off = 0
	}
	return b[off : off+size : off+size]
}

// device is an open raw disk.
type device interface {
	io.WriterAt
	io.ReaderAt
	Sync() error
	Close() error
}

// NextSteps is what to do once the card is written.
const NextSteps = `The card is ready. Put it in the Raspberry Pi 5, plug in the two USB disks and power it on.

Then, in the Off The Cloud app on your phone (iOS or Android), tap "Set up a new device": it finds the Pi over Bluetooth and walks you through the rest.`
