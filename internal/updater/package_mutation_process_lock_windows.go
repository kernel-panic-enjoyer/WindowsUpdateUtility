//go:build windows

package updater

import (
	"context"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	// The package lock is intentionally machine-wide rather than per-user:
	// Chocolatey and source maintenance can mutate machine-scoped package state,
	// and an elevated helper may run under administrator credentials.
	packageMutationMutexName            = `Global\WindowsUpdaterWebUIPackageMutation`
	packageMutationMutexNameOverrideEnv = "UPDATER_PACKAGE_MUTATION_MUTEX_NAME"
	// Authenticated Users receive only SYNCHRONIZE|MUTEX_MODIFY_STATE so a
	// standard WebUI, scheduled task, and alternate-admin worker can wait on and
	// release the same machine-wide lock without granting descriptor control.
	packageMutationMutexSDDL = "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;0x00100001;;;AU)"
)

func acquirePackageMutationProcessLock(ctx context.Context, onWait func()) (func(), error) {
	attributes, cleanup, err := packageMutationMutexSecurityAttributes()
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return acquireCancellableWindowsNamedMutex(ctx, packageMutationProcessMutexName(), attributes, onWait)
}

func packageMutationProcessMutexName() string {
	name := strings.TrimSpace(os.Getenv(packageMutationMutexNameOverrideEnv))
	if strings.HasPrefix(name, `Local\WindowsUpdaterWebUIPackageMutationTest-`) {
		return name
	}
	return packageMutationMutexName
}

func packageMutationMutexSecurityAttributes() (*windows.SecurityAttributes, func(), error) {
	descriptor, err := windows.SecurityDescriptorFromString(packageMutationMutexSecurityDescriptorString())
	if err != nil {
		return nil, func() {}, err
	}
	attributes := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	return attributes, func() {}, nil
}

func packageMutationMutexSecurityDescriptorString() string {
	return packageMutationMutexSDDL
}
