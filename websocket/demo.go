// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"math"
	"reflect"
	"sync"
	"time"

	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// A demo device (`[otc] demo = true`) is a public device anyone can sign in
// to with its published password, to look around Off The Cloud before
// buying the hardware. It answers what only reads and refuses everything
// that would change it - files, people, collections, posts, sharing,
// friends, settings, the password, users, updates and the machine itself -
// with cCodeDemo. demoRules is an allowlist: a request added later is
// refused until it is put there (TestDemoRulesCoverEveryRequest fails until
// it is classified).
//
// The content is loaded with demo off (uploads need a session key), then
// demo is turned on and the device restarted. The owner password must be set
// before the device is reachable (`otc <env> init-owner-password`): a demo
// device refuses the first sign-in that would set it.

// cCodeDemo is RespEnvelope.error_code (and Ack.code) on a request a demo
// device refuses. The message beside it is dev.err.demo.
const cCodeDemo = "demo"

// demoMode is [otc] demo and demoPassword [otc] demo-password, read once
// by Init.
var (
	demoMode     bool
	demoPassword string
)

func initDemoMode() {
	if !cfg.HasSection("otc") {
		return
	}
	demoMode = cfg.GetBool("otc", "demo")
	demoPassword = cfg.GetStr("otc", "demo-password")
	if demoMode {
		log.Info("demo device: every change is refused")
	}
}

// demoPasswordShown is PubKey.demo_password: only a demo device publishes
// one.
func demoPasswordShown() string {
	if !demoMode {
		return ""
	}
	return demoPassword
}

// demoPiStatus makes a demo server's Status read like a device's: a
// Raspberry Pi with 8 GB of memory and a 64 GB card, and two 2 TB disks
// mirrored and in sync. What the library uses stays the real figure, so it
// agrees with Files' Storage view, and the CPU and memory move a little, as
// a device's do. Sizes are in Status' units (bytes / 1024 / 1000).
func demoPiStatus(st *pb.Status, now time.Time) {
	const (
		raidSize = 1_923_800 // 2 TB disks, formatted
		diskSize = 60_546    // a 64 GB card, formatted
		memSize  = 8_047     // 8 GB, as Linux reports it
	)
	t := float64(now.Unix())
	st.Errors = nil
	st.Disks = 2
	st.RaidDevicesActive = 2
	st.RaidLevel = "raid1"
	st.RaidState = pb.RaidState_RaidInSync
	st.RaidSyncPercent = 0
	st.RaidSize = raidSize
	st.RaidUsage = min(max(st.RaidUsage, 0), raidSize)
	st.DiskSize = diskSize
	st.DiskUsage = 9_140
	st.MemSize = memSize
	st.MemUsage = int32(2_450 + 180*math.Sin(t/600))
	st.CpuUsagePrc = float32(5 + 3*math.Sin(t/37) + 1.5*math.Sin(t/11))
}

type demoRule int

const (
	// demoRefuse answers cCodeDemo.
	demoRefuse demoRule = iota
	// demoAllow runs the request as usual: it only reads, or only touches
	// this connection's own memory (keys, sessions, tokens).
	demoAllow
	// demoAck answers ok and stores nothing: requests every app sends by
	// itself after signing in (its language, its push token, notifications
	// seen), which would otherwise mix up visitors or send them the demo's
	// pushes.
	demoAck
)

func demoReq(p any) reflect.Type { return reflect.TypeOf(p) }

