package updater

import (
	"context"
	"errors"
	"sync"
)

type packageMutationProcessLockFunc func(context.Context, func()) (func(), error)

// PackageMutationCoordinator serializes mutable package-manager commands first
// inside this process, then through the Windows process-wide coordinator.
type PackageMutationCoordinator struct {
	mu          sync.Mutex
	processLock packageMutationProcessLockFunc
}

var defaultPackageMutationCoordinator = newPackageMutationCoordinator(acquirePackageMutationProcessLock)

func newPackageMutationCoordinator(processLock packageMutationProcessLockFunc) *PackageMutationCoordinator {
	if processLock == nil {
		processLock = acquirePackageMutationProcessLock
	}
	return &PackageMutationCoordinator{processLock: processLock}
}

func (coordinator *PackageMutationCoordinator) Acquire(ctx context.Context, onWait func()) (func(), error) {
	if coordinator == nil {
		coordinator = newPackageMutationCoordinator(nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var waitOnce sync.Once
	notifyWait := func() {
		if onWait != nil {
			waitOnce.Do(onWait)
		}
	}
	if !lockMutexContextWithWait(ctx, &coordinator.mu, notifyWait) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, context.Canceled
	}
	processRelease, err := coordinator.processLock(ctx, notifyWait)
	if err != nil {
		coordinator.mu.Unlock()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			if processRelease != nil {
				processRelease()
			}
			coordinator.mu.Unlock()
		})
	}, nil
}

func packageMutationLockFailureResult(ctx context.Context, command string, categories []string, err error) CommandResult {
	if err == nil {
		err = context.Canceled
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		return commandContextDoneResult(ctx, command, "while waiting for package operation lock", categories)
	}
	result := CommandResult{
		Code:    127,
		Command: command,
		Stderr:  "Could not acquire package operation lock: " + err.Error(),
	}
	sessionLogs.AppendCategorized("stderr", result.Stderr, categories)
	sessionLogs.AppendCategorized("exit", command+" exited with code 127", categories)
	return result
}
