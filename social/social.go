package social

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/dao"
	filesmanager "github.com/alonsovidales/otc/files_manager"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/profile"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
	"github.com/alonsovidales/otc/settings"
	"github.com/google/uuid"
	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

const (
	EventTypeComment = "comment"
	ActionCreate     = "create"
	ActionModify     = "modify"
	ActionDelete     = "delete"
	PublicationEvent = "publication"
	LikeEvent        = "like_event"
	LikeCommentEvent = "like_comment_event"
	CommentEvent     = "comment"
)

type Social struct {
	dao          *dao.Dao
	filesmanager *filesmanager.Manager
	settings     *settings.Settings
	profile      *profile.Profile
}

type LikePublicationComment struct {
	Uuid         string `json:"uuid"`
	Action       string `json:"action"`
	CommentUUID  string `json:"comment_uuid"`
	Dt           int64  `json:"dt"`
	FriendDomain string `json:"friend_domain"`
}

type LikePublication struct {
	Uuid         string `json:"uuid"`
	Action       string `json:"action"`
	PubUUID      string `json:"pub_uuid"`
	Dt           int64  `json:"dt"`
	FriendDomain string `json:"friend_domain"`
}

type Publication struct {
	Uuid   string `json:"uuid"`
	Action string `json:"action"`
	Dt     int64  `json:"dt"`
	Text   string `json:"comment"`
}

type Comment struct {
	Uuid          string `json:"uuid"`
	Action        string `json:"action"`
	PubUUID       string `json:"pub_uuid"`
	Dt            int64  `json:"dt"`
	Comment       string `json:"comment"`
	PublisherName string `json:"publisher_name"`
}

func Init(dao *dao.Dao, filesmanager *filesmanager.Manager, settings *settings.Settings, profile *profile.Profile) *Social {
	return &Social{
		dao:          dao,
		filesmanager: filesmanager,
		settings:     settings,
		profile:      profile,
	}
}

func (sc *Social) NewPublication(ses *session.Session, text string, paths []string) (pubUuID string, err error) {
	files := make([]*pb.File, len(paths))
	for i, path := range paths {
		file, err := sc.filesmanager.GetFile(ses, path)
		if err != nil {
			log.Error("Error loading file:", err)
		}

		files[i] = file
		unencPath := fmt.Sprintf("%s/%s", cfg.GetStr("otc", "unenc-storage-path"), file.Hash)
		err = os.WriteFile(unencPath, file.Content, 0644) // perms: rw-r--r--
		if err != nil {
			return "", err
		}

		unEncThumb, err := sc.filesmanager.GetThumbnail(ses, file)
		if err != nil {
			return "", err
		}
		unencPathThumb := fmt.Sprintf("%s/%s_thumbnail", cfg.GetStr("otc", "unenc-storage-path"), file.Hash)
		err = os.WriteFile(unencPathThumb, unEncThumb, 0644) // perms: rw-r--r--
		if err != nil {
			return "", err
		}

		log.Debug("Publication in path:", path)
	}

	pubUuid := uuid.New().String()
	json, _ := json.Marshal(Publication{
		Uuid:   pubUuid,
		Action: ActionCreate,
		Dt:     time.Now().Unix(),
		Text:   text,
	})
	err = sc.dao.NewEvent(PublicationEvent, json)
	if err != nil {
		return "", err
	}

	return pubUuid, sc.dao.NewSocialPublication(pubUuID, text, sc.profile.Domain, true, files)
}

func (sc *Social) GetEvents(pr *profile.Profile, since time.Time, total int32) (events []*pb.Event, err error) {
	events, err = sc.dao.GetEvents(since, total)
	if err != nil {
		log.Debug("error retriving events", err)
	}
	return
}

