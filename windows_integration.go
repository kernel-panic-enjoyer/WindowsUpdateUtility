package main

import (
	"os"
	"os/exec"
	"syscall"
	"time"
	"unsafe"
)

const (
	taskStartup           = "WindowsUpdaterWebUI-Startup"
	taskAutoUpdate        = "WindowsUpdaterWebUI-AutoUpdate"
	defaultAutoUpdateTime = "03:00"

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
	proc := shcore.NewProc("SetProcessDpiAwareness")
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

func isAdmin() bool {
	shell32 := syscall.NewLazyDLL("shell32.dll")
	proc := shell32.NewProc("IsUserAnAdmin")
	ret, _, _ := proc.Call()
	return ret != 0
}

func shellExecuteRunas(file string, params string) error {
	shell32 := syscall.NewLazyDLL("shell32.dll")
	proc := shell32.NewProc("ShellExecuteW")
	verb, _ := syscall.UTF16PtrFromString("runas")
	target, _ := syscall.UTF16PtrFromString(file)
	parameters, _ := syscall.UTF16PtrFromString(params)
	dir, _ := syscall.UTF16PtrFromString(appRoot())
	ret, _, err := proc.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(target)), uintptr(unsafe.Pointer(parameters)), uintptr(unsafe.Pointer(dir)), 0)
	if ret <= 32 {
		return err
	}
	return nil
}

func quoteArg(arg string) string {
	return syscall.EscapeArg(arg)
}

func taskExists(name string) bool {
	return runCommand(30*time.Second, "schtasks.exe", "/Query", "/TN", name, "/FO", "LIST").OK
}

func createStartupTask() CommandResult {
	exe, _ := os.Executable()
	action := quoteArg(exe) + " --no-browser"
	return runCommand(60*time.Second, "schtasks.exe", "/Create", "/TN", taskStartup, "/TR", action, "/SC", "ONLOGON", "/RL", "HIGHEST", "/F")
}

func createAutoUpdateTask() CommandResult {
	exe, _ := os.Executable()
	action := quoteArg(exe) + " --task auto-update"
	return runCommand(60*time.Second, "schtasks.exe", "/Create", "/TN", taskAutoUpdate, "/TR", action, "/SC", "DAILY", "/ST", defaultAutoUpdateTime, "/RL", "HIGHEST", "/F")
}

func deleteTask(name string) CommandResult {
	if !taskExists(name) {
		return CommandResult{OK: true, Command: "delete " + name, Stdout: "Task did not exist."}
	}
	return runCommand(60*time.Second, "schtasks.exe", "/Delete", "/TN", name, "/F")
}

func setStartup(enabled bool) CommandResult {
	appLog("Startup task update started: enabled=%t.", enabled)
	var result CommandResult
	if enabled {
		result = createStartupTask()
	} else {
		result = deleteTask(taskStartup)
	}
	appLog("Startup task update finished with code %d.", result.Code)
	return result
}

func setAutoUpdate(global *bool, packageKeys []string, packageEnabled *bool) (State, CommandResult) {
	appLog("Auto-update settings update started.")
	state := loadState()
	if state.AutoUpdatePackages == nil {
		state.AutoUpdatePackages = map[string]bool{}
	}
	if global != nil {
		state.AutoUpdateGlobal = *global
	}
	if packageEnabled != nil {
		for _, key := range packageKeys {
			if _, _, err := splitPackageKey(key); err == nil {
				state.AutoUpdatePackages[normalizeAutoUpdatePackageKey(key)] = *packageEnabled
			}
		}
	}
	_ = saveState(state)
	var result CommandResult
	if state.AutoUpdateGlobal {
		result = createAutoUpdateTask()
	} else {
		result = deleteTask(taskAutoUpdate)
	}
	appLog("Auto-update settings update finished with code %d.", result.Code)
	return state, result
}

func runAutoUpdate() []UpdateResult {
	appLog("Scheduled auto-update task started.")
	state := loadState()
	if !state.AutoUpdateGlobal {
		appLog("Scheduled auto-update skipped because global auto-update is disabled.")
		return nil
	}
	var selected []string
	for key, enabled := range state.AutoUpdatePackages {
		if enabled {
			selected = append(selected, key)
		}
	}
	if len(selected) == 0 {
		appLog("Scheduled auto-update skipped because no packages are opted in.")
		state.LastAutoUpdateAt = utcNow()
		state.LastAutoUpdateResults = nil
		_ = saveState(state)
		return nil
	}
	results := updateAll(selected)
	state.LastAutoUpdateAt = utcNow()
	state.LastAutoUpdateResults = results
	_ = saveState(state)
	appLog("Scheduled auto-update task finished with %d result(s).", len(results))
	return results
}

func openURL(url string) error {
	cmd := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", url)
	cmd.SysProcAttr = hiddenSysProcAttr()
	return cmd.Start()
}