var demoRules = map[reflect.Type]demoRule{
	// Signing in and staying signed in.
	demoReq(&pb.ReqEnvelope_ReqGetPubKey{}):          demoAllow,
	demoReq(&pb.ReqEnvelope_ReqAuth{}):               demoAllow, // only with the owner password set: see demoGate
	demoReq(&pb.ReqEnvelope_ReqAuthWithToken{}):      demoAllow,
	demoReq(&pb.ReqEnvelope_ReqIssueSessionToken{}):  demoAllow,
	demoReq(&pb.ReqEnvelope_ReqRevokeSessionToken{}): demoAllow,
	demoReq(&pb.ReqEnvelope_ReqBridgeClientInfo{}):   demoAllow,
	demoReq(&pb.ReqEnvelope_ReqGetStatus{}):          demoAllow,
	demoReq(&pb.ReqEnvelope_ReqGetSettings{}):        demoAllow,
	demoReq(&pb.ReqEnvelope_ReqGetInstanceRole{}):    demoAllow,
	demoReq(&pb.ReqEnvelope_ReqGetProfile{}):         demoAllow,
	demoReq(&pb.ReqEnvelope_ReqGetStaticAsset{}):     demoAllow,
	demoReq(&pb.ReqEnvelope_ReqGetVapidPublicKey{}):  demoAllow,

	// Files and photos.
	demoReq(&pb.ReqEnvelope_ReqListFiles{}):          demoAllow,
	demoReq(&pb.ReqEnvelope_ReqGetFile{}):            demoAllow,
	demoReq(&pb.ReqEnvelope_ReqReadFile{}):           demoAllow,
	demoReq(&pb.ReqEnvelope_ReqGetFileInfo{}):        demoAllow,
	demoReq(&pb.ReqEnvelope_ReqHasFile{}):            demoAllow,
	demoReq(&pb.ReqEnvelope_ReqHasCloudIds{}):        demoAllow,
	demoReq(&pb.ReqEnvelope_ReqHasHashes{}):          demoAllow,
	demoReq(&pb.ReqEnvelope_ReqListFileVersions{}):   demoAllow,
	demoReq(&pb.ReqEnvelope_ReqListOutOfImages{}):    demoAllow,
	demoReq(&pb.ReqEnvelope_ReqGetFolderUsage{}):     demoAllow,
	demoReq(&pb.ReqEnvelope_ReqSearchFiles{}):        demoAllow,
	demoReq(&pb.ReqEnvelope_ReqSearchPhotos{}):       demoAllow,
	demoReq(&pb.ReqEnvelope_ReqGetTags{}):            demoAllow,
	demoReq(&pb.ReqEnvelope_ReqGetThumbnails{}):      demoAllow,
	demoReq(&pb.ReqEnvelope_ReqPhotoDateBuckets{}):   demoAllow,
	demoReq(&pb.ReqEnvelope_ReqGetPhotoPlaces{}):     demoAllow,
	demoReq(&pb.ReqEnvelope_ReqGetMediaUrl{}):        demoAllow,
	demoReq(&pb.ReqEnvelope_ReqGetMediaRange{}):      demoAllow,
	demoReq(&pb.ReqEnvelope_ReqListPeople{}):         demoAllow,
	demoReq(&pb.ReqEnvelope_ReqListImageGroups{}):    demoAllow,
	demoReq(&pb.ReqEnvelope_ReqGetReprocessStatus{}): demoAllow,

	// Social, sharing and notifications, to read.
	demoReq(&pb.ReqEnvelope_ReqGetSocialPublications{}):     demoAllow,
	demoReq(&pb.ReqEnvelope_ReqGetSocialPublicationFiles{}): demoAllow,
	demoReq(&pb.ReqEnvelope_ReqGetPublicationMedia{}):       demoAllow,
	demoReq(&pb.ReqEnvelope_ReqGetPublication{}):            demoAllow,
	demoReq(&pb.ReqEnvelope_ReqGetPublicationLikers{}):      demoAllow,
	demoReq(&pb.ReqEnvelope_ReqGetCommentLikers{}):          demoAllow,
	demoReq(&pb.ReqEnvelope_ReqGetEvents{}):                 demoAllow,
	demoReq(&pb.ReqEnvelope_ReqFriendshipsList{}):           demoAllow,
	demoReq(&pb.ReqEnvelope_ReqGetFriendshipStatus{}):       demoAllow,
	demoReq(&pb.ReqEnvelope_ReqDidSendFriendshipReq{}):      demoAllow,
	demoReq(&pb.ReqEnvelope_ReqAuthAsFriend{}):              demoAllow,
	demoReq(&pb.ReqEnvelope_ReqListSharedLinks{}):           demoAllow,
	demoReq(&pb.ReqEnvelope_ReqPreviewSharedGallery{}):      demoAllow,
	demoReq(&pb.ReqEnvelope_ReqGetSharedGalleryJob{}):       demoAllow,
	demoReq(&pb.ReqEnvelope_ReqOpenSharedGallery{}):         demoAllow,
	demoReq(&pb.ReqEnvelope_ReqGetSharedGalleryItem{}):      demoAllow,
	demoReq(&pb.ReqEnvelope_ReqGetSharedGalleryStream{}):    demoAllow,
	demoReq(&pb.ReqEnvelope_ReqDownloadSharedLink{}):        demoAllow,
	demoReq(&pb.ReqEnvelope_ReqListNotifications{}):         demoAllow,
	demoReq(&pb.ReqEnvelope_ReqGetNotificationCount{}):      demoAllow,
	demoReq(&pb.ReqEnvelope_ReqListUsers{}):                 demoAllow,
	demoReq(&pb.ReqEnvelope_ReqGetUserMetrics{}):            demoAllow,

	// Sent by the apps on their own: answered, never stored.
	demoReq(&pb.ReqEnvelope_ReqSetLanguage{}):                   demoAck,
	demoReq(&pb.ReqEnvelope_ReqRegisterWebPush{}):               demoAck,
	demoReq(&pb.ReqEnvelope_ReqUnregisterWebPush{}):             demoAck,
	demoReq(&pb.ReqEnvelope_ReqRegisterApnsToken{}):             demoAck,
	demoReq(&pb.ReqEnvelope_ReqUnregisterApnsToken{}):           demoAck,
	demoReq(&pb.ReqEnvelope_ReqRegisterFcmToken{}):              demoAck,
	demoReq(&pb.ReqEnvelope_ReqUnregisterFcmToken{}):            demoAck,
	demoReq(&pb.ReqEnvelope_ReqMarkNotificationsAcknowledged{}): demoAck,

	// Settings shows every section on a demo device, so what they read is
	// answered: the bridge (without the server's own address, see
	// GetBridgeAccess), Tailscale, the update check (kept for a while,
	// demoGate, so visitors don't each reach GitHub), and no disks or
	// Wi-Fi networks to set up (demoGate), and Settings > Wi-Fi on a cable
	// (demoGate), whose change is refused.
	demoReq(&pb.ReqEnvelope_ReqGetBridgeAccess{}):    demoAllow,
	demoReq(&pb.ReqEnvelope_ReqGetTailscaleStatus{}): demoAllow,
	demoReq(&pb.ReqEnvelope_ReqCheckUpdate{}):        demoAllow,
	demoReq(&pb.ReqEnvelope_ReqListStorageDevices{}): demoAllow,
	demoReq(&pb.ReqEnvelope_ReqListWifiNetworks{}):   demoAllow,
	demoReq(&pb.ReqEnvelope_ReqGetWifiStatus{}):      demoAllow,

	// Refused, listed so the test can tell them from requests nobody has
	// classified yet. The logs would show visitors' addresses;
	// GetLocalEndpoint is answered as if there were no home network
	// (demoGate).
	demoReq(&pb.ReqEnvelope_ReqGetLocalEndpoint{}):          demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqGetLogs{}):                   demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqUploadFile{}):                demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqBeginUpload{}):               demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqUploadChunk{}):               demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqFinishUpload{}):              demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqLinkFile{}):                  demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqDelFile{}):                   demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqSetUploadOnly{}):             demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqSetOutOfImages{}):            demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqRenamePerson{}):              demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqDeletePerson{}):              demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqMergePeople{}):               demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqRemoveFromPerson{}):          demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqStartReprocess{}):            demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqStopReprocess{}):             demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqCreateImageGroup{}):          demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqAddToImageGroup{}):           demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqRemoveFromImageGroup{}):      demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqRenameImageGroup{}):          demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqDeleteImageGroup{}):          demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqNewSocialPublication{}):      demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqDelSocialPublication{}):      demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqNewSocialComment{}):          demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqDelSocialComment{}):          demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqLikePublication{}):           demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqLikeComment{}):               demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqFriendshipRequest{}):         demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqFriendshipInterRequest{}):    demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqFriendshipInterDelete{}):     demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqChangeFriendStatus{}):        demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqDeleteFriendship{}):          demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqShareFilesLink{}):            demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqCreateSharedGallery{}):       demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqDeleteSharedLink{}):          demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqChangeKey{}):                 demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqSetSettings{}):               demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqSetProfile{}):                demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqSetFaceRecognitionEnabled{}): demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqSetImageTaggingEnabled{}):    demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqSetPlacesEnabled{}):          demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqSetSocialStorageLimit{}):     demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqCreateUser{}):                demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqDeleteUser{}):                demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqSetUserActive{}):             demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqApplyUpdate{}):               demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqRestartDevice{}):             demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqSetupStorage{}):              demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqUseMirrorDisk{}):             demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqSetWifi{}):                   demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqJoinWifi{}):                  demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqSetupTailscale{}):            demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqBridgeSignIn{}):              demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqEnableBridge{}):              demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqDisableBridge{}):             demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqSetBridgeSecret{}):           demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqRegenerateBridgeSecret{}):    demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqSendLogs{}):                  demoRefuse,
	// Meant for the bridge, never a device.
	demoReq(&pb.ReqEnvelope_ReqBridgeRegister{}):          demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqRotateBridgeSecret{}):      demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqUpdatePushRegistrations{}): demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqBridgeNotify{}):            demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqBridgeReleaseDomain{}):     demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqBridgeSendLogs{}):          demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqSetDeviceDisabled{}):       demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqIsDomainAvailable{}):       demoRefuse,
	demoReq(&pb.ReqEnvelope_ReqRegisterUserDomain{}):      demoRefuse,
}