func (sc *Social) GetPublications(pr *profile.Profile, since time.Time, total int32, ownOnly bool, exclude []string) (publications *pb.SocialPublications, err error) {
	publications, err = sc.dao.GetSocialPublications(since, total, ownOnly, exclude, pr.Name, pr.Text, pr.Image)
	if err != nil {
		log.Debug("error retriving publications", err)
		return
	}

	// Populate the files content
	for _, pub := range publications.Publications {
		for _, file := range pub.Files {
			file.Content, err = os.ReadFile(fmt.Sprintf("%s/%s_thumbnail", cfg.GetStr("otc", "unenc-storage-path"), file.Hash))
			if err != nil {
				return nil, err
			}
		}

		pub.Comments, err = sc.dao.GetSocialPublicationComments(pub.Uuid)
		if err != nil {
			return nil, err
		}
	}

	return
}

func (sc *Social) connectToDevice(domain string) (conn *gorilla.Conn, err error) {
	// If this is a bridge connection, we will retry to connect to the bridge
	u := url.URL{Scheme: "wss", Host: domain, Path: "/ws"}
	log.Debug("Connecting to external:", domain, u)
	h := http.Header{}
	h.Set("Sec-WebSocket-Protocol", "protobuf")
	conn, _, err = gorilla.DefaultDialer.Dial(u.String(), h)
	if err != nil {
		log.Error("dialing websocket:", err)
		return
	}
	log.Debug("Connected to external...")

	return
}

type friendship struct {
	conn *gorilla.Conn
	data *pb.Friendship
	dao  *dao.Dao
	sc   *Social
}

func (sc *Social) SyncWithFriends() (err error) {
	firendships, err := sc.GetFriendships()
	if err != nil {
		log.Error("Error trying to get friendships from the DB:", err)
		return err
	}
	for _, data := range firendships {
		friend := &friendship{
			sc:   sc,
			data: data,
			dao:  sc.dao,
		}

		log.Debug("Updating friendship status for:", data.OriginProfile.Domain)
		friend.conn, err = sc.connectToDevice(friend.data.OriginProfile.Domain)
		if err != nil {
			log.Error("Error connecting to external device:", friend.data.OriginProfile.Domain, err)
			continue
		}
		defer friend.conn.Close()

		err = friend.updateFriendshipStatus()
		if err != nil {
			log.Error("Error trying to update friendship status:", err)
			continue
		}

		if friend.data.Status == pb.FriendShipStatus_Accepted {
			log.Debug("Auth as friend:", friend.data.OriginProfile.Domain)
			err = friend.autAsFriend()
			if err != nil {
				log.Error("Error trying to auth as friend:", err)
				continue
			}

			/*err = friend.updateFriendPublications()
			if err != nil {
				log.Error("Error trying to update friendship:", err)
				continue
			}*/
		}

		// Update friend timeline is the request is accepted
	}

	return
}

func (fr *friendship) updateFriendshipStatus() (err error) {
	log.Debug("Update friendship:", fr.data.OriginProfile.Domain)
	if !fr.data.Sent {
		log.Debug("We are the receivers, we decide, no need to sync")
		// If we are the senders we can't change the status
		return
	}

	msg := &pb.ReqEnvelope{
		Id: 1,
		Payload: &pb.ReqEnvelope_ReqGetFriendshipStatus{
			ReqGetFriendshipStatus: &pb.GetFriendshipStatus{
				Domain: fr.sc.settings.Domain, // We want to get our status, so our domain
				Secret: fr.data.Secret,
			},
		},
	}
	b, _ := proto.Marshal(msg)
	if err = fr.conn.WriteMessage(gorilla.BinaryMessage, b); err != nil {
		log.Error("write error in external:", err)
		return
	}

	_, data, err := fr.conn.ReadMessage()
	if err != nil {
		log.Error("read error in external:", err)
		return
	}

	log.Debug("Getting response for update friendship:", fr.data.OriginProfile.Domain, data)
	var respProf pb.RespEnvelope
	if err = proto.Unmarshal(data, &respProf); err != nil {
		return
	}
	if respProf.Error {
		log.Debug("Error reading friendship status:", respProf.ErrorMessage)
		return errors.New(respProf.ErrorMessage)
	}
	resp := respProf.Payload.(*pb.RespEnvelope_RespFriendshipStatus)
	status := resp.RespFriendshipStatus.Status
	log.Debug("Remote friendship status:", fr.data.OriginProfile.Domain, status)

	return fr.dao.ChangeFriendStatus(fr.data.OriginProfile.Domain, status)
}

