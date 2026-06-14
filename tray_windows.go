package main

import (
	"fmt"
	"os"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	wmDestroy          = 0x0002
	wmClose            = 0x0010
	wmLButtonUp        = 0x0202
	wmLButtonDblClk    = 0x0203
	wmRButtonUp        = 0x0205
	wmUser             = 0x0400
	trayCallbackMsg    = wmUser + 17
	nimAdd             = 0x00000000
	nimDelete          = 0x00000002
	nifMessage         = 0x00000001
	nifIcon            = 0x00000002
	nifTip             = 0x00000004
	idiApplication     = 32512
	mfString           = 0x00000000
	mfSeparator        = 0x00000800
	tpmRightButton     = 0x00000002
	tpmReturnCmd       = 0x00000100
	trayMenuOpenWebUI  = 1001
	trayMenuQuit       = 1002
	trayStartupTimeout = 5 * time.Second
)

var (
	trayUser32              = syscall.NewLazyDLL("user32.dll")
	trayKernel32            = syscall.NewLazyDLL("kernel32.dll")
	trayShell32             = syscall.NewLazyDLL("shell32.dll")
	procAppendMenuW         = trayUser32.NewProc("AppendMenuW")
	procCreatePopupMenu     = trayUser32.NewProc("CreatePopupMenu")
	procCreateWindowExW     = trayUser32.NewProc("CreateWindowExW")
	procDefWindowProcW      = trayUser32.NewProc("DefWindowProcW")
	procDestroyMenu         = trayUser32.NewProc("DestroyMenu")
	procDestroyWindow       = trayUser32.NewProc("DestroyWindow")
	procDispatchMessageW    = trayUser32.NewProc("DispatchMessageW")
	procGetCursorPos        = trayUser32.NewProc("GetCursorPos")
	procGetMessageW         = trayUser32.NewProc("GetMessageW")
	procLoadIconW           = trayUser32.NewProc("LoadIconW")
	procPostMessageW        = trayUser32.NewProc("PostMessageW")
	procPostQuitMessage     = trayUser32.NewProc("PostQuitMessage")
	procRegisterClassExW    = trayUser32.NewProc("RegisterClassExW")
	procSetForegroundWindow = trayUser32.NewProc("SetForegroundWindow")
	procTrackPopupMenu      = trayUser32.NewProc("TrackPopupMenu")
	procTranslateMessage    = trayUser32.NewProc("TranslateMessage")
	procGetModuleHandleW    = trayKernel32.NewProc("GetModuleHandleW")
	procShellNotifyIconW    = trayShell32.NewProc("Shell_NotifyIconW")
)

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type notifyIconGUID struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

type notifyIconData struct {
	Size             uint32
	Wnd              uintptr
	ID               uint32
	Flags            uint32
	CallbackMessage  uint32
	Icon             uintptr
	Tip              [128]uint16
	State            uint32
	StateMask        uint32
	Info             [256]uint16
	TimeoutOrVersion uint32
	InfoTitle        [64]uint16
	InfoFlags        uint32
	GUIDItem         notifyIconGUID
	BalloonIcon      uintptr
}

type trayPoint struct {
	X int32
	Y int32
}

type trayMessage struct {
	HWnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      trayPoint
}

type trayIcon struct {
	app        *App
	url        string
	hwnd       uintptr
	wndProc    uintptr
	className  []uint16
	windowName []uint16
	ready      chan error
	done       chan struct{}
	stopOnce   sync.Once
	removeOnce sync.Once
}

func startTray(app *App, url string) (*trayIcon, error) {
	tray := &trayIcon{
		app:        app,
		url:        url,
		className:  syscall.StringToUTF16(fmt.Sprintf("WindowsUpdaterWebUITray-%d", os.Getpid())),
		windowName: syscall.StringToUTF16(appName),
		ready:      make(chan error, 1),
		done:       make(chan struct{}),
	}
	go tray.run()
	select {
	case err := <-tray.ready:
		if err != nil {
			return nil, err
		}
		return tray, nil
	case <-time.After(trayStartupTimeout):
		return nil, fmt.Errorf("tray startup timed out")
	}
}

