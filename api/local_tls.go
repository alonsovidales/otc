// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"

	"github.com/alonsovidales/otc/lantls"
	"github.com/alonsovidales/otc/log"
)

// startLocalTLS is issue #190's listener: the same mux as the HTTP port,
// over TLS with this instance's own certificate, for the apps at home (see
// lantls). It returns what GetLocalEndpoint reports, or nil (logged) when
// there is nothing to serve; the apps then stay on the bridge.
func (api *API) startLocalTLS(port int, storagePath string) *lantls.Endpoint {
	if port <= 0 {
		return nil
	}
	id, err := lantls.LoadOrCreate(storagePath)
	if err != nil {
		log.Error("no home-network TLS listener (the apps stay on the bridge):", err)
		return nil
	}
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		log.Error("could not listen on the home-network TLS port", port, "(the apps stay on the bridge):", err)
		return nil
	}
	log.Info("Starting the home-network TLS server on port:", port)
	return api.serveLocalTLS(ln, id)
}

// serveLocalTLS serves the mux on ln with id's certificate until the
// listener fails, and reports it stopped from then on.
func (api *API) serveLocalTLS(ln net.Listener, id *lantls.Identity) *lantls.Endpoint {
	srv := newServer("", api.muxHTTPServer)
	srv.TLSConfig = &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{id.Cert},
	}
	// HTTP/1.1 only: /ws is an HTTP/1.1 upgrade, which a client that
	// negotiated HTTP/2 here could not make.
	srv.Protocols = new(http.Protocols)
	srv.Protocols.SetHTTP1(true)

	ep := lantls.NewEndpoint(ln.Addr().(*net.TCPAddr).Port, id.Pin)
	go func() {
		err := srv.ServeTLS(ln, "", "")
		ep.Stop()
		log.Error("the home-network TLS server stopped (the apps stay on the bridge):", err)
	}()
	return ep
}
