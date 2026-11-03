// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/settings"
	"github.com/alonsovidales/otc/supervisor"
	"github.com/alonsovidales/otc/wsframe"
	gorilla "github.com/gorilla/websocket"
	"golang.org/x/sys/unix"
)

// Issue #207: the device without its storage (package nostorage) answers
// every request - GetPubKey first, before any sign-in - with this
// error_code and dev.err.storage_unavailable in the request's language,
// on its own port and through the bridge, with no database. The one
// exception is the bridge's GetStaticAsset: the web app's files are on
// the boot disk, so the page itself still loads and can say why.
const CodeStorageUnavailable = "storage_unavailable"

// cUnavailablePool is how many connections each identity keeps ready on
// the bridge meanwhile: the apps only ask again every 30 s.
const cUnavailablePool = 8

// dialBridge is wsframe.Dial; tests dial their own bridge.
var dialBridge = wsframe.Dial

// unavailableReply is the answer to env while the storage is missing.
func (mg *Manager) unavailableReply(env *pb.ReqEnvelope, lang string) *pb.RespEnvelope {
	resp := &pb.RespEnvelope{Id: env.Id}
	if p, ok := env.Payload.(*pb.ReqEnvelope_ReqGetStaticAsset); ok && mg.staticPath != "" {
		staticAssetReply(mg.staticPath, p.ReqGetStaticAsset.Path, lang, resp)
		return resp
	}
	resp.Error = true
	resp.ErrorCode = CodeStorageUnavailable
	resp.ErrorMessage = i18n.DevErrStorageUnavailable().Render(lang)
	return resp
}

// cIdentityFile is the copy of an instance's bridge identity - the
// relay, the device's id, its name and its secret, as the database's
// settings row holds them - that the device without its storage registers
// with. In the instance's working directory, on the boot disk (never the
// storage): 0600, the service's own. The full instance rewrites it when a
// registration the bridge took differs from it (a new name, a rotated
// secret, switching the bridge on) and removes it when it leaves the
// bridge or runs without one.
const cIdentityFile = ".bridge-identity.json"

// bridgeIdentity is cIdentityFile's content.
type bridgeIdentity struct {
	V         int    `json:"v"`
	Bridge    string `json:"bridge"`
	OwnerUuid string `json:"owner_uuid"`
	Domain    string `json:"domain"`
	Secret    string `json:"secret"`
}

func (id bridgeIdentity) usable() bool {
	return id.V == 1 && id.Bridge != "" && id.OwnerUuid != "" && id.Domain != "" && id.Secret != ""
}

// keepIdentity writes the identity a registration just used when it isn't
// the one kept (full mode only).
func (mg *Manager) keepIdentity(ownerUuid, domain, secret string) {
	if mg.unavailable {
		return
	}
	mg.identity.mu.Lock()
	defer mg.identity.mu.Unlock()
	if mg.identity.file == "" {
		return
	}
	id := bridgeIdentity{V: 1, Bridge: mg.bridgeAddr(), OwnerUuid: ownerUuid, Domain: domain, Secret: secret}
	if id == mg.identity.saved {
		return
	}
	if old, err := readIdentity(mg.identity.file); err == nil && old == id {
		mg.identity.saved = id
		return
	}
	if err := writeIdentity(mg.identity.file, id); err != nil {
		log.Error("could not keep a copy of the bridge identity for the device without its storage:", err)
		return
	}
	mg.identity.saved = id
}

// dropIdentity removes the copy (leaving the bridge).
func (mg *Manager) dropIdentity() {
	mg.identity.mu.Lock()
	defer mg.identity.mu.Unlock()
	mg.identity.saved = bridgeIdentity{}
	if mg.identity.file != "" {
		if err := os.Remove(mg.identity.file); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Error("could not remove the bridge identity's copy:", err)
		}
	}
}

// forgetIdentity removes the copy in dir (an instance without a bridge).
func forgetIdentity(dir string) {
	if err := os.Remove(filepath.Join(dir, cIdentityFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Error("could not remove the bridge identity's copy:", err)
	}
}

// readIdentity reads a copy: a regular file (never through a link) of at
// most 4 KiB.
func readIdentity(path string) (bridgeIdentity, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return bridgeIdentity{}, err
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		return bridgeIdentity{}, errors.New("not a regular file") // i18n-ignore: a log line
	}
	raw, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil {
		return bridgeIdentity{}, err
	}
	var id bridgeIdentity
	if err := json.Unmarshal(raw, &id); err != nil {
		return bridgeIdentity{}, err
	}
	return id, nil
}

