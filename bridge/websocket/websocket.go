package websocket

import (
	"fmt"
	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
	"net/http"
	"sync"
)

const (
	CEndpoint = "/ws"
)

type bridgePool struct {
	availableConns []*gorilla.Conn
	lock           *sync.Mutex
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
	var deviceConn *gorilla.Conn
	for {
		_, frame, err := conn.ReadMessage()
		if err != nil {
			log.Error("error processing message:", err)
			return
		}

		if deviceConn != nil {
			// We are connecting a device with a client, so just forward everything
			log.Debug("New message with connection")
			if err := mg.forwardMessage(conn, deviceConn, frame); err != nil {
				log.Error("Error fordwading message:", err)
				return
			}
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
				} else if err != nil {
					log.Error("error trying to register:", err)
					resp.Error = true
					resp.ErrorMessage = err.Error()
				} else {
					if pool, ok := mg.bridges[p.ReqBridgeRegister.Domain]; !ok {
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

			default:
				defer conn.Close()
				// This may be a direct request to a device, just forward it if the domain exists
				if pool, ok := mg.bridges[r.Host]; ok {
					pool.lock.Lock()
					for deviceConn == nil && len(pool.availableConns) > 0 {
						deviceConn = pool.availableConns[0]
						pool.availableConns = pool.availableConns[1:]
						// Single use connection, close as soon
						// as it is finished since they are authenticated
						defer deviceConn.Close()

						log.Debug("Connecting")
						if err := mg.forwardMessage(conn, deviceConn, frame); err != nil {
							log.Error("Error fordwading message:", err)
							deviceConn = nil
						}
						log.Debug("Connected")
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

func (mg *Manager) forwardMessage(conn, deviceConn *gorilla.Conn, frame []byte) (err error) {
	log.Debug("Forwading message to device")
	if err := deviceConn.WriteMessage(gorilla.BinaryMessage, frame); err != nil {
		log.Error("error forwarding, closing the connection:", err)
		return err
	}
	log.Debug("Reading response from device")
	_, respFrame, err := deviceConn.ReadMessage()
	if err != nil {
		log.Error("error reading from device, closing the connection:", err)
		return err
	}
	log.Debug("Forwading response to client")
	if err := conn.WriteMessage(gorilla.BinaryMessage, respFrame); err != nil {
		log.Error("error forwading respose, closing the connection:", err)
		return err
	}

	return
}
