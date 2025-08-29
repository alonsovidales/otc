package websocket

import (
	"fmt"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
	"net/http"
)

const (
	CEndpoint = "/ws"
)

// Manager Structure that provides HTTP access to manage all the different
// groups and shards on each grorup
type Manager struct {
	baseUrl  string
	upgrader gorilla.Upgrader
}

func Init(baseUrl string) (mg *Manager) {
	mg = &Manager{
		baseUrl: baseUrl,
		upgrader: gorilla.Upgrader{
			// In production, set a proper origin check!
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}

	return
}

// RegenerateGroupKey Creates a new random key for a group
func (mg *Manager) Listen(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")

	conn, err := mg.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error("error trying to upgrade the websocket:", err)
		return
	}
	defer conn.Close()

	for {
		_, frame, err := conn.ReadMessage()
		if err != nil {
			log.Info("read:", err)
			return
		}

		var env pb.Envelope
		if err := proto.Unmarshal(frame, &env); err != nil {
			log.Error("bad proto:", err)
			continue
		}

		switch p := env.Payload.(type) {
		case *pb.Envelope_ReqGetStatus:
			out := &pb.Status{Online: true, LocalIp: 123, NasStatus: 0, Disks: 2}
			bytes, _ := proto.Marshal(out)
			if err := conn.WriteMessage(gorilla.BinaryMessage, bytes); err != nil {
				log.Error("write:", err)
				return
			}
			log.Info(fmt.Sprintf("Requested status!!!! %d", p))
		default:
			log.Info("unknown payload")
		}
	}
}
