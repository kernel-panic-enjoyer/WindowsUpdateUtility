package updater

import (
	"os"
	"time"
)

const (
	taskStartup           = "WindowsUpdaterWebUI-Startup"
	taskAutoUpdate        = "WindowsUpdaterWebUI-AutoUpdate"
	defaultAutoUpdateTime = "03:00"
)

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

var createStartupTaskRunner = createStartupTask
var createAutoUpdateTaskRunner = createAutoUpdateTask
var deleteTaskRunner = deleteTask

func setStartup(enabled bool) CommandResult {
	appLog("Startup task update started: enabled=%t.", enabled)
	var result CommandResult
	if enabled {
		result = createStartupTaskRunner()
	} else {
		result = deleteTaskRunner(taskStartup)
	}
	appLog("Startup task update finished with code %d.", result.Code)
	return result
}
