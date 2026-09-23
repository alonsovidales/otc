// SPDX-License-Identifier: AGPL-3.0-or-later

package tray

import (
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/alonsovidales/otc/app/desktop/internal/engine"
)

// The remote folder picker on Windows is a small native window of our own
// (RemoteFolderPickerView.swift's shape): the subfolders of the folder
// being looked at, double-click to open one, "‹ Up" to go back, and one
// button that chooses the current folder - no Open button, since the
// stock list dialog can't tell a double-click from its OK button.

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	pRegisterClassExW   = user32.NewProc("RegisterClassExW")
	pCreateWindowExW    = user32.NewProc("CreateWindowExW")
	pDefWindowProcW     = user32.NewProc("DefWindowProcW")
	pGetMessageW        = user32.NewProc("GetMessageW")
	pTranslateMessage   = user32.NewProc("TranslateMessage")
	pDispatchMessageW   = user32.NewProc("DispatchMessageW")
	pPostQuitMessage    = user32.NewProc("PostQuitMessage")
	pSendMessageW       = user32.NewProc("SendMessageW")
	pDestroyWindow      = user32.NewProc("DestroyWindow")
	pShowWindow         = user32.NewProc("ShowWindow")
	pSetForegroundWin   = user32.NewProc("SetForegroundWindow")
	pSetWindowTextW     = user32.NewProc("SetWindowTextW")
	pGetSystemMetrics   = user32.NewProc("GetSystemMetrics")
	pLoadCursorW        = user32.NewProc("LoadCursorW")
	pIsDialogMessageW   = user32.NewProc("IsDialogMessageW")
	pSetFocus           = user32.NewProc("SetFocus")
	pCreateFontW        = gdi32.NewProc("CreateFontW")
	pGetModuleHandleW   = kernel32.NewProc("GetModuleHandleW")
	pSetProcessDPIAware = user32.NewProc("SetProcessDPIAware")
	pAdjustWindowRectEx = user32.NewProc("AdjustWindowRectEx")
)

type rect struct{ left, top, right, bottom int32 }

const (
	wsOverlapped      = 0x00000000
	wsCaption         = 0x00C00000
	wsSysMenu         = 0x00080000
	wsVisible         = 0x10000000
	wsChild           = 0x40000000
	wsBorder          = 0x00800000
	wsVScroll         = 0x00200000
	wsTabStop         = 0x00010000
	wsExDlgModalFrame = 0x00000001
	wsExTopmost       = 0x00000008
	lbsNotify         = 0x0001
	lbsNoIntegral     = 0x0100
	bsDefPushButton   = 0x0001
	ssLeft            = 0x0000
	wmDestroy         = 0x0002
	wmClose           = 0x0010
	wmSetFont         = 0x0030
	wmCommand         = 0x0111
	lbResetContent    = 0x0184
	lbAddString       = 0x0180
	lbGetCurSel       = 0x0188
	lbSetCurSel       = 0x0186
	lbnSelChange      = 1
	lbnDblClk         = 2
	bnClicked         = 0
	swShow            = 5
	smCxScreen        = 0
	smCyScreen        = 1
	idcArrow          = 32512
	colorBtnFace      = 15

	idList   = 101
	idChoose = 102
	idCancel = 103
	idLabel  = 104
)

type wndClassEx struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     windows.Handle
	hIcon         windows.Handle
	hCursor       windows.Handle
	hbrBackground windows.Handle
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       windows.Handle
}

type point struct{ x, y int32 }

type winMsg struct {
	hwnd     windows.HWND
	message  uint32
	wParam   uintptr
	lParam   uintptr
	time     uint32
	pt       point
	lPrivate uint32
}

// picker is the one window's state while it is open.
type picker struct {
	list    func(string) ([]engine.RemoteEntry, error)
	hwnd    windows.HWND
	hList   windows.HWND
	hLabel  windows.HWND
	hChoose windows.HWND
	current string
	entries []engine.RemoteEntry
	rows    []string // what each list row navigates to ("" for "‹ Up")
	chosen  string
	ok      bool
	font    windows.Handle
}

var (
	pickerClassOnce bool
	activePicker    *picker
	pickerProcCB    uintptr
)

func utf16(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(s)

	return p
}

