//go:build !windows

package updater

import "runtime"

func detectStoreScanSystemContext() storeScanSystemContext {
	return storeScanSystemContext{
		WindowsVersion: runtime.GOOS,
		Architecture:   runtime.GOARCH,
	}
}
