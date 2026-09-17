// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"github.com/alonsovidales/otc/api"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/files_manager"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/supervisor"
	"github.com/alonsovidales/otc/websocket"
	"os"
	"os/signal"
	"runtime"
	"syscall"
)

func main() {
	env := "dev"
	if len(os.Args) > 1 {
		env = os.Args[1]
		cfg.Init("otc", env)

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

	// Issue #82: multiple OTC "users" on one device. A spawned child has
	// OTC_SUPERVISED_CHILD set in its own environment (see
	// supervisor.ChildEnvVar) - only the primary instance (that var
	// unset) ever starts a supervisor of its own, which is what actually
	// caps the fork/exec recursion at one level. Built before
	// websocket.Init since the Manager holds a reference to it (nil on a
	// child), used to gate every user-management RPC.
	var sup *supervisor.Supervisor
	if os.Getenv(supervisor.ChildEnvVar) == "" {
		exePath, err := os.Executable()
		if err != nil {
			log.Error("could not resolve own executable path, multi-user supervision disabled:", err)
		} else {
			sup = supervisor.Start(dao, exePath, env)
		}
	}

	webSocket := websocket.Init(cfg.GetStr("otc-api", "base-url"), dao, filesManager, sup, cfg.GetStr("otc-api", "static"))

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
	if sup != nil {
		sup.StopAll()
	}
	dao.Stop()
}
