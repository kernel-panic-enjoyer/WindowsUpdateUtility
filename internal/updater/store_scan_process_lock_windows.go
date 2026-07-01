//go:build windows

package updater

import (
	"context"
	"fmt"
)

func acquireStoreScanProcessLock(ctx context.Context, userSID string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name := `Local\WindowsUpdaterWebUIStoreScan-` + shortHash(userSID)
	release, acquired, err := tryAcquireWindowsNamedMutex(ctx, name, nil)
	if err != nil {
		return nil, fmt.Errorf("could not acquire Store scan mutex: %w", err)
	}
	if !acquired {
		return nil, errStoreScanAlreadyRunning
	}
	return release, nil
}
