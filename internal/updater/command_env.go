package updater

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	createNoWindowFlag           = 0x08000000
	systemEnvironmentRegistryKey = `HKLM\SYSTEM\CurrentControlSet\Control\Session Manager\Environment`
	currentUserEnvironmentRegKey = `HKCU\Environment`
)

func hiddenSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindowFlag}
}

func launchEnv() []string {
	commandEnv := os.Environ()
	commandPath := launchPath(os.Getenv("PATH"))
	for _, setting := range []struct {
		key   string
		value string
	}{
		{"PATH", commandPath},
		{"PYTHONUTF8", "1"},
		{"PYTHONIOENCODING", "utf-8"},
		{"LANG", "C.UTF-8"},
		{"LC_ALL", "C.UTF-8"},
	} {
		commandEnv = setCommandEnvValue(commandEnv, setting.key, setting.value)
	}
	return commandEnv
}

func launchPath(pathValue string) string {
	return prependExistingPathEntries(pathValue, launchPathAdditions()...)
}

func launchPathAdditions() []string {
	pathAdditions := []string{}
	if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
		pathAdditions = append(pathAdditions,
			filepath.Join(localAppData, "Microsoft", "WindowsApps"),
			filepath.Join(localAppData, "Microsoft", "WinGet", "Links"),
		)
	}
	if chocolateyInstall := os.Getenv("ChocolateyInstall"); chocolateyInstall != "" {
		pathAdditions = append(pathAdditions, filepath.Join(chocolateyInstall, "bin"))
	}
	if programData := os.Getenv("ProgramData"); programData != "" {
		pathAdditions = append(pathAdditions, filepath.Join(programData, "chocolatey", "bin"))
	}
	return pathAdditions
}

func prependExistingPathEntries(pathValue string, candidateEntries ...string) string {
	for _, candidateEntry := range candidateEntries {
		if _, err := os.Stat(candidateEntry); err == nil {
			pathValue = mergePathLists(candidateEntry, pathValue)
		}
	}
	return pathValue
}

func setCommandEnvValue(commandEnv []string, key, value string) []string {
	prefix := strings.ToUpper(key) + "="
	replacement := key + "=" + value
	filtered := commandEnv[:0]
	replaced := false

	for _, item := range commandEnv {
		if item == "" {
			continue
		}
		if strings.HasPrefix(strings.ToUpper(item), prefix) {
			if !replaced {
				filtered = append(filtered, replacement)
				replaced = true
			}
			continue
		}
		filtered = append(filtered, item)
	}
	if !replaced {
		filtered = append(filtered, replacement)
	}
	return filtered
}

func resolveExecutable(executableName string) string {
	if overridePath := os.Getenv("UPDATER_" + strings.ToUpper(executableName) + "_PATH"); overridePath != "" {
		return overridePath
	}
	if resolvedPath, err := exec.LookPath(executableName); err == nil {
		return resolvedPath
	}
	if strings.EqualFold(executableName, "choco") {
		var candidatePaths []string
		if chocolateyInstall := os.Getenv("ChocolateyInstall"); chocolateyInstall != "" {
			candidatePaths = append(candidatePaths, filepath.Join(chocolateyInstall, "bin", "choco.exe"))
		}
		if programData := os.Getenv("ProgramData"); programData != "" {
			candidatePaths = append(candidatePaths, filepath.Join(programData, "chocolatey", "bin", "choco.exe"))
		}
		if existingPath := firstExistingPath(candidatePaths); existingPath != "" {
			return existingPath
		}
	}
	if strings.EqualFold(executableName, "winget") || strings.EqualFold(executableName, "store") {
		executableFileName := executableName
		if !strings.HasSuffix(strings.ToLower(executableFileName), ".exe") {
			executableFileName += ".exe"
		}
		var candidatePaths []string
		if systemRoot := os.Getenv("SystemRoot"); systemRoot != "" {
			candidatePaths = append(candidatePaths,
				filepath.Join(systemRoot, "System32", executableFileName),
				filepath.Join(systemRoot, "Sysnative", executableFileName),
			)
		}
		for _, envVarName := range []string{"LOCALAPPDATA", "USERPROFILE"} {
			envValue := os.Getenv(envVarName)
			if envValue == "" {
				continue
			}
			localAppData := envValue
			if envVarName == "USERPROFILE" {
				localAppData = filepath.Join(envValue, "AppData", "Local")
			}
			candidatePaths = append(candidatePaths,
				filepath.Join(localAppData, "Microsoft", "WindowsApps", executableFileName),
				filepath.Join(localAppData, "Microsoft", "WinGet", "Links", executableFileName),
			)
		}
		if existingPath := firstExistingPath(candidatePaths); existingPath != "" {
			return existingPath
		}
	}
	return executableName
}

