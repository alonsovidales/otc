// SPDX-License-Identifier: AGPL-3.0-or-later

// Package supervisor is issue #82's answer to "run multiple OTC instances
// on one device": rather than turning the single-owner otc process into a
// shared multi-tenant one (a much larger, riskier rewrite touching every
// unscoped table - settings/profile/vault/social_* all assume exactly one
// row), the one systemd-managed process (the PRIMARY instance, unchanged)
// forks/execs the very same otc binary as plain OS child processes, one
// per additional "user" - each with its own port, its own MySQL database,
// its own storage directory, found via its own ordinary ini file. This
// package owns spawning them at startup, respawning one if it dies (so
// long as its owning user is still active), and stopping them cleanly on
// request (delete/deactivate) or on the primary's own shutdown.
//
// A child never runs this package's Start itself - see bin/otc.go, which
// only calls it when OTC_SUPERVISED_CHILD is unset. That's what actually
// caps the recursion at one level: a spawned child has that env var set,
// so even if it somehow tried to run the supervisor, main() skips it.
package supervisor

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/alonsovidales/otc/alerttext"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/modelserver"
)

// ChildEnvVar, when set in a process's own environment, marks it as one
// of these spawned children rather than the primary - checked by
// bin/otc.go (skip starting a supervisor of its own) and by every
// user-management websocket RPC (refuse to act - see websocket.go).
const ChildEnvVar = "OTC_SUPERVISED_CHILD"

const (
	// respawnDelay is the pause between a child's exit and the next
	// attempt to start it again - same "a few seconds, not a hot loop"
	// spirit as SocialFeedViewModel's own retry convention on the iOS
	// side, just server-side here. It doubles with every exit in a row
	// (respawnBackoff), up to maxRespawnDelay.
	respawnDelay = 4 * time.Second
	// maxRespawnDelay is the longest pause between two starts of a child
	// that keeps stopping: one that always fails was started again every
	// four seconds forever, its config rewritten on the SD card each time.
	maxRespawnDelay = 10 * time.Minute
	// stableUptime is how long a child must have run for its next exit to
	// count as the first of a new series rather than one more in a row.
	stableUptime = 10 * time.Minute
	// alertAfter is how many exits in a row are alerted, once a series.
	alertAfter = 5
	// stopGracePeriod is how long Stop waits for SIGTERM to actually end
	// a process before giving up on a clean wait (it does not escalate to
	// SIGKILL itself - see Stop's doc comment).
	stopGracePeriod = 10 * time.Second
)

type managed struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	exited  chan struct{} // closed once cmd.Wait() returns, whatever the reason
	stopped bool          // Stop() was called - the respawn loop must not restart it
	// wake ends a pause between two starts (Stop), so a user deleted
	// while its instance waits out a long pause is let go at once.
	wake     chan struct{}
	wakeOnce sync.Once
}

// respawnBackoff paces the starts of one user's instance: the pause after
// an exit (or a start that failed) doubles with each one in a row, from
// respawnDelay up to maxRespawnDelay, and a run of stableUptime or more
// starts the count again. The alertAfter-th in a row is the one the owner
// hears about, once a series.
type respawnBackoff struct {
	failures int
	alerted  bool
}

// next counts an end of the instance after it had run for up (0 for a
// start that failed), and says how long to wait before starting it again
// and whether to raise the alert now.
func (b *respawnBackoff) next(up time.Duration) (wait time.Duration, alert bool) {
	if up >= stableUptime {
		b.failures, b.alerted = 0, false
	}
	b.failures++
	wait = respawnDelay
	for i := 1; i < b.failures && wait < maxRespawnDelay; i++ {
		wait *= 2
	}
	wait = min(wait, maxRespawnDelay)
	if b.failures >= alertAfter && !b.alerted {
		b.alerted = true
		alert = true
	}
	return wait, alert
}

type Supervisor struct {
	dao        *dao.Dao
	exePath    string
	primaryEnv string

	mu    sync.Mutex
	procs map[string]*managed // uuid -> managed child
}