func (fr *friendship) autAsFriend() (err error) {
	msg := &pb.ReqEnvelope{
		Id: 1,
		Payload: &pb.ReqEnvelope_ReqAuthAsFriend{
			ReqAuthAsFriend: &pb.AuthAsFriend{
				Domain: fr.sc.settings.Domain,
				Secret: fr.data.Secret,
			},
		},
	}
	b, _ := proto.Marshal(msg)
	if err = fr.conn.WriteMessage(gorilla.BinaryMessage, b); err != nil {
		log.Error("write error trying to auth as friend:", err)
		return
	}

	_, data, err := fr.conn.ReadMessage()
	if err != nil {
		log.Error("read error trying to auth as friend:", err)
		return
	}

	log.Debug("Getting response for update friendship:", fr.data.OriginProfile.Domain)
	var respProf pb.RespEnvelope
	if err = proto.Unmarshal(data, &respProf); err != nil {
		return
	}
	if respProf.Error {
		log.Debug("Error trying to auth as friend:", respProf.ErrorMessage)
		return errors.New(respProf.ErrorMessage)
	}
	resp := respProf.Payload.(*pb.RespEnvelope_RespAck)
	if !resp.RespAck.Ok {
		return errors.New("Error authenticating as friend")
	}

	return
}

/*func (fr *friendship) updateFriendPublications() (err error) {
	log.Debug("Updating publications")
	msg := &pb.ReqEnvelope{
		Id: 1,
		Payload: &pb.ReqEnvelope_ReqGetSocialPublications{
			ReqGetSocialPublications: &pb.GetSocialPublications{
				ExcludeUuids: []string{}, // TODO add the known publications
				Total:        20,         // Update only 20 to don't flood the other device
			},
		},
	}
	b, _ := proto.Marshal(msg)
	if err = fr.conn.WriteMessage(gorilla.BinaryMessage, b); err != nil {
		log.Error("write error trying to get publications from firned:", fr.data.OriginProfile.Domain, err)
		return
	}

	_, data, err := fr.conn.ReadMessage()
	if err != nil {
		log.Error("read error trying to get publications from friend:", fr.data.OriginProfile.Domain, err)
		return
	}

	log.Debug("Getting response for update publications:", fr.data.OriginProfile.Domain)
	var respProf pb.RespEnvelope
	if err = proto.Unmarshal(data, &respProf); err != nil {
		return
	}
	if respProf.Error {
		log.Debug("Error trying to publications from friend:", respProf.ErrorMessage)
		return errors.New(respProf.ErrorMessage)
	}
	resp := respProf.Payload.(*pb.RespEnvelope_RespSocialPublications)
	log.Debug("Publications to update", len(resp.RespSocialPublications.Publications))
pub:
	for _, pub := range resp.RespSocialPublications.Publications {
		// Store the files in the local drive first
		for _, file := range pub.Files {
			sum := sha256.Sum256(file.Content)
			file.Hash = hex.EncodeToString(sum[:])

			unencPathThumb := fmt.Sprintf("%s/%s_thumbnail", cfg.GetStr("otc", "unenc-storage-path"), file.Hash)
			err = os.WriteFile(unencPathThumb, file.Content, 0644) // perms: rw-r--r--
			if err != nil {
				log.Error("Error trying to write file from an external publication")
				continue pub
			}
		}

		_, err = fr.dao.NewSocialPublication(pub.Uuid, pub.Text, fr.data.OriginProfile.Domain, false, pub.Files)
		if err != nil {
			log.Error("Error creating social publication for friend:", err)
			continue
		}
	}

	return
}
*/

