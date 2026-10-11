// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"testing"

	"github.com/alonsovidales/otc/i18n"
)

// The replies the bridge sends devices and clients are, from the catalog,
// what they were before it (HEAD before the api.relay.* keys): always
// English, the bridge never reading a request's language.
func TestRelayTextsAreUnchanged(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{cDeviceUnreachableMsg, "This device isn't reachable right now. It may be switched off or offline - please try again later."},
		{cInternalErrorMsg, "The bridge could not do this right now, try again later"},
		{relayText(i18n.ApiRelayAccountDisabled()), "This account has been disabled."},
		{relayText(i18n.ApiRelayInvalidSecret()), "Invalid Secret"},
		{relayText(i18n.ApiRelayLogsTooBig()), "the logs are too big to send"},
		{relayText(i18n.ApiRelayLogsTooOften()), "logs were sent from this device a few times already - try again in an hour"},
		{relayText(i18n.ApiRelayLogsNoEmail()), "the bridge can't send email right now"},
		{relayText(i18n.ApiRelayLogsSendFailed()), "the logs could not be sent right now"},
		{relayText(i18n.ApiRelayDomainCheckFailed()), "error checking domain"},
	} {
		if c.got != c.want {
			t.Errorf("%q, want %q", c.got, c.want)
		}
	}
}
