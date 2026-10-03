// SPDX-License-Identifier: AGPL-3.0-or-later

package flasher

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func hidden(cmd *exec.Cmd) *exec.Cmd {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	return cmd
}

// ListDisks lists USB and SD disks that Windows doesn't boot or run from.
func ListDisks() ([]Disk, error) {
	const script = `$d = @(Get-Disk | Where-Object { ($_.BusType -eq 'USB' -or $_.BusType -eq 'SD' -or $_.BusType -eq 'MMC') -and -not $_.IsBoot -and -not $_.IsSystem -and $_.Size -gt 0 } | Select-Object Number,FriendlyName,Size); ConvertTo-Json -InputObject $d -Compress`
	out, err := hidden(exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)).Output()
	if err != nil {
		return nil, fmt.Errorf("listing the disks: %w", err)
	}
	var rows []struct {
		Number       int
		FriendlyName string
		Size         int64
	}
	if s := strings.TrimSpace(string(out)); s != "" {
		if err := json.Unmarshal([]byte(s), &rows); err != nil {
			return nil, fmt.Errorf("listing the disks: %w", err)
		}
	}
	var disks []Disk
	for _, r := range rows {
		disks = append(disks, Disk{ID: `\\.\PhysicalDrive` + strconv.Itoa(r.Number), Name: strings.TrimSpace(r.FriendlyName), Size: r.Size})
	}
	return disks, nil
}

func diskNumber(id string) (int, error) {
	return strconv.Atoi(strings.TrimPrefix(id, `\\.\PhysicalDrive`))
}

// openDisk removes the card's partitions (diskpart's clean, which also
// takes its volumes offline - Windows refuses raw writes over a mounted
// volume) and opens it for unbuffered writing.
func openDisk(d Disk) (device, error) {
	n, err := diskNumber(d.ID)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "otc-diskpart-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	script := filepath.Join(dir, "clean.txt")
	if err := os.WriteFile(script, []byte(fmt.Sprintf("select disk %d\r\nattributes disk clear readonly noerr\r\nclean\r\nrescan\r\nexit\r\n", n)), 0o600); err != nil {
		return nil, err
	}
	if out, err := hidden(exec.Command("diskpart.exe", "/s", script)).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("preparing the card (diskpart): %v: %s", err, strings.TrimSpace(string(out)))
	}
	p, err := windows.UTF16PtrFromString(d.ID)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_NO_BUFFERING|windows.FILE_FLAG_WRITE_THROUGH, 0)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", d.ID, err)
	}
	return os.NewFile(uintptr(h), d.ID), nil
}

func dropCache(device) {} // unbuffered: nothing is cached

// eject: Windows mounts the new card's boot partition as soon as it is
// written; it can simply be removed once the wizard says so.
func eject(Disk) {}

func openStatus(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
}

type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         windows.Handle
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     windows.Handle
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    windows.Handle
	dwHotKey     uint32
	hIcon        windows.Handle
	hProcess     windows.Handle
}

var procShellExecuteEx = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

// elevate starts exe with args through UAC ("runas") and returns a wait for
// it.
func elevate(exe string, args []string) (func() error, error) {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = windows.EscapeArg(a)
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	params, _ := windows.UTF16PtrFromString(strings.Join(quoted, " "))
	const seeMaskNoCloseProcess, seeMaskNoAsync = 0x40, 0x100
	info := shellExecuteInfo{fMask: seeMaskNoCloseProcess | seeMaskNoAsync, lpVerb: verb, lpFile: file, lpParameters: params, nShow: windows.SW_HIDE}
	info.cbSize = uint32(unsafe.Sizeof(info))
	if r, _, err := procShellExecuteEx.Call(uintptr(unsafe.Pointer(&info))); r == 0 {
		if errors.Is(err, windows.ERROR_CANCELLED) {
			return nil, ErrCancelled
		}
		return nil, err
	}
	if info.hProcess == 0 {
		return nil, errors.New("the card writer did not start")
	}
	return func() error {
		defer windows.CloseHandle(info.hProcess)
		if _, err := windows.WaitForSingleObject(info.hProcess, windows.INFINITE); err != nil {
			return err
		}
		var code uint32
		if err := windows.GetExitCodeProcess(info.hProcess, &code); err != nil {
			return err
		}
		if code != 0 {
			return fmt.Errorf("exit code %d", code)
		}
		return nil
	}, nil
}