func (sc *Social) GetRemoteProfile(domain string, conn *gorilla.Conn) (name, text string, image []byte, err error) {
	// Get the profile data from the other device
	log.Debug("Getting remote profile:", domain)
	msg := &pb.ReqEnvelope{
		Id: 1,
		Payload: &pb.ReqEnvelope_ReqGetProfile{
			ReqGetProfile: &pb.GetProfile{},
		},
	}
	b, _ := proto.Marshal(msg)
	if err = conn.WriteMessage(gorilla.BinaryMessage, b); err != nil {
		log.Error("write error in external:", err)
		return
	}

	log.Debug("We got remote profile:", domain)
	// We should get back the Ack
	_, data, err := conn.ReadMessage()
	if err != nil {
		log.Error("read error in external:", err)
		return
	}

	var respProf pb.RespEnvelope
	if err = proto.Unmarshal(data, &respProf); err != nil {
		return
	}
	prof := respProf.Payload.(*pb.RespEnvelope_RespProfile).RespProfile
	log.Debug("Remote profile looks good:", domain, prof.Name)

	return prof.Name, prof.Text, prof.Image, nil
}

func (sc *Social) SendFriendshipReq(domain string) (err error) {
	conn, err := sc.connectToDevice(domain)
	if err != nil {
		log.Error("Error connecting to external device:", err)
		return err
	}
	defer conn.Close()

	remoteName, remoteText, remoteImg, err := sc.GetRemoteProfile(domain, conn)
	if err != nil {
		return
	}

	secret := uuid.New().String()

	// Store the remote data and then send the real request
	err = sc.dao.NewFriendship(domain, secret, remoteName, remoteText, remoteImg, true)
	log.Debug("Remote profile stored")
	if err != nil {
		return
	}
	msg := &pb.ReqEnvelope{
		Id: 1,
		Payload: &pb.ReqEnvelope_ReqFriendshipInterRequest{
			ReqFriendshipInterRequest: &pb.FriendshipInterRequest{
				Domain: sc.settings.Domain,
				Secret: secret,
				OriginProfile: &pb.Profile{
					Name:  sc.profile.Name,
					Image: sc.profile.Image,
					Text:  sc.profile.Text,
				},
			},
		},
	}
	b, _ := proto.Marshal(msg)
	if err := conn.WriteMessage(gorilla.BinaryMessage, b); err != nil {
		log.Error("write error in external:", err)
		return err
	}
	log.Debug("Friendship request sent internally")

	// We should get back the Ack
	_, data, err := conn.ReadMessage()
	if err != nil {
		log.Error("read error in friendship internal response:", err)
		return
	}

	var respAck pb.RespEnvelope
	if err = proto.Unmarshal(data, &respAck); err != nil {
		log.Error("read error in unmarshalling friendship internal requestrespons3:", err)
		return
	}
	if respAck.Payload.(*pb.RespEnvelope_RespAck).RespAck.Ok {
		log.Debug("Frienship request internal accepted...")
		return
	}

	log.Debug("Frienship request failed...", respAck.Payload.(*pb.RespEnvelope_RespAck).RespAck.ErrorMsg)
	return errors.New(respAck.Payload.(*pb.RespEnvelope_RespAck).RespAck.ErrorMsg)
}

