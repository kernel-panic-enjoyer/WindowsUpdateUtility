package updater

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var wingetCommandMu sync.Mutex
var packageManagerMutationMu sync.Mutex

func isWingetCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	name := strings.ToLower(filepath.Base(args[0]))
	if name == "winget" || name == "winget.exe" {
		return true
	}
	if name == "cmd.exe" && len(args) >= 4 && strings.EqualFold(args[1], "/d") && strings.EqualFold(args[2], "/c") && strings.EqualFold(args[3], "winget") {
		return true
	}
	return false
}

func isStoreCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	name := strings.ToLower(filepath.Base(args[0]))
	if name == "store" || name == "store.exe" {
		return true
	}
	return name == "cmd.exe" && len(args) >= 4 && strings.EqualFold(args[1], "/d") && strings.EqualFold(args[2], "/c") && strings.EqualFold(args[3], "store")
}

func packageManagerCommandVerb(args []string) (string, string) {
	if len(args) == 0 {
		return "", ""
	}
	name := strings.ToLower(filepath.Base(args[0]))
	if name == "cmd.exe" && len(args) >= 5 && strings.EqualFold(args[1], "/d") && strings.EqualFold(args[2], "/c") {
		return strings.ToLower(args[3]), strings.ToLower(args[4])
	}
	if len(args) < 2 {
		return strings.TrimSuffix(name, ".exe"), ""
	}
	return strings.TrimSuffix(name, ".exe"), strings.ToLower(args[1])
}

func isPackageManagerMutationCommand(args []string) bool {
	manager, verb := packageManagerCommandVerb(args)
	switch manager {
	case "winget":
		return verb == "install" || verb == "upgrade" || verb == "uninstall" || verb == "import" || verb == "configure"
	case "store":
		return verb == "install" || verb == "update" || verb == "updates" || verb == "uninstall"
	case "choco":
		return verb == "install" || verb == "upgrade" || verb == "uninstall" || verb == "pin"
	default:
		return false
	}
}

func lockMutexContext(ctx context.Context, mu *sync.Mutex) bool {
	select {
	case <-ctx.Done():
		return false
	default:
	}
	if mu.TryLock() {
		return true
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			if mu.TryLock() {
				return true
			}
		}
	}
}

func commandContextDoneResult(ctx context.Context, command, action string, categories []string) CommandResult {
	result := CommandResult{Command: command}
	logCommand := func(stream, message string) {
		sessionLogs.AppendCategorized(stream, message, categories)
	}
	verb := "cancelled"
	switch ctx.Err() {
	case context.DeadlineExceeded:
		result.Code = 124
		result.Stderr = "Timed out."
		verb = "timed out"
	default:
		result.Code = commandCancelledCode
		result.Stderr = "Cancelled."
	}
	logCommand("command", command)
	logCommand("stderr", result.Stderr)
	logCommand("exit", fmt.Sprintf("%s %s %s", command, verb, action))
	return result
}
