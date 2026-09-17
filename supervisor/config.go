// SPDX-License-Identifier: AGPL-3.0-or-later

package supervisor

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/alonsovidales/otc/cfg"
)

// UserHome is where a user's own writable state lives - config, nothing
// else (storage lives directly under [otc] storage-path, same convention
// as the primary). Under /var/lib/otc (systemd's StateDirectory for this
// unit), which stays writable even under the primary's own
// ProtectSystem=full - /etc itself does not.
func UserHome(uuid string) string {
	return filepath.Join("/var/lib/otc/users", uuid)
}

// renderUserConfig writes a brand new user's own /etc-equivalent ini,
// reusing every value the primary's own already-loaded config has for
// machine-wide, shared things (bridge address, tagger/face model paths -
// these are big, shared, read-only model files, not per-user) and
// overriding only what must be unique per user: API port, MySQL
// database/credentials, and storage paths. A Go port of the same ini
// template scripts/install.sh and Makefile.pi's `config` target already
// render by hand for the very first device - kept in sync manually since
// there's no single shared source for either today.
func renderUserConfig(u renderParams) error {
	dir := filepath.Join(UserHome(u.Uuid), "etc")
	if err := os.MkdirAll(dir, 0750); err != nil {
		return err
	}

	content := fmt.Sprintf(`[otc]
bridge-addr=%s
storage-path=%s
unenc-storage-path=%s
max-thumbnail-width-px=%s
shared-link-ttl-hours=%s
supervisor-token=%s

[logger]
log_file=/var/log/otc/otc_%s.log
max_log_size_mb=%s
level=%s

[otc-api]
base-url=%s
static=%s
port=%d
ssl-port=%s
ssl-cert=%s
ssl-key=%s

[mysql]
user=%s
pass=%s
port=%s
db=%s

[tagger]
model-path=%s
tags-path=%s
thresholds-path=%s
tags-per-image=%s
max-images-search=%s
`,
		cfg.GetStr("otc", "bridge-addr"),
		u.StoragePath,
		u.UnencStoragePath,
		cfg.GetStr("otc", "max-thumbnail-width-px"),
		cfg.GetStr("otc", "shared-link-ttl-hours"),
		u.SupervisorToken,

		u.Username,
		cfg.GetStr("logger", "max_log_size_mb"),
		cfg.GetStr("logger", "level"),

		cfg.GetStr("otc-api", "base-url"),
		cfg.GetStr("otc-api", "static"),
		u.Port,
		cfg.GetStr("otc-api", "ssl-port"),
		cfg.GetStr("otc-api", "ssl-cert"),
		cfg.GetStr("otc-api", "ssl-key"),

		u.DbUser,
		u.DbPass,
		cfg.GetStr("mysql", "port"),
		u.DbName,

		cfg.GetStr("tagger", "model-path"),
		cfg.GetStr("tagger", "tags-path"),
		cfg.GetStr("tagger", "thresholds-path"),
		cfg.GetStr("tagger", "tags-per-image"),
		cfg.GetStr("tagger", "max-images-search"),
	)

	// [faces] is optional even on the primary (see files_manager's
	// HasSection guard, issue #52/the Cala recovery incident) - only
	// carried over if actually configured there.
	if cfg.HasSection("faces") {
		content += fmt.Sprintf(`
[faces]
detector-model-path=%s
recognizer-model-path=%s
`,
			cfg.GetStr("faces", "detector-model-path"),
			cfg.GetStr("faces", "recognizer-model-path"),
		)
	}

	iniPath := filepath.Join(dir, fmt.Sprintf("otc_%s.ini", u.Env))
	return os.WriteFile(iniPath, []byte(content), 0640)
}

// renderParams is everything renderUserConfig needs - kept separate from
// dao.User/dao.UserInternal since it also carries values (DbUser/DbPass,
// UnencStoragePath, Env) that only matter at provisioning time and are
// never persisted as their own columns.
type renderParams struct {
	Uuid             string
	Username         string
	Env              string
	Port             int
	DbName           string
	DbUser           string
	DbPass           string
	StoragePath      string
	UnencStoragePath string
	SupervisorToken  string
}
