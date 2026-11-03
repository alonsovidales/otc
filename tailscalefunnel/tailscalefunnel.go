// SPDX-License-Identifier: AGPL-3.0-or-later

// Package tailscalefunnel backs issue #80: reaching a device over
// Tailscale Funnel instead of the OTC bridge.
//
// Funnel publishes a local port on the public internet under a
// <machine>.<tailnet>.ts.net name, with a certificate Tailscale manages,
// and without opening anything on the home router - the same problem the
// bridge solves, solved by someone else's infrastructure instead of ours.
//
// It is offered at first setup as an alternative, not a replacement, and
// it costs real things: Funnel serves only ts.net names (so no
// <device>.off-the.cloud), only ports 443, 8443 and 10000, and under
// bandwidth limits Tailscale does not publish. What it buys is a device
// that depends on nothing of ours to be reachable.
//
// tailscaled is the distribution's, installed and updated by it, and the
// node's identity lives with it, so an owner who later wants to manage
// the tailnet by hand finds exactly what they would expect. It runs as
// root, and this package never talks to it (issue #204): releases before
// 168 made the otc user its operator so that this package could drive the
// CLI, which let the service do everything the CLI does - change the
// account or the tailnet, routes and exit nodes, serve, Tailscale SSH -
// when the product needs Funnel on its own web port and nothing else.
// Instead, the root runner (scripts/tailscale-runner) runs the few fixed
// commands Funnel needs, for one word this package writes to
// /var/lib/otc/tailscale.request - on, off or status - and answers in
// /var/lib/otc/tailscale-status.json with the state the setup screen
// shows. An auth key goes to the runner as a file of its own on the
// service's RAM filesystem, which the runner takes before anything else
// and hands to `tailscale up` as a file: never a command line, which every
// account on the machine can read in /proc.
//
// None of it uses sudo, and it must not: otc.service runs with
// NoNewPrivileges=true, which stops any child of this process gaining
// privilege at all - sudo there fails with "the no new privileges flag is
// set" no matter how the sudoers file is written.
package tailscalefunnel

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
)

// The words the runner takes: an allowlist, the only things this package
// can ask of tailscaled.
const (
	cOn     = "on"
	cOff    = "off"
	cStatus = "status"
)

const (
	// cStatusFresh: an answer this recent stands in for a status asked
	// again - Settings asks whenever it shows, the web app at every
	// return to its tab - so a burst runs the runner once.
	cStatusFresh = 2 * time.Second

	// cMaxAnswer bounds what is read of the answer: it is a few hundred
	// bytes.
	cMaxAnswer = 64 << 10
)

// Where the runner is asked and answers, and how long it has (package
// vars: the tests point them at a temporary directory and shorten them).
// The request and the answer are in the service's StateDirectory, as the
// other runners' are; the auth key goes to its RuntimeDirectory, a tmpfs
// emptied whenever the service stops.
var (
	requestPath = "/var/lib/otc/tailscale.request"
	answerPath  = "/var/lib/otc/tailscale-status.json"
	authKeyPath = "/run/otc/tailscale-auth.key"

	// How long each word may take before the device gives up on its
	// answer. on starts tailscaled, may join the tailnet and turns Funnel
	// on (the runner bounds each step, its unit 3 minutes in all); off
	// resets Funnel and stops the daemon; status only reads.
	onWait     = 3*time.Minute + 10*time.Second
	offWait    = 90 * time.Second
	statusWait = 15 * time.Second

	// pollEvery is how often the answer is looked for.
	pollEvery = 100 * time.Millisecond

	// lookPath finds the CLI, only to tell an owner whose device lacks
	// it: nothing here runs it.
	lookPath = exec.LookPath
)

// State is what the setup screen needs to know.
type State struct {
	// Installed is false when the tailscale CLI isn't on this device at
	// all, which is the one problem the owner can't fix from here.
	Installed bool
	// LoggedIn is true once the node has joined a tailnet.
	LoggedIn bool
	// FunnelOn is true when this device is actually being served publicly.
	FunnelOn bool
	// PublicURL is where the device can be reached, once there is one.
	PublicURL string
	// LoginURL is set when a human has to visit a page to authorise this
	// node. There is no way around it without a pre-shared auth key.
	LoginURL string
}

// answer is the runner's tailscale-status.json.
type answer struct {
	State   string          `json:"state"`
	Message string          `json:"message"`
	Code    string          `json:"code"`
	Args    json.RawMessage `json:"args"`
	// Request is the word answered; a runner from before issue #204
	// leaves it out.
	Request   string `json:"request"`
	LoggedIn  bool   `json:"logged_in"`
	FunnelOn  bool   `json:"funnel_on"`
	PublicURL string `json:"public_url"`
	LoginURL  string `json:"login_url"`
}

var (
	// mu: one request to the runner at a time - there is one request
	// file, and one answer.
	mu sync.Mutex
	// last is the state the runner last reported, and when: a status
	// asked while an on or off is under way gets it rather than waiting
	// minutes behind it.
	last atomic.Pointer[seen]
)

type seen struct {
	state State
	at    time.Time
}

// Available reports whether the tailscale CLI exists here.
func Available() bool {
	_, err := lookPath("tailscale")

	return err == nil
}

