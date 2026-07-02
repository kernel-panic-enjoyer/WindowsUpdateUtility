//go:build windows

package updater

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"
	"unsafe"
)

const (
	storeUpdateDiscoveryCommand    = "WinRT AppInstallManager update discovery"
	storeUpdateDiscoveryTimeout    = 4 * time.Minute
	storeUpdateDiscoveryDefaultSKU = "0010"
)

var (
	iidIAppInstallManager                     = mustWindowsGUID("{9353E170-8441-4B45-BD72-7C2FA925BEEE}")
	iidIAppInstallItem                        = mustWindowsGUID("{49D3DFAB-168A-4CBF-A93A-9E448C82737D}")
	iidIAppInstallStatus                      = mustWindowsGUID("{936DCCFA-2450-4126-88B1-6127A644DD5C}")
	iidIAsyncInfo                             = mustWindowsGUID("{00000036-0000-0000-C000-000000000046}")
	iidIAsyncOperationStoreInstallItemList    = mustWindowsGUID("{9267E107-2AC6-5E0D-86E9-3154F616C68B}")
	iidIVectorViewStoreInstallItem            = mustWindowsGUID("{48D7F874-A83C-55DB-B2E6-940BE9569869}")
	appInstallManagerRuntimeClass             = "Windows.ApplicationModel.Store.Preview.InstallControl.AppInstallManager"
	errStoreUpdateDiscoveryUnsupportedContext = errors.New("WinRT Store update discovery requires the non-elevated interactive user context")
)

// storeUpdateDiscoveryWorkerProvider isolates AppInstallManager discovery in a
// same-binary worker for the same reason as Store inventory: the parent must be
// able to kill WinRT work that ignores cancellation or blocks an OS thread.
type storeUpdateDiscoveryWorkerProvider struct {
	Executable         string
	Args               []string
	Env                []string
	Timeout            time.Duration
	SkipElevationCheck bool
	Candidates         []storeUpdateDiscoveryCandidate
}

type winrtStoreUpdateDiscoveryProvider struct {
	Search         func(context.Context, string, []storeUpdateDiscoveryCandidate) ([]storeUpdateDiscoveryItem, error)
	CurrentUserSID func() (string, error)
}

