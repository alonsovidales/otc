package main

import (
	"github.com/alonsovidales/otc/bridge/admin"
	"github.com/alonsovidales/otc/bridge/api"
	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/bridge/websocket"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
	"github.com/google/uuid"
	"os"
	"os/signal"
	"runtime"
	"syscall"
)

func main() {
	if len(os.Args) > 1 {
		cfg.Init("otc", os.Args[1])

		log.SetLogger(
			log.Levels[cfg.GetStr("logger", "level")],
			cfg.GetStr("logger", "log_file"),
			cfg.GetInt("logger", "max_log_size_mb"),
		)
	} else {
		cfg.Init("otc", "dev")
	}
	runtime.GOMAXPROCS(runtime.NumCPU())

	dao := dao.Init()
	adm := admin.Init(dao, sessionSecret())

	// One-off admin bootstrap: `otc_bridge <env> set-admin-password <user> <pass>`
	// sets/changes an admin panel login and exits, rather than starting the
	// server. There's no HTTP endpoint for this on purpose - it should only
	// be settable by whoever already has shell access to the bridge.
	if len(os.Args) > 2 && os.Args[2] == "set-admin-password" {
		if len(os.Args) != 5 {
			log.Fatal("usage: otc_bridge <env> set-admin-password <username> <password>")
		}
		if err := adm.SetPassword(os.Args[3], os.Args[4]); err != nil {
			log.Fatal("error setting admin password:", err)
		}
		log.Info("Admin password set for user:", os.Args[3])
		dao.Stop()
		return
	}

	webSocket := websocket.Init(cfg.GetStr("otc-api", "base-url"), dao)

	api.Init(
		webSocket,
		dao,
		adm,
		cfg.GetStr("otc-api", "static"),
		int(cfg.GetInt("otc-api", "port")),
		int(cfg.GetInt("otc-api", "ssl-port")),
		cfg.GetStr("otc-api", "ssl-cert"),
		cfg.GetStr("otc-api", "ssl-key"))

	log.Info("System started...")
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, os.Kill, syscall.SIGTERM)
	// Block until a signal is received.
	<-c

	log.Info("Stopping all the services")
	dao.Stop()
}

// sessionSecret returns [admin] session-secret from config, so admin panel
// logins survive a restart. If it's not configured, falls back to a
// per-process random value - safe (never predictable), but every restart
// invalidates existing sessions until a persistent secret is set.
func sessionSecret() []byte {
	if s := cfg.GetStr("admin", "session-secret"); s != "" {
		return []byte(s)
	}
	log.Error("[admin] session-secret is not configured - admin panel sessions will not survive a restart. Set one in the config file.")
	return []byte(uuid.New().String())
}
