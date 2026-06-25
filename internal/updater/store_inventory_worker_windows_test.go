//go:build windows

package updater

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const storeInventoryWorkerHelperEnv = "UPDATER_STORE_INVENTORY_WORKER_TEST_HELPER"
const windowsStillActiveExitCode = 259

func TestStoreInventoryWorkerBuildsInventoryFromStructuredResponse(t *testing.T) {
	scan := testNativeInventoryScan("scan-worker-success", "S-1-5-21-test-1001")
	provider := testStoreInventoryWorkerProvider(t, "success")
	inventory, result := provider.Inventory(context.Background(), scan)
	if !result.OK || inventory.Partial || inventory.Scan.CompletionStatus != StoreScanCompleted {
		t.Fatalf("expected healthy worker inventory, inventory=%#v result=%#v", inventory, result)
	}
	if len(inventory.Families) != 1 || inventory.Families[0].Identity.PackageFamilyName != "Vendor.App_abc123" {
		t.Fatalf("unexpected worker inventory families: %#v", inventory.Families)
	}
}

func TestStoreInventoryWorkerRejectsWrongScanID(t *testing.T) {
	scan := testNativeInventoryScan("scan-correct", "S-1-5-21-test-1001")
	response := testWorkerResponse(scan)
	response.ScanID = "scan-wrong"
	if _, err := inventoryFromStoreInventoryWorkerResponse(scan, response); err == nil || !strings.Contains(err.Error(), "scan ID mismatch") {
		t.Fatalf("expected scan ID mismatch, got %v", err)
	}
}

func TestStoreInventoryWorkerRejectsWrongUserSID(t *testing.T) {
	scan := testNativeInventoryScan("scan-user", "S-1-5-21-test-1001")
	response := testWorkerResponse(scan)
	response.UserSID = "S-1-5-21-test-2002"
	if _, err := inventoryFromStoreInventoryWorkerResponse(scan, response); err == nil || !strings.Contains(err.Error(), "user SID mismatch") {
		t.Fatalf("expected user SID mismatch, got %v", err)
	}
}

func TestStoreInventoryWorkerRejectsMalformedJSON(t *testing.T) {
	if _, err := decodeStoreInventoryWorkerResponse([]byte(`{"protocol_version":1}` + "\n{}")); err == nil {
		t.Fatal("expected trailing JSON rejection")
	}
	if _, err := decodeStoreInventoryWorkerResponse([]byte(`{"protocol_version":1,`)); err == nil {
		t.Fatal("expected malformed JSON rejection")
	}
}

func TestStoreInventoryWorkerRejectsUnknownProtocolVersion(t *testing.T) {
	scan := testNativeInventoryScan("scan-protocol", "S-1-5-21-test-1001")
	response := testWorkerResponse(scan)
	response.ProtocolVersion = 99
	if _, err := inventoryFromStoreInventoryWorkerResponse(scan, response); err == nil || !strings.Contains(err.Error(), "protocol version") {
		t.Fatalf("expected protocol rejection, got %v", err)
	}
}

func TestStoreInventoryWorkerRejectsOversizedResponse(t *testing.T) {
	oversized := bytes.Repeat([]byte{'x'}, storeInventoryWorkerResponseLimit+1)
	if _, err := decodeStoreInventoryWorkerResponse(oversized); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected oversized response rejection, got %v", err)
	}
}

func TestStoreInventoryWorkerRejectsTooManyPackageRecords(t *testing.T) {
	scan := testNativeInventoryScan("scan-too-many", "S-1-5-21-test-1001")
	response := testWorkerResponse(scan)
	for len(response.PackageFamilies) <= storeInventoryWorkerMaxFamilies {
		index := len(response.PackageFamilies)
		record := testNativeRecord(scan.UserSID, fmt.Sprintf("Vendor.App%d_abc123", index), fmt.Sprintf("Vendor.App%d_1.0.0.0_x64__abc123", index), "App", StorePackageVersion{Major: 1}, storePackageClassMain)
		response.PackageFamilies = append(response.PackageFamilies, StorePackagedAppFamily{
			Identity:  StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: record.PackageFamilyName},
			Primary:   record,
			Instances: []StorePackagedAppRecord{record},
		})
	}
	if _, err := inventoryFromStoreInventoryWorkerResponse(scan, response); err == nil || !strings.Contains(err.Error(), "family records") {
		t.Fatalf("expected family limit rejection, got %v", err)
	}
}