func (provider storeUpdateDiscoveryWorkerProvider) Discover(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) (storeUpdateDiscoveryWorkerResponse, CommandResult) {
	timeout := provider.Timeout
	if timeout <= 0 {
		timeout = storeUpdateDiscoveryTimeout
	}
	if !provider.SkipElevationCheck && isAdmin() {
		err := errStoreUpdateDiscoveryUnsupportedContext
		result := CommandResult{Command: storeUpdateDiscoveryCommand, Code: 1, Stderr: err.Error()}
		return incompleteStoreUpdateDiscoveryResponse(scan, err), result
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	executable := provider.Executable
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			result := CommandResult{Command: storeUpdateDiscoveryCommand, Code: 127, Stderr: err.Error()}
			return incompleteStoreUpdateDiscoveryResponse(scan, err), result
		}
	}
	args := provider.Args
	if len(args) == 0 {
		args = []string{storeUpdateDiscoveryWorkerFlag}
	}

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().UTC().Add(timeout)
	}
	request := storeUpdateDiscoveryWorkerRequest{
		ProtocolVersion: storeUpdateDiscoveryWorkerProtocolVersion,
		ScanID:          scan.ScanID,
		UserSID:         scan.UserSID,
		Deadline:        deadline.UTC(),
		Candidates:      provider.discoveryCandidates(scan, families),
	}
	requestBytes, err := encodeStoreUpdateDiscoveryWorkerRequest(request)
	if err != nil {
		result := CommandResult{Command: storeUpdateDiscoveryCommand, Code: 2, Stderr: err.Error()}
		return incompleteStoreUpdateDiscoveryResponse(scan, err), result
	}

	commandText := strings.Join(append([]string{executable}, args...), " ")
	result := CommandResult{Command: commandText}
	cmd := exec.Command(executable, args...)
	cmd.Env = launchEnv()
	if len(provider.Env) > 0 {
		cmd.Env = append(cmd.Env, provider.Env...)
	}
	cmd.SysProcAttr = hiddenSysProcAttr()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		result.Code = 127
		result.Stderr = err.Error()
		return incompleteStoreUpdateDiscoveryResponse(scan, err), result
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		result.Code = 127
		result.Stderr = err.Error()
		return incompleteStoreUpdateDiscoveryResponse(scan, err), result
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		result.Code = 127
		result.Stderr = err.Error()
		return incompleteStoreUpdateDiscoveryResponse(scan, err), result
	}

	owner, err := newCommandProcessOwner(true)
	if err != nil {
		result.Code = 127
		result.Stderr = err.Error()
		return incompleteStoreUpdateDiscoveryResponse(scan, err), result
	}
	defer owner.Close()

	if err := cmd.Start(); err != nil {
		result.Code = 127
		result.Stderr = err.Error()
		return incompleteStoreUpdateDiscoveryResponse(scan, err), result
	}
	if err := owner.Assign(cmd); err != nil {
		terminateStartedCommand(cmd, owner)
		_ = cmd.Wait()
		result.Code = 127
		result.Stderr = err.Error()
		return incompleteStoreUpdateDiscoveryResponse(scan, err), result
	}

	writeDone := make(chan error, 1)
	go func() {
		_, writeErr := stdin.Write(requestBytes)
		closeErr := stdin.Close()
		if writeErr != nil {
			writeDone <- writeErr
			return
		}
		writeDone <- closeErr
	}()
	stdoutDone := make(chan []byte, 1)
	stderrTail := newBoundedOutputTail(commandResultStreamLimitBytes)
	stderrDone := make(chan struct{})
	go func() {
		stdoutDone <- readBoundedPipe(stdout, storeUpdateDiscoveryWorkerResponseLimit)
	}()
	go func() {
		_, _ = io.Copy(stderrTail, stderr)
		close(stderrDone)
	}()

	waitErr := waitForStartedCommand(ctx, cmd, owner)
	<-writeDone
	stdoutBytes := <-stdoutDone
	<-stderrDone
	result.Stderr = stderrTail.String()
	if ctx.Err() == context.DeadlineExceeded {
		err := errors.New("Store update discovery worker timed out and was terminated")
		result.Code = 124
		result.Stderr = appendDiagnostic(result.Stderr, err.Error())
		return incompleteStoreUpdateDiscoveryResponse(scan, err), result
	}
	if ctx.Err() == context.Canceled {
		err := errors.New("Store update discovery worker was cancelled and terminated")
		result.Code = commandCancelledCode
		result.Stderr = appendDiagnostic(result.Stderr, err.Error())
		return incompleteStoreUpdateDiscoveryResponse(scan, err), result
	}

	response, parseErr := decodeStoreUpdateDiscoveryWorkerResponse(stdoutBytes)
	var exitCode int
	if waitErr != nil {
		exitCode = commandExitCode(waitErr)
		result.Code = exitCode
		result.Stderr = appendDiagnostic(result.Stderr, waitErr.Error())
	}
	if parseErr != nil {
		result.Code = firstNonZero(result.Code, 2)
		err := fmt.Errorf("invalid Store update discovery worker response: %w", parseErr)
		result.Stderr = appendDiagnostic(result.Stderr, err.Error())
		return incompleteStoreUpdateDiscoveryResponse(scan, err), result
	}
	if _, validationErr := validateStoreUpdateDiscoveryWorkerResponse(scan, response, nil); validationErr != nil {
		result.Code = firstNonZero(result.Code, 2)
		result.Stderr = appendDiagnostic(result.Stderr, validationErr.Error())
		return incompleteStoreUpdateDiscoveryResponse(scan, validationErr), result
	}
	result.Stdout = fmt.Sprintf("Store update discovery worker returned %d item(s).", len(response.Items))
	if waitErr != nil {
		err := fmt.Errorf("Store update discovery worker exited with code %d", exitCode)
		response.Partial = true
		response.Errors = appendBoundedDiagnostics(response.Errors, err.Error())
		result.Stderr = appendDiagnostic(result.Stderr, strings.Join(response.Errors, "\n"))
		return response, result
	}
	if response.Partial || !response.Completed || len(response.Errors) > 0 {
		result.Code = 1
		result.Stderr = appendDiagnostic(result.Stderr, strings.Join(response.Errors, "\n"))
		return response, result
	}
	result.OK = true
	return response, result
}

func (provider storeUpdateDiscoveryWorkerProvider) discoveryCandidates(scan StoreScanGeneration, families []StorePackagedAppFamily) []storeUpdateDiscoveryCandidate {
	if provider.Candidates != nil {
		return provider.Candidates
	}
	return storeUpdateDiscoveryCandidates(scan, families, StoreScanSnapshot{}, false, time.Time{}, "")
}

func runStoreUpdateDiscoveryWorkerFromArgs() int {
	if len(os.Args) != 2 || os.Args[1] != storeUpdateDiscoveryWorkerFlag {
		fmt.Fprintln(os.Stderr, "internal unsupported mode requires exactly --store-update-discovery-worker")
		return 2
	}
	return runStoreUpdateDiscoveryWorker(os.Stdin, os.Stdout, os.Stderr, winrtStoreUpdateDiscoveryProvider{})
}

