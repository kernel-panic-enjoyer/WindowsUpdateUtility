package updater

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

const createNoWindow = 0x08000000

func hiddenSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

func launchEnv() []string {
	env := os.Environ()
	path := launchPath(os.Getenv("PATH"))
	return upsertEnv(env, "PATH", path)
}

func launchPath(path string) string {
	return appendExistingPathEntries(path, launchPathAdditions()...)
}

func launchPathAdditions() []string {
	additions := []string{}
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		additions = append(additions,
			filepath.Join(local, "Microsoft", "WindowsApps"),
			filepath.Join(local, "Microsoft", "WinGet", "Links"),
		)
	}
	if chocoInstall := os.Getenv("ChocolateyInstall"); chocoInstall != "" {
		additions = append(additions, filepath.Join(chocoInstall, "bin"))
	}
	if programData := os.Getenv("ProgramData"); programData != "" {
		additions = append(additions, filepath.Join(programData, "chocolatey", "bin"))
	}
	return additions
}

func appendExistingPathEntries(path string, additions ...string) string {
	for _, addition := range additions {
		if _, err := os.Stat(addition); err == nil {
			path = mergePathLists(addition, path)
		}
	}
	return path
}

func upsertEnv(env []string, key, value string) []string {
	prefix := strings.ToUpper(key) + "="
	replaced := false
	for index, item := range env {
		if strings.HasPrefix(strings.ToUpper(item), prefix) {
			if !replaced {
				env[index] = key + "=" + value
				replaced = true
			} else {
				env[index] = ""
			}
		}
	}
	if !replaced {
		env = append(env, key+"="+value)
	}
	filtered := env[:0]
	for _, item := range env {
		if item != "" {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func resolveExecutable(name string) string {
	if override := os.Getenv("UPDATER_" + strings.ToUpper(name) + "_PATH"); override != "" {
		return override
	}
	if found, err := exec.LookPath(name); err == nil {
		return found
	}
	if strings.EqualFold(name, "choco") {
		var candidates []string
		if chocoInstall := os.Getenv("ChocolateyInstall"); chocoInstall != "" {
			candidates = append(candidates, filepath.Join(chocoInstall, "bin", "choco.exe"))
		}
		if programData := os.Getenv("ProgramData"); programData != "" {
			candidates = append(candidates, filepath.Join(programData, "chocolatey", "bin", "choco.exe"))
		}
		if candidate := firstExistingPath(candidates); candidate != "" {
			return candidate
		}
	}
	if strings.EqualFold(name, "winget") || strings.EqualFold(name, "store") {
		exeName := name
		if !strings.HasSuffix(strings.ToLower(exeName), ".exe") {
			exeName += ".exe"
		}
		var candidates []string
		if root := os.Getenv("SystemRoot"); root != "" {
			candidates = append(candidates, filepath.Join(root, "System32", exeName), filepath.Join(root, "Sysnative", exeName))
		}
		for _, env := range []string{"LOCALAPPDATA", "USERPROFILE"} {
			value := os.Getenv(env)
			if value == "" {
				continue
			}
			base := value
			if env == "USERPROFILE" {
				base = filepath.Join(value, "AppData", "Local")
			}
			candidates = append(candidates,
				filepath.Join(base, "Microsoft", "WindowsApps", exeName),
				filepath.Join(base, "Microsoft", "WinGet", "Links", exeName),
			)
		}
		if candidate := firstExistingPath(candidates); candidate != "" {
			return candidate
		}
	}
	return name
}

func firstExistingPath(paths []string) string {
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

func refreshProcessEnvironmentFromRegistry() {
	appLog("Refreshing process environment from registry.")
	if value := registryEnvironmentValue(`HKLM\SYSTEM\CurrentControlSet\Control\Session Manager\Environment`, "ChocolateyInstall"); value != "" {
		_ = os.Setenv("ChocolateyInstall", expandWindowsEnvRefs(value))
	}
	paths := []string{os.Getenv("PATH")}
	for _, key := range []string{
		`HKLM\SYSTEM\CurrentControlSet\Control\Session Manager\Environment`,
		`HKCU\Environment`,
	} {
		if value := registryEnvironmentValue(key, "Path"); value != "" {
			paths = append(paths, expandWindowsEnvRefs(value))
		}
	}
	refreshed := launchPath(mergePathLists(paths...))
	if refreshed != "" {
		_ = os.Setenv("PATH", refreshed)
	}
}

func registryEnvironmentValue(key, value string) string {
	result := runCommand(managerDetectionTimeout, "reg.exe", "query", key, "/v", value)
	if !result.OK {
		return ""
	}
	return parseRegistryQueryValue(result.Stdout, value)
}

func parseRegistryQueryValue(output, value string) string {
	for _, raw := range strings.Split(output, "\n") {
		fields := strings.Fields(strings.TrimSpace(raw))
		if len(fields) < 3 || !strings.EqualFold(fields[0], value) || !strings.HasPrefix(strings.ToUpper(fields[1]), "REG_") {
			continue
		}
		return strings.Join(fields[2:], " ")
	}
	return ""
}

func expandWindowsEnvRefs(value string) string {
	var expanded strings.Builder
	for i := 0; i < len(value); {
		if value[i] != '%' {
			expanded.WriteByte(value[i])
			i++
			continue
		}
		end := strings.IndexByte(value[i+1:], '%')
		if end < 0 {
			expanded.WriteByte(value[i])
			i++
			continue
		}
		end += i + 1
		name := value[i+1 : end]
		if name == "" {
			expanded.WriteString("%%")
			i = end + 1
			continue
		}
		if replacement := os.Getenv(name); replacement != "" {
			expanded.WriteString(replacement)
		} else {
			expanded.WriteString(value[i : end+1])
		}
		i = end + 1
	}
	return expanded.String()
}

func mergePathLists(paths ...string) string {
	var merged []string
	seen := map[string]bool{}
	for _, path := range paths {
		for _, entry := range filepath.SplitList(path) {
			entry = strings.Trim(strings.TrimSpace(entry), `"`)
			if entry == "" {
				continue
			}
			key := strings.ToLower(strings.TrimRight(entry, `\/`))
			if seen[key] {
				continue
			}
			seen[key] = true
			merged = append(merged, entry)
		}
	}
	return strings.Join(merged, string(os.PathListSeparator))
}

func managerCommand(manager string, args ...string) []string {
	resolved := resolveExecutable(manager)
	if resolved != manager {
		return append([]string{resolved}, args...)
	}
	if manager == "winget" || manager == "store" {
		return append([]string{"cmd.exe", "/d", "/c", manager}, args...)
	}
	return append([]string{manager}, args...)
}