// writeIdentity writes id beside path (0600), flushes it and renames it
// over path.
func writeIdentity(path string, id bridgeIdentity) error {
	raw, err := json.Marshal(id)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), cIdentityFile+".tmp.*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if err = tmp.Chmod(0600); err == nil {
		if _, err = tmp.Write(raw); err == nil {
			err = tmp.Sync()
		}
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		os.Remove(name)
	}
	return err
}

// Unavailable is the device without its storage: one Manager answering
// storage_unavailable for this instance (its port's /ws, and the bridge
// when its identity's copy is there) and, on the main instance, one more
// per user whose instance kept a copy, so their addresses say the same.
type Unavailable struct {
	mgs      []*Manager
	stopNet  chan struct{}
	stopOnce sync.Once
}

// NewUnavailable starts the device without its storage: staticPath is
// the web app's files (served to the bridge's GetStaticAsset), primary
// whether this is the main instance (which also registers its users'
// addresses). Nothing here opens the database or touches the storage.
func NewUnavailable(staticPath string, primary bool) *Unavailable {
	u := &Unavailable{stopNet: make(chan struct{})}
	newManager := func() *Manager {
		return &Manager{
			unavailable: true,
			staticPath:  staticPath,
			poolTarget:  cUnavailablePool,
			upgrader:    gorilla.Upgrader{CheckOrigin: originAllowed},
		}
	}
	local := newManager()
	u.mgs = append(u.mgs, local)
	if !bridgeConfigured() {
		log.Info("no bridge-addr configured: the device without its storage answers on its own port only")
		return u
	}
	bridge := cfg.GetStr("otc", "bridge-addr")
	wd, err := os.Getwd()
	if err != nil {
		log.Error("no working directory, so no bridge identity to register with:", err)
		return u
	}
	wsframe.KeepAddressesIn(filepath.Join(wd, ".bridge-addresses.json"))
	files := []string{filepath.Join(wd, cIdentityFile)}
	if primary {
		users, _ := filepath.Glob(filepath.Join(supervisor.UserHome("*"), cIdentityFile))
		files = append(files, users...)
	}
	for i, file := range files {
		id, err := readIdentity(file)
		if err != nil {
			if i == 0 && !errors.Is(err, os.ErrNotExist) {
				log.Error("the bridge identity's copy can't be read:", err)
			} else if i == 0 {
				log.Info("no copy of the bridge identity yet: the device without its storage answers on its own port only")
			}
			continue
		}
		// Only for the relay [otc] names now: a copy from before a switch
		// of bridge, or a switch-off, says nothing.
		if !id.usable() || id.Bridge != bridge {
			continue
		}
		mg := local
		if i > 0 {
			mg = newManager()
			u.mgs = append(u.mgs, mg)
		}
		mg.bridgeHost = id.Bridge
		mg.settings = settings.Offline(id.OwnerUuid, id.Domain, id.Secret)
		log.Info("registering", id.Domain, "with the bridge, to say the storage can't be reached")
		mg.ensureBridgePool()
		go mg.watchNetwork(u.stopNet)
	}
	return u
}

// Listen serves the instance's own port's /ws.
func (u *Unavailable) Listen(w http.ResponseWriter, r *http.Request) {
	u.mgs[0].Listen(w, r)
}

// Stop closes every connection - the bridge's ready ones and those
// serving someone - and keeps the pools from dialling again, before the
// full instance starts its own.
func (u *Unavailable) Stop() {
	u.stopOnce.Do(func() { close(u.stopNet) })
	for _, mg := range u.mgs {
		mg.closed.Store(true)
		mg.bridgePool.mu.Lock()
		for c := range mg.bridgePool.idle {
			c.Close()
		}
		if mg.bridgePool.wake != nil {
			close(mg.bridgePool.wake)
			mg.bridgePool.wake = nil
		}
		mg.bridgePool.mu.Unlock()
		mg.liveMu.Lock()
		closers := make([]func(), 0, len(mg.live))
		for _, closeConn := range mg.live {
			closers = append(closers, closeConn)
		}
		mg.liveMu.Unlock()
		for _, closeConn := range closers {
			closeConn()
		}
	}
}
