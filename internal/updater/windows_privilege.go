package updater

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func isAdmin() bool {
	shell32 := syscall.NewLazyDLL("shell32.dll")
	proc := shell32.NewProc("IsUserAnAdmin")
	ret, _, _ := proc.Call()
	return ret != 0
}

func shellExecuteRunas(file string, params string) error {
	shell32 := syscall.NewLazyDLL("shell32.dll")
	proc := shell32.NewProc("ShellExecuteW")
	verb, _ := syscall.UTF16PtrFromString("runas")
	target, _ := syscall.UTF16PtrFromString(file)
	parameters, _ := syscall.UTF16PtrFromString(params)
	dir, _ := syscall.UTF16PtrFromString(appRoot())
	ret, _, err := proc.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(target)), uintptr(unsafe.Pointer(parameters)), uintptr(unsafe.Pointer(dir)), 0)
	if ret <= 32 {
		return err
	}
	return nil
}

type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         uintptr
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     uintptr
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    uintptr
	dwHotKey     uint32
	hIcon        uintptr
	hProcess     windows.Handle
}

const seeMaskNoCloseProcess = 0x00000040
const tokenElevationTypeLimited = 3

func shellExecuteRunasProcess(file string, params string) (windows.Handle, error) {
	shell32 := syscall.NewLazyDLL("shell32.dll")
	proc := shell32.NewProc("ShellExecuteExW")
	verb, _ := syscall.UTF16PtrFromString("runas")
	target, _ := syscall.UTF16PtrFromString(file)
	parameters, _ := syscall.UTF16PtrFromString(params)
	dir, _ := syscall.UTF16PtrFromString(appRoot())
	info := shellExecuteInfo{
		cbSize:       uint32(unsafe.Sizeof(shellExecuteInfo{})),
		fMask:        seeMaskNoCloseProcess,
		lpVerb:       verb,
		lpFile:       target,
		lpParameters: parameters,
		lpDirectory:  dir,
		nShow:        0,
	}
	ret, _, err := proc.Call(uintptr(unsafe.Pointer(&info)))
	if ret == 0 {
		if err != syscall.Errno(0) {
			return 0, err
		}
		return 0, fmt.Errorf("ShellExecuteExW failed")
	}
	return info.hProcess, nil
}

func quoteArg(arg string) string {
	return syscall.EscapeArg(arg)
}

func currentUserSID() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	if user == nil || user.User.Sid == nil {
		return "", fmt.Errorf("current process token has no user SID")
	}
	return user.User.Sid.String(), nil
}

func currentSessionID() (uint32, error) {
	var sessionID uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &sessionID); err != nil {
		return 0, err
	}
	return sessionID, nil
}

func currentUserCanElevateSameUser() bool {
	if isAdmin() {
		return false
	}
	var elevationType uint32
	var outLen uint32
	err := windows.GetTokenInformation(
		windows.GetCurrentProcessToken(),
		windows.TokenElevationType,
		(*byte)(unsafe.Pointer(&elevationType)),
		uint32(unsafe.Sizeof(elevationType)),
		&outLen,
	)
	return err == nil && outLen == uint32(unsafe.Sizeof(elevationType)) && elevationType == tokenElevationTypeLimited
}

func launchElevatedWorkerProcess(pipeName, capability, userSID string, sessionID uint32) (elevatedWorkerProcess, error) {
	exe, err := osExecutable()
	if err != nil {
		return elevatedWorkerProcess{}, err
	}
	args := []string{
		"--elevated-worker",
		"--worker-pipe=" + pipeName,
		"--worker-capability=" + capability,
		"--worker-user-sid=" + userSID,
		fmt.Sprintf("--worker-session-id=%d", sessionID),
	}
	handle, err := shellExecuteRunasProcess(exe, strings.Join(quoteArgs(args), " "))
	if err != nil {
		return elevatedWorkerProcess{}, err
	}
	return elevatedWorkerProcess{handle: handle}, nil
}

func osExecutable() (string, error) {
	return os.Executable()
}

func quoteArgs(args []string) []string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, quoteArg(arg))
	}
	return quoted
}
