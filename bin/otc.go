package main

import (
	"github.com/alonsovidales/otc/api"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/files_manager"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/websocket"
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

	filesManager := filesmanager.Init(cfg.GetStr("otc-api", "base-url"), dao)
	webSocket := websocket.Init(cfg.GetStr("otc-api", "base-url"), dao, filesManager)

	api.Init(
		filesManager,
		webSocket,
		dao,
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
	//shardsManager.Stop()
}
