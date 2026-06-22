//go:build windows

package updater

import (
	"fmt"
	"runtime"
	"strings"

	"golang.org/x/sys/windows/registry"
)

func detectStoreScanSystemContext() storeScanSystemContext {
	context := storeScanSystemContext{
		WindowsVersion: runtime.GOOS,
		Architecture:   runtime.GOARCH,
	}
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return context
	}
	defer key.Close()

	productName := registryStringValue(key, "ProductName")
	displayVersion := firstNonEmpty(registryStringValue(key, "DisplayVersion"), registryStringValue(key, "ReleaseId"))
	context.WindowsVersion = strings.TrimSpace(strings.Join(nonEmptyStrings(productName, displayVersion), " "))

	buildNumber := registryStringValue(key, "CurrentBuildNumber")
	major, hasMajor := registryIntegerValue(key, "CurrentMajorVersionNumber")
	minor, hasMinor := registryIntegerValue(key, "CurrentMinorVersionNumber")
	ubr, hasUBR := registryIntegerValue(key, "UBR")
	if buildNumber != "" && hasMajor && hasMinor && hasUBR {
		context.WindowsBuild = fmt.Sprintf("%d.%d.%s.%d", major, minor, buildNumber, ubr)
	} else if buildNumber != "" && hasUBR {
		context.WindowsBuild = fmt.Sprintf("%s.%d", buildNumber, ubr)
	} else {
		context.WindowsBuild = buildNumber
	}
	return context
}

func registryStringValue(key registry.Key, name string) string {
	value, _, err := key.GetStringValue(name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func registryIntegerValue(key registry.Key, name string) (uint64, bool) {
	value, _, err := key.GetIntegerValue(name)
	return value, err == nil
}

func nonEmptyStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}
