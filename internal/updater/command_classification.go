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

const (
	commandProcessorExecutable         = "cmd.exe"
	commandProcessorDisableAutoRunFlag = "/d"
	commandProcessorRunCommandFlag     = "/c"
)

func isWingetCommand(args []string) bool {
	return isPackageManagerCommand(args, managerWinget)
}

func isStoreCommand(args []string) bool {
	return isPackageManagerCommand(args, managerStore)
}

func isPackageManagerCommand(args []string, manager string) bool {
	if len(args) == 0 {
		return false
	}
	executableName := strings.ToLower(filepath.Base(args[0]))
	if isPackageManagerExecutable(executableName, manager) {
		return true
	}
	return hasCmdRunPrefix(executableName, args, 4) && strings.EqualFold(args[3], manager)
}

func isPackageManagerExecutable(executableName, manager string) bool {
	return executableName == manager || executableName == manager+".exe"
}

func hasCmdRunPrefix(executableName string, args []string, minArgs int) bool {
	return executableName == commandProcessorExecutable &&
		len(args) >= minArgs &&
		strings.EqualFold(args[1], commandProcessorDisableAutoRunFlag) &&
		strings.EqualFold(args[2], commandProcessorRunCommandFlag)
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
	executableName := packageManagerNameFromArg(args[0])
	if hasCmdRunPrefix(executableName, args, 5) {
		return packageManagerIDFromExecutable(packageManagerNameFromArg(args[3])), strings.ToLower(args[4]), args[5:]
	}
	if len(args) < 2 {
		return packageManagerIDFromExecutable(executableName), "", nil
	}
	return packageManagerIDFromExecutable(executableName), strings.ToLower(args[1]), args[2:]
}

func packageManagerNameFromArg(arg string) string {
	arg = strings.Trim(strings.TrimSpace(arg), `"'`)
	return strings.ToLower(filepath.Base(arg))
}

func packageManagerIDFromExecutable(executableName string) string {
	return strings.TrimSuffix(executableName, ".exe")
}

func isPackageManagerMutationCommand(args []string) bool {
	manager, verb, remainingArgs := packageManagerCommandParts(args)
	switch manager {
	case managerWinget:
		if verb == "upgrade" {
			return wingetUpgradeArgsIncludeMutationTarget(remainingArgs)
		}
		if verb == "source" {
			return wingetSourceSubcommandMutates(remainingArgs)
		}
		return verb == "install" || verb == "uninstall" || verb == "import" || verb == "configure"
	case managerStore:
		if (verb == "update" || verb == "updates") && commandHasApplyFalse(args) {
			return false
		}
		return verb == "install" || verb == "update" || verb == "updates" || verb == "uninstall"
	case managerChoco:
		return verb == "install" || verb == "upgrade" || verb == "uninstall" || verb == "pin"
	default:
		return false
	}
}

func wingetSourceSubcommandMutates(sourceArgs []string) bool {
	if len(sourceArgs) == 0 {
		return false
	}
	subcommand := strings.ToLower(strings.TrimSpace(sourceArgs[0]))
	return subcommand == "update" || subcommand == "reset"
}

func wingetUpgradeArgsIncludeMutationTarget(upgradeArgs []string) bool {
	skipOptionValue := false
	for _, arg := range upgradeArgs {
		if skipOptionValue {
			skipOptionValue = false
			continue
		}
		normalizedArg := strings.ToLower(strings.TrimSpace(arg))
		if normalizedArg == "" {
			continue
		}
		switch normalizedArg {
		case "--all":
			return true
		case "--id", "--name":
			return true
		case "--source", "-s", "--scope", "--locale", "--version", "--manifest", "--custom", "--location", "--log", "--authentication-mode", "--authentication-account":
			skipOptionValue = true
			continue
		}
		if strings.HasPrefix(normalizedArg, "--id=") || strings.HasPrefix(normalizedArg, "--name=") {
			return true
		}
		if strings.HasPrefix(normalizedArg, "-") {
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
	for argIndex, arg := range args {
		if !strings.EqualFold(arg, "--apply") {
			continue
		}
		return argIndex+1 < len(args) && strings.EqualFold(args[argIndex+1], "false")
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
		sessionLogs.AppendContext(ctx, stream, message, categories)
	}
	completionVerb := "cancelled"
	switch ctx.Err() {
	case context.DeadlineExceeded:
		result.Code = 124
		result.Stderr = "Timed out."
		completionVerb = "timed out"
	default:
		result.Code = commandCancelledCode
		result.Stderr = "Cancelled."
	}
	logCommand("stderr", result.Stderr)
	logCommand("exit", fmt.Sprintf("%s %s %s", command, completionVerb, action))
	return result
}
