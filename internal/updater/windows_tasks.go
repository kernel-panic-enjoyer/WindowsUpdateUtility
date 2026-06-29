package updater

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	exe, err := osExecutable()
	if err != nil {
		return validationCommandResult("startup task", err)
	}
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
	exe, err := osExecutable()
	if err != nil {
		return validationCommandResult("auto-update task", err)
	}
	if err := validateAutoUpdateTaskExecutable(exe); err != nil {
		return validationCommandResult("auto-update task", err)
	}
	action := quoteArg(exe) + " --task auto-update"
	return runCommand(60*time.Second, "schtasks.exe", "/Create", "/TN", taskAutoUpdate, "/TR", action, "/SC", "DAILY", "/ST", defaultAutoUpdateTime, "/RL", "HIGHEST", "/F")
}

func validateAutoUpdateTaskExecutable(exe string) error {
	if pathWithinAnyRoot(exe, trustedAutoUpdateTaskRoots()) {
		return nil
	}
	return errors.New("auto-update task requires the executable to be installed under Program Files or Windows")
}

func trustedAutoUpdateTaskRoots() []string {
	return []string{
		os.Getenv("ProgramFiles"),
		os.Getenv("ProgramFiles(x86)"),
		os.Getenv("SystemRoot"),
	}
}

func pathWithinAnyRoot(path string, roots []string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	candidate, err := filepath.Abs(path)
	if err != nil {
		candidate = path
	}
	candidate = filepath.Clean(candidate)
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		rootPath, err := filepath.Abs(root)
		if err != nil {
			rootPath = root
		}
		rootPath = filepath.Clean(rootPath)
		rel, err := filepath.Rel(rootPath, candidate)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue
		}
		return true
	}
	return false
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
