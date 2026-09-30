// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/alonsovidales/otc/api"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/files_manager"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/session"
	"github.com/alonsovidales/otc/supervisor"
	"github.com/alonsovidales/otc/websocket"
	"io"
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

// initProfile applies what the setup wizard asked about the owner (issue
// #178), all optional: the profile's name, description and picture (an
// already-cropped JPEG, base64), and whether face recognition is on (off
// unless chosen). JSON on stdin; fields left empty keep their defaults.
// In this file for the same reason as initOwnerPassword.
func initProfile(d *dao.Dao) int {
	var in struct {
		Name  string `json:"name"`
		Text  string `json:"text"`
		Image string `json:"image"`
		Faces bool   `json:"faces"`
	}
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&in); err != nil {
		fmt.Fprintln(os.Stderr, "init-profile: bad input:", err)
		return 2
	}
	name, text, image, err := d.GetProfile()
	if err != nil {
		fmt.Fprintln(os.Stderr, "init-profile:", err)
		return 1
	}
	if v := strings.TrimSpace(in.Name); v != "" {
		name = v
	}
	if v := strings.TrimSpace(in.Text); v != "" {
		text = v
	}
	if in.Image != "" {
		raw, err := base64.StdEncoding.DecodeString(in.Image)
		if err != nil || len(raw) < 3 || raw[0] != 0xFF || raw[1] != 0xD8 {
			fmt.Fprintln(os.Stderr, "init-profile: the picture is not a JPEG - left as it is")
		} else {
			image = raw
		}
	}
	if err := d.UpdateProfile(name, text, image); err != nil {
		fmt.Fprintln(os.Stderr, "init-profile:", err)
		return 1
	}
	if err := d.SetFaceRecognitionEnabled(in.Faces); err != nil {
		fmt.Fprintln(os.Stderr, "init-profile:", err)
		return 1
	}
	fmt.Println("init-profile: profile set, face recognition", map[bool]string{true: "on", false: "off"}[in.Faces])
	return 0
}

func main() {
	setMemoryLimit()
	env := "dev"
	if len(os.Args) > 1 {
		env = os.Args[1]
		cfg.Init("otc", env)

		log.SetLogger(
			logLevel(),
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
	// `otc <env> init-profile` (JSON on stdin): the wizard's profile and
	// face-recognition choice, set before the service first starts.
	if len(os.Args) > 2 && os.Args[2] == "init-profile" {
		os.Exit(initProfile(dao))
	}
	lockMemory()
	secureTempDir()

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

// logLevel is [logger] level, any case; an unknown one stops the start
// rather than logging everything (issue #162).
func logLevel() int {
	l, err := log.ParseLevel(cfg.GetStr("logger", "level"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	return l
}
