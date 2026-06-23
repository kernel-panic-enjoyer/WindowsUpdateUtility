package updater

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func appRoot() string {
	exe, err := os.Executable()
	if err != nil {
		wd, _ := os.Getwd()
		return wd
	}
	return filepath.Dir(exe)
}

func stateDir() (string, error) {
	if override := os.Getenv("UPDATER_STATE_DIR"); override != "" {
		if err := os.MkdirAll(override, 0o755); err != nil {
			return "", err
		}
		if !canWriteDir(override) {
			return "", fmt.Errorf("state directory is not writable: %s", override)
		}
		return override, nil
	}

	var candidates []string
	for _, env := range []string{"LOCALAPPDATA", "APPDATA", "USERPROFILE", "ProgramData"} {
		if value := os.Getenv(env); value != "" {
			candidates = append(candidates, filepath.Join(value, appDirName))
		}
	}
	candidates = append(candidates, filepath.Join(appRoot(), ".state"))

	for _, candidate := range candidates {
		if err := os.MkdirAll(candidate, 0o755); err == nil && canWriteDir(candidate) {
			return candidate, nil
		}
	}
	return "", errors.New("could not create a state directory")
}

func appTempDir() (string, error) {
	if override := os.Getenv("UPDATER_TEMP_DIR"); override != "" {
		if err := os.MkdirAll(override, 0o755); err != nil {
			return "", err
		}
		if !canWriteDir(override) {
			return "", fmt.Errorf("temporary directory is not writable: %s", override)
		}
		return override, nil
	}

	candidates := []string{}
	if value := os.TempDir(); value != "" {
		candidates = append(candidates, filepath.Join(value, appDirName))
	}

	for _, candidate := range candidates {
		if err := os.MkdirAll(candidate, 0o755); err == nil && canWriteDir(candidate) {
			return candidate, nil
		}
	}
	return "", errors.New("could not create a temporary directory")
}

func canWriteDir(dir string) bool {
	path := filepath.Join(dir, fmt.Sprintf(".write-test-%d-%d", os.Getpid(), time.Now().UnixNano()))
	if err := os.WriteFile(path, []byte("ok"), 0o600); err != nil {
		return false
	}
	_ = os.Remove(path)
	return true
}
