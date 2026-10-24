// SPDX-License-Identifier: AGPL-3.0-or-later

// Package downloads reads how many times the project's GitHub release
// files were downloaded, for the admin panel's Growth tab: every cEvery
// each bridge node asks GitHub's public API for the releases and stores
// the totals it keeps as today's snapshot (dao.SaveDownloadCounts). Both
// nodes write the same totals over each other, so which one polled last
// doesn't matter. GitHub's API says nothing about who downloaded; only the
// counts the releases page shows are read.
package downloads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/log"
)

const (
	// cRepo is the repository whose releases are counted.
	cRepo = "alonsovidales/otc"
	// cEvery: the counts move slowly, and GitHub allows an address 60
	// requests an hour without a token; a poll is one per 100 releases.
	cEvery = 6 * time.Hour
	// cFirstAfter: the first poll waits until the node has settled after
	// a start.
	cFirstAfter = 2 * time.Minute
	// cTimeout bounds each request to GitHub.
	cTimeout = 15 * time.Second
	// cMaxPages is 500 releases: past that the oldest aren't counted.
	cMaxPages = 5
	// cMaxPage is the most read of one page: 100 releases with their
	// notes are well under a megabyte.
	cMaxPage = 16 << 20
	// cMaxAsset is download_counts.asset's length; a longer name is
	// skipped rather than cut into another file's.
	cMaxAsset = 120
	cAPI      = "https://api.github.com/"
	cAgent    = "otc-bridge (https://github.com/alonsovidales/otc)"
)

// cDeviceTag is a device release's tag (scripts/release.sh tags v<N>).
var cDeviceTag = regexp.MustCompile(`^v[0-9]+$`)

// Store keeps one poll's totals as day's snapshot, giving up when ctx
// ends.
type Store interface {
	SaveDownloadCounts(ctx context.Context, day time.Time, counts map[string]int64) error
}

// Poller polls Repo's releases every Every, the first time FirstAfter
// after Start, reading at most MaxPage bytes of each page. New fills these
// with the constants above; tests put their own, and a Client whose
// transport answers for GitHub.
type Poller struct {
	Repo       string
	Every      time.Duration
	FirstAfter time.Duration
	MaxPage    int64
	Client     *http.Client
	Store      Store

	cancel context.CancelFunc
	done   chan struct{}
}

// New is the poller main starts, storing in store.
func New(store Store) *Poller {
	return &Poller{
		Repo:       cRepo,
		Every:      cEvery,
		FirstAfter: cFirstAfter,
		MaxPage:    cMaxPage,
		Client:     &http.Client{Timeout: cTimeout},
		Store:      store,
	}
}

// Start polls in the background until Stop.
func (p *Poller) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel, p.done = cancel, make(chan struct{})
	go func() {
		defer close(p.done)
		t := time.NewTimer(p.FirstAfter)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := p.Poll(ctx, time.Now()); err != nil && ctx.Err() == nil {
					log.Error("downloads: could not read the GitHub releases' download counts:", err)
				}
				t.Reset(p.Every)
			}
		}
	}()
}

// Stop ends the polling, a request or write under way included, and
// waits for it.
func (p *Poller) Stop() {
	if p.cancel == nil {
		return
	}
	p.cancel()
	<-p.done
}

// Poll reads the counts once and stores them as now's day (UTC). A poll
// that fails anywhere stores nothing.
func (p *Poller) Poll(ctx context.Context, now time.Time) error {
	counts, err := p.fetch(ctx)
	if err != nil {
		return err
	}
	if len(counts) == 0 {
		return errors.New("no release files to count")
	}
	return p.Store.SaveDownloadCounts(ctx, now.UTC(), counts)
}

// release is what a poll reads of a GitHub release.
type release struct {
	Tag    string `json:"tag_name"`
	Assets []struct {
		Name      string `json:"name"`
		Downloads int64  `json:"download_count"`
	} `json:"assets"`
}

// fetch reads every page of the releases (at most cMaxPages) and keeps
// what counts.
func (p *Poller) fetch(ctx context.Context) (map[string]int64, error) {
	var all []release
	next := cAPI + "repos/" + p.Repo + "/releases?per_page=100"
	for page := 0; next != "" && page < cMaxPages; page++ {
		releases, link, err := p.page(ctx, next)
		if err != nil {
			return nil, err
		}
		all = append(all, releases...)
		next = nextPage(link)
	}
	return counted(all), nil
}

func (p *Poller) page(ctx context.Context, url string) ([]release, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", cAgent)
	resp, err := p.Client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("GitHub answered %s", resp.Status)
	}
	var releases []release
	if err := json.NewDecoder(io.LimitReader(resp.Body, p.MaxPage)).Decode(&releases); err != nil {
		return nil, "", fmt.Errorf("reading GitHub's answer: %w", err)
	}
	return releases, resp.Header.Get("Link"), nil
}

// nextPage is the rel="next" address of a Link header, "" for none - or
// for one that isn't GitHub's API, which is never followed.
func nextPage(link string) string {
	for _, part := range strings.Split(link, ",") {
		target, params, ok := strings.Cut(strings.TrimSpace(part), ";")
		if !ok || !strings.HasPrefix(target, "<") || !strings.HasSuffix(target, ">") {
			continue
		}
		for _, param := range strings.Split(params, ";") {
			if strings.TrimSpace(param) == `rel="next"` {
				if url := target[1 : len(target)-1]; strings.HasPrefix(url, cAPI) {
					return url
				}
				return ""
			}
		}
	}
	return ""
}

// counted is what a poll keeps: every file of the "image" release (the
// Raspberry Pi image and its checksum and signature) and of the "desktop"
// one (otc-sync's binaries and the manifest they update from), by their
// names, and each device release's source archive, by the release
// (dao.ReleaseSourcePrefix).
func counted(releases []release) map[string]int64 {
	out := map[string]int64{}
	for _, r := range releases {
		for _, a := range r.Assets {
			name := ""
			switch {
			case r.Tag == "image" || r.Tag == "desktop":
				name = a.Name
			case cDeviceTag.MatchString(r.Tag) && a.Name == "src.tar.gz":
				name = dao.ReleaseSourcePrefix + r.Tag[1:]
			}
			if name == "" || len(name) > cMaxAsset {
				continue
			}
			out[name] += a.Downloads
		}
	}
	return out
}
