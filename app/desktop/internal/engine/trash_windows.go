// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
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

// systemTrash moves p to the Recycle Bin, as Explorer's Delete does, with
// no dialog. Only on a fixed disk: elsewhere the shell deletes outright.
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
