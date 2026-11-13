// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/bridge/site"
)

// A click is counted under the link's own address.
func TestClickPagesAreTheLinks(t *testing.T) {
	if dao.ClickPagePrefix != site.DownloadPrefix {
		t.Errorf("clicks are counted under %q, the links are %q", dao.ClickPagePrefix, site.DownloadPrefix)
	}
}

// Each of the site's download links is a 302 to its file or store page,
// asked for again at each click, and a person's click is counted under
// its own page.
func TestDownloadRedirects(t *testing.T) {
	for target, want := range map[string]string{
		"pi-image":    "https://github.com/alonsovidales/otc/releases/download/image/off-the-cloud-rpi-lite-arm64.img.xz",
		"windows":     "https://github.com/alonsovidales/otc/releases/download/desktop/otc-sync-windows-amd64.exe",
		"windows-arm": "https://github.com/alonsovidales/otc/releases/download/desktop/otc-sync-windows-arm64.exe",
		"linux":       "https://github.com/alonsovidales/otc/releases/download/desktop/otc-sync-linux-amd64",
		"linux-arm":   "https://github.com/alonsovidales/otc/releases/download/desktop/otc-sync-linux-arm64",
		"mac":         "https://apps.apple.com/app/id6814658734",
		"ios":         "https://apps.apple.com/app/id6814658734",
		"android":     "https://play.google.com/store/apps/details?id=cloud.offthe.otc",
	} {
		api, got := visitsAPI(t)
		rec := siteGet(api, http.MethodGet, "/get/"+target, map[string]string{"User-Agent": cBrowserUA})
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
			t.Errorf("%s: %d to %q, want %q", target, rec.Code, rec.Header().Get("Location"), want)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: Cache-Control %q", target, rec.Header().Get("Cache-Control"))
		}
		if len(*got) != 1 || (*got)[0] != (visit{"/get/" + target, ""}) {
			t.Errorf("%s: counted %+v", target, *got)
		}
	}
}

// Anything else under /get/ is the site's 404 - its page for a browser,
// the bare one otherwise - with no redirect and nothing counted.
func TestDownloadNotFound(t *testing.T) {
	for _, target := range []string{"/get/", "/get", "/get/nope", "/get/PI-IMAGE", "/get/pi-image/", "/get/pi-image/x",
		"/get/../admin", "/get/..%2Fadmin", "/get/https:%2F%2Fevil.example", "/get//evil.example", "/get/pi-image.sha256", "/es/get/pi-image"} {
		for _, accept := range []string{"text/html", "*/*"} {
			api, got := visitsAPI(t)
			rec := siteGet(api, http.MethodGet, target, map[string]string{"User-Agent": cBrowserUA, "Accept": accept})
			if rec.Code != http.StatusNotFound || rec.Header().Get("Location") != "" || len(*got) != 0 {
				t.Errorf("%s (%s): %d, Location %q, counted %+v", target, accept, rec.Code, rec.Header().Get("Location"), *got)
			}
			if page := strings.Contains(rec.Body.String(), "<html"); page != (accept == "text/html") {
				t.Errorf("%s (%s): the site's page %v", target, accept, page)
			}
		}
	}
}

// Nothing in a request leads anywhere but a target's own destination: not
// a source, another parameter, the Referer or the path.
func TestDownloadIsNoOpenRedirect(t *testing.T) {
	hosts := map[string]bool{"github.com": true, "apps.apple.com": true, "play.google.com": true}
	hostile := []string{
		"?s=https://evil.example/", "?s=//evil.example", "?s=evil.example/x", "?s=ref:evil%0d%0aLocation:%20https://evil.example",
		"?next=https://evil.example/", "?url=https://evil.example/", "?redirect=//evil.example", "?ref=https://evil.example",
		"?utm_source=evil.example&s=ref:x", "?s=ref:launch&s=https://evil.example",
	}
	for _, target := range []string{"pi-image", "windows", "windows-arm", "linux", "linux-arm", "mac", "ios", "android"} {
		for _, q := range append(hostile, "") {
			for _, referer := range []string{"", "https://evil.example/", "javascript:alert(1)"} {
				api, _ := visitsAPI(t)
				rec := siteGet(api, http.MethodGet, "/get/"+target+q, map[string]string{"User-Agent": cBrowserUA, "Referer": referer})
				loc := rec.Header().Get("Location")
				u, err := url.Parse(loc)
				if rec.Code != http.StatusFound || err != nil || u.Scheme != "https" || !hosts[u.Host] {
					t.Errorf("%s%s from %q: %d to %q", target, q, referer, rec.Code, loc)
					continue
				}
				// The source may reach Play's referrer= only as its own
				// value: the listing and the app stay the same.
				if strings.Contains(strings.ToLower(u.Path+u.Query().Get("id")), "evil") {
					t.Errorf("%s%s from %q: to %q", target, q, referer, loc)
				}
				if want, _ := site.DownloadURL(target, ""); !strings.HasPrefix(loc, want) {
					t.Errorf("%s%s from %q: %q isn't %q", target, q, referer, loc, want)
				}
			}
		}
	}
}

