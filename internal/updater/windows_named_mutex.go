//go:build windows

package updater

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

const windowsNamedMutexPollInterval = 50 * time.Millisecond

func acquireCancellableWindowsNamedMutex(ctx context.Context, name string, attributes *windows.SecurityAttributes, onWait func()) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	runtime.LockOSThread()
	handle, err := windows.CreateMutex(attributes, false, namePtr)
	if err != nil && (handle == 0 || err != windows.ERROR_ALREADY_EXISTS) {
		if err == windows.ERROR_ACCESS_DENIED {
			opened, openErr := windows.OpenMutex(windows.SYNCHRONIZE|windows.MUTEX_MODIFY_STATE, false, namePtr)
			if openErr == nil {
				handle = opened
				err = nil
			} else {
				runtime.UnlockOSThread()
				return nil, fmt.Errorf("could not create or open named mutex %s: create: %w; open: %w", name, err, openErr)
			}
		} else {
			runtime.UnlockOSThread()
			return nil, fmt.Errorf("could not create named mutex %s: %w", name, err)
		}
	}
	if handle == 0 {
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("could not create named mutex %s", name)
	}
	releaseThread := true
	defer func() {
		if releaseThread {
			runtime.UnlockOSThread()
		}
	}()

	waitLogged := false
	for {
		wait, waitErr := windows.WaitForSingleObject(handle, 0)
		if waitErr != nil {
			_ = windows.CloseHandle(handle)
			return nil, fmt.Errorf("could not acquire named mutex %s: %w", name, waitErr)
		}
		switch wait {
		case windows.WAIT_OBJECT_0, windows.WAIT_ABANDONED:
			releaseThread = false
			var once sync.Once
			return func() {
				once.Do(func() {
					_ = windows.ReleaseMutex(handle)
					_ = windows.CloseHandle(handle)
					runtime.UnlockOSThread()
				})
			}, nil
		case uint32(windows.WAIT_TIMEOUT):
			if !waitLogged && onWait != nil {
				waitLogged = true
				onWait()
			}
			timer := time.NewTimer(windowsNamedMutexPollInterval)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				_ = windows.CloseHandle(handle)
				return nil, ctx.Err()
			case <-timer.C:
			}
		default:
			_ = windows.CloseHandle(handle)
			return nil, fmt.Errorf("unexpected named mutex wait result for %s: %d", name, wait)
		}
	}
}

func tryAcquireWindowsNamedMutex(ctx context.Context, name string, attributes *windows.SecurityAttributes) (func(), bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, false, err
	}
	runtime.LockOSThread()
	handle, err := windows.CreateMutex(attributes, false, namePtr)
	if err != nil && (handle == 0 || err != windows.ERROR_ALREADY_EXISTS) {
		if err == windows.ERROR_ACCESS_DENIED {
			opened, openErr := windows.OpenMutex(windows.SYNCHRONIZE|windows.MUTEX_MODIFY_STATE, false, namePtr)
			if openErr == nil {
				handle = opened
				err = nil
			} else {
				runtime.UnlockOSThread()
				return nil, false, fmt.Errorf("could not create or open named mutex %s: create: %w; open: %w", name, err, openErr)
			}
		} else {
			runtime.UnlockOSThread()
			return nil, false, fmt.Errorf("could not create named mutex %s: %w", name, err)
		}
	}
	if handle == 0 {
		runtime.UnlockOSThread()
		return nil, false, fmt.Errorf("could not create named mutex %s", name)
	}

	wait, waitErr := windows.WaitForSingleObject(handle, 0)
	if waitErr != nil {
		_ = windows.CloseHandle(handle)
		runtime.UnlockOSThread()
		return nil, false, fmt.Errorf("could not acquire named mutex %s: %w", name, waitErr)
	}
	switch wait {
	case windows.WAIT_OBJECT_0, windows.WAIT_ABANDONED:
		var once sync.Once
		return func() {
			once.Do(func() {
				_ = windows.ReleaseMutex(handle)
				_ = windows.CloseHandle(handle)
				runtime.UnlockOSThread()
			})
		}, true, nil
	case uint32(windows.WAIT_TIMEOUT):
		_ = windows.CloseHandle(handle)
		runtime.UnlockOSThread()
		return nil, false, nil
	default:
		_ = windows.CloseHandle(handle)
		runtime.UnlockOSThread()
		return nil, false, fmt.Errorf("unexpected named mutex wait result for %s: %d", name, wait)
	}
}
