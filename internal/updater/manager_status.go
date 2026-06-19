package updater

import (
	"strings"
	"sync"
	"time"
)

const (
	managerDetectionTimeout = 20 * time.Second
	managerStatusCacheTTL   = 10 * time.Second
	wingetDetectionRetryGap = 350 * time.Millisecond
)

type managerDetectionState struct {
	mu        sync.Mutex
	cached    map[string]ManagerStatus
	fetchedAt time.Time
	inFlight  chan struct{}
}

var managerDetectionCache = &managerDetectionState{}

func wingetSourceListCommand() []string {
	return managerCommand(managerWinget, "source", "list", "--disable-interactivity")
}

func wingetSourceResetCommand() []string {
	return managerCommand(managerWinget, "source", "reset", "--force", "--disable-interactivity")
}

func wingetSourceUpdateCommand() []string {
	return managerCommand(managerWinget, "source", "update", "--disable-interactivity")
}

func detectManager(manager string) ManagerStatus {
	if manager == managerStore {
		return detectStoreCLIManager()
	}
	result := runCommand(managerDetectionTimeout, managerCommand(manager, "--version")...)
	if manager == managerWinget && isWingetTransientFailure(result) {
		appLog("Winget version detection failed with transient code %d; retrying once.", result.Code)
		time.Sleep(wingetDetectionRetryGap)
		result = runCommand(managerDetectionTimeout, managerCommand(manager, "--version")...)
	}
	output := strings.TrimSpace(result.Stdout)
	if output == "" {
		output = strings.TrimSpace(result.Stderr)
	}
	status := ManagerStatus{Available: result.OK}
	if result.OK {
		lines := strings.Split(output, "\n")
		status.Version = strings.TrimSpace(lines[0])
		status.Path = resolveExecutable(manager)
	} else {
		status.Error = strings.TrimSpace(result.Stderr + result.Stdout)
	}
	return status
}

func detectStoreCLIManager() ManagerStatus {
	result := runCommand(managerDetectionTimeout, managerCommand(managerStore, "--help")...)
	status := ManagerStatus{
		Available: result.OK,
		Path:      resolveExecutable(managerStore),
	}
	if status.Available {
		status.Version = parseStoreHelpVersion(result.Stdout + "\n" + result.Stderr)
		status.ActionBackend = backendStoreCLI
		return status
	}
	if strings.TrimSpace(result.Stderr+result.Stdout) != "" {
		status.Error = strings.TrimSpace(result.Stderr + result.Stdout)
	} else {
		status.Error = "native Store CLI was not found"
	}
	return status
}

func parseStoreHelpVersion(output string) string {
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "usage:") || strings.Contains(lower, "<command>") {
			continue
		}
		if strings.Contains(lower, "version") {
			return line
		}
	}
	return ""
}

func isWingetTransientFailure(result CommandResult) bool {
	if result.OK {
		return false
	}
	output := strings.ToLower(result.Stdout + "\n" + result.Stderr)
	if strings.Contains(output, "another transaction") || strings.Contains(output, "currently running") {
		return true
	}
	return result.Code == 2316632065
}

func detectManagers() map[string]ManagerStatus {
	return detectManagersCached(false)
}

func detectManagersFresh() map[string]ManagerStatus {
	return detectManagersCached(true)
}

func detectManagersCached(force bool) map[string]ManagerStatus {
	for {
		managerDetectionCache.mu.Lock()
		if !force && managerDetectionCache.cached != nil && time.Since(managerDetectionCache.fetchedAt) < managerStatusCacheTTL {
			managers := cloneManagerStatuses(managerDetectionCache.cached)
			managerDetectionCache.mu.Unlock()
			return managers
		}
		if managerDetectionCache.inFlight != nil {
			inFlight := managerDetectionCache.inFlight
			managerDetectionCache.mu.Unlock()
			<-inFlight
			force = false
			continue
		}
		inFlight := make(chan struct{})
		managerDetectionCache.inFlight = inFlight
		managerDetectionCache.mu.Unlock()

		managers := detectManagersUncached()

		managerDetectionCache.mu.Lock()
		managerDetectionCache.cached = cloneManagerStatuses(managers)
		managerDetectionCache.fetchedAt = time.Now()
		managerDetectionCache.inFlight = nil
		close(inFlight)
		managerDetectionCache.mu.Unlock()
		return cloneManagerStatuses(managers)
	}
}

func invalidateManagerDetectionCache() {
	managerDetectionCache.mu.Lock()
	defer managerDetectionCache.mu.Unlock()
	managerDetectionCache.cached = nil
	managerDetectionCache.fetchedAt = time.Time{}
}

func cloneManagerStatuses(input map[string]ManagerStatus) map[string]ManagerStatus {
	cloned := make(map[string]ManagerStatus, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

func detectManagersUncached() map[string]ManagerStatus {
	managers := map[string]ManagerStatus{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, manager := range managedPackageManagers {
		manager := manager
		wg.Add(1)
		go func() {
			defer wg.Done()
			status := detectManager(manager)
			mu.Lock()
			managers[manager] = status
			mu.Unlock()
		}()
	}
	wg.Wait()
	store := managers[managerStore]
	if !store.Available && managers[managerWinget].Available {
		store.ActionBackend = backendWingetMSStoreFallback
		if store.Error == "" {
			store.Error = "native Store CLI was not found"
		}
		store.Error += "\nStore installs and updates can fall back to winget msstore when a compatible package ID is known."
		managers[managerStore] = store
	}
	return managers
}
