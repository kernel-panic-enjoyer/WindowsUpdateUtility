package updater

import "syscall"

const (
	dpiAwarenessAlreadySet       = uintptr(0x80070005)
	processPerMonitorDPIAware    = uintptr(2)
	processDPIUnawareFallbackLog = "DPI awareness could not be set; Windows may scale tray menus."
)

var dpiAwarenessContextPerMonitorAwareV2 = ^uintptr(3)

func enableDPIAwareness() {
	if setProcessDPIAwarenessContext() {
		appLog("DPI awareness set to per-monitor v2.")
		return
	}
	if setProcessDPIAwareness() {
		appLog("DPI awareness set to per-monitor.")
		return
	}
	if setProcessDPIAware() {
		appLog("DPI awareness set to system-aware fallback.")
		return
	}
	appLog(processDPIUnawareFallbackLog)
}

func setProcessDPIAwarenessContext() bool {
	user32 := syscall.NewLazyDLL("user32.dll")
	proc := user32.NewProc("SetProcessDpiAwarenessContext")
	if err := proc.Find(); err != nil {
		return false
	}
	ret, _, _ := proc.Call(dpiAwarenessContextPerMonitorAwareV2)
	return ret != 0
}

func setProcessDPIAwareness() bool {
	shcore := syscall.NewLazyDLL("shcore.dll")
	proc := shcore.NewProc("SetProcessDPIAwareness")
	if err := proc.Find(); err != nil {
		return false
	}
	ret, _, _ := proc.Call(processPerMonitorDPIAware)
	return ret == 0 || ret == dpiAwarenessAlreadySet
}

func setProcessDPIAware() bool {
	user32 := syscall.NewLazyDLL("user32.dll")
	proc := user32.NewProc("SetProcessDPIAware")
	if err := proc.Find(); err != nil {
		return false
	}
	ret, _, _ := proc.Call()
	return ret != 0
}
