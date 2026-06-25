//go:build windows

package updater

import "context"

func acquireStateStoreProcessLock(ctx context.Context, dir string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name := `Local\WindowsUpdaterWebUIState-` + shortHash(dir)
	return acquireCancellableWindowsNamedMutex(ctx, name, nil, nil)
}