// A person's click is counted; a robot's, a prefetch, a HEAD and a GET
// without a User-Agent are sent on uncounted; anything but GET and HEAD
// is refused; and with counting off the links still lead on.
func TestDownloadCounting(t *testing.T) {
	browser := map[string]string{"User-Agent": cBrowserUA}
	cases := []struct {
		name, method string
		header       map[string]string
		status       int
		counted      bool
	}{
		{"a person", "GET", browser, http.StatusFound, true},
		{"HEAD", "HEAD", browser, http.StatusFound, false},
		{"POST", "POST", browser, http.StatusMethodNotAllowed, false},
		{"prefetch", "GET", map[string]string{"User-Agent": cBrowserUA, "Sec-Purpose": "prefetch"}, http.StatusFound, false},
		{"prefetch (Purpose)", "GET", map[string]string{"User-Agent": cBrowserUA, "Purpose": "prefetch"}, http.StatusFound, false},
		{"search engine", "GET", map[string]string{"User-Agent": "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"}, http.StatusFound, false},
		{"link preview", "GET", map[string]string{"User-Agent": "facebookexternalhit/1.1"}, http.StatusFound, false},
		{"curl", "GET", map[string]string{"User-Agent": "curl/8.7.1"}, http.StatusFound, false},
		{"no User-Agent", "GET", map[string]string{}, http.StatusFound, false},
	}
	for _, c := range cases {
		api, got := visitsAPI(t)
		rec := siteGet(api, c.method, "/get/pi-image", c.header)
		if rec.Code != c.status || (len(*got) == 1) != c.counted || len(*got) > 1 {
			t.Errorf("%s: %d, counted %+v; want %d, counted %v", c.name, rec.Code, *got, c.status, c.counted)
		}
	}
	api := siteAPI(t)
	if rec := siteGet(api, http.MethodGet, "/get/android", browser); rec.Code != http.StatusFound {
		t.Errorf("counting off: %d", rec.Code)
	}
}

// The source a click is counted under, and what the store is told: the
// click's own (a ref= or utm_source= on the link, a website linking it),
// else the one our page's link carried when it is one - never what an
// address made up says.
func TestDownloadSource(t *testing.T) {
	own := "https://bridge.test/raspberry-pi-home-server"
	cases := []struct {
		name, target, referer, want string
	}{
		{"none", "/get/android", own, ""},
		{"carried from our page", "/get/android?s=ref:reddit-rpi", own + "?utm_source=reddit-rpi", "ref:reddit-rpi"},
		{"carried, escaped", "/get/android?s=ref%3Areddit-rpi", own, "ref:reddit-rpi"},
		{"a website's, carried", "/get/pi-image?s=news.ycombinator.com", "https://bridge.test/", "news.ycombinator.com"},
		{"the click's own website first", "/get/android?s=ref:reddit-rpi", "https://lobste.rs/s/abc", "lobste.rs"},
		{"the link's own ref first", "/get/android?utm_source=newsletter&s=ref:reddit-rpi", own, "ref:newsletter"},
		{"not a source", "/get/android?s=hello%20world", own, ""},
		{"upper case", "/get/android?s=ref:Reddit", own, ""},
		{"a name too long", "/get/android?s=ref:" + strings.Repeat("x", 41), own, ""},
		{"the site itself", "/get/android?s=bridge.test", own, ""},
		{"a device's address", "/get/android?s=cala.bridge.test", own, ""},
		{"an address", "/get/android?s=192.168.1.20", own, ""},
		{"www. is never carried", "/get/android?s=www.reddit.com", own, ""},
		{"a port", "/get/android?s=reddit.com:443", own, ""},
		{"a path", "/get/android?s=reddit.com/r/selfhosted", own, ""},
		{"the dao's bucket", "/get/android?s=(other)", own, ""},
		{"an empty name", "/get/android?s=ref:", own, ""},
	}
	for _, c := range cases {
		api, got := visitsAPI(t)
		rec := siteGet(api, http.MethodGet, c.target, map[string]string{"User-Agent": cBrowserUA, "Referer": c.referer})
		if len(*got) != 1 || (*got)[0].source != c.want {
			t.Errorf("%s: counted %+v, want source %q", c.name, *got, c.want)
		}
		target, _, _ := strings.Cut(strings.TrimPrefix(c.target, site.DownloadPrefix), "?")
		if want, _ := site.DownloadURL(target, c.want); rec.Header().Get("Location") != want {
			t.Errorf("%s: to %q, want %q", c.name, rec.Header().Get("Location"), want)
		}
	}
}

