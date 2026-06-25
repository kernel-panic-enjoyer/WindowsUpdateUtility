package updater

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const commandCancelledCode = 130

type CommandResult struct {
	OK      bool   `json:"ok"`
	Code    int    `json:"code"`
	Stdout  string `json:"stdout"`
	Stderr  string `json:"stderr"`
	Command string `json:"command"`
}

func validationCommandResult(command string, err error) CommandResult {
	return CommandResult{Code: 2, Stderr: err.Error(), Command: command}
}

func runCommand(timeout time.Duration, args ...string) CommandResult {
	return runCommandContext(context.Background(), timeout, args...)
}

func runCommandContext(parent context.Context, timeout time.Duration, args ...string) CommandResult {
	result := CommandResult{Command: strings.Join(args, " ")}
	categories := logCategoriesForCommand(args)
	logCommand := func(stream, message string) {
		sessionLogs.AppendCategorized(stream, message, categories)
	}
	// fail127 records an internal launch failure (code 127) consistently: the
	// process never produced its own exit code, so we synthesize one and log it.
	fail127 := func(message string) CommandResult {
		result.Code = 127
		result.Stderr = message
		logCommand("stderr", message)
		logCommand("exit", fmt.Sprintf("%s exited with code 127", result.Command))
		return result
	}
	if len(args) == 0 {
		result.Stderr = "empty command"
		result.Code = 127
		logCommand("command", "<empty command>")
		logCommand("stderr", result.Stderr)
		logCommand("exit", "empty command exited with code 127")
		return result
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	logCommand("command", result.Command)
	mutationCommand := isPackageManagerMutationCommand(args)
	if mutationCommand {
		releasePackageOperation, err := defaultPackageMutationCoordinator.Acquire(ctx, func() {
			logCommand("app", "Waiting for another package operation before running "+result.Command)
		})
		if err != nil {
			return packageMutationLockFailureResult(ctx, result.Command, categories, err)
		}
		defer releasePackageOperation()
	}
	if shouldAcquireWingetCommandLock(args) {
		if !lockMutexContextWithWait(ctx, &wingetCommandMu, func() {
			logCommand("app", "Waiting for another winget mutation to finish before running "+result.Command)
		}) {
			return commandContextDoneResult(ctx, result.Command, "while waiting for winget lock", categories)
		}
		defer wingetCommandMu.Unlock()
	}

	owner, ownerErr := newCommandProcessOwner(shouldOwnCommandProcessTree(args))
	if ownerErr != nil {
		return fail127(ownerErr.Error())
	}
	if owner != nil {
		defer owner.Close()
	}

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = launchEnv()
	cmd.SysProcAttr = hiddenSysProcAttr()
	stdout := newBoundedOutputTail(commandResultStreamLimitBytes)
	stderr := newBoundedOutputTail(commandResultStreamLimitBytes)
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fail127(err.Error())
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return fail127(err.Error())
	}

	if err := cmd.Start(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			result.Code = 124
			result.Stderr = "Timed out."
			logCommand("stderr", result.Stderr)
			logCommand("exit", fmt.Sprintf("%s timed out before start", result.Command))
			return result
		}
		if ctx.Err() == context.Canceled {
			result.Code = commandCancelledCode
			result.Stderr = "Cancelled."
			logCommand("stderr", result.Stderr)
			logCommand("exit", fmt.Sprintf("%s cancelled before start", result.Command))
			return result
		}
		return fail127(err.Error())
	}
	if owner != nil {
		ownerErr = owner.Assign(cmd)
	}
	if ownerErr != nil {
		terminateStartedCommand(cmd, owner)
		_ = cmd.Wait()
		return fail127(ownerErr.Error())
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go streamCommandOutputCategorized(stdoutPipe, "stdout", stdout, &wg, categories)
	go streamCommandOutputCategorized(stderrPipe, "stderr", stderr, &wg, categories)
	err = waitForStartedCommand(ctx, cmd, owner)
	wg.Wait()

	result.Stdout = stdout.String()
	result.Stderr = stderr.String()
	if ctx.Err() == context.DeadlineExceeded {
		result.Code = 124
		result.Stderr += "\nTimed out."
		logCommand("stderr", "Timed out.")
		logCommand("exit", fmt.Sprintf("%s exited with code 124", result.Command))
		return result
	}
	if ctx.Err() == context.Canceled {
		result.Code = commandCancelledCode
		result.Stderr += "\nCancelled."
		logCommand("stderr", "Cancelled.")
		logCommand("exit", fmt.Sprintf("%s cancelled with code %d", result.Command, result.Code))
		return result
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.Code = exitErr.ExitCode()
		} else {
			result.Code = 127
			if result.Stderr == "" {
				result.Stderr = err.Error()
			}
		}
		logCommand("exit", fmt.Sprintf("%s exited with code %d", result.Command, result.Code))
		return result
	}
	result.OK = true
	logCommand("exit", fmt.Sprintf("%s exited with code 0", result.Command))
	return result
}
