// SPDX-License-Identifier: AGPL-3.0-or-later

package tailscalefunnel

import (
	"errors"
	"testing"

	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/i18n"
)

// The errors Enable, Disable and switchDaemon return keep their English
// (the functions themselves drive the real tailscale CLI, so only their
// errors are built here), and a runner's failure is its message as
// written.
func TestErrorsEnglishUnchanged(t *testing.T) {
	cause := errors.New("tailscale: exit status 1: boom")
	cases := []struct {
		err  error
		want string
	}{
		{devtext.New(i18n.DevStatusTailscaleCliMissing()), "tailscale is not installed on this device"},
		{devtext.Wrap(i18n.DevStatusTailscaleEnableFailed(cause.Error()), cause), "could not enable Funnel: tailscale: exit status 1: boom"},
		{devtext.Wrap(i18n.DevStatusTailscaleDisableFailed(cause.Error()), cause), "could not disable Funnel: tailscale: exit status 1: boom"},
		{devtext.Wrap(i18n.DevStatusTailscaleRequestOnFailed(cause.Error()), cause), "could not ask for Tailscale to be switched on: tailscale: exit status 1: boom"},
		{devtext.Wrap(i18n.DevStatusTailscaleRequestOffFailed(cause.Error()), cause), "could not ask for Tailscale to be switched off: tailscale: exit status 1: boom"},
		{devtext.New(i18n.DevStatusTailscaleTimeoutOn()), "Tailscale did not switch on in time - try again"},
		{devtext.New(i18n.DevStatusTailscaleTimeoutOff()), "Tailscale did not switch off in time - try again"},
		{devtext.Wrap(i18n.DevStatusTailscaleJoinFailed(cause.Error()), cause), "could not join the tailnet: tailscale: exit status 1: boom"},
		{devtext.TailscaleError("start_failed", []byte(`{}`), "tailscaled could not be started"), "tailscaled could not be started"},
		{devtext.TailscaleError("", nil, "an older runner's words"), "an older runner's words"},
	}
	for _, c := range cases {
		for _, lang := range []string{"", "en", "it"} {
			if got := devtext.Text(lang, c.err); got != c.want || c.err.Error() != c.want {
				t.Errorf("in %q: got %q (Error %q), want %q", lang, got, c.err.Error(), c.want)
			}
		}
	}
}
