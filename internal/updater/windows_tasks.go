package updater

import (
	"context"
	"os"
	"time"
)

const (
	taskStartup           = "WindowsUpdaterWebUI-Startup"
	taskAutoUpdate        = "WindowsUpdaterWebUI-AutoUpdate"
	defaultAutoUpdateTime = "03:00"
)

func taskExists(name string) bool {
	return taskExistsContext(context.Background(), name)
}

func taskExistsContext(ctx context.Context, name string) bool {
	return runCommandContext(ctx, 30*time.Second, "schtasks.exe", "/Query", "/TN", name, "/FO", "LIST").OK
}

func createStartupTask() CommandResult {
	return createStartupTaskDirect()
}

func createStartupTaskDirect() CommandResult {
	exe, _ := os.Executable()
	action := quoteArg(exe) + " --no-browser"
	return runCommand(60*time.Second, "schtasks.exe", "/Create", "/TN", taskStartup, "/TR", action, "/SC", "ONLOGON", "/RL", "LIMITED", "/F")
}

func createAutoUpdateTask() CommandResult {
	if !isAdmin() {
		return runElevatedWorkerOperation(context.Background(), elevatedWorkerInvocation{
			Operation: workerOperationAutoUpdateTask,
			Payload:   elevatedWorkerTaskPayload{Enabled: true},
		})
	}
	return createAutoUpdateTaskDirect()
}

func createAutoUpdateTaskDirect() CommandResult {
	exe, _ := os.Executable()
	action := quoteArg(exe) + " --task auto-update"
	return runCommand(60*time.Second, "schtasks.exe", "/Create", "/TN", taskAutoUpdate, "/TR", action, "/SC", "DAILY", "/ST", defaultAutoUpdateTime, "/RL", "HIGHEST", "/F")
}

func deleteTask(name string) CommandResult {
	if name == taskAutoUpdate && !isAdmin() {
		return runElevatedWorkerOperation(context.Background(), elevatedWorkerInvocation{
			Operation: workerOperationAutoUpdateTask,
			Payload:   elevatedWorkerTaskPayload{Enabled: false},
		})
	}
	return deleteTaskDirect(name)
}

func deleteTaskDirect(name string) CommandResult {
	if !taskExists(name) {
		return CommandResult{OK: true, Command: "delete " + name, Stdout: "Task did not exist."}
	}
	return runCommand(60*time.Second, "schtasks.exe", "/Delete", "/TN", name, "/F")
}

func setStartupTaskDirect(enabled bool) CommandResult {
	if enabled {
		return createStartupTaskDirect()
	}
	return deleteTaskDirect(taskStartup)
}

func setAutoUpdateTaskDirect(enabled bool) CommandResult {
	if enabled {
		return createAutoUpdateTaskDirect()
	}
	return deleteTaskDirect(taskAutoUpdate)
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