// What one of our links may carry: a source visits could have counted, as
// it was written; anything else is none.
func TestCarriedSource(t *testing.T) {
	long := strings.Repeat("a", 100)
	for v, want := range map[string]string{
		"":                        "",
		"ref:reddit-rpi":          "ref:reddit-rpi",
		"ref:hn-launch_2.0":       "ref:hn-launch_2.0",
		"ref:" + long[:40]:        "ref:" + long[:40],
		"ref:" + long[:41]:        "",
		"ref:":                    "",
		"ref:a/b":                 "",
		"ref:A":                   "",
		"REF:a":                   "",
		"reddit.com":              "reddit.com",
		"news.ycombinator.com":    "news.ycombinator.com",
		"com.google.android.gm":   "com.google.android.gm",
		long[:96] + ".com":        long[:96] + ".com",
		long + ".com":             "",
		"Reddit.com":              "",
		"www.reddit.com":          "",
		"reddit.com.":             "",
		"reddit.com:443":          "",
		"user@reddit.com":         "",
		"reddit.com/x":            "",
		"reddit.com?x":            "",
		"reddit.com#x":            "",
		"bridge.test":             "",
		"cala.bridge.test":        "",
		"192.168.1.20":            "",
		"[2001:db8::1]":           "",
		"(other)":                 "",
		"exa mple.com":            "",
		"https://evil.example":    "",
		"//evil.example":          "",
		"evil.example\r\nX-A: b":  "",
		"éxample.com":             "",
		"xn--xample-9ua.com":      "xn--xample-9ua.com",
		"javascript:alert(1)":     "",
		"ref:x\nLocation: /admin": "",
	} {
		if got := carriedSource(v, "bridge.test"); got != want {
			t.Errorf("%q: %q, want %q", v, got, want)
		}
	}
}