// pickRemoteFolder shows the picker and returns the chosen remote path.
func pickRemoteFolder(list func(string) ([]engine.RemoteEntry, error)) (string, bool) {
	// Win32 windows belong to the thread that made them and need that
	// thread's message loop, so the whole dialog lives on one OS thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	_, _, _ = pSetProcessDPIAware.Call()
	hInst, _, _ := pGetModuleHandleW.Call(0)
	className := utf16("OTCRemotePicker")
	if !pickerClassOnce {
		pickerProcCB = windows.NewCallback(pickerProc)
		cursor, _, _ := pLoadCursorW.Call(0, idcArrow)
		wc := wndClassEx{
			cbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
			lpfnWndProc:   pickerProcCB,
			hInstance:     windows.Handle(hInst),
			hCursor:       windows.Handle(cursor),
			hbrBackground: windows.Handle(colorBtnFace + 1),
			lpszClassName: className,
		}
		if r, _, _ := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
			return "", false
		}
		pickerClassOnce = true
	}

	p := &picker{list: list, current: "/"}
	activePicker = p
	defer func() { activePicker = nil }()

	// w/h are the client area; the frame and title bar are added on top so
	// the controls laid out below actually fit.
	const w, h = 480, 520
	const style = wsOverlapped | wsCaption | wsSysMenu
	const exStyle = wsExDlgModalFrame | wsExTopmost
	rc := rect{0, 0, w, h}
	_, _, _ = pAdjustWindowRectEx.Call(uintptr(unsafe.Pointer(&rc)), style, 0, exStyle)
	ww, wh := int(rc.right-rc.left), int(rc.bottom-rc.top)
	sx, _, _ := pGetSystemMetrics.Call(smCxScreen)
	sy, _, _ := pGetSystemMetrics.Call(smCyScreen)
	x := (int(sx) - ww) / 2
	y := (int(sy) - wh) / 2
	hwnd, _, _ := pCreateWindowExW.Call(
		exStyle, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(utf16("Choose a Remote Folder"))),
		style, uintptr(x), uintptr(y), uintptr(ww), uintptr(wh), 0, 0, hInst, 0)
	if hwnd == 0 {
		return "", false
	}
	p.hwnd = windows.HWND(hwnd)
	// -16 is 12pt at 96 dpi; Segoe UI is what every other dialog uses.
	font, _, _ := pCreateFontW.Call(uintptr(0xFFFFFFF0), 0, 0, 0, 400, 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(utf16("Segoe UI"))))
	p.font = windows.Handle(font)

	child := func(class, text string, style uintptr, id int, cx, cy, cw, ch int) windows.HWND {
		hw, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(utf16(class))), uintptr(unsafe.Pointer(utf16(text))),
			wsChild|wsVisible|style, uintptr(cx), uintptr(cy), uintptr(cw), uintptr(ch), hwnd, uintptr(id), hInst, 0)
		_, _, _ = pSendMessageW.Call(hw, wmSetFont, font, 1)

		return windows.HWND(hw)
	}
	p.hLabel = child("STATIC", "", ssLeft, idLabel, 14, 12, w-28, 44)
	p.hList = child("LISTBOX", "", wsBorder|wsVScroll|wsTabStop|lbsNotify|lbsNoIntegral, idList, 14, 62, w-28, h-62-58)
	p.hChoose = child("BUTTON", "Choose", wsTabStop|bsDefPushButton, idChoose, w-14-150-8-100, h-44, 150, 30)
	child("BUTTON", "Cancel", wsTabStop, idCancel, w-14-100, h-44, 100, 30)

	p.load()
	_, _, _ = pShowWindow.Call(hwnd, swShow)
	_, _, _ = pSetForegroundWin.Call(hwnd)
	_, _, _ = pSetFocus.Call(uintptr(p.hList))

	var m winMsg
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if r == 0 || int32(r) == -1 {
			break
		}
		if isDlg, _, _ := pIsDialogMessageW.Call(hwnd, uintptr(unsafe.Pointer(&m))); isDlg != 0 {
			continue
		}
		_, _, _ = pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		_, _, _ = pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}

	return p.chosen, p.ok
}

// load fills the list for p.current and relabels the Choose button.
func (p *picker) load() {
	label := baseName(p.current)
	if label == "" {
		label = "/"
	}
	_, _, _ = pSetWindowTextW.Call(uintptr(p.hLabel), uintptr(unsafe.Pointer(utf16("Remote folder: "+p.current+"\r\nDouble-click a folder to open it; the button keeps “"+label+"” in sync with a folder here."))))
	_, _, _ = pSetWindowTextW.Call(uintptr(p.hChoose), uintptr(unsafe.Pointer(utf16("Choose “"+label+"”"))))
	_, _, _ = pSendMessageW.Call(uintptr(p.hList), lbResetContent, 0, 0)
	p.rows = nil
	entries, err := p.list(p.current)
	if err != nil {
		p.addRow("⚠ "+err.Error(), "")

		return
	}
	p.entries = entries
	if p.current != "/" {
		p.addRow("‹ Up", "")
	}
	for _, e := range entries {
		if e.IsDir {
			p.addRow("📁 "+e.Name, e.Path)
		}
	}
	if len(p.rows) == 0 {
		p.addRow("(no subfolders here)", "")
	}
	_, _, _ = pSendMessageW.Call(uintptr(p.hList), lbSetCurSel, ^uintptr(0), 0)
}

func (p *picker) addRow(text, target string) {
	_, _, _ = pSendMessageW.Call(uintptr(p.hList), lbAddString, 0, uintptr(unsafe.Pointer(utf16(text))))
	p.rows = append(p.rows, target)
}

func (p *picker) openSelected() {
	sel, _, _ := pSendMessageW.Call(uintptr(p.hList), lbGetCurSel, 0, 0)
	i := int(int32(sel))
	if i < 0 || i >= len(p.rows) {
		return
	}
	switch {
	case p.rows[i] != "":
		p.current = p.rows[i]
	case p.current != "/" && i == 0:
		p.current = parentPath(p.current)
	default:
		return
	}
	p.load()
}

func pickerProc(hwnd windows.HWND, msg uint32, wParam, lParam uintptr) uintptr {
	p := activePicker
	switch msg {
	case wmCommand:
		if p == nil {
			break
		}
		id := int(wParam & 0xFFFF)
		code := int((wParam >> 16) & 0xFFFF)
		switch {
		case id == idList && code == lbnDblClk:
			p.openSelected()
		case id == idChoose && code == bnClicked:
			p.chosen = strings.TrimSuffix(p.current, "/")
			if p.chosen == "" {
				p.chosen = "/"
			}
			p.ok = true
			_, _, _ = pDestroyWindow.Call(uintptr(hwnd))
		case id == idCancel && code == bnClicked:
			_, _, _ = pDestroyWindow.Call(uintptr(hwnd))
		}

		return 0
	case wmClose:
		_, _, _ = pDestroyWindow.Call(uintptr(hwnd))

		return 0
	case wmDestroy:
		_, _, _ = pPostQuitMessage.Call(0)

		return 0
	}
	r, _, _ := pDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)

	return r
}
