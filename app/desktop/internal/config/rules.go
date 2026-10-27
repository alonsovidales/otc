// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/text/unicode/norm"

	"github.com/alonsovidales/otc/i18n"
)

// ---- folders synced once --------------------------------------

// A folder is synced once. One added inside another synced folder, or
// holding one - on this computer or on the device - had two passes sync
// the same files, each undoing what the other did, and both writing the
// same partial downloads. The command line and the tray refuse it; the
// Mac's SyncPaths.overlap is the same rule.

// OverlapKind is how a folder overlaps one already synced.
type OverlapKind int

const (
	OverlapSame   OverlapKind = iota + 1 // it is that folder
	OverlapInside                        // it is inside it
	OverlapHolds                         // it holds it
)

// Overlap is why a folder can't be added: Kind, with the name of the
// folder already synced (Other) and the one being added (Name).
type Overlap struct {
	Kind        OverlapKind
	Name, Other string
}

// Msg is the reason in the tray's words: the overlap, then why a folder is
// synced only once.
func (o *Overlap) Msg() (reason, why i18n.Msg) {
	switch o.Kind {
	case OverlapInside:
		reason = i18n.DeskMainFolderOverlapInside(o.Name, o.Other)
	case OverlapHolds:
		reason = i18n.DeskMainFolderOverlapHolds(o.Name, o.Other)
	default:
		reason = i18n.DeskMainFolderOverlapSame(o.Name)
	}

	return reason, i18n.DeskMainFolderOverlapWhy()
}

// Error is the command line's English.
func (o *Overlap) Error() string {
	switch o.Kind {
	case OverlapInside:
		return fmt.Sprintf("%s is inside %s, which is already synced: a folder can be synced only once", o.Name, o.Other) // i18n-ignore: the command line's English; the tray words Msg
	case OverlapHolds:
		return fmt.Sprintf("%s holds %s, which is already synced: a folder can be synced only once", o.Name, o.Other) // i18n-ignore: the command line's English; the tray words Msg
	}

	return fmt.Sprintf("%s is already synced", o.Name) // i18n-ignore: the command line's English; the tray words Msg
}

// NamedPath is a folder's path, and the name it is shown by.
type NamedPath struct {
	Path, Name string
}

// PathOverlap compares path with others - all local paths, or all device
// paths ("/" separated) - in NFC, without a trailing separator, and
// ignoring case when ignoreCase. nil when it overlaps none.
func PathOverlap(path, name string, others []NamedPath, ignoreCase bool) *Overlap {
	clean := func(p string) string {
		s := norm.NFC.String(filepath.ToSlash(p))
		if ignoreCase {
			s = strings.ToLower(s)
		}
		for len(s) > 1 && strings.HasSuffix(s, "/") {
			s = strings.TrimSuffix(s, "/")
		}

		return s
	}
	under := func(p, dir string) bool {
		if dir == "/" {
			return p != "/"
		}

		return strings.HasPrefix(p, dir+"/")
	}
	mine := clean(path)
	for _, o := range others {
		theirs := clean(o.Path)
		switch {
		case mine == theirs:
			return &Overlap{Kind: OverlapSame, Name: name, Other: o.Name}
		case under(mine, theirs):
			return &Overlap{Kind: OverlapInside, Name: name, Other: o.Name}
		case under(theirs, mine):
			return &Overlap{Kind: OverlapHolds, Name: name, Other: o.Name}
		}
	}

	return nil
}

// IgnoresCase: this computer's folders, and the device's as compared here,
// are told apart regardless of case (Windows; macOS, where only the
// development build runs).
func IgnoresCase() bool { return runtime.GOOS == "windows" || runtime.GOOS == "darwin" }

// FolderOverlap is how a folder at local (on this computer), syncing with
// remote on the device, overlaps the folders c already syncs - first on
// this computer, then on the device - or nil. devicePath is a folder from
// this computer's path on the device (engine.DevicePathFor).
func (c *Config) FolderOverlap(local, remote string, devicePath func(string) string) *Overlap {
	var locals, remotes []NamedPath
	for _, f := range c.Folders {
		locals = append(locals, NamedPath{f.Path, filepath.Base(f.Path)})
		remotes = append(remotes, NamedPath{devicePath(f.Path), filepath.Base(f.Path)})
	}
	for _, f := range c.RemoteFolders {
		locals = append(locals, NamedPath{f.LocalPath, filepath.Base(f.LocalPath)})
		remotes = append(remotes, NamedPath{f.RemotePath, filepath.Base(f.LocalPath)})
	}
	name := filepath.Base(local)
	if o := PathOverlap(local, name, locals, IgnoresCase()); o != nil {
		return o
	}
	if o := PathOverlap(remote, name, remotes, IgnoresCase()); o != nil {
		return o
	}

	return nil
}

// ---- unencrypted addresses -----------------------------------

// Unencrypted: domain is a ws:// address - nothing encrypted - for a device
// that isn't on this network, where anyone on the way could read the files
// and take the password (whose encryption key itself comes over the same
// connection). Not loopback, private (10/8, 172.16/12, 192.168/16,
// fc00::/7) or link-local addresses, nor a .local, .lan, .home.arpa or
// .internal name, nor a single-label one ("nas"): the home network. The
// tray and the command line ask before using one; the Mac the same
// (SyncPaths.isUnencryptedRemote).
func Unencrypted(domain string) bool {
	d := strings.TrimSpace(domain)
	if !strings.HasPrefix(strings.ToLower(d), "ws://") {
		return false
	}
	u, err := url.Parse(d)
	if err != nil || u.Hostname() == "" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return false
	}
	if ip := net.ParseIP(h); ip != nil {
		return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
	}
	if !strings.Contains(h, ".") {
		return false
	}
	for _, suffix := range []string{".local", ".lan", ".home.arpa", ".internal"} {
		if strings.HasSuffix(h, suffix) {
			return false
		}
	}

	return true
}
