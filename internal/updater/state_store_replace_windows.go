//go:build windows

package updater

import (
	"errors"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const replaceFileWriteThrough = 0x00000001

var procReplaceFileW = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReplaceFileW")

func replaceFileKeepingBackup(tempPath, targetPath, backupPath string) error {
	tempPtr, err := windows.UTF16PtrFromString(tempPath)
	if err != nil {
		return err
	}
	targetPtr, err := windows.UTF16PtrFromString(targetPath)
	if err != nil {
		return err
	}
	backupPtr, err := windows.UTF16PtrFromString(backupPath)
	if err != nil {
		return err
	}
	if _, err := os.Stat(targetPath); errors.Is(err, os.ErrNotExist) {
		return windows.MoveFileEx(tempPtr, targetPtr, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
	} else if err != nil {
		return err
	}
	if backupPath == "" {
		return windows.MoveFileEx(tempPtr, targetPtr, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
	}
	return replaceFileW(targetPtr, tempPtr, backupPtr)
}

func replaceFileW(replaced, replacement, backup *uint16) error {
	result, _, err := syscall.SyscallN(
		procReplaceFileW.Addr(),
		uintptr(unsafe.Pointer(replaced)),
		uintptr(unsafe.Pointer(replacement)),
		uintptr(unsafe.Pointer(backup)),
		uintptr(replaceFileWriteThrough),
		0,
		0,
	)
	if result == 0 {
		if err != 0 {
			return err
		}
		return syscall.EINVAL
	}
	return nil
}
