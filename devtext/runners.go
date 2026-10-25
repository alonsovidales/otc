// SPDX-License-Identifier: AGPL-3.0-or-later

package devtext

import (
	"github.com/alonsovidales/otc/i18n"
)

// The root-side runners report through status files in /var/lib/otc:
// update-status.json (scripts/update-runner/otc-update-runner.sh,
// otc-update-stopped.sh and scripts/update.sh), bridge-status.json
// (scripts/bridge-runner), tailscale-status.json (scripts/tailscale-runner)
// and wifi-restart-status.json (scripts/device-runner's Wi-Fi restart).
// (The Wi-Fi change runner, scripts/device-runner/otc-wifi-join-runner.py,
// answers in its own directory, /run/otc-wifi-join/status.json, with codes
// only: WifiJoinText below.) Each line they write is
//
//	{"state": ..., "message": ..., "code": ..., "args": {...}, ...}
//
// message is the English, which devices from before codes show as it is;
// code names the same text in the catalog and args holds its arguments,
// every one a string, so the text can be shown in the reader's language.
// A runner from before codes writes neither. Codes are runner tokens,
// never translated: each table below maps the codes one file may carry to
// their keys - an allowlist, so the file (in a directory the otc user
// owns) can't name any other key.

var (
	updateCodes = codes("dev.update.",
		"applying", "binary_install_failed", "build_failed", "building", "checking",
		"checking_signature", "downloading", "generating_protobuf", "installing_web", "interrupted",
		"manifest_download_failed", "manifest_fetch_failed", "manifest_not_signed",
		"no_release_key", "no_room", "no_source_archive", "no_source_dir",
		"protobuf_version_missing", "protoc_download_failed", "protoc_failed",
		"protoc_gen_go_failed", "protoc_hash_mismatch", "protoc_unpack_failed",
		"repo_not_https", "restarting", "script_checksum", "script_failed", "script_missing",
		"source_download_failed", "source_hash_mismatch", "source_unpack_failed",
		"stage_failed", "staging", "unsupported_arch", "updated", "uptodate",
		"verified_manifest_missing", "web_checksum", "web_download_failed",
		"web_install_failed", "web_unpack_failed")
	bridgeCodes = codes("dev.status.bridge.",
		"already_on", "config_missing", "config_update_failed", "no_name", "not_host_name",
		"on_bridge", "switching_off", "switching_on")
	tailscaleCodes = codes("dev.status.tailscale.",
		"not_installed", "operator_failed", "start_failed", "unknown_request")
	wifiRestartCodes = codes("dev.status.wifi.",
		"busy", "no_connection", "not_wifi", "rebooted", "restarted", "restarting", "too_soon")

	// Settings > Wi-Fi: a change's state and codes (WifiJoin.code; the
	// runner's CODES, which TestWifiJoinRunnerCodes holds to this table,
	// lists all of them but no_answer, which only the device decides).
	wifiJoinCodes = codes("dev.status.wifi_join.",
		"joining", "connected", "wrong_password", "auth_failed", "not_found", "timed_out",
		"no_address", "no_internet", "unsupported_security", "password_required", "open_network",
		"bad_request", "no_wifi", "busy", "update_running", "too_many", "interrupted", "no_answer",
		"join_failed")
	// A failure that can come after the device left its Wi-Fi network for
	// the new one says where it ended up: back on its previous network, or
	// not (lost).
	wifiJoinBackCodes = codes("dev.status.wifi_join_back.", wifiJoinVariants...)
	wifiJoinLostCodes = codes("dev.status.wifi_join_lost.", wifiJoinVariants...)
)

var wifiJoinVariants = []string{
	"wrong_password", "auth_failed", "not_found", "timed_out", "no_address", "no_internet",
	"join_failed", "interrupted",
}

// codes maps each code to prefix+code: the codes are the keys' last
// segment.
func codes(prefix string, list ...string) map[string]string {
	m := make(map[string]string, len(list))
	for _, c := range list {
		m[c] = prefix + c
	}
	return m
}

// runnerText renders a runner's status text in lang: the key keys maps
// code to, with args, or message as the runner wrote it - in English, for
// no code or one the table doesn't list (a newer runner), and for args
// that don't fit the key's declaration (i18n.RenderStored).
func runnerText(lang string, keys map[string]string, code string, args []byte, message string) string {
	key, ok := keys[code]
	if code == "" || !ok {
		return message
	}
	return i18n.RenderStored(lang, key, args, message)
}

// UpdateStatus is update-status.json's message in lang.
func UpdateStatus(lang, code string, args []byte, message string) string {
	return runnerText(lang, updateCodes, code, args, message)
}

// BridgeStatus is bridge-status.json's message in lang.
func BridgeStatus(lang, code string, args []byte, message string) string {
	return runnerText(lang, bridgeCodes, code, args, message)
}

// TailscaleStatus is tailscale-status.json's message in lang.
func TailscaleStatus(lang, code string, args []byte, message string) string {
	return runnerText(lang, tailscaleCodes, code, args, message)
}

// WifiRestartStatus is wifi-restart-status.json's message in lang.
func WifiRestartStatus(lang, code string, args []byte, message string) string {
	return runnerText(lang, wifiRestartCodes, code, args, message)
}

// WifiRestartKey is the catalog key of a Wi-Fi restart status code, for a
// reader that stores the text as a key rather than rendering it now (an
// Alert): "" for no code or one this build doesn't know.
func WifiRestartKey(code string) string { return wifiRestartCodes[code] }

// WifiJoinCode reports whether code is one of a Wi-Fi change's codes.
func WifiJoinCode(code string) bool {
	_, ok := wifiJoinCodes[code]
	return ok
}

// WifiJoinText is a Wi-Fi change's message in lang: its code's text (a
// code this build doesn't know reads as join_failed) naming the network.
// A failure after the device left its Wi-Fi network for the new one says
// where it ended up: back on that network (restored) or not; one where it
// never left (before switching, or with no Wi-Fi network before) doesn't.
// The runner writes no English, so every language, English included, is
// rendered from the catalog.
func WifiJoinText(lang, code, ssid string, left, restored bool) string {
	if _, ok := wifiJoinCodes[code]; !ok {
		code = "join_failed"
	}
	key := wifiJoinCodes[code]
	if left {
		variants := wifiJoinLostCodes
		if restored {
			variants = wifiJoinBackCodes
		}
		if k, ok := variants[code]; ok {
			key = k
		}
	}
	m := i18n.Msg{Key: key, Args: map[string]any{"ssid": ssid}}
	if m.Check() != nil {
		// A key without the network's name.
		m.Args = nil
	}
	return m.Render(lang)
}

// runnerError is a runner's failure as an error: its English message as
// Error(), and the same text in a request's language through its code.
type runnerError struct {
	keys    map[string]string
	code    string
	args    []byte
	message string
}

func (e *runnerError) Error() string { return e.message }

func (e *runnerError) Localized(lang string) string {
	return runnerText(lang, e.keys, e.code, e.args, e.message)
}

// TailscaleError is a failure the Tailscale runner reported as an error the
// apps show.
func TailscaleError(code string, args []byte, message string) error {
	return &runnerError{keys: tailscaleCodes, code: code, args: args, message: message}
}
