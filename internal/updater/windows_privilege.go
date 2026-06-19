package updater

import (
	"syscall"
	"unsafe"
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

func quoteArg(arg string) string {
	return syscall.EscapeArg(arg)
}
