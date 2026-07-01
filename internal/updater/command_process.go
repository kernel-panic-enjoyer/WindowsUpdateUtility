package updater

import (
	"context"
	"os/exec"
)

func waitForStartedCommand(ctx context.Context, startedCommand *exec.Cmd, processOwner *commandProcessOwner) error {
	waitResult := make(chan error, 1)
	go func() {
		waitResult <- startedCommand.Wait()
	}()
	select {
	case err := <-waitResult:
		return err
	case <-ctx.Done():
		terminateStartedCommand(startedCommand, processOwner)
		return <-waitResult
	}
}

func terminateStartedCommand(startedCommand *exec.Cmd, processOwner *commandProcessOwner) {
	if processOwner != nil {
		processOwner.Terminate()
		return
	}
	if startedCommand == nil || startedCommand.Process == nil {
		return
	}
	_ = startedCommand.Process.Kill()
}
