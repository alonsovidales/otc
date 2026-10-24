// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const cBrowserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15"

type visit struct{ page, source string }

// visitsAPI is siteAPI counting its visits into the returned slice, with
// the static directory's other files too: a page that isn't counted
// (admin.html), a verification file, the service worker, a font.
func visitsAPI(t *testing.T) (*API, *[]visit) {
	t.Helper()
	api := siteAPI(t)
	for name, body := range map[string]string{
		"terms.html":                  "the terms",
		"admin.html":                  "the admin panel",
		"googlebc3c0c0b52da3eb6.html": "google-site-verification",
		"sw.js":                       "self.addEventListener('fetch', () => {})",
		"fonts/inter-0123abcd.woff2":  "a font",
	} {
		if err := os.MkdirAll(filepath.Dir(api.staticPath+name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(api.staticPath+name, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var got []visit
	api.recordVisit = func(page, source string) { got = append(got, visit{page, source}) }
	return api, &got
}

// What is a visit: a person's GET of one of the site's pages, answered
// 200 - nothing else the site's host answers.
func TestVisitCounting(t *testing.T) {
	browser := map[string]string{"User-Agent": cBrowserUA}
	with := func(h map[string]string) map[string]string {
		out := map[string]string{"User-Agent": cBrowserUA}
		for k, v := range h {
			out[k] = v
		}
		return out
	}
	cases := []struct {
		name, method, target string
		header               map[string]string
		want                 *visit // nil: not counted
	}{
		{"landing", "GET", "/", browser, &visit{"/", ""}},
		{"landing in Spanish", "GET", "/es/", browser, &visit{"/es/", ""}},
		{"English through the switcher", "GET", "/en/", browser, &visit{"/en/", ""}},
		{"comparison page", "GET", "/private-social-network", browser, &visit{"/private-social-network", ""}},
		{"comparison page in French", "GET", "/fr/google-drive-alternative", browser, &visit{"/fr/google-drive-alternative", ""}},
		{"account page", "GET", "/account", browser, &visit{"/account", ""}},
		{"account page in German, one page", "GET", "/de/account?complete=1", browser, &visit{"/account", ""}},
		{"privacy", "GET", "/privacy", browser, &visit{"/privacy", ""}},
		{"privacy.html", "GET", "/privacy.html", browser, &visit{"/privacy", ""}},
		{"terms", "GET", "/terms", browser, &visit{"/terms", ""}},

		{"HEAD", "HEAD", "/", browser, nil},
		{"POST", "POST", "/", browser, nil},
		{"redirect to the visitor's language", "GET", "/", with(map[string]string{"Accept-Language": "es"}), nil},
		{"redirect to /es/", "GET", "/es", browser, nil},
		{"old address", "GET", "/landing", browser, nil},
		{"not modified", "GET", "/privacy", with(map[string]string{"If-Modified-Since": "Mon, 01 Jan 2101 00:00:00 GMT"}), nil},
		{"404", "GET", "/nope", browser, nil},
		{"404 under a language", "GET", "/es/nope", browser, nil},
		{"a language not carried", "GET", "/xx/", browser, nil},
		{"admin panel", "GET", "/admin", browser, nil},
		{"admin.html", "GET", "/admin.html", browser, nil},
		{"verification file", "GET", "/googlebc3c0c0b52da3eb6.html", browser, nil},
		{"service worker", "GET", "/sw.js", browser, nil},
		{"font", "GET", "/fonts/inter-0123abcd.woff2", browser, nil},
		{"picture", "GET", "/img/device.jpg", browser, nil},
		{"robots.txt", "GET", "/robots.txt", browser, nil},
		{"sitemap", "GET", "/sitemap.xml", browser, nil},
		{"disabled page preview", "GET", "/disabled", browser, nil},

		{"prefetch (Sec-Purpose)", "GET", "/", with(map[string]string{"Sec-Purpose": "prefetch"}), nil},
		{"prerender (Sec-Purpose)", "GET", "/", with(map[string]string{"Sec-Purpose": "prefetch;prerender"}), nil},
		{"prefetch (Purpose)", "GET", "/", with(map[string]string{"Purpose": "Prefetch"}), nil},
		{"no User-Agent", "GET", "/", map[string]string{}, nil},
		{"search engine", "GET", "/", map[string]string{"User-Agent": "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"}, nil},
		{"crawler", "GET", "/", map[string]string{"User-Agent": "CCBot-Crawler/2.0"}, nil},
		{"Yahoo", "GET", "/", map[string]string{"User-Agent": "Mozilla/5.0 (compatible; Yahoo! Slurp)"}, nil},
		{"spider", "GET", "/", map[string]string{"User-Agent": "Baiduspider"}, nil},
		{"curl", "GET", "/", map[string]string{"User-Agent": "curl/8.7.1"}, nil},
		{"wget", "GET", "/", map[string]string{"User-Agent": "Wget/1.21"}, nil},
		{"python", "GET", "/", map[string]string{"User-Agent": "python-requests/2.32"}, nil},
		{"Go", "GET", "/", map[string]string{"User-Agent": "Go-http-client/2.0"}, nil},
		{"headless browser", "GET", "/", map[string]string{"User-Agent": "Mozilla/5.0 HeadlessChrome/131.0"}, nil},
		{"Lighthouse", "GET", "/", map[string]string{"User-Agent": "Mozilla/5.0 Chrome-Lighthouse"}, nil},
		{"uptime monitor", "GET", "/", map[string]string{"User-Agent": "Mozilla/5.0 (compatible; UptimeRobot/2.0; http://www.uptimerobot.com/) Monitor"}, nil},
		{"link preview", "GET", "/", map[string]string{"User-Agent": "Mozilla/5.0 (compatible; Discordbot/2.0) Preview"}, nil},
		{"fetcher", "GET", "/", map[string]string{"User-Agent": "Feedfetcher-Google"}, nil},
		{"Facebook's preview", "GET", "/", map[string]string{"User-Agent": "facebookexternalhit/1.1 (+http://www.facebook.com/externalhit_uatext.php)"}, nil},
		{"WhatsApp's preview", "GET", "/", map[string]string{"User-Agent": "WhatsApp/2.23.20.0"}, nil},
		{"a Mastodon server", "GET", "/", map[string]string{"User-Agent": "http.rb/5.1.1 (Mastodon/4.3.0; +https://mastodon.social/)"}, nil},
		{"Bluesky's preview", "GET", "/", map[string]string{"User-Agent": "Mozilla/5.0 (compatible; Bluesky Cardyb/1.1; +mailto:support@bsky.app)"}, nil},

		{"from another site", "GET", "/", with(map[string]string{"Referer": "https://www.reddit.com/r/selfhosted/comments/abc/?utm=x"}), &visit{"/", "reddit.com"}},
		{"from our own page", "GET", "/es/", with(map[string]string{"Referer": "https://bridge.test/"}), &visit{"/es/", ""}},
		{"from one of our links", "GET", "/?ref=Newsletter", with(map[string]string{"Referer": "https://mail.example.com/"}), &visit{"/", "ref:newsletter"}},
		{"back from Google's sign-in", "GET", "/account", with(map[string]string{"Referer": "https://accounts.google.com/"}), &visit{"/account", ""}},
		{"back from Apple's sign-in, to complete the profile", "GET", "/account?complete=1", with(map[string]string{"Referer": "https://appleid.apple.com/"}), &visit{"/account", ""}},
		{"an email's link, from webmail", "GET", "/es/account", with(map[string]string{"Referer": "https://mail.smith-family.example/"}), &visit{"/account", ""}},
		{"one of our links to the account page", "GET", "/account?ref=launch", with(map[string]string{"Referer": "https://news.ycombinator.com/"}), &visit{"/account", "ref:launch"}},
	}
	for _, c := range cases {
		api, got := visitsAPI(t)
		rec := siteGet(api, c.method, c.target, c.header)
		switch {
		case c.want == nil && len(*got) != 0:
			t.Errorf("%s: counted %+v (status %d)", c.name, *got, rec.Code)
		case c.want != nil && (len(*got) != 1 || (*got)[0] != *c.want):
			t.Errorf("%s: counted %+v (status %d), want %+v", c.name, *got, rec.Code, *c.want)
		}
	}
}

// A device's address is never counted, nor anything when counting is
// off: the answer goes through as it is.
func TestVisitCountingOnlyOnTheSiteHost(t *testing.T) {
	api, got := visitsAPI(t)
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "cala.bridge.test"
	req.Header.Set("User-Agent", cBrowserUA)
	rec := httptest.NewRecorder()
	w, counted := api.countVisit(rec, req)
	if w != http.ResponseWriter(rec) {
		t.Error("a device's request was wrapped")
	}
	w.WriteHeader(http.StatusOK)
	counted()
	if len(*got) != 0 {
		t.Errorf("counted %+v", *got)
	}

	api.recordVisit = nil
	req.Host = "bridge.test"
	if w, _ := api.countVisit(rec, req); w != http.ResponseWriter(rec) {
		t.Error("wrapped with counting off")
	}
}

// The name a page is counted under: the page's address with its
// language, short and known; nothing for what isn't a page counted.
func TestVisitPage(t *testing.T) {
	for path, want := range map[string]string{
		"/":                              "/",
		"/en/":                           "/en/",
		"/pt/":                           "/pt/",
		"/account":                       "/account",
		"/nl/account":                    "/account",
		"/en/account":                    "/account",
		"/google-photos-alternative":     "/google-photos-alternative",
		"/it/private-social-network":     "/it/private-social-network",
		"/privacy":                       "/privacy",
		"/privacy.html":                  "/privacy",
		"/terms.html":                    "/terms",
		"/es":                            "",
		"/es/account/":                   "",
		"/xx/":                           "",
		"/zz/account":                    "",
		"/landing":                       "",
		"/disabled":                      "",
		"/admin":                         "",
		"/api/contact":                   "",
		"/media/abc":                     "",
		"/img/hero-900.webp":             "",
		"/fonts/fonts.css":               "",
		"/sw.js":                         "",
		"/BingSiteAuth.xml":              "",
		"/googlebc3c0c0b52da3eb6.html":   "",
		"/es/google-photos-alternative/": "",
	} {
		got, ok := visitPage(path)
		if ok != (want != "") || got != want {
			t.Errorf("%s: %q, %v; want %q", path, got, ok, want)
		}
	}
	for _, p := range []string{"/", "/es/", "/account", "/privacy"} {
		if got, _ := visitPage(p); len(got) > cVisitPageMax {
			t.Errorf("%s: %q is longer than the column", p, got)
		}
	}
}

// Where a visit came from: one of our links' ref= or utm_source= when it
// is a plain name, else the referring website's name and nothing else of
// its address, else nothing.
func TestVisitSource(t *testing.T) {
	long := strings.Repeat("a", 150) + ".example"
	cases := []struct {
		name, target, referer, want string
	}{
		{"none", "/", "", ""},
		{"a website", "/", "https://news.ycombinator.com/item?id=1", "news.ycombinator.com"},
		{"www. dropped, lower case", "/", "https://WWW.Example.COM/Some/Path?q=secret#frag", "example.com"},
		{"a port dropped", "/", "http://blog.example.org:8080/post", "blog.example.org"},
		{"credentials never kept", "/", "https://user:pass@forum.example.net/", "forum.example.net"},
		{"an app", "/", "android-app://com.google.android.gm/", "com.google.android.gm"},
		{"our own site", "/", "https://bridge.test/es/", ""},
		{"our own site, www.", "/", "https://www.bridge.test/", ""},
		{"a device's address", "/", "https://cala.bridge.test/", ""},
		{"an IP address", "/", "http://192.168.1.20:8080/", ""},
		{"an IPv6 address", "/", "http://[2001:db8::1]/", ""},
		{"not a URL", "/", "reddit.com", ""},
		{"not a host name", "/", "https://exa%20mple.com/", ""},
		{"a long name, cut", "/", "https://" + long + "/", long[:cVisitHostMax]},
		{"ref", "/?ref=reddit", "https://www.reddit.com/", "ref:reddit"},
		{"ref, lowercased", "/?ref=HN-launch_2.0", "", "ref:hn-launch_2.0"},
		{"utm_source", "/?utm_source=newsletter&utm_medium=email", "", "ref:newsletter"},
		{"ref before utm_source", "/?utm_source=b&ref=a", "", "ref:a"},
		{"a ref that isn't a name: the referrer", "/?ref=hello%20world", "https://lobste.rs/", "lobste.rs"},
		{"a ref too long", "/?ref=" + strings.Repeat("x", 41), "", ""},
		{"an empty ref, then utm_source", "/?ref=&utm_source=mastodon", "", "ref:mastodon"},
		{"a ref with a slash", "/?ref=a/b", "", ""},
		{"never the dao's bucket for too many sources", "/?ref=(other)", "", ""},
	}
	for _, c := range cases {
		req := httptest.NewRequest("GET", c.target, nil)
		if c.referer != "" {
			req.Header.Set("Referer", c.referer)
		}
		if got := visitSource(req, "bridge.test"); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	// A test bridge's tld carries its port.
	if got := referrerSite("https://bridge.test:8081/", "bridge.test:8081"); got != "" {
		t.Errorf("own site with a port: %q", got)
	}
}