// runStoreUpdateDiscoveryWorker exposes exactly one internal operation:
// current-user Store update discovery. It rejects interactive use and receives
// only bounded PFN/ProductID candidates plus scan metadata over stdin.
func runStoreUpdateDiscoveryWorker(input io.Reader, output io.Writer, diagnostics io.Writer, provider winrtStoreUpdateDiscoveryProvider) int {
	request, err := decodeStoreUpdateDiscoveryWorkerRequest(input)
	if err != nil {
		fmt.Fprintln(diagnostics, err)
		return 2
	}
	if err := validateStoreUpdateDiscoveryWorkerRequest(request); err != nil {
		_ = encodeStoreUpdateDiscoveryWorkerResponse(output, storeUpdateDiscoveryWorkerResponse{
			ProtocolVersion: storeUpdateDiscoveryWorkerProtocolVersion,
			ScanID:          request.ScanID,
			UserSID:         request.UserSID,
			Partial:         true,
			Errors:          []string{err.Error()},
		})
		return 2
	}
	now := time.Now().UTC()
	if request.Deadline.Before(now) {
		_ = encodeStoreUpdateDiscoveryWorkerResponse(output, storeUpdateDiscoveryWorkerResponse{
			ProtocolVersion: storeUpdateDiscoveryWorkerProtocolVersion,
			ScanID:          request.ScanID,
			UserSID:         request.UserSID,
			Partial:         true,
			Errors:          []string{"Store update discovery worker deadline already expired"},
		})
		return 1
	}
	ctx, cancel := context.WithDeadline(context.Background(), request.Deadline)
	defer cancel()
	search := provider.Search
	if search == nil {
		search = enumerateWinRTStoreUpdates
	}
	currentSID := currentUserSID
	if provider.CurrentUserSID != nil {
		currentSID = provider.CurrentUserSID
	}
	sid, err := currentSID()
	if err != nil {
		_ = encodeStoreUpdateDiscoveryWorkerResponse(output, storeUpdateDiscoveryWorkerResponse{
			ProtocolVersion: storeUpdateDiscoveryWorkerProtocolVersion,
			ScanID:          request.ScanID,
			UserSID:         request.UserSID,
			Partial:         true,
			Errors:          []string{err.Error()},
		})
		return 1
	}
	if !strings.EqualFold(strings.TrimSpace(sid), strings.TrimSpace(request.UserSID)) {
		err := fmt.Errorf("Store update discovery user SID mismatch: process user %q, scan user %q", sid, request.UserSID)
		_ = encodeStoreUpdateDiscoveryWorkerResponse(output, storeUpdateDiscoveryWorkerResponse{
			ProtocolVersion: storeUpdateDiscoveryWorkerProtocolVersion,
			ScanID:          request.ScanID,
			UserSID:         request.UserSID,
			Partial:         true,
			Errors:          []string{err.Error()},
		})
		return 1
	}
	items, err := search(ctx, request.UserSID, request.Candidates)
	response := storeUpdateDiscoveryWorkerResponse{
		ProtocolVersion: storeUpdateDiscoveryWorkerProtocolVersion,
		ScanID:          request.ScanID,
		UserSID:         request.UserSID,
		Completed:       err == nil,
		Partial:         err != nil,
		Items:           items,
	}
	if err != nil {
		response.Errors = appendBoundedDiagnostics(response.Errors, err.Error())
	}
	if err := encodeStoreUpdateDiscoveryWorkerResponse(output, response); err != nil {
		fmt.Fprintln(diagnostics, err)
		return 2
	}
	if response.Partial || !response.Completed {
		return 1
	}
	return 0
}

func incompleteStoreUpdateDiscoveryResponse(scan StoreScanGeneration, err error) storeUpdateDiscoveryWorkerResponse {
	return storeUpdateDiscoveryWorkerResponse{
		ProtocolVersion: storeUpdateDiscoveryWorkerProtocolVersion,
		ScanID:          scan.ScanID,
		UserSID:         scan.UserSID,
		Partial:         true,
		Errors:          appendBoundedDiagnostics(nil, err.Error()),
	}
}

func encodeStoreUpdateDiscoveryWorkerRequest(request storeUpdateDiscoveryWorkerRequest) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	if err := encoder.Encode(request); err != nil {
		return nil, err
	}
	if buffer.Len() > storeUpdateDiscoveryWorkerRequestLimit {
		return nil, fmt.Errorf("Store update discovery worker request exceeds %d bytes", storeUpdateDiscoveryWorkerRequestLimit)
	}
	return buffer.Bytes(), nil
}

func decodeStoreUpdateDiscoveryWorkerRequest(input io.Reader) (storeUpdateDiscoveryWorkerRequest, error) {
	var request storeUpdateDiscoveryWorkerRequest
	data := readBoundedPipe(input, storeUpdateDiscoveryWorkerRequestLimit)
	if len(data) > storeUpdateDiscoveryWorkerRequestLimit {
		return request, fmt.Errorf("Store update discovery worker request exceeds %d bytes", storeUpdateDiscoveryWorkerRequestLimit)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return request, err
	}
	return request, nil
}

func encodeStoreUpdateDiscoveryWorkerResponse(output io.Writer, response storeUpdateDiscoveryWorkerResponse) error {
	data, err := json.Marshal(response)
	if err != nil {
		return err
	}
	if len(data) > storeUpdateDiscoveryWorkerResponseLimit {
		return fmt.Errorf("Store update discovery worker response exceeds %d bytes", storeUpdateDiscoveryWorkerResponseLimit)
	}
	_, err = output.Write(append(data, '\n'))
	return err
}

