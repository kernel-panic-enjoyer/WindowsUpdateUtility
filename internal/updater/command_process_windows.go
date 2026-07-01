//go:build windows

package updater

import (
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

type commandProcessOwner struct {
	jobHandle windows.Handle
	closeOnce sync.Once
}

func newCommandProcessOwner(enabled bool) (*commandProcessOwner, error) {
	if !enabled {
		return nil, nil
	}
	jobHandle, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	processOwner := &commandProcessOwner{jobHandle: jobHandle}
	killOnCloseLimit := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	killOnCloseLimit.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		jobHandle,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&killOnCloseLimit)),
		uint32(unsafe.Sizeof(killOnCloseLimit)),
	); err != nil {
		processOwner.Close()
		return nil, err
	}
	return processOwner, nil
}

func (processOwner *commandProcessOwner) Assign(command *exec.Cmd) error {
	if processOwner == nil || processOwner.jobHandle == 0 || command == nil || command.Process == nil {
		return nil
	}
	processHandle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(command.Process.Pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(processHandle)
	return windows.AssignProcessToJobObject(processOwner.jobHandle, processHandle)
}

func (processOwner *commandProcessOwner) Terminate() {
	if processOwner == nil || processOwner.jobHandle == 0 {
		return
	}
	_ = windows.TerminateJobObject(processOwner.jobHandle, uint32(commandCancelledCode))
}

func (processOwner *commandProcessOwner) Close() {
	if processOwner == nil {
		return
	}
	processOwner.closeOnce.Do(func() {
		if processOwner.jobHandle != 0 {
			_ = windows.CloseHandle(processOwner.jobHandle)
			processOwner.jobHandle = 0
		}
	})
}
