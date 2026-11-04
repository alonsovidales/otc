// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"strconv"

	pb "github.com/alonsovidales/otc/proto/generated"
)

// The longest owner, domain and secret a device can present: the columns
// of the devices table that hold them. Nothing longer can match one.
const (
	cMaxOwnerLen  = 64
	cMaxDomainLen = 150
	cMaxSecretLen = 150
)

// plausibleDomain reports whether d can be a device's domain: no longer
// than its column, and made of a host name's characters (a port, an IPv6
// literal's brackets: development bridges use them). A message before
// sign-in may carry 8 MiB of anything, which went into the log and into a
// query carried to the primary; it is refused before either now.
func plausibleDomain(d string) bool {
	if d == "" || len(d) > cMaxDomainLen {
		return false
	}
	for i := 0; i < len(d); i++ {
		switch c := d[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.', c == '-', c == '_', c == ':', c == '[', c == ']':
		default:
			return false
		}
	}
	return true
}

// plausibleIdentity: a plausible domain, and an owner and a secret no
// longer than their columns.
func plausibleIdentity(owner, domain, secret string) bool {
	return plausibleDomain(domain) && len(owner) <= cMaxOwnerLen && len(secret) <= cMaxSecretLen
}

// deviceIdentity is the identity a device's message presents - each one
// is authenticated by its owner, domain and secret - and whether env is
// one of them.
func deviceIdentity(env *pb.ReqEnvelope) (owner, domain, secret string, ok bool) {
	type identity interface {
		GetOwnerUuid() string
		GetDomain() string
		GetSecret() string
	}
	var m identity
	switch p := env.Payload.(type) {
	case *pb.ReqEnvelope_ReqBridgeRegister:
		m = p.ReqBridgeRegister
	case *pb.ReqEnvelope_ReqRotateBridgeSecret:
		m = p.ReqRotateBridgeSecret
	case *pb.ReqEnvelope_ReqBridgeReleaseDomain:
		m = p.ReqBridgeReleaseDomain
	case *pb.ReqEnvelope_ReqBridgeSendLogs:
		m = p.ReqBridgeSendLogs
	case *pb.ReqEnvelope_ReqIsDomainAvailable:
		m = p.ReqIsDomainAvailable
	case *pb.ReqEnvelope_ReqRegisterUserDomain:
		m = p.ReqRegisterUserDomain
	case *pb.ReqEnvelope_ReqSetDeviceDisabled:
		m = p.ReqSetDeviceDisabled
	case *pb.ReqEnvelope_ReqBridgeNotify:
		m = p.ReqBridgeNotify
	case *pb.ReqEnvelope_ReqUpdatePushRegistrations:
		m = p.ReqUpdatePushRegistrations
	default:
		return "", "", "", false
	}
	return m.GetOwnerUuid(), m.GetDomain(), m.GetSecret(), true
}

// cLoggedMax is how much of a value a client sent goes into a log line.
const cLoggedMax = 160

// logged is s as a log line may carry it: at most cLoggedMax bytes, and
// how long it was when it was longer.
func logged(s string) string {
	if len(s) <= cLoggedMax {
		return s
	}
	return s[:cLoggedMax] + "...(" + strconv.Itoa(len(s)) + " bytes)"
}
