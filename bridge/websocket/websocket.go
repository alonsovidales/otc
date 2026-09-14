// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"errors"
	"fmt"
	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/google/uuid"
	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
	"net/http"
	"runtime/debug"
	"sync"
)

const (
	CEndpoint = "/ws"

	// Issue #53 follow-up: a device's own pool grows dynamically under
	// load now instead of dialing a fixed count once (see
	// websocket.ensureBridgePool on the device side) - this default caps
	// that growth if [bridge] is left unconfigured, so one device can't
	// accumulate an unbounded number of idle connections here.
	cDefaultMaxConnectionsPerDevice = 100
)

// maxConnectionsPerDevice reads [bridge] max-connections-per-device,
// falling back to cDefaultMaxConnectionsPerDevice if that section/key is
// absent - deliberately optional config, not a required one, so existing
// deployments don't need an ini change just to pick up this cap.
func maxConnectionsPerDevice() int {
	if !cfg.HasSection("bridge") {
		return cDefaultMaxConnectionsPerDevice
	}
	if v := cfg.GetInt("bridge", "max-connections-per-device"); v > 0 {
		return int(v)
	}
	return cDefaultMaxConnectionsPerDevice
}

type bridgePool struct {
	availableConns []*gorilla.Conn
	lock           *sync.Mutex
}

// deviceRelay wraps one paired device connection with request/response
// multiplexing by envelope id, so several client requests can be in flight
// to the same device connection at once instead of strictly one at a time.
// The device itself now processes a connection's requests concurrently
// (see websocket.handleConnection's doc comment on the device side, added
// for the same reason: a slow request — a large GetFile needing a HEIC
// decode, say — used to sit in front of a cheap, unrelated one like
// GetFileInfo, on the very same connection). Relaying them here strictly
// one at a time would reintroduce that identical head-of-line blocking one
// hop earlier, this time in a place the device-side fix can't reach at
// all — every request/response for a given client<->device pairing was
// forced through a single write-then-block-for-the-matching-read step
// before the bridge would even read the client's next frame.
type deviceRelay struct {
	conn    *gorilla.Conn
	writeMu sync.Mutex // gorilla tolerates only one concurrent writer

	mu      sync.Mutex
	waiters map[int32]chan []byte
}

func newDeviceRelay(conn *gorilla.Conn) *deviceRelay {
	d := &deviceRelay{conn: conn, waiters: make(map[int32]chan []byte)}
	go d.readLoop()
	return d
}

// readLoop is this relay's one and only reader — gorilla tolerates only
// one concurrent reader, same as one writer — so every response coming
// back from the device passes through here and gets routed to whichever
// forward() call is waiting on that response's envelope id.
func (d *deviceRelay) readLoop() {
	for {
		_, frame, err := d.conn.ReadMessage()
		if err != nil {
			d.failAll()
			return
		}
		var env pb.RespEnvelope
		if err := proto.Unmarshal(frame, &env); err != nil {
			log.Error("bad proto from device:", err)
			continue
		}
		d.mu.Lock()
		ch, ok := d.waiters[env.Id]
		if ok {
			delete(d.waiters, env.Id)
		}
		d.mu.Unlock()
		if ok {
			ch <- frame
		}
		// No waiter for this id (already gave up, or a stray/duplicate
		// message) — nothing to deliver it to, so just drop it.
	}
}

// failAll unblocks every still-pending forward() call once the device
// connection itself has died, instead of leaving each one hanging forever
// waiting on a response that will now never arrive.
func (d *deviceRelay) failAll() {
	d.mu.Lock()
	waiters := d.waiters
	d.waiters = make(map[int32]chan []byte)
	d.mu.Unlock()
	for _, ch := range waiters {
		close(ch)
	}
}

// forward sends one request frame to the device and returns its matching
// response frame, correlated by envelope id — safe to call concurrently
// for several requests in flight on the same relay at once.
func (d *deviceRelay) forward(frame []byte) ([]byte, error) {
	var env pb.ReqEnvelope
	if err := proto.Unmarshal(frame, &env); err != nil {
		return nil, fmt.Errorf("bad proto: %w", err)
	}

	ch := make(chan []byte, 1)
	d.mu.Lock()
	d.waiters[env.Id] = ch
	d.mu.Unlock()

	d.writeMu.Lock()
	err := d.conn.WriteMessage(gorilla.BinaryMessage, frame)
	d.writeMu.Unlock()
	if err != nil {
		d.mu.Lock()
		delete(d.waiters, env.Id)
		d.mu.Unlock()
		return nil, err
	}

	resp, ok := <-ch
	if !ok {
		return nil, errors.New("device connection closed")
	}
	return resp, nil
}

func (d *deviceRelay) Close() error {
	return d.conn.Close()
}

// Manager Structure that provides HTTP access to manage all the different
// groups and shards on each grorup
type Manager struct {
	baseUrl  string
	dao      *dao.Dao
	upgrader gorilla.Upgrader
	bridges  map[string]*bridgePool // The domain is the key and the value the pool of connections
}

