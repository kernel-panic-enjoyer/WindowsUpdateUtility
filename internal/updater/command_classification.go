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

func shouldOwnCommandProcessTree(args []string) bool {
	return isPackageManagerMutationCommand(args)
}

func packageManagerCommandVerb(args []string) (string, string) {
	manager, verb, _ := packageManagerCommandParts(args)
	return manager, verb
}

func packageManagerCommandParts(args []string) (string, string, []string) {
	if len(args) == 0 {
		return "", "", nil
	}
	name := packageManagerNameFromArg(args[0])
	if name == "cmd.exe" && len(args) >= 5 && strings.EqualFold(args[1], "/d") && strings.EqualFold(args[2], "/c") {
		return strings.TrimSuffix(packageManagerNameFromArg(args[3]), ".exe"), strings.ToLower(args[4]), args[5:]
	}
	if len(args) < 2 {
		return strings.TrimSuffix(name, ".exe"), "", nil
	}
	return strings.TrimSuffix(name, ".exe"), strings.ToLower(args[1]), args[2:]
}

func packageManagerNameFromArg(arg string) string {
	arg = strings.Trim(strings.TrimSpace(arg), `"'`)
	return strings.ToLower(filepath.Base(arg))
}

func isPackageManagerMutationCommand(args []string) bool {
	manager, verb, tail := packageManagerCommandParts(args)
	switch manager {
	case "winget":
		if verb == "upgrade" {
			return wingetUpgradeHasMutationTarget(tail)
		}
		if verb == "source" {
			return wingetSourceCommandMutates(tail)
		}
		return verb == "install" || verb == "uninstall" || verb == "import" || verb == "configure"
	case "store":
		if (verb == "update" || verb == "updates") && commandHasApplyFalse(args) {
			return false
		}
		return verb == "install" || verb == "update" || verb == "updates" || verb == "uninstall"
	case "choco":
		return verb == "install" || verb == "upgrade" || verb == "uninstall" || verb == "pin"
	default:
		return false
	}
}

func wingetSourceCommandMutates(args []string) bool {
	if len(args) == 0 {
		return false
	}
	action := strings.ToLower(strings.TrimSpace(args[0]))
	return action == "update" || action == "reset"
}

func wingetUpgradeHasMutationTarget(args []string) bool {
	skipNext := false
	for _, arg := range args {
		if skipNext {
			skipNext = false
			continue
		}
		lower := strings.ToLower(strings.TrimSpace(arg))
		if lower == "" {
			continue
		}
		switch lower {
		case "--all":
			return true
		case "--id", "--name":
			return true
		case "--source", "-s", "--scope", "--locale", "--version", "--manifest", "--custom", "--location", "--log", "--authentication-mode", "--authentication-account":
			skipNext = true
			continue
		}
		if strings.HasPrefix(lower, "--id=") || strings.HasPrefix(lower, "--name=") {
			return true
		}
		if strings.HasPrefix(lower, "-") {
			continue
		}
		return true
	}
	return false
}

func shouldAcquireWingetCommandLock(args []string) bool {
	return isWingetCommand(args) && isPackageManagerMutationCommand(args)
}

func commandHasApplyFalse(args []string) bool {
	for index, arg := range args {
		if !strings.EqualFold(arg, "--apply") {
			continue
		}
		return index+1 < len(args) && strings.EqualFold(args[index+1], "false")
	}
	return false
}

func lockMutexContext(ctx context.Context, mu *sync.Mutex) bool {
	return lockMutexContextWithWait(ctx, mu, nil)
}

func lockMutexContextWithWait(ctx context.Context, mu *sync.Mutex, onWait func()) bool {
	select {
	case <-ctx.Done():
		return false
	default:
	}
	if mu.TryLock() {
		return true
	}
	if onWait != nil {
		onWait()
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
	logCommand("stderr", result.Stderr)
	logCommand("exit", fmt.Sprintf("%s %s %s", command, verb, action))
	return result
}
