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
		return
	}
	var auth pb.Auth
	if err := proto.Unmarshal(frame, &auth); err != nil {
		log.Error("error reading auth package:", err)
		return
	}

	// Now we have a session, we can just process all the messages using this from now on
	log.Debug("Auth with:", auth.Key, auth.Create)
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

		var env pb.ReqEnvelope
		if err := proto.Unmarshal(frame, &env); err != nil {
			log.Error("bad proto:", err)
			continue
		}

		resp := &pb.RespEnvelope{
			Id: env.Id,
		}
		switch p := env.Payload.(type) {
		case *pb.ReqEnvelope_ReqUploadFile:
			log.Info("Uploading file with path:", p.ReqUploadFile.Path)
			pbFile, err := mg.filesManager.UploadFile(session, p.ReqUploadFile.Path, p.ReqUploadFile.Content, p.ReqUploadFile.ForceOverride)
			if err != nil {
				log.Error("error trying to upload file:", err)
				// TODO: Return an error message
				continue
			}
			resp.Payload = &pb.RespEnvelope_RespFile{
				RespFile: pbFile,
			}

		case *pb.ReqEnvelope_ReqGetFile:
			log.Info("Get file with path:", p.ReqGetFile.Path)
			pbFile, err := mg.filesManager.GetFile(session, p.ReqGetFile.Path)
			if err != nil {
				log.Error("error trying to retrieve file:", err)
				// TODO: Return an error message
				continue
			}
			resp.Payload = &pb.RespEnvelope_RespFile{
				RespFile: pbFile,
			}

		case *pb.ReqEnvelope_ReqDelFile:
			log.Info("Del file by path:", p.ReqDelFile.Path)
			err := mg.filesManager.DelFile(session, p.ReqDelFile.Path)
			if err != nil {
				log.Error("error trying to delete file:", err)
				// TODO: Return an error message
				continue
			}

			log.Info("Deleted file by path:", p.ReqDelFile.Path)
			// Acknoledge the Deletion
			resp.Payload = &pb.RespEnvelope_RespAck{
				RespAck: &pb.Ack{
					Ok: true,
				},
			}

		case *pb.ReqEnvelope_ReqListFiles:
			log.Info("List of file by path:", p.ReqListFiles.Path)
			files, err := mg.filesManager.ListFiles(session, p.ReqListFiles.Globbing, p.ReqListFiles.Path)
			if err != nil {
				log.Error("error trying to list files:", err)
				// TODO: Return an error message
				continue
			}
			log.Debug("Files to return:", len(files))

			resp.Payload = &pb.RespEnvelope_RespListOfFiles{
				RespListOfFiles: &pb.ListOfFiles{
					Files: files,
				},
			}

		case *pb.ReqEnvelope_ReqGetStatus:
			log.Info(fmt.Sprintf("Requested status!!!! %d", p))

			status := &pb.Status{Online: true, LocalIp: 123, NasStatus: 0, Disks: 2}
			resp.Payload = &pb.RespEnvelope_RespStatus{
				RespStatus: status,
			}

		default:
			log.Info("unknown payload")
		}

		respBin, _ := proto.Marshal(resp)
		if err := conn.WriteMessage(gorilla.BinaryMessage, respBin); err != nil {
			log.Error("error responding, closing the connection:", err)
			return
		}
	}
}