func firstExistingPath(candidatePaths []string) string {
	for _, candidatePath := range candidatePaths {
		if _, err := os.Stat(candidatePath); err == nil {
			return candidatePath
		}
	}
	return ""
}

func refreshProcessEnvironmentFromRegistry() {
	appLog("Refreshing process environment from registry.")
	if chocolateyInstall := queryRegistryEnvironmentValue(systemEnvironmentRegistryKey, "ChocolateyInstall"); chocolateyInstall != "" {
		_ = os.Setenv("ChocolateyInstall", expandWindowsEnvRefs(chocolateyInstall))
	}
	pathValues := []string{os.Getenv("PATH")}
	for _, registryKey := range []string{
		systemEnvironmentRegistryKey,
		currentUserEnvironmentRegKey,
	} {
		if pathValue := queryRegistryEnvironmentValue(registryKey, "Path"); pathValue != "" {
			pathValues = append(pathValues, expandWindowsEnvRefs(pathValue))
		}
	}
	refreshedPath := launchPath(mergePathLists(pathValues...))
	if refreshedPath != "" {
		_ = os.Setenv("PATH", refreshedPath)
	}
}

func queryRegistryEnvironmentValue(registryKey, valueName string) string {
	result := runCommand(managerDetectionTimeout, "reg.exe", "query", registryKey, "/v", valueName)
	if !result.OK {
		return ""
	}
	return parseRegistryQueryValue(result.Stdout, valueName)
}

func parseRegistryQueryValue(output, valueName string) string {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 3 || !strings.EqualFold(fields[0], valueName) || !strings.HasPrefix(strings.ToUpper(fields[1]), "REG_") {
			continue
		}
		return strings.Join(fields[2:], " ")
	}
	return ""
}

func expandWindowsEnvRefs(input string) string {
	var expanded strings.Builder
	for index := 0; index < len(input); {
		if input[index] != '%' {
			expanded.WriteByte(input[index])
			index++
			continue
		}
		closingOffset := strings.IndexByte(input[index+1:], '%')
		if closingOffset < 0 {
			expanded.WriteByte(input[index])
			index++
			continue
		}
		closingIndex := index + 1 + closingOffset
		variableName := input[index+1 : closingIndex]
		if variableName == "" {
			expanded.WriteString("%%")
			index = closingIndex + 1
			continue
		}
		if replacement := os.Getenv(variableName); replacement != "" {
			expanded.WriteString(replacement)
		} else {
			expanded.WriteString(input[index : closingIndex+1])
		}
		index = closingIndex + 1
	}
	return expanded.String()
}

func mergePathLists(pathLists ...string) string {
	var mergedEntries []string
	seenEntries := map[string]bool{}
	for _, pathList := range pathLists {
		for _, pathEntry := range filepath.SplitList(pathList) {
			pathEntry = strings.Trim(strings.TrimSpace(pathEntry), `"`)
			if pathEntry == "" {
				continue
			}
			normalizedEntry := strings.ToLower(strings.TrimRight(pathEntry, `\/`))
			if seenEntries[normalizedEntry] {
				continue
			}
			seenEntries[normalizedEntry] = true
			mergedEntries = append(mergedEntries, pathEntry)
		}
	}
	return strings.Join(mergedEntries, string(os.PathListSeparator))
}

func managerCommand(managerName string, args ...string) []string {
	executablePath := resolveExecutable(managerName)
	if executablePath != managerName {
		return append([]string{executablePath}, args...)
	}
	if managerName == "winget" || managerName == "store" {
		return append([]string{"cmd.exe", "/d", "/c", managerName}, args...)
	}
	return append([]string{managerName}, args...)
}