// demoLanguage is Settings.language: absent on a demo device.
func demoLanguage(stored string) *string {
	if demoMode {
		return nil
	}
	return &stored
}

// cDemoUpdateFor is how long a demo device keeps its answer to the update
// check, per language.
const cDemoUpdateFor = 10 * time.Minute

var (
	demoUpdateMu    sync.Mutex
	demoUpdateCache = map[string]demoUpdateEntry{}
	// buildDemoUpdateInfo is buildUpdateInfo; the tests stand in for it.
	buildDemoUpdateInfo = buildUpdateInfo
)

type demoUpdateEntry struct {
	at   time.Time
	info *pb.RespUpdateInfo
}

// demoUpdateInfo is the update check on a demo device: the same answer, kept
// for cDemoUpdateFor, so a page of visitors opening Settings doesn't each
// fetch the release manifest.
func demoUpdateInfo(lang string, now time.Time) *pb.RespUpdateInfo {
	demoUpdateMu.Lock()
	defer demoUpdateMu.Unlock()
	if e, ok := demoUpdateCache[lang]; ok && now.Sub(e.at) < cDemoUpdateFor {
		return e.info
	}
	info := buildDemoUpdateInfo(lang)
	demoUpdateCache[lang] = demoUpdateEntry{at: now, info: info}
	return info
}