// Where a page's links come from: the visit's own source, else a carried
// one; on the account page never its own Referer; nothing for a robot or
// on a device's address.
func TestLinkSource(t *testing.T) {
	cases := []struct {
		name, host, target, referer, ua, page, want string
	}{
		{"none", "bridge.test", "/", "", cBrowserUA, site.Landing, ""},
		{"an ad's link", "bridge.test", "/raspberry-pi-home-server?utm_source=reddit-rpi", "", cBrowserUA, site.Pi, "ref:reddit-rpi"},
		{"a website", "bridge.test", "/", "https://www.reddit.com/r/selfhosted/", cBrowserUA, site.Landing, "reddit.com"},
		{"carried from our page", "bridge.test", "/?s=ref:reddit-rpi", "https://bridge.test/raspberry-pi-home-server?utm_source=reddit-rpi", cBrowserUA, site.Landing, "ref:reddit-rpi"},
		{"a shared link with a carried source, from elsewhere", "bridge.test", "/?s=ref:reddit-rpi", "https://news.ycombinator.com/", cBrowserUA, site.Landing, "news.ycombinator.com"},
		{"not a source", "bridge.test", "/?s=%3Cscript%3E", "", cBrowserUA, site.Landing, ""},
		{"the account page, back from a sign-in", "bridge.test", "/account", "https://accounts.google.com/", cBrowserUA, site.Account, ""},
		{"the account page, carried", "bridge.test", "/account?s=reddit.com", "https://bridge.test/", cBrowserUA, site.Account, "reddit.com"},
		{"the account page, our link", "bridge.test", "/account?ref=launch", "https://mail.example.com/", cBrowserUA, site.Account, "ref:launch"},
		{"a robot", "bridge.test", "/?utm_source=reddit-rpi", "", "Mozilla/5.0 (compatible; Googlebot/2.1)", site.Landing, ""},
		{"no User-Agent", "bridge.test", "/?s=ref:launch", "", "", site.Landing, ""},
		{"a device's address", "cala.bridge.test", "/?s=ref:launch", "", cBrowserUA, site.Disabled, ""},
		{"a download", "bridge.test", "/get/android?s=ref:launch", "https://bridge.test/", cBrowserUA, "", "ref:launch"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, c.target, nil)
		req.Host = c.host
		if c.referer != "" {
			req.Header.Set("Referer", c.referer)
		}
		if c.ua != "" {
			req.Header.Set("User-Agent", c.ua)
		}
		if got := linkSource(req, "bridge.test", c.page); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// Through the site: a visitor from an ad lands on the Raspberry Pi page,
// goes on to the landing page's install section and clicks the image -
// counted under the ad's link, with each page's visit counted as before
// (the second from our own site). A robot's page, and one asked with a
// source that isn't one, have plain links.
func TestTheSourceSurvivesTheHop(t *testing.T) {
	api, got := visitsAPI(t)
	browser := func(referer string) map[string]string {
		return map[string]string{"User-Agent": cBrowserUA, "Referer": referer}
	}
	pi := siteGet(api, http.MethodGet, "/raspberry-pi-home-server?utm_source=reddit-rpi", browser(""))
	if pi.Code != http.StatusOK || !strings.Contains(pi.Body.String(), `href="/?s=ref%3Areddit-rpi#install"`) {
		t.Fatalf("the Raspberry Pi page (%d) doesn't link #install with the source", pi.Code)
	}
	landing := siteGet(api, http.MethodGet, "/?s=ref%3Areddit-rpi", browser("https://bridge.test/raspberry-pi-home-server?utm_source=reddit-rpi"))
	body := landing.Body.String()
	if landing.Code != http.StatusOK || !strings.Contains(body, `href="/get/pi-image?s=ref%3Areddit-rpi"`) ||
		!strings.Contains(body, `href="/get/android?s=ref%3Areddit-rpi"`) {
		t.Fatalf("the landing page (%d) doesn't carry the source to its downloads", landing.Code)
	}
	click := siteGet(api, http.MethodGet, "/get/pi-image?s=ref%3Areddit-rpi", browser("https://bridge.test/?s=ref%3Areddit-rpi"))
	if click.Code != http.StatusFound {
		t.Fatalf("the click: %d", click.Code)
	}
	want := []visit{{"/raspberry-pi-home-server", "ref:reddit-rpi"}, {"/", ""}, {"/get/pi-image", "ref:reddit-rpi"}}
	if len(*got) != len(want) {
		t.Fatalf("counted %+v, want %+v", *got, want)
	}
	for i := range want {
		if (*got)[i] != want[i] {
			t.Errorf("counted %+v, want %+v", *got, want)
		}
	}

	for name, header := range map[string]map[string]string{
		"a robot":          {"User-Agent": "Mozilla/5.0 (compatible; Googlebot/2.1)"},
		"no User-Agent":    {},
		"a made-up source": {"User-Agent": cBrowserUA},
	} {
		target := "/?utm_source=reddit-rpi"
		if name == "a made-up source" {
			target = "/?s=https%3A%2F%2Fevil.example"
		}
		rec := siteGet(api, http.MethodGet, target, header)
		if rec.Code != http.StatusOK || carriesSource.MatchString(rec.Body.String()) {
			t.Errorf("%s: %d, a link with a source", name, rec.Code)
		}
	}
}

// A device's own page, answered by the bridge, never carries a source.
func TestDevicePagesCarryNoSource(t *testing.T) {
	api := siteAPI(t)
	req := httptest.NewRequest(http.MethodGet, "/?s=ref:launch&utm_source=launch", nil)
	req.Host = "pit.bridge.test"
	req.Header.Set("Accept", "text/html")
	req.Header.Set("User-Agent", cBrowserUA)
	rec := httptest.NewRecorder()
	api.serveOwnPage(rec, req, site.Unavailable, http.StatusServiceUnavailable)
	if rec.Code != http.StatusServiceUnavailable || carriesSource.MatchString(rec.Body.String()) {
		t.Errorf("a device's page (%d) carries a source", rec.Code)
	}
}

// carriesSource finds a link's carried source in a page: ?s= or &s=, as
// html/template writes them.
var carriesSource = regexp.MustCompile(`(\?|&amp;)` + site.SourceParam + `=`)