func decodeStoreUpdateDiscoveryWorkerResponse(data []byte) (storeUpdateDiscoveryWorkerResponse, error) {
	var response storeUpdateDiscoveryWorkerResponse
	if len(data) > storeUpdateDiscoveryWorkerResponseLimit {
		return response, fmt.Errorf("response exceeds %d bytes", storeUpdateDiscoveryWorkerResponseLimit)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return response, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return response, err
	}
	return response, nil
}

func validateStoreUpdateDiscoveryWorkerRequest(request storeUpdateDiscoveryWorkerRequest) error {
	if request.ProtocolVersion != storeUpdateDiscoveryWorkerProtocolVersion {
		return fmt.Errorf("unsupported Store update discovery worker protocol version %d", request.ProtocolVersion)
	}
	if strings.TrimSpace(request.ScanID) == "" {
		return errors.New("Store update discovery worker request missing scan ID")
	}
	if strings.TrimSpace(request.UserSID) == "" {
		return errors.New("Store update discovery worker request missing user SID")
	}
	if request.Deadline.IsZero() {
		return errors.New("Store update discovery worker request missing deadline")
	}
	if len(request.Candidates) > storeUpdateDiscoveryWorkerMaxCandidates {
		return fmt.Errorf("Store update discovery worker request contains %d candidates; limit is %d", len(request.Candidates), storeUpdateDiscoveryWorkerMaxCandidates)
	}
	seen := map[string]bool{}
	for _, candidate := range request.Candidates {
		pfn := strings.TrimSpace(candidate.PackageFamilyName)
		if !validStorePackageFamilyName(pfn) {
			return fmt.Errorf("Store update discovery worker request contains malformed package family name %q", candidate.PackageFamilyName)
		}
		key := strings.ToLower(pfn)
		if seen[key] {
			return fmt.Errorf("Store update discovery worker request contains duplicate package family %q", pfn)
		}
		seen[key] = true
		productID := strings.TrimSpace(candidate.ProductID)
		if productID != "" && !looksLikeStoreProductID(productID) {
			return fmt.Errorf("Store update discovery worker request contains malformed Product ID %q", productID)
		}
	}
	return nil
}

func enumerateWinRTStoreUpdates(ctx context.Context, userSID string, candidates []storeUpdateDiscoveryCandidate) ([]storeUpdateDiscoveryItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// Why: InstallControl must be called from the interactive user's WinRT
	// context. Running it here avoids depending on the Microsoft Store app UI
	// while preserving the same SID/PFN identity model as inventory.
	if err := winrtInitialize(); err != nil {
		return nil, err
	}
	defer winrtUninitialize()

	className, err := newHString(appInstallManagerRuntimeClass)
	if err != nil {
		return nil, err
	}
	defer className.Delete()

	var inspectable unsafe.Pointer
	if err := winrtCall("RoActivateInstance AppInstallManager", procRoActivateInstance.Addr(), className.Handle, uintptr(unsafe.Pointer(&inspectable))); err != nil {
		return nil, err
	}
	if inspectable == nil {
		return nil, errors.New("AppInstallManager activation returned nil")
	}
	defer winrtRelease(inspectable)

	managerPtr, err := winrtQueryInterface(inspectable, iidIAppInstallManager)
	if err != nil {
		return nil, fmt.Errorf("IAppInstallManager: %w", err)
	}
	defer winrtRelease(managerPtr)

	manager := (*winrtAppInstallManager)(managerPtr)
	searchItems, searchErr := searchWinRTStoreUpdates(ctx, managerPtr, manager)
	targetedItems, targetedErr := searchTargetedWinRTStoreUpdates(ctx, managerPtr, manager, candidates)
	queueItems, queueErr := currentWinRTStoreInstallItems(managerPtr, manager)
	items := mergeStoreUpdateDiscoveryItems(searchItems, targetedItems, queueItems)
	if searchErr != nil && targetedErr != nil && queueErr != nil {
		return items, fmt.Errorf("SearchForAllUpdatesAsync failed: %v; targeted SearchForUpdatesAsync failed: %v; AppInstallItems failed: %w", searchErr, targetedErr, queueErr)
	}
	if len(items) == 0 {
		if searchErr != nil {
			return items, searchErr
		}
		if targetedErr != nil {
			return items, targetedErr
		}
		if queueErr != nil {
			return items, queueErr
		}
	}
	return items, nil
}

func searchWinRTStoreUpdates(ctx context.Context, managerPtr unsafe.Pointer, manager *winrtAppInstallManager) ([]storeUpdateDiscoveryItem, error) {
	var asyncPtr unsafe.Pointer
	if err := winrtCall("IAppInstallManager.SearchForAllUpdatesAsync", manager.Vtbl.SearchForAllUpdatesAsync, uintptr(managerPtr), uintptr(unsafe.Pointer(&asyncPtr))); err != nil {
		return nil, err
	}
	if asyncPtr == nil {
		return nil, errors.New("SearchForAllUpdatesAsync returned nil")
	}
	defer winrtRelease(asyncPtr)

	resultPtr, err := waitForStoreUpdateDiscoveryAsync(ctx, asyncPtr)
	if err != nil {
		return nil, err
	}
	if resultPtr == nil {
		return nil, errors.New("SearchForAllUpdatesAsync returned nil update collection")
	}
	defer winrtRelease(resultPtr)

	viewPtr, err := winrtQueryInterface(resultPtr, iidIVectorViewStoreInstallItem)
	if err != nil {
		return nil, fmt.Errorf("IVectorView<AppInstallItem>: %w; class=%s", err, winrtRuntimeClassName(resultPtr))
	}
	defer winrtRelease(viewPtr)

	items, err := appInstallItemsFromVectorView(viewPtr, storeUpdateDiscoverySourceSearch)
	if err != nil {
		return nil, err
	}
	return items, nil
}

