//go:build windows

package updater

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestCommandProcessOwnerTerminatesGrandchildProcessTree(t *testing.T) {
	testDir := t.TempDir()
	grandchildStartGatePath := testDir + `\gate.txt`
	grandchildPIDPath := testDir + `\child.pid`
	launcherScript := `
$gate = ` + quotePowerShellSingleQuotedString(grandchildStartGatePath) + `
$pidPath = ` + quotePowerShellSingleQuotedString(grandchildPIDPath) + `
while (!(Test-Path -LiteralPath $gate)) { Start-Sleep -Milliseconds 25 }
$child = Start-Process powershell.exe -PassThru -WindowStyle Hidden -ArgumentList '-NoProfile','-ExecutionPolicy','Bypass','-Command','Start-Sleep -Seconds 60'
Set-Content -LiteralPath $pidPath -Value $child.Id
Start-Sleep -Seconds 60
`
	parentCommand := exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", launcherScript)
	parentCommand.SysProcAttr = hiddenSysProcAttr()
	processOwner, err := newCommandProcessOwner(true)
	if err != nil {
		t.Fatal(err)
	}
	defer processOwner.Close()
	if err := parentCommand.Start(); err != nil {
		t.Fatal(err)
	}
	defer terminateStartedCommand(parentCommand, processOwner)
	if err := processOwner.Assign(parentCommand); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(grandchildStartGatePath, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	grandchildPID := waitForProcessIDFile(t, grandchildPIDPath)
	if !isProcessRunning(grandchildPID) {
		t.Fatalf("expected grandchild process %d to be running before termination", grandchildPID)
	}

	processOwner.Terminate()
	commandExited := make(chan error, 1)
	go func() { commandExited <- parentCommand.Wait() }()
	select {
	case <-commandExited:
	case <-time.After(5 * time.Second):
		t.Fatal("owned process did not exit after job termination")
	}
	grandchildExitDeadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(grandchildExitDeadline) {
		if !isProcessRunning(grandchildPID) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("grandchild process %d survived job termination", grandchildPID)
}

func quotePowerShellSingleQuotedString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func waitForProcessIDFile(t *testing.T, path string) uint32 {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err != nil {
			time.Sleep(25 * time.Millisecond)
			continue
		}

		processID, parseErr := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 32)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		return uint32(processID)
	}
	t.Fatalf("pid file %s was not written", path)
	return 0
}

func isProcessRunning(processID uint32) bool {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, processID)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	waitResult, err := windows.WaitForSingleObject(handle, 0)
	return err == nil && waitResult == uint32(windows.WAIT_TIMEOUT)
}

func TestRunCommandContextCancellationTerminatesOwnedPackageProcessTree(t *testing.T) {
	cancelledContext, cancel := context.WithCancel(context.Background())
	cancel()
	commandResult := runCommandContext(cancelledContext, time.Minute, "cmd.exe", "/d", "/c", "winget", "--version")
	if commandResult.Code != commandCancelledCode {
		t.Fatalf("expected cancelled owned command, got %#v", commandResult)
	}
}
