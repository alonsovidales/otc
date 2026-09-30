// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"crypto/tls"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/alonsovidales/otc/log"
)

// Issue #161: plain HTTP served the whole site - sign-in pages with their
// passwords, device sites, the websocket. Port 80 now only sends people to
// HTTPS (the certificate is renewed through DNS, so nothing else needs
// it), and HTTPS responses tell browsers never to try HTTP again.

// cHSTS covers the device sites too (<name>.<tld>, served under the same
// certificate). Not preloaded yet: the preload list is hard to leave.
const cHSTS = "max-age=31536000; includeSubDomains"

// redirectToHTTPS answers everything on port 80 with a permanent redirect
// to the same address over HTTPS - except the health check, which carries
// nothing and is what a monitor may poll.
func redirectToHTTPS(health string, mux http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == health {
			mux.ServeHTTP(w, r)
			return
		}
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host == "" {
			http.Error(w, "use https", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), http.StatusMovedPermanently)
	})
}

// withHSTS adds Strict-Transport-Security to every HTTPS response.
func withHSTS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Strict-Transport-Security", cHSTS)
		next.ServeHTTP(w, r)
	})
}

// certReloader serves the certificate files as they are now: certbot
// renews them in place (its hook reloads nginx/apache, which this server
// isn't), and the bridge read them once at start - so a bridge up for 90
// days would have served an expired certificate. Checked at most once a
// minute.
type certReloader struct {
	certFile, keyFile string

	mu      sync.Mutex
	cert    *tls.Certificate
	modTime time.Time
	checked time.Time
}

func (c *certReloader) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cert != nil && time.Since(c.checked) < time.Minute {
		return c.cert, nil
	}
	c.checked = time.Now()
	fi, err := os.Stat(c.certFile)
	if err == nil && c.cert != nil && !fi.ModTime().After(c.modTime) {
		return c.cert, nil
	}
	cert, loadErr := tls.LoadX509KeyPair(c.certFile, c.keyFile)
	if loadErr != nil {
		if c.cert != nil {
			log.Error("could not reload the TLS certificate, keeping the current one:", loadErr)
			return c.cert, nil
		}
		return nil, loadErr
	}
	if c.cert != nil {
		log.Info("TLS certificate reloaded")
	}
	c.cert = &cert
	if err == nil {
		c.modTime = fi.ModTime()
	}
	return c.cert, nil
}