func Init(baseUrl string, dao *dao.Dao) (mg *Manager) {
	mg = &Manager{
		baseUrl: baseUrl,
		dao:     dao,
		upgrader: gorilla.Upgrader{
			// In production, set a proper origin check!
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		bridges: make(map[string]*bridgePool),
	}

	return
}

func (mg *Manager) closeWithError(conn *gorilla.Conn, id int32, err error) {
	log.Error("closing socket with error:", err)
	// Acknoledge the authentication
	respAuth := &pb.RespEnvelope{
		Id: id,
		Payload: &pb.RespEnvelope_RespAck{
			RespAck: &pb.Ack{
				Ok:       false,
				ErrorMsg: fmt.Sprintf("Error: %s", err),
			},
		},
	}
	resp, _ := proto.Marshal(respAuth)
	if err := conn.WriteMessage(gorilla.BinaryMessage, resp); err != nil {
		log.Error("error responding, closing the connection:", err)
	}
	conn.Close()

}

func (mg *Manager) Listen(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")

	conn, err := mg.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error("error trying to upgrade the websocket:", err)
		return
	}

	//defer conn.Close()

	mg.handleConnection(conn, r)
}

func (mg *Manager) handleConnection(conn *gorilla.Conn, r *http.Request) {
	// This serves every device and client connection the bridge relays, so
	// a bug triggered by any single one of them (a malformed message, an
	// edge case in a handler below) must not be able to take the whole
	// bridge — and every device relying on it — down with an unrecovered
	// panic. Closing just this connection is the correct blast radius.
	defer func() {
		if r := recover(); r != nil {
			log.Error("recovered from panic handling connection:", r, string(debug.Stack()))
			conn.Close()
		}
	}()

	// Once a client is paired with a device (relay != nil), each of its
	// requests is relayed in its own goroutine via relay.forward, which
	// multiplexes them over the one device connection by envelope id
	// instead of forcing them through one at a time — see deviceRelay's
	// doc comment for why that matters. writeMu serializes the responses
	// those goroutines write back to conn (the client side; gorilla
	// tolerates only one concurrent writer there too), and wg — waited on
	// before this function returns — makes sure none of them are left
	// trying to write to conn after it's already closed.
	var relay *deviceRelay
	var writeMu sync.Mutex
	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		_, frame, err := conn.ReadMessage()
		if err != nil {
			log.Error("error processing message:", err)
			return
		}

		if relay != nil {
			wg.Add(1)
			go func(frame []byte) {
				defer wg.Done()
				// Mirrors handleConnection's own top-level recover: this
				// now runs on its own goroutine, which the outer recover
				// above can't reach — an unrecovered panic here would
				// otherwise still take the whole process down.
				defer func() {
					if r := recover(); r != nil {
						log.Error("recovered from panic relaying message:", r, string(debug.Stack()))
					}
				}()

				respFrame, err := relay.forward(frame)
				if err != nil {
					log.Error("Error fordwading message:", err)
					return
				}

				writeMu.Lock()
				writeErr := conn.WriteMessage(gorilla.BinaryMessage, respFrame)
				writeMu.Unlock()
				if writeErr != nil {
					log.Error("error forwading respose, closing the connection:", writeErr)
					return
				}

				if err := mg.dao.RecordDeviceActivity(r.Host, int64(len(frame)), int64(len(respFrame))); err != nil {
					// Metrics are best-effort: never fail the actual relay
					// over a metrics-write error.
					log.Error("error recording device activity:", err)
				}
			}(frame)
		} else {
			var env pb.ReqEnvelope
			if err := proto.Unmarshal(frame, &env); err != nil {
				log.Error("bad proto:", err)
				return
			}

			resp := &pb.RespEnvelope{
				Id: env.Id,
			}
			switch p := env.Payload.(type) {
			case *pb.ReqEnvelope_ReqBridgeRegister:
				log.Info("Register device")
				// Check if we have the device already registered and if the pass is ok
				defined, validSecret, err := mg.dao.IsValidDevice(p.ReqBridgeRegister.OwnerUuid, p.ReqBridgeRegister.Domain, p.ReqBridgeRegister.Secret)
				if !defined {
					err = mg.dao.RegistreDevice(p.ReqBridgeRegister.OwnerUuid, p.ReqBridgeRegister.Domain, p.ReqBridgeRegister.Secret)
				}
				if defined && !validSecret {
					log.Error("error registering bridge:", err)
					resp.Error = true
					resp.ErrorMessage = "Invalid Secret"
					// Someone tried to register an already-claimed domain with
					// the wrong owner_uuid/secret - could be a misconfigured
					// device, or someone probing for a weak/leaked secret.
					// Surfaced in the admin panel's security log (issue #8).
					if logErr := mg.dao.LogAuthEvent(uuid.New().String(), p.ReqBridgeRegister.Domain, p.ReqBridgeRegister.OwnerUuid, conn.RemoteAddr().String(), "invalid_secret"); logErr != nil {
						log.Error("error logging auth event:", logErr)
					}
				} else if err != nil {
					log.Error("error trying to register:", err)
					resp.Error = true
					resp.ErrorMessage = err.Error()
				} else if pool, ok := mg.bridges[p.ReqBridgeRegister.Domain]; ok && len(pool.availableConns) >= maxConnectionsPerDevice() {
					// Issue #53 follow-up: a device now grows its own pool
					// dynamically under load (see websocket.ensureBridgePool
					// on the device side) rather than dialing a fixed count
					// once - this is the backstop against that (or anything
					// else) growing one device's pool unbounded.
					log.Error("device at its connection cap, rejecting:", p.ReqBridgeRegister.Domain, len(pool.availableConns))
					resp.Error = true
					resp.ErrorMessage = "Device connection pool is full"
				} else {
					if !ok {
						log.Debug("Creating new pool")
						mg.bridges[p.ReqBridgeRegister.Domain] = &bridgePool{
							availableConns: []*gorilla.Conn{conn},
							lock:           new(sync.Mutex),
						}
					} else {
						log.Debug("Adding to the pool:", len(pool.availableConns))
						pool.lock.Lock()
						pool.availableConns = append(pool.availableConns, conn)
						pool.lock.Unlock()
					}
					resp.Payload = &pb.RespEnvelope_RespBridgeAckOnboard{
						RespBridgeAckOnboard: &pb.BridgeAckOnboard{
							Ok: true,
						},
					}
				}

				respBin, _ := proto.Marshal(resp)
				if err := conn.WriteMessage(gorilla.BinaryMessage, respBin); err != nil {
					log.Error("error responding, closing the connection:", err)
					conn.Close()
				}
				// After the connection is created, we leave it open and return
				return

			case *pb.ReqEnvelope_ReqRotateBridgeSecret:
				// Self-service "Regenerate" (issue #40 follow-up): the
				// device's current secret is the only thing that
				// authenticates this — see dao.RotateSecret's compare-and-
				// swap. A one-off request/response, not a pooled relay
				// connection, so this always closes the connection instead
				// of returning early like ReqBridgeRegister does above.
				defer conn.Close()
				log.Info("Rotate bridge secret for device:", p.ReqRotateBridgeSecret.Domain)
				newSecret := uuid.New().String() + uuid.New().String()
				ok, err := mg.dao.RotateSecret(
					p.ReqRotateBridgeSecret.OwnerUuid,
					p.ReqRotateBridgeSecret.Domain,
					p.ReqRotateBridgeSecret.Secret,
					newSecret,
				)
				if err != nil {
					log.Error("error rotating secret:", err)
					resp.Error = true
					resp.ErrorMessage = err.Error()
				} else if !ok {
					log.Error("rotate secret rejected: no matching device/secret")
					if logErr := mg.dao.LogAuthEvent(uuid.New().String(), p.ReqRotateBridgeSecret.Domain, p.ReqRotateBridgeSecret.OwnerUuid, conn.RemoteAddr().String(), "invalid_secret"); logErr != nil {
						log.Error("error logging auth event:", logErr)
					}
					resp.Error = true
					resp.ErrorMessage = "Invalid Secret"
				} else {
					resp.Payload = &pb.RespEnvelope_RespRotateBridgeSecretAck{
						RespRotateBridgeSecretAck: &pb.RotateBridgeSecretAck{
							NewSecret: newSecret,
						},
					}
				}

				respBin, _ := proto.Marshal(resp)
				if err := conn.WriteMessage(gorilla.BinaryMessage, respBin); err != nil {
					log.Error("error responding:", err)
				}
				return

			default:
				defer conn.Close()
				// This may be a direct request to a device: pick one off
				// the pool and pair with it for the rest of this
				// connection's life (see deviceRelay/the relay != nil
				// branch above) if the domain exists.
				var deviceConn *gorilla.Conn
				if pool, ok := mg.bridges[r.Host]; ok {
					pool.lock.Lock()
					for deviceConn == nil && len(pool.availableConns) > 0 {
						deviceConn = pool.availableConns[0]
						pool.availableConns = pool.availableConns[1:]

						log.Debug("Connecting")
						candidate := newDeviceRelay(deviceConn)
						respFrame, err := candidate.forward(frame)
						if err != nil {
							log.Error("Error fordwading message:", err)
							candidate.Close()
							deviceConn = nil
							continue
						}
						log.Debug("Connected")

						relay = candidate
						// Single use connection, close as soon as it is
						// finished since they are authenticated.
						defer relay.Close()

						if err := conn.WriteMessage(gorilla.BinaryMessage, respFrame); err != nil {
							log.Error("error responding, closing the connection:", err)
							pool.lock.Unlock()
							return
						}
						if err := mg.dao.RecordDeviceActivity(r.Host, int64(len(frame)), int64(len(respFrame))); err != nil {
							log.Error("error recording device activity:", err)
						}
					}
					pool.lock.Unlock()
				}

				if deviceConn == nil {
					log.Error("No available connections in the pool for this device")
					resp.Error = true
					resp.ErrorMessage = "No available connections in the pool for this device"
					respBin, _ := proto.Marshal(resp)
					if err := conn.WriteMessage(gorilla.BinaryMessage, respBin); err != nil {
						log.Error("error responding, closing the connection:", err)
						return
					}
				}
			}
		}
	}
}
