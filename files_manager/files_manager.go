package filesmanager

import (
	//"github.com/alonsovidales/otc/cfg"
	//"github.com/alonsovidales/otc/log"
	"fmt"
	"net/http"
)

const (
	// CRegenerateGroupKey Endpoint used to regenerate the security key for
	// a shard
	CGet = "/get"
)

// Manager Structure that provides HTTP access to manage all the different
// groups and shards on each grorup
type Manager struct {
	baseUrl string
}

func Init(baseUrl string) (mg *Manager) {
	mg = &Manager{
		baseUrl: baseUrl,
	}

	return
}

// RegenerateGroupKey Creates a new random key for a group
func (mg *Manager) Get(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")

	file := r.FormValue("file")

	w.WriteHeader(200)
	w.Write([]byte(fmt.Sprintf("Good : %s", file)))
}
