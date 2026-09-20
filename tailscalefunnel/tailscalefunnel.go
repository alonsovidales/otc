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
// Everything here shells out to the tailscale CLI rather than embedding
// tsnet: the daemon is installed and updated by the distribution, the
// node's identity lives with it, and an owner who later wants to manage
// the tailnet by hand finds exactly what they would expect.
//
// None of it uses sudo, and it must not: otc.service runs with
// NoNewPrivileges=true, which stops any child of this process gaining
// privilege at all - sudo there fails with "the no new privileges flag is
// set" no matter how the sudoers file is written. Tailscale's own answer
// is an operator: `tailscale set --operator=otc`, run once as root by the
// installer, after which this user drives the daemon directly. That is
// also the better outcome - the device never needs a path to root.
package tailscalefunnel

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/alonsovidales/otc/log"
)

const (
	// cLocalPort is what Funnel is pointed at - the device's own plain
	// HTTP server. Tailscale terminates TLS itself, so the device needs
	// no certificate of its own on this path.
	cLocalPort = 8080

	// cPublicPort is Funnel's own listener. It only accepts 443, 8443 or
	// 10000, and 443 is the one a browser reaches without a port in the
	// URL.
	cPublicPort = 443

	// cCommandTimeout bounds every CLI call so a wedged daemon can't hang
	// the RPC that triggered it.
	cCommandTimeout = 30 * time.Second

	// cLoginTimeout is how long to wait for `tailscale up` to print a
	// login URL before giving up on it. Short: it prints the URL almost
	// immediately and then blocks waiting for a human, which is the
	// opposite of what an RPC wants to do.
	cLoginTimeout = 10 * time.Second
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

// statusJSON is the subset of `tailscale status --json` this needs.
type statusJSON struct {
	BackendState string `json:"BackendState"`
	AuthURL      string `json:"AuthURL"`
	Self         struct {
		DNSName string `json:"DNSName"`
	} `json:"Self"`
}

var loginURLPattern = regexp.MustCompile(`https://login\.tailscale\.com/\S+`)

// Available reports whether the tailscale CLI exists here.
func Available() bool {
	_, err := exec.LookPath("tailscale")

	return err == nil
}

// Status reports where this device currently stands.
func Status() State {
	state := State{Installed: Available()}
	if !state.Installed {
		return state
	}

	out, err := run("tailscale", "status", "--json")
	if err != nil {
		log.Debug("tailscale status failed:", err)
		return state
	}

	var parsed statusJSON
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		log.Debug("could not parse tailscale status:", err)
		return state
	}

	state.LoggedIn = parsed.BackendState == "Running"
	state.LoginURL = parsed.AuthURL
	// DNSName comes back fully qualified with a trailing dot.
	if host := strings.TrimSuffix(parsed.Self.DNSName, "."); host != "" && state.LoggedIn {
		state.PublicURL = "https://" + host
	}

	if funnelOut, err := run("tailscale", "funnel", "status"); err == nil {
		// The exact shape of this output has changed between releases,
		// so this looks for the port rather than parsing it: all that
		// matters is whether this device is being served publicly.
		state.FunnelOn = strings.Contains(funnelOut, fmt.Sprintf("%d", cLocalPort))
	}

	return state
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
		return State{}, fmt.Errorf("tailscale is not installed on this device")
	}

	state := Status()
	if !state.LoggedIn {
		loginURL, err := joinTailnet(authKey)
		if err != nil {
			return state, err
		}
		if loginURL != "" {
			// Authorisation is still outstanding: report it and stop.
			// Turning Funnel on before the node has joined would just
			// fail.
			state.LoginURL = loginURL
			return state, nil
		}
		state = Status()
	}

	// --bg so it survives reboots rather than needing to be re-run; 443
	// so the public URL carries no port.
	if _, err := run(
		"tailscale", "funnel", "--bg", "--yes",
		fmt.Sprintf("--https=%d", cPublicPort),
		fmt.Sprintf("localhost:%d", cLocalPort),
	); err != nil {
		return state, fmt.Errorf("could not enable Funnel: %w", err)
	}

	log.Info("tailscale funnel enabled on port", cLocalPort)

	return Status(), nil
}

// Disable stops serving this device publicly. The node stays in the
// tailnet - leaving that is the owner's business, not something a
// checkbox here should decide.
func Disable() error {
	if !Available() {
		return fmt.Errorf("tailscale is not installed on this device")
	}
	if _, err := run("tailscale", "funnel", "reset"); err != nil {
		return fmt.Errorf("could not disable Funnel: %w", err)
	}

	return nil
}

// joinTailnet runs `tailscale up`, returning a login URL when a human
// still has to authorise the node.
//
// --timeout is load-bearing, not tidiness: without an auth key
// `tailscale up` blocks until somebody visits the login page, which for
// an RPC means never returning at all. Bounding it makes the call come
// back, and the pending authorisation is left with the daemon - where
// Status finds it as AuthURL, which is how the URL still reaches the
// screen even though the command that triggered it has already exited.
func joinTailnet(authKey string) (string, error) {
	args := []string{"up", "--timeout=" + cLoginTimeout.String()}
	if authKey != "" {
		args = append(args, "--auth-key="+authKey)
	}

	out, err := run("tailscale", args...)

	// A URL means the node is waiting to be authorised, which is a
	// perfectly good outcome - and the command exiting non-zero because
	// it timed out waiting is expected in exactly that case, so the URL
	// is looked for before the error is believed.
	if url := loginURLPattern.FindString(out); url != "" {
		return url, nil
	}
	if state := Status(); state.LoginURL != "" {
		return state.LoginURL, nil
	} else if state.LoggedIn {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("could not join the tailnet: %w", err)
	}

	return "", nil
}

func run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = cmd.CombinedOutput()
		close(done)
	}()

	select {
	case <-done:
		if err != nil {
			return string(out), fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(out)))
		}
		return string(out), nil
	case <-time.After(cCommandTimeout):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return "", fmt.Errorf("%s timed out", name)
	}
}
