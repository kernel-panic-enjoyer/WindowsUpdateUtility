//go:build windows

package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

func launchSelfUpdateApply(ctx context.Context, artifact selfUpdateArtifact, targetPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if artifact.Path == "" || artifact.SHA256 == "" {
		return errors.New("self-update artifact is incomplete")
	}
	args := []string{
		flagSelfUpdateApply,
		"--self-update-target=" + targetPath,
		fmt.Sprintf("--self-update-parent-pid=%d", os.Getpid()),
		"--self-update-sha256=" + artifact.SHA256,
		"--self-update-restart=true",
	}
	cmd := exec.Command(artifact.Path, args...)
	cmd.Env = launchEnv()
	cmd.SysProcAttr = hiddenSysProcAttr()
	return cmd.Start()
}

func runSelfUpdateApply(request selfUpdateApplyRequest) error {
	if err := validateSelfUpdateApplyRequest(request); err != nil {
		return err
	}
	if request.ParentPID > 0 {
		if err := waitForParentExit(request.ParentPID, selfUpdateApplyTimeout); err != nil {
			return err
		}
	}
	if err := replaceExecutableForSelfUpdate(request); err != nil {
		if !request.Elevated && isSelfUpdatePermissionError(err) {
			return relaunchSelfUpdateApplyElevated(request)
		}
		return err
	}
	if request.Restart {
		return restartSelfUpdatedApp(request.TargetPath)
	}
	return nil
}

func waitForParentExit(pid int, timeout time.Duration) error {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return nil
		}
		return err
	}
	defer windows.CloseHandle(handle)
	deadline := uint32(timeout / time.Millisecond)
	result, err := windows.WaitForSingleObject(handle, deadline)
	if err != nil {
		return err
	}
	if result == uint32(windows.WAIT_TIMEOUT) {
		return fmt.Errorf("timed out waiting for parent process %d", pid)
	}
	return nil
}

func relaunchSelfUpdateApplyElevated(request selfUpdateApplyRequest) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	request.Elevated = true
	args := selfUpdateApplyArgs(request)
	process, err := shellExecuteRunasProcess(exe, strings.Join(quoteArgs(args), " "))
	if err != nil {
		return err
	}
	if process != 0 {
		_ = windows.CloseHandle(process)
	}
	return nil
}

func selfUpdateApplyArgs(request selfUpdateApplyRequest) []string {
	args := []string{
		flagSelfUpdateApply,
		"--self-update-target=" + request.TargetPath,
		fmt.Sprintf("--self-update-parent-pid=%d", request.ParentPID),
		"--self-update-sha256=" + request.ExpectedSHA256,
		fmt.Sprintf("--self-update-restart=%t", request.Restart),
	}
	if request.Elevated {
		args = append(args, "--self-update-elevated")
	}
	return args
}

func restartSelfUpdatedApp(targetPath string) error {
	if isAdmin() {
		cmd := exec.Command("explorer.exe", targetPath)
		cmd.SysProcAttr = hiddenSysProcAttr()
		return cmd.Start()
	}
	cmd := exec.Command(targetPath)
	cmd.SysProcAttr = hiddenSysProcAttr()
	return cmd.Start()
}

func isSelfUpdatePermissionError(err error) bool {
	for err != nil {
		if errors.Is(err, os.ErrPermission) {
			return true
		}
		if errno, ok := err.(syscall.Errno); ok && (errno == syscall.Errno(windows.ERROR_ACCESS_DENIED) || errno == syscall.Errno(windows.ERROR_SHARING_VIOLATION)) {
			return true
		}
		err = errors.Unwrap(err)
	}
	return false
}

func selfUpdateApplyRequestFromOptions(options cliOptions) selfUpdateApplyRequest {
	return selfUpdateApplyRequest{
		SourcePath:     executablePathOrEmpty(),
		TargetPath:     options.SelfUpdateTarget,
		ExpectedSHA256: strings.ToLower(strings.TrimSpace(options.SelfUpdateSHA256)),
		ParentPID:      options.SelfUpdateParentPID,
		Restart:        options.SelfUpdateRestart,
		Elevated:       options.SelfUpdateElevated,
	}
}

func executablePathOrEmpty() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return exe
}

func parseSelfUpdateParentPID(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, errors.New("self-update parent PID is required")
	}
	pid, err := strconv.Atoi(raw)
	if err != nil || pid < 0 {
		return 0, fmt.Errorf("invalid self-update parent PID %q", raw)
	}
	return pid, nil
}
