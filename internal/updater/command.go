package updater

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	commandTimeoutCode       = 124
	commandCancelledCode     = 130
	commandLaunchFailureCode = 127
)

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

func runCommandContext(parentCtx context.Context, timeout time.Duration, args ...string) CommandResult {
	result := CommandResult{Command: strings.Join(args, " ")}
	logCategories := logCategoriesForCommand(args)
	commandLogCtx := withLogMetadata(parentCtx, logMetadata{CommandID: nextCommandLogID()})
	logCommand := func(stream, message string) {
		sessionLogs.AppendContext(commandLogCtx, stream, message, logCategories)
	}
	// launchFailureResult records an internal launch failure consistently: the
	// process never produced its own exit code, so we synthesize one and log it.
	launchFailureResult := func(message string) CommandResult {
		result.Code = commandLaunchFailureCode
		result.Stderr = message
		logCommand("stderr", message)
		logCommand("exit", fmt.Sprintf("%s exited with code %d", result.Command, commandLaunchFailureCode))
		return result
	}
	if len(args) == 0 {
		result.Stderr = "empty command"
		result.Code = commandLaunchFailureCode
		logCommand("command", "<empty command>")
		logCommand("stderr", result.Stderr)
		logCommand("exit", fmt.Sprintf("empty command exited with code %d", commandLaunchFailureCode))
		return result
	}
	commandCtx, cancel := context.WithTimeout(commandLogCtx, timeout)
	defer cancel()

	startedAt := time.Now()
	logCommand("command", result.Command)
	if isPackageManagerMutationCommand(args) {
		releasePackageOperation, err := defaultPackageMutationCoordinator.Acquire(commandCtx, func() {
			logCommand("app", "Waiting for another package operation before running "+result.Command)
		})
		if err != nil {
			return packageMutationLockFailureResult(commandCtx, result.Command, logCategories, err)
		}
		defer releasePackageOperation()
	}
	if shouldAcquireWingetCommandLock(args) {
		if !lockMutexContextWithWait(commandCtx, &wingetCommandMu, func() {
			logCommand("app", "Waiting for another winget mutation to finish before running "+result.Command)
		}) {
			return commandContextDoneResult(commandCtx, result.Command, "while waiting for winget lock", logCategories)
		}
		defer wingetCommandMu.Unlock()
	}

	processOwner, err := newCommandProcessOwner(shouldOwnCommandProcessTree(args))
	if err != nil {
		return launchFailureResult(err.Error())
	}
	if processOwner != nil {
		defer processOwner.Close()
	}

	commandProcess := exec.Command(args[0], args[1:]...)
	commandProcess.Env = launchEnv()
	commandProcess.SysProcAttr = hiddenSysProcAttr()
	stdoutTail := newBoundedOutputTail(commandResultStreamLimitBytes)
	stderrTail := newBoundedOutputTail(commandResultStreamLimitBytes)
	stdoutPipe, err := commandProcess.StdoutPipe()
	if err != nil {
		return launchFailureResult(err.Error())
	}
	stderrPipe, err := commandProcess.StderrPipe()
	if err != nil {
		return launchFailureResult(err.Error())
	}

	if err := commandProcess.Start(); err != nil {
		if commandCtx.Err() == context.DeadlineExceeded {
			result.Code = commandTimeoutCode
			result.Stderr = "Timed out."
			logCommand("stderr", result.Stderr)
			logCommand("exit", fmt.Sprintf("%s timed out before start", result.Command))
			return result
		}
		if commandCtx.Err() == context.Canceled {
			result.Code = commandCancelledCode
			result.Stderr = "Cancelled."
			logCommand("stderr", result.Stderr)
			logCommand("exit", fmt.Sprintf("%s cancelled before start", result.Command))
			return result
		}
		return launchFailureResult(err.Error())
	}
	if processOwner != nil {
		if err := processOwner.Assign(commandProcess); err != nil {
			terminateStartedCommand(commandProcess, processOwner)
			_ = commandProcess.Wait()
			return launchFailureResult(err.Error())
		}
	}

	var outputReaders sync.WaitGroup
	emitStdoutToSessionLog := !suppressCommandStdoutInSessionLog(args)
	outputReaders.Add(2)
	go streamCommandOutputContext(commandCtx, stdoutPipe, "stdout", stdoutTail, &outputReaders, logCategories, emitStdoutToSessionLog)
	go streamCommandOutputContext(commandCtx, stderrPipe, "stderr", stderrTail, &outputReaders, logCategories, true)
	err = waitForStartedCommand(commandCtx, commandProcess, processOwner)
	outputReaders.Wait()

	result.Stdout = stdoutTail.String()
	result.Stderr = stderrTail.String()
	logDetectionSummaryIfStdoutSuppressed := func() {
		if emitStdoutToSessionLog {
			return
		}
		logStoreDetectionCommandSummary(commandCtx, args, result, logCategories, time.Since(startedAt))
	}
	if commandCtx.Err() == context.DeadlineExceeded {
		result.Code = commandTimeoutCode
		result.Stderr += "\nTimed out."
		logCommand("stderr", "Timed out.")
		logDetectionSummaryIfStdoutSuppressed()
		logCommand("exit", fmt.Sprintf("%s exited with code %d", result.Command, commandTimeoutCode))
		return result
	}
	if commandCtx.Err() == context.Canceled {
		result.Code = commandCancelledCode
		result.Stderr += "\nCancelled."
		logCommand("stderr", "Cancelled.")
		logDetectionSummaryIfStdoutSuppressed()
		logCommand("exit", fmt.Sprintf("%s cancelled with code %d", result.Command, result.Code))
		return result
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.Code = exitErr.ExitCode()
		} else {
			result.Code = commandLaunchFailureCode
			if result.Stderr == "" {
				result.Stderr = err.Error()
			}
		}
		logDetectionSummaryIfStdoutSuppressed()
		logCommand("exit", fmt.Sprintf("%s exited with code %d", result.Command, result.Code))
		return result
	}
	result.OK = true
	logDetectionSummaryIfStdoutSuppressed()
	logCommand("exit", fmt.Sprintf("%s exited with code 0", result.Command))
	return result
}
