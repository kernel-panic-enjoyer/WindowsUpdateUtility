//go:build windows

package updater

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

const storeUpdateDiscoveryWorkerHelperEnv = "UPDATER_STORE_UPDATE_DISCOVERY_WORKER_TEST_HELPER"

func TestStoreUpdateDiscoveryWorkerRejectsMalformedJSON(t *testing.T) {
	if _, err := decodeStoreUpdateDiscoveryWorkerResponse([]byte(`{"protocol_version":1}` + "\n{}")); err == nil {
		t.Fatal("expected trailing JSON rejection")
	}
	if _, err := decodeStoreUpdateDiscoveryWorkerResponse([]byte(`{"protocol_version":1,`)); err == nil {
		t.Fatal("expected malformed JSON rejection")
	}
}

func TestStoreUpdateDiscoveryWorkerRejectsUnknownProtocolVersion(t *testing.T) {
	scan := completedStoreScan("scan-winrt-protocol", "S-1-5-21-winrt-worker", StoreProviderIdentity{ID: storeWinRTDiscoveryProviderID})
	response := testStoreUpdateDiscoveryWorkerResponse(scan)
	response.ProtocolVersion = 99
	if _, err := validateStoreUpdateDiscoveryWorkerResponse(scan, response, nil); err == nil || !strings.Contains(err.Error(), "protocol version") {
		t.Fatalf("expected protocol rejection, got %v", err)
	}
}

func TestStoreUpdateDiscoveryWorkerRejectsWrongScanID(t *testing.T) {
	scan := completedStoreScan("scan-winrt-correct", "S-1-5-21-winrt-worker", StoreProviderIdentity{ID: storeWinRTDiscoveryProviderID})
	response := testStoreUpdateDiscoveryWorkerResponse(scan)
	response.ScanID = "scan-winrt-wrong"
	if _, err := validateStoreUpdateDiscoveryWorkerResponse(scan, response, nil); err == nil || !strings.Contains(err.Error(), "scan ID mismatch") {
		t.Fatalf("expected scan ID mismatch, got %v", err)
	}
}

func TestStoreUpdateDiscoveryWorkerRejectsWrongUserSID(t *testing.T) {
	scan := completedStoreScan("scan-winrt-user", "S-1-5-21-winrt-worker", StoreProviderIdentity{ID: storeWinRTDiscoveryProviderID})
	response := testStoreUpdateDiscoveryWorkerResponse(scan)
	response.UserSID = "S-1-5-21-other-user"
	if _, err := validateStoreUpdateDiscoveryWorkerResponse(scan, response, nil); err == nil || !strings.Contains(err.Error(), "user SID mismatch") {
		t.Fatalf("expected user SID mismatch, got %v", err)
	}
}

func TestStoreUpdateDiscoveryWorkerRejectsMalformedPackageFields(t *testing.T) {
	scan := completedStoreScan("scan-winrt-malformed", "S-1-5-21-winrt-worker", StoreProviderIdentity{ID: storeWinRTDiscoveryProviderID})
	response := testStoreUpdateDiscoveryWorkerResponse(scan)
	response.Items[0].PackageFamilyName = `Microsoft.Bad\Family_8wekyb3d8bbwe`
	if _, err := validateStoreUpdateDiscoveryWorkerResponse(scan, response, nil); err == nil || !strings.Contains(err.Error(), "package family name") {
		t.Fatalf("expected malformed PFN rejection, got %v", err)
	}
}

func TestStoreUpdateDiscoveryWorkerRejectsDuplicatePFN(t *testing.T) {
	scan := completedStoreScan("scan-winrt-duplicate", "S-1-5-21-winrt-worker", StoreProviderIdentity{ID: storeWinRTDiscoveryProviderID})
	response := testStoreUpdateDiscoveryWorkerResponse(scan)
	response.Items = append(response.Items, response.Items[0])
	if _, err := validateStoreUpdateDiscoveryWorkerResponse(scan, response, nil); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate PFN rejection, got %v", err)
	}
}

func TestStoreUpdateDiscoveryWorkerRejectsOversizedResponse(t *testing.T) {
	oversized := bytes.Repeat([]byte{'x'}, storeUpdateDiscoveryWorkerResponseLimit+1)
	if _, err := decodeStoreUpdateDiscoveryWorkerResponse(oversized); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected oversized response rejection, got %v", err)
	}
}