// Start reads every currently-active user and spawns one respawn-loop
// goroutine per user. Safe to call only once per process (the primary's
// own bin/otc.go does this exactly once, right after api.Init).
func Start(d *dao.Dao, exePath, primaryEnv string) *Supervisor {
	s := &Supervisor{
		dao:        d,
		exePath:    exePath,
		primaryEnv: primaryEnv,
		procs:      make(map[string]*managed),
	}

	users, err := d.ListActiveUsersInternal()
	if err != nil {
		log.Error("supervisor: could not list active users at startup:", err)
		return s
	}
	for _, u := range users {
		s.SpawnNow(u)
	}
	return s
}

// SpawnNow starts (or, if already running, does nothing to) one user's
// respawn loop - called both by Start for every already-active user and
// immediately after a brand new user is provisioned, so the admin sees it
// come up live rather than waiting for the next full restart.
func (s *Supervisor) SpawnNow(u *dao.UserInternal) {
	s.mu.Lock()
	if _, exists := s.procs[u.Uuid]; exists {
		s.mu.Unlock()
		return
	}
	m := &managed{exited: make(chan struct{}), wake: make(chan struct{})}
	s.procs[u.Uuid] = m
	s.mu.Unlock()

	go s.runLoop(u.Uuid, m)
}

// runLoop is the respawn loop for exactly one user: render its config
// (idempotent - safe to re-render on every (re)spawn, e.g. after a
// primary upgrade changed a shared value like bridge-addr), start it,
// wait for it to exit, and - unless Stop() was called or the user is no
// longer active - try again after respawnDelay. Re-checks `active` from
// the DB on every iteration (not just once at the top) so a delete/
// deactivate that happens mid-crash-loop stops the retries promptly.
func (s *Supervisor) runLoop(uuid string, m *managed) {
	var backoff respawnBackoff
	// pause counts one more end in a row (after up of running) and waits
	// before the next start, alerting the owner once a series.
	pause := func(username string, up time.Duration) {
		wait, alert := backoff.next(up)
		if alert {
			s.alertKeepsStopping(username, backoff.failures)
		}
		if backoff.failures > 1 {
			log.Info("supervisor:", username, "stopped", backoff.failures, "times in a row, starting it again in", wait)
		}
		select {
		case <-time.After(wait):
		case <-m.wake:
		}
	}
	for {
		m.mu.Lock()
		if m.stopped {
			m.mu.Unlock()
			s.forget(uuid)
			return
		}
		m.mu.Unlock()

		u, err := s.dao.GetUserInternal(uuid)
		if err != nil {
			log.Error("supervisor: user", uuid, "vanished, stopping its loop:", err)
			s.forget(uuid)
			return
		}
		if !u.Active {
			log.Info("supervisor: user", u.Username, "is no longer active, stopping its loop")
			s.forget(uuid)
			return
		}

		env := s.primaryEnv + "_" + u.Username
		if err := renderUserConfig(paramsFor(u, env)); err != nil {
			log.Error("supervisor: could not render config for", u.Username, ":", err)
			pause(u.Username, 0)
			continue
		}

		cmd := exec.Command(s.exePath, env)
		cmd.Dir = UserHome(u.Uuid)
		// Issue #167: and the primary's model socket, so the child uses the
		// models loaded here instead of loading its own copies.
		cmd.Env = append(os.Environ(), ChildEnvVar+"="+u.Uuid, modelserver.EnvSocket+"="+modelserver.SocketPath())
		// Each child logs to its own file via its own [logger] config
		// (see renderUserConfig) once it gets that far - this crash log
		// only ever catches a startup failure from before that point
		// (e.g. the binary itself failing to exec, or a panic before
		// cfg.Init runs), truncated on every attempt so it never grows
		// unbounded.
		crashLog, logErr := os.Create(UserHome(u.Uuid) + "/last-crash.log")
		if logErr == nil {
			cmd.Stdout = crashLog
			cmd.Stderr = crashLog
		}

		m.mu.Lock()
		if m.stopped {
			m.mu.Unlock()
			if crashLog != nil {
				crashLog.Close()
			}
			s.forget(uuid)
			return
		}
		if err := cmd.Start(); err != nil {
			m.mu.Unlock()
			log.Error("supervisor: could not start", u.Username, ":", err)
			if crashLog != nil {
				crashLog.Close()
			}
			pause(u.Username, 0)
			continue
		}
		m.cmd = cmd
		m.mu.Unlock()

		log.Info("supervisor: started", u.Username, "pid", cmd.Process.Pid, "port", u.Port)
		startedAt := time.Now()
		err = cmd.Wait()
		log.Info("supervisor:", u.Username, "exited:", err)
		if crashLog != nil {
			crashLog.Close()
		}

		m.mu.Lock()
		m.cmd = nil
		stopped := m.stopped
		m.mu.Unlock()
		if stopped {
			s.forget(uuid)
			return
		}

		pause(u.Username, time.Since(startedAt))
	}
}

