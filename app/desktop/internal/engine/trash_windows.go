// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var procSHFileOperationW = windows.NewLazySystemDLL("shell32.dll").NewProc("SHFileOperationW")

// shFileOpStruct is SHFILEOPSTRUCTW as the 64-bit Windows otc-sync is built
// for lays it out (natural alignment; packed only on 32-bit).
type shFileOpStruct struct {
	hwnd                  uintptr
	wFunc                 uint32
	pFrom                 *uint16
	pTo                   *uint16
	fFlags                uint16
	fAnyOperationsAborted int32
	hNameMappings         uintptr
	lpszProgressTitle     *uint16
}

const (
	foDelete          = 0x0003
	fofSilent         = 0x0004
	fofNoConfirmation = 0x0010
	fofAllowUndo      = 0x0040
	fofNoErrorUI      = 0x0400
)

// errNoRecycleBin: the file's disk has no Recycle Bin (a removable or a
// network one), where the shell would delete it for good.
var errNoRecycleBin = errors.New("no Recycle Bin on this disk") // i18n-ignore: a file's error, only logged

// errNotRecycled: the disk's Recycle Bin would delete the file for good -
// it is turned off, or the file is larger than it holds (recycleBinTakes).
var errNotRecycled = errors.New("the Recycle Bin would delete it for good (turned off, or the file is too large for it)") // i18n-ignore: a file's error, only logged

// recycleBinSettings are the Recycle Bin settings of the volume whose root
// is vol, as Explorer's Properties keeps them for this user: whether it is
// off (NukeOnDelete, or the NoRecycleFiles policy for this user or the
// computer) and its size in megabytes (MaxCapacity), when it has one.
func recycleBinSettings(vol *uint16) (off bool, maxMB uint64, known bool) {
	const policies = `Software\Microsoft\Windows\CurrentVersion\Policies\Explorer`
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		if dwordValue(root, policies, "NoRecycleFiles") == 1 {
			return true, 0, false
		}
	}
	name := make([]uint16, 64)
	if windows.GetVolumeNameForVolumeMountPoint(vol, &name[0], uint32(len(name))) != nil {
		return false, 0, false
	}
	// \\?\Volume{guid}\ -> {guid}
	guid := windows.UTF16ToString(name)
	i, j := strings.IndexByte(guid, '{'), strings.IndexByte(guid, '}')
	if i < 0 || j < i {
		return false, 0, false
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Explorer\BitBucket\Volume\`+guid[i:j+1], registry.QUERY_VALUE)
	if err != nil {
		return false, 0, false
	}
	defer k.Close()
	if n, _, err := k.GetIntegerValue("NukeOnDelete"); err == nil && n != 0 {
		return true, 0, false
	}
	if n, _, err := k.GetIntegerValue("MaxCapacity"); err == nil {
		return false, n, true
	}

	return false, 0, false
}

// dwordValue is the integer value name of key path under root, 0 when it
// isn't there.
func dwordValue(root registry.Key, path, name string) uint64 {
	k, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		return 0
	}
	defer k.Close()
	n, _, err := k.GetIntegerValue(name)
	if err != nil {
		return 0
	}

	return n
}

// systemTrash moves p to the Recycle Bin, as Explorer's Delete does, with
// no dialog. Only on a fixed disk, and where the bin keeps the file
// (recycleBinTakes): elsewhere the shell deletes outright.
func systemTrash(p string) error {
	abs, err := filepath.Abs(p)
	if err != nil {
		return err
	}
	// The shell takes neither long paths nor the \\?\ form.
	if len(abs) >= windows.MAX_PATH {
		return errNoRecycleBin
	}
	name, err := windows.UTF16PtrFromString(abs)
	if err != nil {
		return err
	}
	vol := make([]uint16, windows.MAX_PATH+1)
	if err := windows.GetVolumePathName(name, &vol[0], uint32(len(vol))); err != nil {
		return err
	}
	if windows.GetDriveType(&vol[0]) != windows.DRIVE_FIXED {
		return errNoRecycleBin
	}
	// A file the Recycle Bin wouldn't keep - too large for it, or the bin
	// turned off - the shell deletes for good: it goes to the folder's own
	// trash instead.
	fi, err := os.Lstat(abs)
	if err != nil {
		return err
	}
	off, maxMB, known := recycleBinSettings(&vol[0])
	var free, total, totalFree uint64
	_ = windows.GetDiskFreeSpaceEx(&vol[0], &free, &total, &totalFree)
	if !recycleBinTakes(fi.Size(), off, maxMB, known, total) {
		return errNotRecycled
	}
	// pFrom is a list of names, each ending in a NUL, ending in another.
	from, err := windows.UTF16FromString(abs)
	if err != nil {
		return err
	}
	from = append(from, 0)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// The shell's file operations run on COM; S_FALSE is a thread that had
	// it already, which is undone all the same.
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); err == nil || errors.Is(err, syscall.Errno(1)) {
		defer windows.CoUninitialize()
	}
	op := shFileOpStruct{wFunc: foDelete, pFrom: &from[0], fFlags: fofAllowUndo | fofNoConfirmation | fofSilent | fofNoErrorUI}
	r, _, _ := procSHFileOperationW.Call(uintptr(unsafe.Pointer(&op)))
	runtime.KeepAlive(from)
	if r != 0 || op.fAnyOperationsAborted != 0 {
		return fmt.Errorf("the Recycle Bin refused it (%#x)", r) // i18n-ignore: a file's error, only logged
	}
	if _, err := os.Lstat(abs); err == nil {
		return errors.New("still there after the Recycle Bin took it") // i18n-ignore: a file's error, only logged
	}

	return nil
}

// pruneSystemTrash: the Recycle Bin is Windows' to empty (its size limit
// and Storage Sense).
func pruneSystemTrash(time.Time) {}