func searchTargetedWinRTStoreUpdates(ctx context.Context, managerPtr unsafe.Pointer, manager *winrtAppInstallManager, candidates []storeUpdateDiscoveryCandidate) ([]storeUpdateDiscoveryItem, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	seenProductIDs := map[string]bool{}
	items := make([]storeUpdateDiscoveryItem, 0)
	var firstErr error
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return items, err
		}
		productID := strings.TrimSpace(candidate.ProductID)
		if productID == "" {
			continue
		}
		productKey := strings.ToLower(productID)
		if seenProductIDs[productKey] {
			continue
		}
		seenProductIDs[productKey] = true
		item, err := searchSingleWinRTStoreProductUpdate(ctx, managerPtr, manager, productID)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if item == nil {
			continue
		}
		items = append(items, *item)
	}
	if len(items) > 0 {
		return items, nil
	}
	if errors.Is(firstErr, context.Canceled) || errors.Is(firstErr, context.DeadlineExceeded) {
		return nil, firstErr
	}
	return nil, nil
}

func searchSingleWinRTStoreProductUpdate(ctx context.Context, managerPtr unsafe.Pointer, manager *winrtAppInstallManager, productID string) (*storeUpdateDiscoveryItem, error) {
	var firstErr error
	for _, skuID := range []string{storeUpdateDiscoveryDefaultSKU, ""} {
		item, err := searchSingleWinRTStoreProductUpdateSKU(ctx, managerPtr, manager, productID, skuID)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if item != nil {
			return item, nil
		}
	}
	return nil, firstErr
}

