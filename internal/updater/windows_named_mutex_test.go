//go:build windows

package updater

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"testing"
	"time"
)

func TestTryAcquireWindowsNamedMutexReacquiresAfterRelease(t *testing.T) {
	name := fmt.Sprintf(`Local\WindowsUpdaterWebUITest-%d`, time.Now().UnixNano())
	release, acquired, err := tryAcquireWindowsNamedMutex(context.Background(), name, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("first mutex acquire unexpectedly timed out")
	}
	runtime.Gosched()
	blockedResult := make(chan struct {
		acquired bool
		err      error
	}, 1)
	go func() {
		blockedRelease, blocked, blockedErr := tryAcquireWindowsNamedMutex(context.Background(), name, nil)
		if blockedRelease != nil {
			blockedRelease()
		}
		blockedResult <- struct {
			acquired bool
			err      error
		}{acquired: blocked, err: blockedErr}
	}()
	result := <-blockedResult
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.acquired {
		t.Fatal("second acquire succeeded while first mutex was still held")
	}
	release()

	reacquiredRelease, reacquired, err := tryAcquireWindowsNamedMutex(context.Background(), name, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reacquired {
		t.Fatal("mutex could not be reacquired after release")
	}
	reacquiredRelease()
}

func TestAcquireStoreScanProcessLockWindowsAlreadyRunning(t *testing.T) {
	userSID := fmt.Sprintf("S-1-5-21-test-store-scan-lock-%d", time.Now().UnixNano())
	release, err := acquireStoreScanProcessLock(context.Background(), userSID)
	if err != nil {
		t.Fatal(err)
	}
	secondResult := make(chan error, 1)
	go func() {
		secondRelease, secondErr := acquireStoreScanProcessLock(context.Background(), userSID)
		if secondRelease != nil {
			secondRelease()
		}
		secondResult <- secondErr
	}()
	if err := <-secondResult; !errors.Is(err, errStoreScanAlreadyRunning) {
		t.Fatalf("second acquire error=%v, want errStoreScanAlreadyRunning", err)
	}
	release()

	reacquiredRelease, err := acquireStoreScanProcessLock(context.Background(), userSID)
	if err != nil {
		t.Fatalf("could not reacquire Store scan lock after release: %v", err)
	}
	reacquiredRelease()
}
