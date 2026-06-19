package updater

import (
	"strings"
	"testing"
	"time"
)

func TestWingetSourceCommandsAvoidUnsupportedAgreementFlag(t *testing.T) {
	for _, command := range [][]string{wingetSourceListCommand(), wingetSourceResetCommand()} {
		joined := strings.Join(command, " ")
		if strings.Contains(joined, "--accept-source-agreements") {
			t.Fatalf("winget source command included unsupported agreement flag: %#v", command)
		}
		if !strings.Contains(joined, "--disable-interactivity") {
			t.Fatalf("winget source command should disable interactivity: %#v", command)
		}
	}
}

func TestWingetTransientFailureDetection(t *testing.T) {
	if !isWingetTransientFailure(CommandResult{Code: 2316632065}) {
		t.Fatal("expected App Installer winget code to be transient")
	}
	if !isWingetTransientFailure(CommandResult{Code: 1, Stderr: "Another transaction is currently running"}) {
		t.Fatal("expected concurrent transaction message to be transient")
	}
	if isWingetTransientFailure(CommandResult{OK: true, Code: 0}) {
		t.Fatal("successful command must not be transient")
	}
	if isWingetTransientFailure(CommandResult{Code: 1, Stderr: "ordinary failure"}) {
		t.Fatal("ordinary failure should not be treated as transient")
	}
}

func TestDetectManagersReturnsCachedCopy(t *testing.T) {
	invalidateManagerDetectionCache()
	t.Cleanup(invalidateManagerDetectionCache)
	managerDetectionCache.mu.Lock()
	managerDetectionCache.cached = map[string]ManagerStatus{
		managerStore: {Available: true, Version: "cached"},
	}
	managerDetectionCache.fetchedAt = time.Now()
	managerDetectionCache.inFlight = nil
	managerDetectionCache.mu.Unlock()

	got := detectManagers()
	got[managerStore] = ManagerStatus{Available: false}
	again := detectManagers()
	if !again[managerStore].Available || again[managerStore].Version != "cached" {
		t.Fatalf("cached manager status should be copied defensively, got %#v", again)
	}
}

func TestInvalidateManagerDetectionCache(t *testing.T) {
	managerDetectionCache.mu.Lock()
	managerDetectionCache.cached = map[string]ManagerStatus{managerStore: {Available: true}}
	managerDetectionCache.fetchedAt = time.Now()
	managerDetectionCache.mu.Unlock()

	invalidateManagerDetectionCache()

	managerDetectionCache.mu.Lock()
	defer managerDetectionCache.mu.Unlock()
	if managerDetectionCache.cached != nil || !managerDetectionCache.fetchedAt.IsZero() {
		t.Fatalf("manager detection cache was not cleared: %#v at %s", managerDetectionCache.cached, managerDetectionCache.fetchedAt)
	}
}