// Status reports where this device currently stands.
func Status() State {
	if !Available() {
		return State{}
	}
	if s := last.Load(); s != nil && time.Since(s.at) < cStatusFresh {
		return s.state
	}
	if !mu.TryLock() {
		// Funnel is being switched: what the runner last said.
		if s := last.Load(); s != nil {
			return s.state
		}
		return State{Installed: true}
	}
	defer mu.Unlock()

	a, err := ask(cStatus, "", statusWait)
	if err != nil {
		log.Debug("tailscale status:", err)
		return State{Installed: true}
	}
	return a.state()
}

// Enable joins the tailnet if needed and turns Funnel on for the device's
// own HTTP port.
//
// authKey is optional. With one, the whole thing is non-interactive.
// Without, Tailscale needs a human to authorise the node, and the URL to
// do that on comes back in State.LoginURL - there is no way around that,
// so the caller shows it rather than pretending the setup finished.
func Enable(authKey string) (State, error) {
	if !Available() {
		return State{}, devtext.New(i18n.DevStatusTailscaleCliMissing())
	}
	mu.Lock()
	defer mu.Unlock()

	a, err := ask(cOn, strings.TrimSpace(authKey), onWait)
	if err != nil {
		return a.state(), err
	}
	st := a.state()
	switch {
	case st.FunnelOn:
		log.Info("tailscale funnel enabled")
	case st.LoginURL != "":
		log.Info("tailscale: the node waits to be authorised")
	}
	return st, nil
}

// Disable stops serving this device publicly, and tailscaled with it. The
// node stays in the tailnet - leaving that is the owner's business, not
// something a checkbox here should decide.
func Disable() (State, error) {
	if !Available() {
		return State{}, devtext.New(i18n.DevStatusTailscaleCliMissing())
	}
	mu.Lock()
	defer mu.Unlock()

	a, err := ask(cOff, "", offWait)
	if err == nil {
		log.Info("tailscale funnel disabled")
	}
	return a.state(), err
}

// ask hands the runner word (and, for on, the auth key) and waits up to
// wait for its answer. Its error is the runner's failure as it worded it,
// or why there was no answer. Callers hold mu.
func ask(word, authKey string, wait time.Duration) (answer, error) {
	// Never an answer left from a request before this one.
	if err := os.Remove(answerPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Debug("tailscale: could not remove the previous answer:", err)
	}
	// No key left from before goes with this request; the one written
	// here goes once the answer is in, if the runner never took it.
	os.Remove(authKeyPath)
	if authKey != "" {
		if err := writeFile(authKeyPath, authKey); err != nil {
			return answer{}, requestFailed(word, err)
		}
		defer os.Remove(authKeyPath)
	}
	if err := writeFile(requestPath, word); err != nil {
		return answer{}, requestFailed(word, err)
	}

	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		time.Sleep(pollEvery)
		a, ok := readAnswer(word)
		if !ok {
			continue
		}
		last.Store(&seen{state: a.state(), at: time.Now()})
		if a.State == "failed" {
			return a, devtext.TailscaleError(a.Code, a.Args, a.Message)
		}
		return a, nil
	}
	// Not taken in all that time: the runner never started (its path unit
	// isn't there, or failed). Withdrawn, so it can't act on it later -
	// at the next boot, say - when nobody asked any more.
	os.Remove(requestPath)
	switch word {
	case cOn:
		return answer{}, devtext.New(i18n.DevStatusTailscaleTimeoutOn())
	case cOff:
		return answer{}, devtext.New(i18n.DevStatusTailscaleTimeoutOff())
	}
	return answer{}, errors.New("tailscale: no answer from the runner") // i18n-ignore: Status only logs it
}

func requestFailed(word string, err error) error {
	if word == cOff {
		return devtext.Wrap(i18n.DevStatusTailscaleRequestOffFailed(err.Error()), err)
	}
	return devtext.Wrap(i18n.DevStatusTailscaleRequestOnFailed(err.Error()), err)
}

// writeFile puts content at path whole: written to a temporary file of
// this account's beside it (0600) and renamed, so the runner, started as
// soon as the name appears, never reads half of it.
func writeFile(path, content string) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.WriteString(content)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// readAnswer is the runner's answer to word, once it is there.
func readAnswer(word string) (answer, bool) {
	f, err := os.Open(answerPath)
	if err != nil {
		return answer{}, false
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, cMaxAnswer))
	if err != nil {
		return answer{}, false
	}
	var a answer
	if json.Unmarshal(raw, &a) != nil || (a.State != "done" && a.State != "failed") {
		return answer{}, false
	}
	// An answer to another word is a late one to a request before this
	// one. A runner from before issue #204 names none.
	if a.Request != word && a.Request != "" {
		return answer{}, false
	}
	return a, true
}

// state is the answer as the setup screen sees it. The addresses are
// links an app opens: only web ones.
func (a answer) state() State {
	st := State{Installed: true, LoggedIn: a.LoggedIn, FunnelOn: a.FunnelOn}
	if strings.HasPrefix(a.PublicURL, "https://") && !strings.ContainsAny(a.PublicURL, " \t\r\n") {
		st.PublicURL = a.PublicURL
	}
	if (strings.HasPrefix(a.LoginURL, "https://") || strings.HasPrefix(a.LoginURL, "http://")) &&
		!strings.ContainsAny(a.LoginURL, " \t\r\n") {
		st.LoginURL = a.LoginURL
	}
	return st
}