func (sc *Social) ExternalFriendshipRequest(extDomain, secret, name, profileText string, image []byte) (err error) {
	log.Debug("Got an internal friendship req, checking foreign domain:", extDomain)
	// Check if the request came from the other side
	conn, err := sc.connectToDevice(extDomain)
	defer conn.Close()
	if err != nil {
		log.Error("Error connecting to external device:", err)
		return err
	}

	// Check if the other device sent the request
	msg := &pb.ReqEnvelope{
		Id: 1,
		Payload: &pb.ReqEnvelope_ReqDidSendFriendshipReq{
			ReqDidSendFriendshipReq: &pb.DidSendFriendshipReq{
				Domain: sc.settings.Domain,
				Secret: secret,
			},
		},
	}
	b, _ := proto.Marshal(msg)
	if err := conn.WriteMessage(gorilla.BinaryMessage, b); err != nil {
		log.Error("write error in external:", err)
		return err
	}

	log.Debug("Processing response form External Domain:", extDomain)
	// We should get back the Ack
	_, data, err := conn.ReadMessage()
	if err != nil {
		log.Error("read error validating external friendship request:", err)
		return
	}

	var respAck pb.RespEnvelope
	if err = proto.Unmarshal(data, &respAck); err != nil {
		log.Error("read error unmarshalling external friendship request:", err)
		return
	}
	if respAck.Payload.(*pb.RespEnvelope_RespAck).RespAck.Ok {
		log.Debug("Frienship ack request sent...")
		return sc.dao.NewFriendship(extDomain, secret, name, profileText, image, false)
	}

	log.Debug("Frienship ack request failed...", respAck.Payload.(*pb.RespEnvelope_RespAck).RespAck.ErrorMsg)
	return errors.New(respAck.Payload.(*pb.RespEnvelope_RespAck).RespAck.ErrorMsg)
}

func (sc *Social) GetFriendship(domain, secret string) (friendship *pb.Friendship, err error) {
	status, name, text, image, _, err := sc.dao.GetFriendship(domain, secret)
	if err != nil {
		return nil, err
	}

	return &pb.Friendship{
		OriginProfile: &pb.Profile{
			Name:   name,
			Image:  image,
			Text:   text,
			Domain: domain,
		},
		Status: sc.statusToPb(status),
	}, nil
}

func (sc *Social) GetFriendships() (friendships []*pb.Friendship, err error) {
	return sc.dao.GetFriendships()
}

func (sc *Social) statusToPb(status string) (pbStatus pb.FriendShipStatus) {
	switch status {
	case "pending":
		return pb.FriendShipStatus_Pending
	case "accepted":
		return pb.FriendShipStatus_Accepted
	case "blocked":
		return pb.FriendShipStatus_Blocked
	}

	return
}

func (sc *Social) NewLikePublicationComment(pr *profile.Profile, commentUuid string) (err error) {
	likeUuid := uuid.New().String()
	json, err := json.Marshal(LikePublicationComment{
		Uuid:         likeUuid,
		Action:       ActionCreate,
		CommentUUID:  commentUuid,
		Dt:           time.Now().Unix(),
		FriendDomain: pr.Domain,
	})
	err = sc.dao.NewEvent(LikeEvent, json)
	if err != nil {
		return err
	}
	return sc.dao.NewLikePublicationComment(commentUuid, pr.Domain)
}

func (sc *Social) NewLikePublication(pr *profile.Profile, pubUuid string) (err error) {
	likeUuid := uuid.New().String()
	json, err := json.Marshal(LikePublication{
		Uuid:         likeUuid,
		Action:       ActionCreate,
		PubUUID:      pubUuid,
		Dt:           time.Now().Unix(),
		FriendDomain: pr.Domain,
	})
	err = sc.dao.NewEvent(LikeEvent, json)
	if err != nil {
		return err
	}
	return sc.dao.NewLikePublication(pubUuid, pr.Domain)
}

func (sc *Social) NewSocialComment(pr *profile.Profile, pubUuid, comment string) (err error) {
	commentUuid := uuid.New().String()
	json, err := json.Marshal(Comment{
		Uuid:          commentUuid,
		Action:        ActionCreate,
		PubUUID:       pubUuid,
		Comment:       comment,
		Dt:            time.Now().Unix(),
		PublisherName: pr.Name,
	})
	err = sc.dao.NewEvent(CommentEvent, json)
	if err != nil {
		return err
	}
	return sc.dao.NewComment(commentUuid, pr.Name, pubUuid, comment)
}

func (sc *Social) ChangeFriendStatus(domain string, status pb.FriendShipStatus) (err error) {
	return sc.dao.ChangeFriendStatus(domain, status)
}