// alertKeepsStopping tells the owner, in Alerts, that username's instance
// has stopped failures times in a row.
func (s *Supervisor) alertKeepsStopping(username string, failures int) {
	log.Error("supervisor:", username, "stopped", failures, "times in a row")
	if s.dao == nil {
		return
	}
	a := alerttext.New(i18n.DevAlertUserKeepsStopping(username, failures), i18n.DevAlertUserKeepsStoppingDetail())
	if err := s.dao.AddErrorNotification(a); err != nil {
		log.Error("supervisor: could not record the alert:", err)
	}
}

func (s *Supervisor) forget(uuid string) {
	s.mu.Lock()
	if m, ok := s.procs[uuid]; ok {
		close(m.exited)
		delete(s.procs, uuid)
	}
	s.mu.Unlock()
}

// Stop asks one user's process to exit (SIGTERM) and blocks until it
// actually does (or stopGracePeriod elapses) - used before anything that
// must not race a still-running process, i.e. dropping its database or
// deleting its storage directory. Marks the loop stopped first so it
// won't respawn out from under the caller. Deliberately does not escalate
// to SIGKILL: a process that ignores SIGTERM for 10s is left running and
// the caller gets an error back instead of silently killing it mid-write.
func (s *Supervisor) Stop(uuid string) error {
	s.mu.Lock()
	m, exists := s.procs[uuid]
	s.mu.Unlock()
	if !exists {
		return nil // nothing running - already stopped/never started, fine
	}

	m.mu.Lock()
	m.stopped = true
	cmd := m.cmd
	m.mu.Unlock()
	// Out of a pause between two starts, if it is in one.
	m.wakeOnce.Do(func() { close(m.wake) })

	if cmd != nil && cmd.Process != nil {
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			log.Error("supervisor: SIGTERM to", uuid, "failed:", err)
		}
	}

	select {
	case <-m.exited:
		return nil
	case <-time.After(stopGracePeriod):
		return errTimeout
	}
}

// StopAll signals every managed child and waits (briefly, best-effort)
// for them to exit - called from bin/otc.go's own shutdown path, right
// before dao.Stop().
func (s *Supervisor) StopAll() {
	s.mu.Lock()
	uuids := make([]string, 0, len(s.procs))
	for uuid := range s.procs {
		uuids = append(uuids, uuid)
	}
	s.mu.Unlock()

	var wg sync.WaitGroup
	for _, uuid := range uuids {
		wg.Add(1)
		go func(uuid string) {
			defer wg.Done()
			if err := s.Stop(uuid); err != nil {
				log.Error("supervisor: shutdown wait for", uuid, "failed:", err)
			}
		}(uuid)
	}
	wg.Wait()
}

func paramsFor(u *dao.UserInternal, env string) renderParams {
	return renderParams{
		Uuid:             u.Uuid,
		Username:         u.Username,
		Env:              env,
		Port:             u.Port,
		DbName:           u.DbName,
		DbUser:           u.DbName, // one dedicated MySQL user per database, same name as the database itself
		DbPass:           u.DbPass,
		StoragePath:      u.StoragePath + "/",
		UnencStoragePath: u.StoragePath + "/unencrypted/",
		SupervisorToken:  u.SupervisorToken,
		BridgeAccess:     u.BridgeAccess,
	}
}

var errTimeout = errors.New("timed out waiting for process to exit")