func TestStoreUpdateDiscoveryWorkerRejectsTooManyRecords(t *testing.T) {
	scan := completedStoreScan("scan-winrt-too-many", "S-1-5-21-winrt-worker", StoreProviderIdentity{ID: storeWinRTDiscoveryProviderID})
	response := testStoreUpdateDiscoveryWorkerResponse(scan)
	for len(response.Items) <= storeUpdateDiscoveryWorkerMaxItems {
		index := len(response.Items)
		response.Items = append(response.Items, storeUpdateDiscoveryItem{
			PackageFamilyName: fmt.Sprintf("Vendor.App%d_8wekyb3d8bbwe", index),
			ProductID:         "9WZDNCRFHVN5",
			OfferAvailable:    true,
		})
	}
	if _, err := validateStoreUpdateDiscoveryWorkerResponse(scan, response, nil); err == nil || !strings.Contains(err.Error(), "records") {
		t.Fatalf("expected record limit rejection, got %v", err)
	}
}

func TestStoreUpdateDiscoveryWorkerStructuredErrorWithNonzeroExitIsNotHealthy(t *testing.T) {
	scan := completedStoreScan("scan-winrt-structured-error", "S-1-5-21-winrt-worker", StoreProviderIdentity{ID: storeWinRTDiscoveryProviderID})
	provider := testStoreUpdateDiscoveryWorkerProvider(t, "structured-error-nonzero")
	response, result := provider.Discover(context.Background(), scan, nil)
	if result.OK || !response.Partial || !strings.Contains(result.Stderr, "worker reported structured failure") {
		t.Fatalf("expected nonzero structured error to remain incomplete, response=%#v result=%#v", response, result)
	}
}

func TestStoreUpdateDiscoveryWorkerProviderSendsProductIDCandidates(t *testing.T) {
	scan := completedStoreScan("scan-winrt-candidate-request", "S-1-5-21-winrt-worker", StoreProviderIdentity{ID: storeWinRTDiscoveryProviderID})
	provider := testStoreUpdateDiscoveryWorkerProvider(t, "validate-product-candidate")
	provider.Candidates = []storeUpdateDiscoveryCandidate{{
		PackageFamilyName: "Microsoft.WindowsCalculator_8wekyb3d8bbwe",
		ProductID:         "9WZDNCRFHVN5",
	}}
	response, result := provider.Discover(context.Background(), scan, nil)
	if !result.OK || !response.Completed {
		t.Fatalf("expected helper to accept Product ID candidate, response=%#v result=%#v", response, result)
	}
}

func TestStoreUpdateDiscoveryWorkerInvalidResponseWithNonzeroExitIsRejected(t *testing.T) {
	scan := completedStoreScan("scan-winrt-invalid-nonzero", "S-1-5-21-winrt-worker", StoreProviderIdentity{ID: storeWinRTDiscoveryProviderID})
	provider := testStoreUpdateDiscoveryWorkerProvider(t, "invalid-nonzero")
	_, result := provider.Discover(context.Background(), scan, nil)
	if result.OK || !strings.Contains(result.Stderr, "invalid Store update discovery worker response") {
		t.Fatalf("expected invalid nonzero response rejection, result=%#v", result)
	}
}

func TestStoreUpdateDiscoveryWorkerHangCancellationTerminatesWorker(t *testing.T) {
	pidFile := t.TempDir() + `\worker.pid`
	scan := completedStoreScan("scan-winrt-hang", "S-1-5-21-winrt-worker", StoreProviderIdentity{ID: storeWinRTDiscoveryProviderID})
	provider := testStoreUpdateDiscoveryWorkerProvider(t, "hang")
	provider.Timeout = 2 * time.Second
	args := append([]string{}, provider.Args...)
	args = append(args, "--pid-file", pidFile)
	provider.Args = args
	response, result := provider.Discover(context.Background(), scan, nil)
	if result.Code != 124 || !response.Partial {
		t.Fatalf("expected timeout cancellation, response=%#v result=%#v", response, result)
	}
	pidData, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read helper pid: %v", err)
	}
	assertProcessExited(t, strings.TrimSpace(string(pidData)))
}