func (tray *trayIcon) run() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(tray.done)

	instance, _, _ := procGetModuleHandleW.Call(0)
	tray.wndProc = syscall.NewCallback(tray.windowProc)
	wc := wndClassEx{
		WndProc:   tray.wndProc,
		Instance:  instance,
		ClassName: &tray.className[0],
	}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if atom, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 {
		tray.ready <- fmt.Errorf("register tray window class: %v", err)
		return
	}

	hwnd, _, err := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(&tray.className[0])),
		uintptr(unsafe.Pointer(&tray.windowName[0])),
		0,
		0, 0, 0, 0,
		0, 0,
		instance,
		0,
	)
	if hwnd == 0 {
		tray.ready <- fmt.Errorf("create tray window: %v", err)
		return
	}
	tray.hwnd = hwnd
	if err := tray.addIcon(); err != nil {
		_, _, _ = procDestroyWindow.Call(hwnd)
		tray.ready <- err
		return
	}
	tray.ready <- nil
	appLog("Tray icon started.")

	var message trayMessage
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		_, _, _ = procTranslateMessage.Call(uintptr(unsafe.Pointer(&message)))
		_, _, _ = procDispatchMessageW.Call(uintptr(unsafe.Pointer(&message)))
	}
	tray.removeIcon()
	appLog("Tray icon stopped.")
}

func (tray *trayIcon) Stop() {
	tray.stopOnce.Do(func() {
		if tray.hwnd != 0 {
			_, _, _ = procPostMessageW.Call(tray.hwnd, wmClose, 0, 0)
		}
		select {
		case <-tray.done:
		case <-time.After(2 * time.Second):
			tray.removeIcon()
		}
	})
}

func (tray *trayIcon) windowProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	switch message {
	case trayCallbackMsg:
		switch uint32(lParam) {
		case wmLButtonUp, wmLButtonDblClk:
			tray.openWebUI()
			return 0
		case wmRButtonUp:
			tray.showMenu(hwnd)
			return 0
		}
	case wmClose:
		tray.removeIcon()
		_, _, _ = procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		tray.removeIcon()
		_, _, _ = procPostQuitMessage.Call(0)
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wParam, lParam)
	return ret
}

func (tray *trayIcon) addIcon() error {
	var data notifyIconData
	data.Size = uint32(unsafe.Sizeof(data))
	data.Wnd = tray.hwnd
	data.ID = 1
	data.Flags = nifMessage | nifIcon | nifTip
	data.CallbackMessage = trayCallbackMsg
	icon, _, _ := procLoadIconW.Call(0, idiApplication)
	data.Icon = icon
	copy(data.Tip[:], syscall.StringToUTF16(appName))
	if ret, _, err := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&data))); ret == 0 {
		return fmt.Errorf("add tray icon: %v", err)
	}
	return nil
}

func (tray *trayIcon) removeIcon() {
	tray.removeOnce.Do(func() {
		if tray.hwnd == 0 {
			return
		}
		var data notifyIconData
		data.Size = uint32(unsafe.Sizeof(data))
		data.Wnd = tray.hwnd
		data.ID = 1
		_, _, _ = procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&data)))
	})
}

func (tray *trayIcon) openWebUI() {
	appLog("Tray requested opening the WebUI.")
	if err := openURL(tray.url); err != nil {
		appLog("Could not open WebUI from tray: %s", err)
	}
}

func (tray *trayIcon) quitApplication(hwnd uintptr) {
	go func() {
		tray.app.requestShutdown("Tray")
	}()
	tray.removeIcon()
	_, _, _ = procDestroyWindow.Call(hwnd)
}

func (tray *trayIcon) showMenu(hwnd uintptr) {
	menu, _, err := procCreatePopupMenu.Call()
	if menu == 0 {
		appLog("Could not create tray menu: %s", err)
		return
	}
	defer procDestroyMenu.Call(menu)

	openText := syscall.StringToUTF16("Open WebUI")
	quitText := syscall.StringToUTF16("Quit")
	_, _, _ = procAppendMenuW.Call(menu, mfString, trayMenuOpenWebUI, uintptr(unsafe.Pointer(&openText[0])))
	_, _, _ = procAppendMenuW.Call(menu, mfSeparator, 0, 0)
	_, _, _ = procAppendMenuW.Call(menu, mfString, trayMenuQuit, uintptr(unsafe.Pointer(&quitText[0])))

	var point trayPoint
	_, _, _ = procGetCursorPos.Call(uintptr(unsafe.Pointer(&point)))
	_, _, _ = procSetForegroundWindow.Call(hwnd)
	command, _, _ := procTrackPopupMenu.Call(
		menu,
		tpmRightButton|tpmReturnCmd,
		uintptr(uint32(point.X)),
		uintptr(uint32(point.Y)),
		0,
		hwnd,
		0,
	)
	switch command {
	case trayMenuOpenWebUI:
		tray.openWebUI()
	case trayMenuQuit:
		tray.quitApplication(hwnd)
	}
}