func TestStoreInventoryWorkerRejectsDuplicatePFN(t *testing.T) {
	scan := testNativeInventoryScan("scan-duplicate", "S-1-5-21-test-1001")
	response := testWorkerResponse(scan)
	response.PackageFamilies = append(response.PackageFamilies, response.PackageFamilies[0])
	if _, err := inventoryFromStoreInventoryWorkerResponse(scan, response); err == nil || !strings.Contains(err.Error(), "duplicate package family") {
		t.Fatalf("expected duplicate PFN rejection, got %v", err)
	}
}

func TestStoreInventoryWorkerRejectsMalformedPackageFields(t *testing.T) {
	scan := testNativeInventoryScan("scan-malformed-fields", "S-1-5-21-test-1001")
	response := testWorkerResponse(scan)
	response.PackageFamilies[0].Instances[0].PackageFullName = `Vendor.App\bad`
	if _, err := inventoryFromStoreInventoryWorkerResponse(scan, response); err == nil || !strings.Contains(err.Error(), "package full name") {
		t.Fatalf("expected malformed full name rejection, got %v", err)
	}
}

func TestStoreInventoryWorkerStructuredErrorWithNonzeroExitIsNotHealthy(t *testing.T) {
	scan := testNativeInventoryScan("scan-structured-error", "S-1-5-21-test-1001")
	provider := testStoreInventoryWorkerProvider(t, "structured-error-nonzero")
	inventory, result := provider.Inventory(context.Background(), scan)
	if result.OK || !inventory.Partial || inventory.Scan.CompletionStatus != StoreScanIncomplete {
		t.Fatalf("expected nonzero structured error to remain incomplete, inventory=%#v result=%#v", inventory, result)
	}
	if !strings.Contains(result.Stderr, "worker reported structured failure") {
		t.Fatalf("expected structured worker error in stderr, got %q", result.Stderr)
	}
}

func TestStoreInventoryWorkerInvalidResponseWithNonzeroExitIsRejected(t *testing.T) {
	scan := testNativeInventoryScan("scan-invalid-nonzero", "S-1-5-21-test-1001")
	provider := testStoreInventoryWorkerProvider(t, "invalid-nonzero")
	inventory, result := provider.Inventory(context.Background(), scan)
	if result.OK || !inventory.Partial || !strings.Contains(result.Stderr, "invalid Store inventory worker response") {
		t.Fatalf("expected invalid nonzero response rejection, inventory=%#v result=%#v", inventory, result)
	}
}

func TestStoreInventoryWorkerCrashReturnsIncomplete(t *testing.T) {
	scan := testNativeInventoryScan("scan-crash", "S-1-5-21-test-1001")
	provider := testStoreInventoryWorkerProvider(t, "crash")
	inventory, result := provider.Inventory(context.Background(), scan)
	if result.OK || !inventory.Partial || result.Code == 0 {
		t.Fatalf("expected crash to produce incomplete inventory, inventory=%#v result=%#v", inventory, result)
	}
}

func TestStoreInventoryWorkerHangCancellationTerminatesWorker(t *testing.T) {
	pidFile := t.TempDir() + `\worker.pid`
	scan := testNativeInventoryScan("scan-hang", "S-1-5-21-test-1001")
	provider := testStoreInventoryWorkerProvider(t, "hang")
	provider.Timeout = 250 * time.Millisecond
	args := append([]string{}, provider.Args...)
	args = append(args, "--pid-file", pidFile)
	provider.Args = args
	inventory, result := provider.Inventory(context.Background(), scan)
	if result.Code != 124 || !inventory.Partial {
		t.Fatalf("expected timeout cancellation, inventory=%#v result=%#v", inventory, result)
	}
	pidData, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read helper pid: %v", err)
	}
	pid := strings.TrimSpace(string(pidData))
	if pid == "" {
		t.Fatal("helper did not write pid")
	}
	assertProcessExited(t, pid)
}

func TestStoreInventoryWorkerRequestRejectsOrdinaryInteractiveUse(t *testing.T) {
	code := runStoreInventoryWorker(bytes.NewReader(nil), io.Discard, io.Discard, winrtStorePackagedAppInventoryProvider{})
	if code == 0 {
		t.Fatal("worker accepted empty interactive input")
	}
}

