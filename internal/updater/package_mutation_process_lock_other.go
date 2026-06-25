//go:build !windows

package updater

import "context"

func acquirePackageMutationProcessLock(ctx context.Context, onWait func()) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return func() {}, nil
}
