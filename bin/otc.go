// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"fmt"
	"github.com/alonsovidales/otc/api"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/files_manager"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/session"
	"github.com/alonsovidales/otc/supervisor"
	"github.com/alonsovidales/otc/websocket"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
)

// initOwnerPassword sets the device's owner password from stdin, once:
// what the first sign-in does (session.New derives the key from it and
// stores the validator), run by install.sh with the password the setup
// wizard collected. It lives in this file on purpose: install.sh and the
// updater build the device with `go build ./bin/otc.go`, which compiles
// this one file only - a second file in bin/ is invisible to them.
func initOwnerPassword(d *dao.Dao) int {
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(os.Stderr, "init-owner-password: no password on stdin")
		return 2
	}
	pw := strings.TrimRight(line, "\r\n")
	if len(pw) < 8 {
		fmt.Fprintln(os.Stderr, "init-owner-password: the password must have 8 characters or more")
		return 2
	}
	defined, err := d.IsSecretDefined()
	if err != nil {
		fmt.Fprintln(os.Stderr, "init-owner-password:", err)
		return 1
	}
	if defined {
		fmt.Println("init-owner-password: this device already has a password - left as it is")
		return 0
	}
	if _, err := session.New("setup", pw, true, d); err != nil {
		fmt.Fprintln(os.Stderr, "init-owner-password:", err)
		return 1
	}
	fmt.Println("init-owner-password: owner password set")
	return 0
}

// setMemoryLimit gives the Go runtime a soft limit of 40% of the machine's
// memory, unless GOMEMLIMIT already sets one. Without it the collector lets
// the heap grow to twice what is live before collecting, which on an 8 GB
// Raspberry Pi running a second user's process, MariaDB and the ML models
// ended in the kernel killing the service. It lives in this file because
// install.sh and the updater build ./bin/otc.go alone.
func setMemoryLimit() {
	if os.Getenv("GOMEMLIMIT") != "" {
		return
	}
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			if kb, err := strconv.ParseInt(fields[1], 10, 64); err == nil && kb > 0 {
				debug.SetMemoryLimit(kb << 10 * 2 / 5)
			}
			return
		}
	}
}

func main() {
	setMemoryLimit()
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

	// `otc <env> init-owner-password` (the password on stdin): the setup
	// wizard's password, set before the service first starts - the same as
	// the first sign-in would, with nothing else loaded. A password that is
	// already set (a recovered device) is left alone.
	if len(os.Args) > 2 && os.Args[2] == "init-owner-password" {
		os.Exit(initOwnerPassword(dao))
	}

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
