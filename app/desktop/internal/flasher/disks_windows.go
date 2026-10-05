// SPDX-License-Identifier: AGPL-3.0-or-later

package flasher

import (
	"crypto/rand"
	"encoding/hex"
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
// The card writer runs this as administrator, so PowerShell is Windows' own
// (by its full path) with Windows' own modules only: PSModulePath starts
// with the user's Documents\WindowsPowerShell\Modules, where any program of
// the user could plant a module that Get-Disk would load.
func ListDisks() ([]Disk, error) {
	const script = `$d = @(Get-Disk | Where-Object { ($_.BusType -eq 'USB' -or $_.BusType -eq 'SD' -or $_.BusType -eq 'MMC') -and -not $_.IsBoot -and -not $_.IsSystem -and $_.Size -gt 0 } | Select-Object Number,FriendlyName,Size); ConvertTo-Json -InputObject $d -Compress`
	ps, env := "powershell.exe", os.Environ()
	if sys, err := windows.GetSystemDirectory(); err == nil {
		home := filepath.Join(sys, "WindowsPowerShell", "v1.0")
		ps = filepath.Join(home, "powershell.exe")
		env = withEnv(env, "PSModulePath", filepath.Join(home, "Modules"))
	}
	cmd := hidden(exec.Command(ps, "-NoProfile", "-NonInteractive", "-Command", script))
	cmd.Env = env
	out, err := cmd.Output()
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

// withEnv is env with name set to value (names ignore case on Windows).
func withEnv(env []string, name, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if k, _, ok := strings.Cut(kv, "="); ok && strings.EqualFold(k, name) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, name+"="+value)
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
	dir, err := adminTempDir()
	if err != nil {
		// Not possible on this system: where it always was, rather than
		// no card writing at all.
		if dir, err = os.MkdirTemp("", "otc-diskpart-"); err != nil {
			return nil, err
		}
	}
	defer os.RemoveAll(dir)
	script := filepath.Join(dir, "clean.txt")
	if err := os.WriteFile(script, []byte(fmt.Sprintf("select disk %d\r\nattributes disk clear readonly noerr\r\nclean\r\nrescan\r\nexit\r\n", n)), 0o600); err != nil {
		return nil, err
	}
	diskpart := "diskpart.exe"
	if sys, err := windows.GetSystemDirectory(); err == nil {
		diskpart = filepath.Join(sys, "diskpart.exe")
	}
	if out, err := hidden(exec.Command(diskpart, "/s", script)).CombinedOutput(); err != nil {
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

// adminTempDir makes a new directory that only SYSTEM and Administrators
// can open, for diskpart's script. The elevated writer's %TEMP% is the
// user's own, where any program of the user could rewrite the script
// between its writing and diskpart reading it - and diskpart runs as
// administrator. Under Windows\Temp users can't rename or remove what
// they didn't make; the user's %TEMP% is the fallback.
func adminTempDir() (string, error) {
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if err != nil {
		return "", err
	}
	sa := &windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(*sa))
	var bases []string
	if win, err := windows.GetSystemWindowsDirectory(); err == nil {
		bases = append(bases, filepath.Join(win, "Temp"))
	}
	bases = append(bases, os.TempDir())
	lastErr := errors.New("no temporary directory")
	for _, base := range bases {
		for try := 0; try < 5; try++ {
			var b [8]byte
			if _, err := rand.Read(b[:]); err != nil {
				return "", err
			}
			dir := filepath.Join(base, "otc-diskpart-"+hex.EncodeToString(b[:]))
			p, err := windows.UTF16PtrFromString(dir)
			if err != nil {
				return "", err
			}
			// A new directory every time, never one that is already there.
			if lastErr = windows.CreateDirectory(p, sa); lastErr == nil {
				return dir, nil
			}
			if !errors.Is(lastErr, windows.ERROR_ALREADY_EXISTS) {
				break
			}
		}
	}
	return "", lastErr
}

func dropCache(device) {} // unbuffered: nothing is cached

// eject: Windows mounts the new card's boot partition as soon as it is
// written; it can simply be removed once the wizard says so.
func eject(Disk) {}

// openStatus opens the progress file the unelevated wizard made, to append
// to it as administrator. It sits in the user's %TEMP%, so it must be, as
// opened: no link or junction (which would have this process write into
// whatever file it points to), not a directory, a single name, and still
// empty as the wizard left it - as O_NOFOLLOW on Linux.
func openStatus(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.FILE_APPEND_DATA|windows.SYNCHRONIZE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	var fi windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &fi); err != nil {
		_ = windows.CloseHandle(h)
		return nil, err
	}
	if fi.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 ||
		fi.NumberOfLinks != 1 || fi.FileSizeHigh != 0 || fi.FileSizeLow != 0 {
		_ = windows.CloseHandle(h)
		return nil, errors.New("the progress file is not the one the wizard made")
	}
	return os.NewFile(uintptr(h), path), nil
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
