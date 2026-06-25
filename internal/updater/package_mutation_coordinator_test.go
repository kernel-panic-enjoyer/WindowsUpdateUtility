package updater

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPackageMutationCoordinatorCancelsWhileWaitingInProcess(t *testing.T) {
	coordinator := newPackageMutationCoordinator(func(context.Context, func()) (func(), error) {
		return func() {}, nil
	})
	release, err := coordinator.Acquire(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = coordinator.Acquire(ctx, nil)
	elapsed := time.Since(started)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline while waiting for package mutation coordinator, got %v", err)
	}
	if elapsed > time.Second {
		t.Fatalf("coordinator cancellation took too long: %s", elapsed)
	}
}

func TestPackageMutationMutexDescriptorIsCreatorIndependent(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows named mutex security descriptor test")
	}
	sddl := packageMutationMutexSecurityDescriptorString()
	if !strings.Contains(sddl, ";;;SY") || !strings.Contains(sddl, ";;;BA") || !strings.Contains(sddl, ";;;AU") {
		t.Fatalf("package mutation mutex descriptor does not name the expected stable principals: %q", sddl)
	}
	if strings.Contains(sddl, "(A;;GA;;;AU)") {
		t.Fatalf("package mutation mutex descriptor grants Authenticated Users full control: %q", sddl)
	}
	if userSID, err := currentUserSID(); err == nil && userSID != "" && strings.Contains(sddl, userSID) {
		t.Fatalf("package mutation mutex descriptor depends on current creator SID %s: %q", userSID, sddl)
	}
}