// demoGate answers, on a demo device, a request the device must not run as
// usual (handled true); for the rest it returns handled false and the
// request goes on to the usual switches.
func (ch *connHandler) demoGate(env *pb.ReqEnvelope, lang string) (resp *pb.RespEnvelope, handled bool) {
	switch env.Payload.(type) {
	case *pb.ReqEnvelope_ReqAuth:
		// The first password sets the owner password: on a public device
		// that would be the first visitor's. Set it beforehand.
		defined, err := ch.mg.dao.IsSecretDefined()
		if err != nil || !defined {
			return demoRefusal(env.Id, lang), true
		}
		return nil, false
	case *pb.ReqEnvelope_ReqListStorageDevices:
		return &pb.RespEnvelope{Id: env.Id, Payload: &pb.RespEnvelope_RespStorageDevices{RespStorageDevices: &pb.StorageDevices{}}}, true
	case *pb.ReqEnvelope_ReqListWifiNetworks:
		return &pb.RespEnvelope{Id: env.Id, Payload: &pb.RespEnvelope_RespWifiNetworks{RespWifiNetworks: &pb.WifiNetworks{}}}, true
	case *pb.ReqEnvelope_ReqGetWifiStatus:
		// Settings > Wi-Fi shows, as on a device on a cable; changing it
		// is refused (demoRules). Signed in only, like every Settings
		// read: without a session the usual refusal stands.
		if ch.getSession() == nil {
			return nil, false
		}
		return &pb.RespEnvelope{Id: env.Id, Payload: &pb.RespEnvelope_RespWifiStatus{RespWifiStatus: &pb.WifiStatus{Managed: true, Wired: true}}}, true
	case *pb.ReqEnvelope_ReqCheckUpdate:
		return &pb.RespEnvelope{Id: env.Id, Payload: &pb.RespEnvelope_RespUpdateInfo{RespUpdateInfo: demoUpdateInfo(lang, time.Now())}}, true
	case *pb.ReqEnvelope_ReqGetLocalEndpoint:
		// No home network: the apps forget any endpoint they hold.
		return &pb.RespEnvelope{
			Id:           env.Id,
			Error:        true,
			ErrorCode:    cCodeLocalUnavailable,
			ErrorMessage: i18n.DevErrDemo().Render(lang),
		}, true
	}
	switch demoRules[reflect.TypeOf(env.Payload)] {
	case demoAllow:
		return nil, false
	case demoAck:
		return &pb.RespEnvelope{
			Id:      env.Id,
			Payload: &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}},
		}, true
	default:
		return demoRefusal(env.Id, lang), true
	}
}

// demoRefusal is cCodeDemo both on the envelope and as an Ack, like
// notAuthenticatedResponse: requests answered with an Ack are read there by
// the apps, the rest on the envelope.
func demoRefusal(id int32, lang string) *pb.RespEnvelope {
	msg := i18n.DevErrDemo().Render(lang)
	return &pb.RespEnvelope{
		Id:           id,
		Error:        true,
		ErrorCode:    cCodeDemo,
		ErrorMessage: msg,
		Payload: &pb.RespEnvelope_RespAck{
			RespAck: &pb.Ack{Ok: false, ErrorMsg: msg, Code: cCodeDemo},
		},
	}
}
