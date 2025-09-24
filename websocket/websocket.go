package websocket

import (
	"fmt"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/files_manager"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/profile"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
	"github.com/alonsovidales/otc/settings"
	"github.com/alonsovidales/otc/status"
	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
	"math/rand"
	"net/http"
	"net/url"
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
	settings     *settings.Settings
	profile      *profile.Profile
}

func Init(baseUrl string, dao *dao.Dao, filesManager *filesmanager.Manager) (mg *Manager) {
	st, err := settings.Init(dao)
	if err != nil {
		log.Fatal("Error loading the settings", err)
	}
	pr, err := profile.Init(dao)
	if err != nil {
		log.Fatal("Error loading the profile", err)
	}
	mg = &Manager{
		baseUrl:      baseUrl,
		dao:          dao,
		filesManager: filesManager,
		upgrader: gorilla.Upgrader{
			// In production, set a proper origin check!
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		settings: st,
		profile:  pr,
	}

	for i := 0; i < int(cfg.GetInt("otc", "bridge-connections")); i++ {
		go mg.OpenBridge()
	}

	rand.Seed(time.Now().UnixNano())

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

func (mg *Manager) OpenBridge() {
	// If this is a bridge connection, we will retry to connect to the bridge
	defer func() {
		log.Debug("Connection finished, open a new one")
		d := (3 * rand.Float64()) * float64(time.Second) // Sleep in between 0.0–3.0 s
		time.Sleep(time.Duration(d))
		go mg.OpenBridge()
	}()

	u := url.URL{Scheme: "wss", Host: cfg.GetStr("otc", "bridge-addr"), Path: "/ws"}
	log.Debug("Connecting to bridge:", cfg.GetStr("otc", "bridge-addr"), u)
	h := http.Header{}
	h.Set("Sec-WebSocket-Protocol", "protobuf")
	c, _, err := gorilla.DefaultDialer.Dial(u.String(), h)
	if err != nil {
		log.Error("dialing websocket:", err)
		return
	}
	log.Debug("Connected to bridge...")
	defer c.Close()

	// AUTH the connection
	msg := &pb.ReqEnvelope{
		Id: 1,
		Payload: &pb.ReqEnvelope_ReqBridgeRegister{
			ReqBridgeRegister: &pb.BridgeRegister{
				OwnerUuid: mg.settings.DeviceUuid,
				Domain:    mg.settings.Domain,
				Secret:    mg.settings.BridgeSecret,
			},
		},
	}
	b, _ := proto.Marshal(msg)
	if err := c.WriteMessage(gorilla.BinaryMessage, b); err != nil {
		log.Error("write:", err)
		return
	}

	// We should get back the Ack
	_, data, err := c.ReadMessage()
	if err != nil {
		log.Error("read:", err)
		return
	}

	var respAck pb.RespEnvelope
	_ = proto.Unmarshal(data, &respAck)
	if respAck.Payload.(*pb.RespEnvelope_RespBridgeAckOnboard).RespBridgeAckOnboard.Ok {
		log.Debug("Authenticated in the bridge, waiting for messages...")

		mg.handleConnection(c, nil)
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

	mg.handleConnection(conn, r)
}

type connHandler struct {
	mg      *Manager
	session *session.Session
}

func (ch *connHandler) processNonAuthRequest(env pb.ReqEnvelope) (resp *pb.RespEnvelope, closeConn bool) {
	resp = &pb.RespEnvelope{
		Id: env.Id,
	}

	switch p := env.Payload.(type) {
	case *pb.ReqEnvelope_ReqAuth:
		var err error
		ch.session, err = session.New(p.ReqAuth.Uuid, p.ReqAuth.Key, p.ReqAuth.Create, ch.mg.dao)

		if err != nil {
			time.Sleep(1)
			resp.Payload = &pb.RespEnvelope_RespAck{
				RespAck: &pb.Ack{
					Ok:       false,
					ErrorMsg: fmt.Sprintf("Error: %s", err),
				},
			}
			return resp, true
		}
		log.Info("Authenticated session")

		resp.Payload = &pb.RespEnvelope_RespAck{
			RespAck: &pb.Ack{
				Ok: true,
			},
		}

	case *pb.ReqEnvelope_ReqGetProfile:
		log.Info("Get profile")

		resp.Payload = &pb.RespEnvelope_RespProfile{
			RespProfile: &pb.Profile{
				Name:  ch.mg.profile.Name,
				Image: ch.mg.profile.Image,
				Text:  ch.mg.profile.Text,
			},
		}

	default:
		return nil, false
	}

	return
}

func (ch *connHandler) processAuthRequest(env pb.ReqEnvelope) (resp *pb.RespEnvelope, closeConn bool) {
	resp = &pb.RespEnvelope{
		Id: env.Id,
	}

	switch p := env.Payload.(type) {
	case *pb.ReqEnvelope_ReqUploadFile:
		log.Info("Uploading file with path:", p.ReqUploadFile.Path)
		pbFile, err := ch.mg.filesManager.UploadFile(ch.session, p.ReqUploadFile.Path, p.ReqUploadFile.Content, p.ReqUploadFile.ForceOverride, p.ReqUploadFile.Created)
		if err != nil {
			resp.Error = true
			resp.ErrorMessage = fmt.Sprintf("error trying to upload file: %s", err)
		} else {
			resp.Payload = &pb.RespEnvelope_RespFile{
				RespFile: pbFile,
			}
		}

	case *pb.ReqEnvelope_ReqGetFile:
		log.Info("Get file with path:", p.ReqGetFile.Path)
		pbFile, err := ch.mg.filesManager.GetFile(ch.session, p.ReqGetFile.Path)
		if err != nil {
			resp.Error = true
			resp.ErrorMessage = fmt.Sprintf("error trying to retrieve file: %s", err)
		} else {
			resp.Payload = &pb.RespEnvelope_RespFile{
				RespFile: pbFile,
			}
		}

	case *pb.ReqEnvelope_ReqDelFile:
		log.Info("Del file by path:", p.ReqDelFile.Path)
		err := ch.mg.filesManager.DelFile(ch.session, p.ReqDelFile.Path)
		if err != nil {
			resp.Error = true
			resp.ErrorMessage = fmt.Sprintf("error trying to delete file: %s", err)
		} else {
			log.Info("Deleted file by path:", p.ReqDelFile.Path)
			// Acknoledge the Deletion
			resp.Payload = &pb.RespEnvelope_RespAck{
				RespAck: &pb.Ack{
					Ok: true,
				},
			}
		}

	case *pb.ReqEnvelope_ReqListFiles:
		log.Info("List of file by path:", p.ReqListFiles.Path)
		files, err := ch.mg.filesManager.ListFiles(ch.session, p.ReqListFiles.Path)
		if err != nil {
			log.Error("error trying to list files:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			log.Debug("Files to return:", len(files))

			resp.Payload = &pb.RespEnvelope_RespListOfFiles{
				RespListOfFiles: &pb.ListOfFiles{
					Files: files,
				},
			}
		}

	case *pb.ReqEnvelope_ReqGetTags:
		log.Info("Get Tags")
		tags, err := ch.mg.dao.GetTags()
		if err != nil {
			log.Error("error trying to get tags:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			log.Debug("Available tags:", len(tags))

			resp.Payload = &pb.RespEnvelope_RespTagsList{
				RespTagsList: &pb.TagsList{
					Tags: tags,
				},
			}
		}

	case *pb.ReqEnvelope_ReqSearchPhotos:
		log.Info("Search by text:", p.ReqSearchPhotos.Tags)
		files, err := ch.mg.filesManager.ImageSearch(ch.session, "", p.ReqSearchPhotos.Tags)
		if err != nil {
			log.Error("error trying to list files:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			log.Debug("Files to return:", len(files))

			resp.Payload = &pb.RespEnvelope_RespListOfFiles{
				RespListOfFiles: &pb.ListOfFiles{
					Files: files,
				},
			}
		}

	case *pb.ReqEnvelope_ReqGetStatus:
		log.Info(fmt.Sprintf("Requested status %d", p))
		st, err := status.GetStatus()

		if err != nil {
			log.Error("error trying to retrive status:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			log.Debug("Current status:", st)

			resp.Payload = &pb.RespEnvelope_RespStatus{
				RespStatus: st,
			}
		}

	case *pb.ReqEnvelope_ReqChangeKey:
		log.Info(fmt.Sprintf("Change key %d", p))
		err := ch.session.ChangeKey(p.ReqChangeKey.OldKey, p.ReqChangeKey.NewKey)

		if err != nil {
			log.Error("error trying to change secret key:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			resp.Payload = &pb.RespEnvelope_RespAck{
				RespAck: &pb.Ack{
					Ok: true,
				},
			}
		}

	case *pb.ReqEnvelope_ReqSetSettings:
		log.Info("Set settings")
		err := ch.mg.settings.SetSettings(p.ReqSetSettings.Domain)

		if err != nil {
			log.Error("error trying to change secret key:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			resp.Payload = &pb.RespEnvelope_RespAck{
				RespAck: &pb.Ack{
					Ok: true,
				},
			}
		}

	case *pb.ReqEnvelope_ReqSetProfile:
		log.Info("Set profile")
		err := ch.mg.profile.SetProfile(p.ReqSetProfile.Name, p.ReqSetProfile.Image, p.ReqSetProfile.Text)

		if err != nil {
			log.Error("error trying to get serrings:", err)
			resp.Error = true
			resp.ErrorMessage = err.Error()
		} else {
			resp.Payload = &pb.RespEnvelope_RespAck{
				RespAck: &pb.Ack{
					Ok: true,
				},
			}
		}

	case *pb.ReqEnvelope_ReqGetSettings:
		log.Info("Get settings")

		resp.Payload = &pb.RespEnvelope_RespSettings{
			RespSettings: &pb.Settings{
				Domain: ch.mg.settings.Domain,
			},
		}

	default:
		log.Error("unknown payload:", p)
		resp.Error = true
		resp.ErrorMessage = "unknown payload"
	}

	return
}

func (mg *Manager) handleConnection(conn *gorilla.Conn, r *http.Request) {
	ch := &connHandler{
		mg: mg,
	}

	for {
		log.Debug("Waiting for messages")
		_, frame, err := conn.ReadMessage()
		if err != nil {
			log.Error("error processing message:", err)
			return
		}

		var env pb.ReqEnvelope
		if err := proto.Unmarshal(frame, &env); err != nil {
			log.Error("bad proto:", err)
			return
		}

		resp, closeConn := ch.processNonAuthRequest(env)
		if resp == nil && ch.session != nil {
			resp, closeConn = ch.processAuthRequest(env)
		}

		respBin, _ := proto.Marshal(resp)
		if err := conn.WriteMessage(gorilla.BinaryMessage, respBin); err != nil {
			log.Error("error responding, closing the connection:", err)
			return
		}
		if closeConn {
			return
		}
	}
}