func TestPackageMutationCoordinatorSerializesWebUIScheduledAndWorkerHelpers(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows named mutex integration test")
	}
	cases := []struct {
		holder    string
		contender string
	}{
		{holder: "webui", contender: "webui"},
		{holder: "webui", contender: "scheduled"},
		{holder: "webui", contender: "elevated-worker"},
	}
	for _, tc := range cases {
		t.Run(tc.holder+"_blocks_"+tc.contender, func(t *testing.T) {
			mutexName := packageMutationTestMutexName(t)
			dir := t.TempDir()
			holderReady := filepathForTest(dir, "holder.ready")
			releaseHolder := filepathForTest(dir, "release-holder")
			contenderAcquired := filepathForTest(dir, "contender.acquired")

			holder := packageMutationHelperCommand(t, "hold-"+tc.holder, mutexName, holderReady, releaseHolder, "")
			if err := holder.Start(); err != nil {
				t.Fatal(err)
			}
			waitForTestFile(t, holderReady, 5*time.Second)

			contender := packageMutationHelperCommand(t, "try-"+tc.contender, mutexName, "", "", contenderAcquired)
			if err := contender.Start(); err != nil {
				t.Fatal(err)
			}
			time.Sleep(250 * time.Millisecond)
			if testFileExists(contenderAcquired) {
				t.Fatalf("%s acquired package mutation mutex while %s still held it", tc.contender, tc.holder)
			}

			if err := os.WriteFile(releaseHolder, []byte("release"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := holder.Wait(); err != nil {
				t.Fatalf("holder helper: %v", err)
			}
			if err := contender.Wait(); err != nil {
				t.Fatalf("contender helper: %v", err)
			}
			if !testFileExists(contenderAcquired) {
				t.Fatalf("%s did not acquire package mutation mutex after %s released it", tc.contender, tc.holder)
			}
		})
	}
}

func TestPackageMutationCoordinatorWaitingHelperCancelsPromptly(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows named mutex integration test")
	}
	mutexName := packageMutationTestMutexName(t)
	dir := t.TempDir()
	holderReady := filepathForTest(dir, "holder.ready")
	releaseHolder := filepathForTest(dir, "release-holder")
	waiterTimedOut := filepathForTest(dir, "waiter.timedout")

	holder := packageMutationHelperCommand(t, "hold-webui", mutexName, holderReady, releaseHolder, "")
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	waitForTestFile(t, holderReady, 5*time.Second)

	waiter := packageMutationHelperCommand(t, "wait-timeout", mutexName, "", "", waiterTimedOut)
	started := time.Now()
	if err := waiter.Run(); err != nil {
		t.Fatalf("waiting helper should exit cleanly after context timeout: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("waiting helper cancellation took too long: %s", elapsed)
	}
	if !testFileExists(waiterTimedOut) {
		t.Fatal("waiting helper did not record timeout")
	}

	if err := os.WriteFile(releaseHolder, []byte("release"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := holder.Wait(); err != nil {
		t.Fatalf("holder helper: %v", err)
	}
}

func TestPackageMutationCoordinatorRecoversAbandonedMutex(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows named mutex integration test")
	}
	mutexName := packageMutationTestMutexName(t)
	dir := t.TempDir()
	crashReady := filepathForTest(dir, "crash.ready")

	crasher := packageMutationHelperCommand(t, "crash-hold", mutexName, crashReady, "", "")
	if err := crasher.Start(); err != nil {
		t.Fatal(err)
	}
	waitForTestFile(t, crashReady, 5*time.Second)
	if err := crasher.Wait(); err == nil {
		t.Fatal("crash helper unexpectedly exited successfully")
	}

	t.Setenv(packageMutationMutexNameOverrideEnv, mutexName)
	release, err := acquirePackageMutationProcessLock(context.Background(), nil)
	if err != nil {
		t.Fatalf("abandoned package mutation mutex was not recoverable: %v", err)
	}
	release()
}

func TestPackageMutationCoordinatorHelperProcess(t *testing.T) {
	mode := os.Getenv("UPDATER_PACKAGE_MUTATION_HELPER")
	if mode == "" {
		return
	}
	mutexName := os.Getenv(packageMutationMutexNameOverrideEnv)
	readyPath := os.Getenv("UPDATER_PACKAGE_MUTATION_READY")
	releasePath := os.Getenv("UPDATER_PACKAGE_MUTATION_RELEASE")
	acquiredPath := os.Getenv("UPDATER_PACKAGE_MUTATION_ACQUIRED")
	if mutexName == "" {
		t.Fatal("package mutation helper requires mutex name")
	}
	t.Setenv(packageMutationMutexNameOverrideEnv, mutexName)
	switch {
	case strings.HasPrefix(mode, "hold-"):
		release, err := defaultPackageMutationCoordinator.Acquire(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(readyPath, []byte("ready"), 0o644); err != nil {
			t.Fatal(err)
		}
		waitForTestFile(t, releasePath, 10*time.Second)
		release()
	case strings.HasPrefix(mode, "try-"):
		release, err := defaultPackageMutationCoordinator.Acquire(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(acquiredPath, []byte("acquired"), 0o644); err != nil {
			release()
			t.Fatal(err)
		}
		release()
	case mode == "wait-timeout":
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		_, err := defaultPackageMutationCoordinator.Acquire(ctx, nil)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected timeout while waiting for package mutation mutex, got %v", err)
		}
		if err := os.WriteFile(acquiredPath, []byte("timed out"), 0o644); err != nil {
			t.Fatal(err)
		}
	case mode == "crash-hold":
		release, err := acquirePackageMutationProcessLock(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = release
		if err := os.WriteFile(readyPath, []byte("ready"), 0o644); err != nil {
			t.Fatal(err)
		}
		os.Exit(7)
	default:
		t.Fatalf("unknown package mutation helper mode %q", mode)
	}
	os.Exit(0)
}

func packageMutationHelperCommand(t *testing.T, mode, mutexName, readyPath, releasePath, acquiredPath string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run", "^TestPackageMutationCoordinatorHelperProcess$", "-test.v")
	cmd.Env = append(os.Environ(),
		"UPDATER_PACKAGE_MUTATION_HELPER="+mode,
		packageMutationMutexNameOverrideEnv+"="+mutexName,
		"UPDATER_PACKAGE_MUTATION_READY="+readyPath,
		"UPDATER_PACKAGE_MUTATION_RELEASE="+releasePath,
		"UPDATER_PACKAGE_MUTATION_ACQUIRED="+acquiredPath,
	)
	var output strings.Builder
	cmd.Stdout = &output
	cmd.Stderr = &output
	t.Cleanup(func() {
		if output.Len() > 0 && cmd.ProcessState != nil && !cmd.ProcessState.Success() {
			t.Log(output.String())
		}
	})
	return cmd
}

func packageMutationTestMutexName(t *testing.T) string {
	t.Helper()
	return `Local\WindowsUpdaterWebUIPackageMutationTest-` + shortHash(t.Name()+"-"+t.TempDir())
}

func filepathForTest(dir, name string) string {
	return dir + string(os.PathSeparator) + name
}

func waitForTestFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if testFileExists(path) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func testFileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}