func TestStoreUpdateDiscoveryWorkerRejectsOrdinaryInteractiveUse(t *testing.T) {
	code := runStoreUpdateDiscoveryWorker(bytes.NewReader(nil), io.Discard, io.Discard, winrtStoreUpdateDiscoveryProvider{})
	if code == 0 {
		t.Fatal("worker accepted empty interactive input")
	}
}

func TestStoreUpdateDiscoveryWorkerHelperProcess(t *testing.T) {
	if os.Getenv(storeUpdateDiscoveryWorkerHelperEnv) == "" {
		return
	}
	mode := os.Getenv(storeUpdateDiscoveryWorkerHelperEnv)
	switch mode {
	case "structured-error-nonzero":
		var request storeUpdateDiscoveryWorkerRequest
		_ = json.NewDecoder(os.Stdin).Decode(&request)
		_ = encodeStoreUpdateDiscoveryWorkerResponse(os.Stdout, storeUpdateDiscoveryWorkerResponse{
			ProtocolVersion: storeUpdateDiscoveryWorkerProtocolVersion,
			ScanID:          request.ScanID,
			UserSID:         request.UserSID,
			Partial:         true,
			Errors:          []string{"worker reported structured failure"},
		})
		os.Exit(1)
	case "invalid-nonzero":
		fmt.Fprint(os.Stdout, "{not-json")
		os.Exit(7)
	case "validate-product-candidate":
		var request storeUpdateDiscoveryWorkerRequest
		if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
			os.Exit(2)
		}
		if len(request.Candidates) != 1 ||
			request.Candidates[0].PackageFamilyName != "Microsoft.WindowsCalculator_8wekyb3d8bbwe" ||
			request.Candidates[0].ProductID != "9WZDNCRFHVN5" {
			_ = encodeStoreUpdateDiscoveryWorkerResponse(os.Stdout, storeUpdateDiscoveryWorkerResponse{
				ProtocolVersion: storeUpdateDiscoveryWorkerProtocolVersion,
				ScanID:          request.ScanID,
				UserSID:         request.UserSID,
				Partial:         true,
				Errors:          []string{fmt.Sprintf("unexpected candidates: %#v", request.Candidates)},
			})
			os.Exit(1)
		}
		_ = encodeStoreUpdateDiscoveryWorkerResponse(os.Stdout, storeUpdateDiscoveryWorkerResponse{
			ProtocolVersion: storeUpdateDiscoveryWorkerProtocolVersion,
			ScanID:          request.ScanID,
			UserSID:         request.UserSID,
			Completed:       true,
		})
		os.Exit(0)
	case "hang":
		if pidFile := helperArgValue("--pid-file"); pidFile != "" {
			_ = os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", os.Getpid())), 0o600)
		}
		time.Sleep(30 * time.Second)
	default:
		os.Exit(2)
	}
}

func testStoreUpdateDiscoveryWorkerProvider(t *testing.T, mode string) storeUpdateDiscoveryWorkerProvider {
	t.Helper()
	return storeUpdateDiscoveryWorkerProvider{
		Executable:         os.Args[0],
		Args:               []string{"-test.run=TestStoreUpdateDiscoveryWorkerHelperProcess", "--", storeUpdateDiscoveryWorkerFlag},
		Env:                []string{storeUpdateDiscoveryWorkerHelperEnv + "=" + mode},
		Timeout:            5 * time.Second,
		SkipElevationCheck: true,
	}
}

func testStoreUpdateDiscoveryWorkerResponse(scan StoreScanGeneration) storeUpdateDiscoveryWorkerResponse {
	return storeUpdateDiscoveryWorkerResponse{
		ProtocolVersion: storeUpdateDiscoveryWorkerProtocolVersion,
		ScanID:          scan.ScanID,
		UserSID:         scan.UserSID,
		Completed:       true,
		Items: []storeUpdateDiscoveryItem{{
			PackageFamilyName: "Microsoft.WindowsCalculator_8wekyb3d8bbwe",
			ProductID:         "9WZDNCRFHVN5",
			OfferAvailable:    true,
			InstallState:      storeInstallStateReadyToDownload,
		}},
	}
}
