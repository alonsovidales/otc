package websocket

import (
	"fmt"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/files_manager"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
	"net/http"
	"time"
)

const (
	CEndpoint = "/ws"
)

// Manager Structure that provides HTTP access to manage all the different
// groups and shards on each grorup
type Manager struct {
	baseUrl      string
	dao          *dao.Dao
	upgrader     gorilla.Upgrader
	filesManager *filesmanager.Manager
}

func Init(baseUrl string, dao *dao.Dao, filesManager *filesmanager.Manager) (mg *Manager) {
	mg = &Manager{
		baseUrl:      baseUrl,
		dao:          dao,
		filesManager: filesManager,
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

	// The first message should always be an auth, it not we will just close here
	_, frame, err := conn.ReadMessage()
	if err != nil {
		log.Error("error in Auth:", err)
		return
	}
	var auth pb.Auth
	if err := proto.Unmarshal(frame, &auth); err != nil {
		log.Error("error reading auth package:", err)
		return
	}

	// Now we have a session, we can just process all the messages using this from now on
	session, err := session.New(auth.Uuid, auth.Key, auth.Create, mg.dao)
	if err != nil {
		time.Sleep(1)
		log.Info("Authentication error:", err)
		return
	}
	log.Info(fmt.Sprintf("Authenticated session: %s", auth))

	// Acknoledge the authentication
	resp, _ := proto.Marshal(&pb.Ack{Ok: true})
	if err := conn.WriteMessage(gorilla.BinaryMessage, resp); err != nil {
		log.Error("error responding, closing the connection:", err)
		return
	}

	for {
		_, frame, err := conn.ReadMessage()
		if err != nil {
			log.Info("error processing message:", err)
			return
		}

		var env pb.Envelope
		if err := proto.Unmarshal(frame, &env); err != nil {
			log.Error("bad proto:", err)
			continue
		}

		var resp []byte
		switch p := env.Payload.(type) {
		case *pb.Envelope_ReqUploadFile:
			log.Info("Uploading file with path:", p.ReqUploadFile.Path)
			out, err := mg.filesManager.UploadFile(session, p.ReqUploadFile.Path, p.ReqUploadFile.Content, p.ReqUploadFile.ForceOverride)
			if err != nil {
				log.Error("error trying to upload file:", err)
				// TODO: Return an error message
				continue
			}
			resp, _ = proto.Marshal(out)

		case *pb.Envelope_ReqGetFile:
			log.Info("Get file with path:", p.ReqGetFile.Path)
			file, err := mg.filesManager.GetFile(session, p.ReqGetFile.Path)
			if err != nil {
				log.Error("error trying to retrieve file:", err)
				// TODO: Return an error message
				continue
			}
			resp, _ = proto.Marshal(file)

		case *pb.Envelope_ReqDelFile:
			log.Info("Del file by path:", p.ReqDelFile.Path)
			err := mg.filesManager.DelFile(session, p.ReqDelFile.Path)
			if err != nil {
				log.Error("error trying to delete file:", err)
				// TODO: Return an error message
				continue
			}

			log.Info("Deleted file by path:", p.ReqDelFile.Path)
			// Acknoledge the Deletion
			resp, _ = proto.Marshal(&pb.Ack{Ok: true})

		case *pb.Envelope_ReqListFiles:
			log.Info("List of file by path:", p.ReqListFiles.Path)
			files, err := mg.filesManager.ListFiles(session, p.ReqListFiles.Globbing, p.ReqListFiles.Path)
			if err != nil {
				log.Error("error trying to list files:", err)
				// TODO: Return an error message
				continue
			}
			log.Debug("Files to return:", len(files))

			resp, _ = proto.Marshal(&pb.ListOfFiles{Files: files})

		case *pb.Envelope_ReqGetStatus:
			log.Info(fmt.Sprintf("Requested status!!!! %d", p))

			out := &pb.Status{Online: true, LocalIp: 123, NasStatus: 0, Disks: 2}
			resp, _ = proto.Marshal(out)

		default:
			log.Info("unknown payload")
		}

		if err := conn.WriteMessage(gorilla.BinaryMessage, resp); err != nil {
			log.Error("error responding, closing the connection:", err)
			return
		}
	}
}
