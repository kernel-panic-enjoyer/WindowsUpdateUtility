package updater

import "runtime"

type storeScanSystemContext struct {
	WindowsVersion string
	WindowsBuild   string
	Architecture   string
}

var storeScanSystemContextProvider = detectStoreScanSystemContext

func currentStoreScanSystemContext() storeScanSystemContext {
	context := storeScanSystemContextProvider()
	if context.WindowsVersion == "" {
		context.WindowsVersion = runtime.GOOS
	}
	if context.Architecture == "" {
		context.Architecture = runtime.GOARCH
	}
	return context
}