func searchSingleWinRTStoreProductUpdateSKU(ctx context.Context, managerPtr unsafe.Pointer, manager *winrtAppInstallManager, productID, skuID string) (*storeUpdateDiscoveryItem, error) {
	productHString, err := newHString(productID)
	if err != nil {
		return nil, err
	}
	defer productHString.Delete()
	skuHString, err := newHString(skuID)
	if err != nil {
		return nil, err
	}
	defer skuHString.Delete()

	var asyncPtr unsafe.Pointer
	if err := winrtCall("IAppInstallManager.SearchForUpdatesAsync", manager.Vtbl.SearchForUpdatesAsync, uintptr(managerPtr), productHString.Handle, skuHString.Handle, uintptr(unsafe.Pointer(&asyncPtr))); err != nil {
		return nil, err
	}
	if asyncPtr == nil {
		return nil, nil
	}
	defer winrtRelease(asyncPtr)

	resultPtr, err := waitForStoreUpdateDiscoveryItemAsync(ctx, asyncPtr, "SearchForUpdatesAsync")
	if err != nil {
		return nil, err
	}
	if resultPtr == nil {
		return nil, nil
	}
	defer winrtRelease(resultPtr)

	item, err := storeUpdateDiscoveryItemFromWinRT(resultPtr, storeUpdateDiscoverySourceSearch)
	if err != nil {
		return nil, err
	}
	if item.ProductID == "" {
		item.ProductID = productID
	} else if !strings.EqualFold(item.ProductID, productID) {
		return nil, fmt.Errorf("SearchForUpdatesAsync returned Product ID %q for requested Product ID %q", item.ProductID, productID)
	}
	item.Diagnostic = appendDiagnostic(item.Diagnostic, "targeted Product ID discovery")
	item, err = normalizeStoreUpdateDiscoveryItem(item)
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func currentWinRTStoreInstallItems(managerPtr unsafe.Pointer, manager *winrtAppInstallManager) ([]storeUpdateDiscoveryItem, error) {
	var viewPtr unsafe.Pointer
	if err := winrtCall("IAppInstallManager.AppInstallItems", manager.Vtbl.GetAppInstallItems, uintptr(managerPtr), uintptr(unsafe.Pointer(&viewPtr))); err != nil {
		return nil, err
	}
	if viewPtr == nil {
		return nil, nil
	}
	defer winrtRelease(viewPtr)
	return appInstallItemsFromVectorView(viewPtr, storeUpdateDiscoverySourceQueue)
}

func waitForStoreUpdateDiscoveryItemAsync(ctx context.Context, asyncPtr unsafe.Pointer, operationName string) (unsafe.Pointer, error) {
	infoPtr, err := winrtQueryInterface(asyncPtr, iidIAsyncInfo)
	if err != nil {
		return nil, fmt.Errorf("IAsyncInfo: %w", err)
	}
	defer winrtRelease(infoPtr)
	info := (*winrtAsyncInfo)(infoPtr)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		var status int32
		if err := winrtCall("IAsyncInfo.Status", info.Vtbl.GetStatus, uintptr(infoPtr), uintptr(unsafe.Pointer(&status))); err != nil {
			return nil, err
		}
		switch status {
		case 1:
			operation := (*winrtAsyncOperationStoreInstallItem)(asyncPtr)
			var resultPtr unsafe.Pointer
			if err := winrtCall("IAsyncOperation<AppInstallItem>.GetResults", operation.Vtbl.GetResults, uintptr(asyncPtr), uintptr(unsafe.Pointer(&resultPtr))); err != nil {
				return nil, err
			}
			return resultPtr, nil
		case 2:
			return nil, fmt.Errorf("%s was cancelled", operationName)
		case 3:
			var code uint32
			_ = winrtCall("IAsyncInfo.ErrorCode", info.Vtbl.GetErrorCode, uintptr(infoPtr), uintptr(unsafe.Pointer(&code)))
			return nil, fmt.Errorf("%s failed: HRESULT 0x%08X", operationName, code)
		}
		select {
		case <-ctx.Done():
			_ = winrtCall("IAsyncInfo.Cancel", info.Vtbl.Cancel, uintptr(infoPtr))
			_ = winrtCall("IAsyncInfo.Close", info.Vtbl.Close, uintptr(infoPtr))
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func waitForStoreUpdateDiscoveryAsync(ctx context.Context, asyncPtr unsafe.Pointer) (unsafe.Pointer, error) {
	infoPtr, err := winrtQueryInterface(asyncPtr, iidIAsyncInfo)
	if err != nil {
		return nil, fmt.Errorf("IAsyncInfo: %w", err)
	}
	defer winrtRelease(infoPtr)
	info := (*winrtAsyncInfo)(infoPtr)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		var status int32
		if err := winrtCall("IAsyncInfo.Status", info.Vtbl.GetStatus, uintptr(infoPtr), uintptr(unsafe.Pointer(&status))); err != nil {
			return nil, err
		}
		switch status {
		case 1:
			operationPtr, err := winrtQueryInterface(asyncPtr, iidIAsyncOperationStoreInstallItemList)
			if err != nil {
				return nil, fmt.Errorf("IAsyncOperation<AppInstallItemList>: %w", err)
			}
			defer winrtRelease(operationPtr)
			operation := (*winrtAsyncOperationStoreInstallItemList)(operationPtr)
			var resultPtr unsafe.Pointer
			if err := winrtCall("IAsyncOperation<AppInstallItemList>.GetResults", operation.Vtbl.GetResults, uintptr(operationPtr), uintptr(unsafe.Pointer(&resultPtr))); err != nil {
				return nil, err
			}
			return resultPtr, nil
		case 2:
			return nil, errors.New("SearchForAllUpdatesAsync was cancelled")
		case 3:
			var code uint32
			_ = winrtCall("IAsyncInfo.ErrorCode", info.Vtbl.GetErrorCode, uintptr(infoPtr), uintptr(unsafe.Pointer(&code)))
			return nil, fmt.Errorf("SearchForAllUpdatesAsync failed: HRESULT 0x%08X", code)
		}
		select {
		case <-ctx.Done():
			_ = winrtCall("IAsyncInfo.Cancel", info.Vtbl.Cancel, uintptr(infoPtr))
			_ = winrtCall("IAsyncInfo.Close", info.Vtbl.Close, uintptr(infoPtr))
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

type storeUpdateDiscoverySource int

const (
	storeUpdateDiscoverySourceSearch storeUpdateDiscoverySource = iota
	storeUpdateDiscoverySourceQueue
)

func appInstallItemsFromVectorView(viewPtr unsafe.Pointer, source storeUpdateDiscoverySource) ([]storeUpdateDiscoveryItem, error) {
	view := (*winrtVectorViewStoreInstallItem)(viewPtr)
	var size uint32
	if err := winrtCall("IVectorView<AppInstallItem>.Size", view.Vtbl.GetSize, uintptr(viewPtr), uintptr(unsafe.Pointer(&size))); err != nil {
		return nil, err
	}
	if size > storeUpdateDiscoveryWorkerMaxItems {
		return nil, fmt.Errorf("WinRT Store update discovery returned %d items; limit is %d", size, storeUpdateDiscoveryWorkerMaxItems)
	}
	items := make([]storeUpdateDiscoveryItem, 0, size)
	for index := uint32(0); index < size; index++ {
		var itemInspectable unsafe.Pointer
		if err := winrtCall("IVectorView<AppInstallItem>.GetAt", view.Vtbl.GetAt, uintptr(viewPtr), uintptr(index), uintptr(unsafe.Pointer(&itemInspectable))); err != nil {
			return nil, err
		}
		if itemInspectable == nil {
			continue
		}
		item, err := storeUpdateDiscoveryItemFromWinRT(itemInspectable, source)
		winrtRelease(itemInspectable)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func storeUpdateDiscoveryItemFromWinRT(itemInspectable unsafe.Pointer, source storeUpdateDiscoverySource) (storeUpdateDiscoveryItem, error) {
	itemPtr, err := winrtQueryInterface(itemInspectable, iidIAppInstallItem)
	if err != nil {
		return storeUpdateDiscoveryItem{}, fmt.Errorf("IAppInstallItem: %w", err)
	}
	defer winrtRelease(itemPtr)
	itemValue := (*winrtAppInstallItem)(itemPtr)
	item := storeUpdateDiscoveryItem{
		ProductID:         mustWinRTString(itemValue.Vtbl.GetProductID, itemPtr),
		PackageFamilyName: mustWinRTString(itemValue.Vtbl.GetPackageFamilyName, itemPtr),
	}
	var installType int32
	if err := winrtCall("IAppInstallItem.InstallType", itemValue.Vtbl.GetInstallType, uintptr(itemPtr), uintptr(unsafe.Pointer(&installType))); err == nil {
		item.InstallTypeCode = int(installType)
		item.InstallType = storeInstallTypeName(installType)
	}
	var statusInspectable unsafe.Pointer
	if err := winrtCall("IAppInstallItem.GetCurrentStatus", itemValue.Vtbl.GetCurrentStatus, uintptr(itemPtr), uintptr(unsafe.Pointer(&statusInspectable))); err == nil && statusInspectable != nil {
		status, statusErr := storeUpdateDiscoveryStatusFromWinRT(statusInspectable)
		winrtRelease(statusInspectable)
		if statusErr != nil {
			return storeUpdateDiscoveryItem{}, statusErr
		}
		item.InstallState = status.InstallState
		item.InstallStateCode = status.InstallStateCode
		item.PercentComplete = status.PercentComplete
		item.DownloadSizeBytes = status.DownloadSizeBytes
		item.BytesDownloaded = status.BytesDownloaded
		item.ErrorCode = status.ErrorCode
	}
	switch source {
	case storeUpdateDiscoverySourceSearch:
		item.OfferAvailable = true
	case storeUpdateDiscoverySourceQueue:
		item.OfferAvailable = item.InstallTypeCode == 1
		item.QueueStatusOnly = !item.OfferAvailable
	}
	return normalizeStoreUpdateDiscoveryItem(item)
}

func mergeStoreUpdateDiscoveryItems(groups ...[]storeUpdateDiscoveryItem) []storeUpdateDiscoveryItem {
	byPFN := map[string]storeUpdateDiscoveryItem{}
	for _, group := range groups {
		for _, item := range group {
			key := strings.ToLower(strings.TrimSpace(item.PackageFamilyName))
			if key == "" {
				continue
			}
			existing, found := byPFN[key]
			if !found || storeUpdateDiscoveryItemPriority(item) > storeUpdateDiscoveryItemPriority(existing) {
				byPFN[key] = item
			}
		}
	}
	keys := make([]string, 0, len(byPFN))
	for key := range byPFN {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]storeUpdateDiscoveryItem, 0, len(keys))
	for _, key := range keys {
		out = append(out, byPFN[key])
	}
	return out
}

func storeUpdateDiscoveryItemPriority(item storeUpdateDiscoveryItem) int {
	if item.OfferAvailable && !item.QueueStatusOnly {
		return 3
	}
	if item.OfferAvailable {
		return 2
	}
	return 1
}

func storeUpdateDiscoveryStatusFromWinRT(statusInspectable unsafe.Pointer) (storeUpdateDiscoveryItem, error) {
	statusPtr, err := winrtQueryInterface(statusInspectable, iidIAppInstallStatus)
	if err != nil {
		return storeUpdateDiscoveryItem{}, fmt.Errorf("IAppInstallStatus: %w", err)
	}
	defer winrtRelease(statusPtr)
	statusValue := (*winrtAppInstallStatus)(statusPtr)
	var state int32
	if err := winrtCall("IAppInstallStatus.InstallState", statusValue.Vtbl.GetInstallState, uintptr(statusPtr), uintptr(unsafe.Pointer(&state))); err != nil {
		return storeUpdateDiscoveryItem{}, err
	}
	var downloadSize uint64
	_ = winrtCall("IAppInstallStatus.DownloadSizeInBytes", statusValue.Vtbl.GetDownloadSizeInBytes, uintptr(statusPtr), uintptr(unsafe.Pointer(&downloadSize)))
	var downloaded uint64
	_ = winrtCall("IAppInstallStatus.BytesDownloaded", statusValue.Vtbl.GetBytesDownloaded, uintptr(statusPtr), uintptr(unsafe.Pointer(&downloaded)))
	var percent float64
	_ = winrtCall("IAppInstallStatus.PercentComplete", statusValue.Vtbl.GetPercentComplete, uintptr(statusPtr), uintptr(unsafe.Pointer(&percent)))
	var errorCode uint32
	_ = winrtCall("IAppInstallStatus.ErrorCode", statusValue.Vtbl.GetErrorCode, uintptr(statusPtr), uintptr(unsafe.Pointer(&errorCode)))
	item := storeUpdateDiscoveryItem{
		InstallStateCode:  int(state),
		InstallState:      storeInstallStateName(state),
		DownloadSizeBytes: downloadSize,
		BytesDownloaded:   downloaded,
		PercentComplete:   percent,
	}
	if errorCode != 0 {
		item.ErrorCode = fmt.Sprintf("0x%08X", errorCode)
	}
	return item, nil
}

func storeInstallStateName(value int32) string {
	switch value {
	case 0:
		return storeInstallStatePending
	case 1:
		return storeInstallStateStarting
	case 2:
		return storeInstallStateAcquiringLicense
	case 3:
		return storeInstallStateDownloading
	case 4:
		return storeInstallStateRestoringData
	case 5:
		return storeInstallStateInstalling
	case 6:
		return storeInstallStateCompleted
	case 7:
		return storeInstallStateCanceled
	case 8:
		return storeInstallStatePaused
	case 9:
		return storeInstallStateError
	case 10:
		return storeInstallStatePausedLowBattery
	case 11:
		return "paused_wifi_recommended"
	case 12:
		return storeInstallStatePausedWiFiRequired
	case 13:
		return storeInstallStateReadyToDownload
	default:
		return fmt.Sprintf("state_%d", value)
	}
}

func storeInstallTypeName(value int32) string {
	switch value {
	case 0:
		return storeInstallTypeInstall
	case 1:
		return storeInstallTypeUpdate
	case 2:
		return storeInstallTypeRepair
	default:
		return fmt.Sprintf("type_%d", value)
	}
}

type winrtAppInstallManager struct {
	Vtbl *winrtAppInstallManagerVtbl
}

type winrtAppInstallManagerVtbl struct {
	winrtInspectableVtbl
	GetAppInstallItems            uintptr
	Cancel                        uintptr
	Pause                         uintptr
	Restart                       uintptr
	AddItemCompleted              uintptr
	RemoveItemCompleted           uintptr
	AddItemStatusChanged          uintptr
	RemoveItemStatusChanged       uintptr
	GetAutoUpdateSetting          uintptr
	PutAutoUpdateSetting          uintptr
	GetAcquisitionIdentity        uintptr
	PutAcquisitionIdentity        uintptr
	GetIsApplicableAsync          uintptr
	StartAppInstallAsync          uintptr
	UpdateAppByPackageFamilyName  uintptr
	SearchForUpdatesAsync         uintptr
	SearchForAllUpdatesAsync      uintptr
	IsStoreBlockedByPolicyAsync   uintptr
	GetIsAppAllowedToInstallAsync uintptr
}

type winrtAsyncInfo struct {
	Vtbl *winrtAsyncInfoVtbl
}

type winrtAsyncInfoVtbl struct {
	winrtInspectableVtbl
	GetID        uintptr
	GetStatus    uintptr
	GetErrorCode uintptr
	Cancel       uintptr
	Close        uintptr
}

type winrtAsyncOperationStoreInstallItemList struct {
	Vtbl *winrtAsyncOperationStoreInstallItemListVtbl
}

type winrtAsyncOperationStoreInstallItemListVtbl struct {
	winrtInspectableVtbl
	PutCompleted uintptr
	GetCompleted uintptr
	GetResults   uintptr
}

type winrtAsyncOperationStoreInstallItem struct {
	Vtbl *winrtAsyncOperationStoreInstallItemVtbl
}

type winrtAsyncOperationStoreInstallItemVtbl struct {
	winrtInspectableVtbl
	PutCompleted uintptr
	GetCompleted uintptr
	GetResults   uintptr
}

type winrtVectorViewStoreInstallItem struct {
	Vtbl *winrtVectorViewStoreInstallItemVtbl
}

type winrtVectorViewStoreInstallItemVtbl struct {
	winrtInspectableVtbl
	GetAt   uintptr
	GetSize uintptr
	IndexOf uintptr
	GetMany uintptr
}

type winrtAppInstallItem struct {
	Vtbl *winrtAppInstallItemVtbl
}

type winrtAppInstallItemVtbl struct {
	winrtInspectableVtbl
	GetProductID         uintptr
	GetPackageFamilyName uintptr
	GetInstallType       uintptr
	GetIsUserInitiated   uintptr
	GetCurrentStatus     uintptr
	Cancel               uintptr
	Pause                uintptr
	Restart              uintptr
	AddCompleted         uintptr
	RemoveCompleted      uintptr
	AddStatusChanged     uintptr
	RemoveStatusChanged  uintptr
}

type winrtAppInstallStatus struct {
	Vtbl *winrtAppInstallStatusVtbl
}

type winrtAppInstallStatusVtbl struct {
	winrtInspectableVtbl
	GetInstallState        uintptr
	GetDownloadSizeInBytes uintptr
	GetBytesDownloaded     uintptr
	GetPercentComplete     uintptr
	GetErrorCode           uintptr
}