func TestStoreInventoryWorkerPerformanceOverhead(t *testing.T) {
	scan := testNativeInventoryScan("scan-overhead", "S-1-5-21-test-1001")
	provider := testStoreInventoryWorkerProvider(t, "success")
	started := time.Now()
	inventory, result := provider.Inventory(context.Background(), scan)
	elapsed := time.Since(started)
	if !result.OK || inventory.Partial {
		t.Fatalf("worker failed during overhead measurement: inventory=%#v result=%#v", inventory, result)
	}
	t.Logf("store inventory worker helper startup plus enumeration overhead: %s", elapsed)
}

func TestStoreInventoryWorkerHelperProcess(t *testing.T) {
	if os.Getenv(storeInventoryWorkerHelperEnv) == "" {
		return
	}
	mode := os.Getenv(storeInventoryWorkerHelperEnv)
	switch mode {
	case "success":
		os.Exit(runStoreInventoryWorker(os.Stdin, os.Stdout, os.Stderr, winrtStorePackagedAppInventoryProvider{
			CurrentUserSID: func() (string, error) { return "S-1-5-21-test-1001", nil },
			Enumerate: func(ctx context.Context, userSID string) ([]StorePackagedAppRecord, error) {
				return []StorePackagedAppRecord{testNativeRecord(userSID, "Vendor.App_abc123", "Vendor.App_1.0.0.0_x64__abc123", "Vendor App", StorePackageVersion{Major: 1}, storePackageClassMain)}, nil
			},
		}))
	case "structured-error-nonzero":
		var request storeInventoryWorkerRequest
		_ = json.NewDecoder(os.Stdin).Decode(&request)
		_ = encodeStoreInventoryWorkerResponse(os.Stdout, storeInventoryWorkerResponse{
			ProtocolVersion: storeInventoryWorkerProtocolVersion,
			ScanID:          request.ScanID,
			UserSID:         request.UserSID,
			Partial:         true,
			Errors:          []string{"worker reported structured failure"},
		})
		os.Exit(1)
	case "invalid-nonzero":
		fmt.Fprint(os.Stdout, "{not-json")
		os.Exit(7)
	case "crash":
		os.Exit(9)
	case "hang":
		if pidFile := helperArgValue("--pid-file"); pidFile != "" {
			_ = os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", os.Getpid())), 0o600)
		}
		time.Sleep(30 * time.Second)
	default:
		os.Exit(2)
	}
}

func testStoreInventoryWorkerProvider(t *testing.T, mode string) storeInventoryWorkerProvider {
	t.Helper()
	return storeInventoryWorkerProvider{
		Executable:         os.Args[0],
		Args:               []string{"-test.run=TestStoreInventoryWorkerHelperProcess", "--", storeInventoryWorkerFlag},
		Env:                []string{storeInventoryWorkerHelperEnv + "=" + mode},
		Timeout:            5 * time.Second,
		SkipElevationCheck: true,
	}
}

func testWorkerResponse(scan StoreScanGeneration) storeInventoryWorkerResponse {
	record := testNativeRecord(scan.UserSID, "Vendor.App_abc123", "Vendor.App_1.0.0.0_x64__abc123", "Vendor App", StorePackageVersion{Major: 1}, storePackageClassMain)
	return storeInventoryWorkerResponse{
		ProtocolVersion: storeInventoryWorkerProtocolVersion,
		ScanID:          scan.ScanID,
		UserSID:         scan.UserSID,
		Completed:       true,
		PackageFamilies: []StorePackagedAppFamily{{
			Identity:    StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: record.PackageFamilyName},
			Primary:     record,
			Instances:   []StorePackagedAppRecord{record},
			DisplayName: "Vendor App",
			ProductLike: true,
		}},
	}
}

func helperArgValue(name string) string {
	for index, arg := range os.Args {
		if arg == name && index+1 < len(os.Args) {
			return os.Args[index+1]
		}
	}
	return ""
}

func assertProcessExited(t *testing.T, pidText string) {
	t.Helper()
	var pid uint32
	if _, err := fmt.Sscanf(pidText, "%d", &pid); err != nil {
		t.Fatalf("parse pid %q: %v", pidText, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
		if err != nil {
			return
		}
		var exitCode uint32
		err = windows.GetExitCodeProcess(handle, &exitCode)
		_ = windows.CloseHandle(handle)
		if err == nil && exitCode != windowsStillActiveExitCode {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	out, _ := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid)).CombinedOutput()
	t.Fatalf("worker process %d still appears active:\n%s", pid, out)
}
