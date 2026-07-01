package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	elevatedWorkerStartupTimeout  = 2 * time.Minute
	elevatedWorkerShutdownTimeout = 5 * time.Second
	elevatedWorkerCancelTimeout   = 10 * time.Second
	elevatedWorkerPipeBufferSize  = 64 * 1024
)

type elevatedWorkerInvocation struct {
	Operation string
	Payload   any
	Progress  func(elevatedWorkerProgress)
}

type elevatedWorkerOperationResult struct {
	Result  CommandResult
	Results []UpdateResult
}

type elevatedWorkerProcess struct {
	handle windows.Handle
}

type elevatedWorkerExit struct {
	Code uint32
	Err  error
}

func (process elevatedWorkerProcess) Close() {
	if process.handle != 0 {
		_ = windows.CloseHandle(process.handle)
	}
}

func (process elevatedWorkerProcess) Terminate() {
	if process.handle != 0 {
		_ = windows.TerminateProcess(process.handle, uint32(commandCancelledCode))
	}
}

func (process elevatedWorkerProcess) Wait() elevatedWorkerExit {
	if process.handle == 0 {
		return elevatedWorkerExit{}
	}
	event, err := windows.WaitForSingleObject(process.handle, windows.INFINITE)
	if err != nil {
		return elevatedWorkerExit{Err: err}
	}
	if event != windows.WAIT_OBJECT_0 {
		return elevatedWorkerExit{Err: fmt.Errorf("unexpected elevated worker wait result %d", event)}
	}
	var code uint32
	if err := windows.GetExitCodeProcess(process.handle, &code); err != nil {
		return elevatedWorkerExit{Err: err}
	}
	return elevatedWorkerExit{Code: code}
}

func runElevatedWorkerOperation(ctx context.Context, invocation elevatedWorkerInvocation) CommandResult {
	if err := validateWorkerOperationPayloadFromAny(invocation.Operation, invocation.Payload); err != nil {
		return validationCommandResult(invocation.Operation, err)
	}
	response, err := runElevatedWorkerInvocation(ctx, invocation)
	if err != nil {
		return workerCommandResultError(invocation.Operation, err)
	}
	appendWorkerResultLogsContext(ctx, response.Result)
	return response.Result
}

func runElevatedWorkerInvocation(ctx context.Context, invocation elevatedWorkerInvocation) (elevatedWorkerResponse, error) {
	if err := validateWorkerOperationPayloadFromAny(invocation.Operation, invocation.Payload); err != nil {
		return elevatedWorkerResponse{}, err
	}
	requestID, err := randomToken()
	if err != nil {
		return elevatedWorkerResponse{}, err
	}
	capability, err := randomToken()
	if err != nil {
		return elevatedWorkerResponse{}, err
	}
	userSID, err := currentUserSID()
	if err != nil {
		return elevatedWorkerResponse{}, err
	}
	sessionID, err := currentSessionID()
	if err != nil {
		return elevatedWorkerResponse{}, err
	}
	authContext := elevatedWorkerAuthContext{Capability: capability, UserSID: userSID, SessionID: sessionID}
	workerRequest, err := newElevatedWorkerRequest(authContext, requestID, invocation.Operation, invocation.Payload)
	if err != nil {
		return elevatedWorkerResponse{}, err
	}

	pipeName := `\\.\pipe\WindowsUpdaterWebUI-` + requestID
	pipeServer, err := newElevatedWorkerPipeServer(pipeName, userSID)
	if err != nil {
		return elevatedWorkerResponse{}, err
	}
	defer pipeServer.Close()

	appLogContext(ctx, "Launching elevated worker for %s. Approve the Windows UAC prompt if shown.", invocation.Operation)
	workerProcess, err := launchElevatedWorkerProcess(pipeName, capability, userSID, sessionID)
	if err != nil {
		appLogContext(ctx, "Elevated worker launch failed for %s: %s.", invocation.Operation, err)
		return elevatedWorkerResponse{}, err
	}
	defer workerProcess.Close()

	appLogContext(ctx, "Elevated worker launched for %s; waiting for connection.", invocation.Operation)
	startupCtx, cancelStartup := context.WithTimeout(ctx, elevatedWorkerStartupTimeout)
	pipeConn, err := acceptElevatedWorkerConnection(startupCtx, pipeServer, workerProcess)
	cancelStartup()
	if err != nil {
		workerProcess.Terminate()
		appLogContext(ctx, "Elevated worker did not connect for %s: %s.", invocation.Operation, err)
		return elevatedWorkerResponse{}, fmt.Errorf("elevated worker did not connect: %w", err)
	}
	defer pipeConn.Close()
	appLogContext(ctx, "Elevated worker connected for %s.", invocation.Operation)

	workerExchangeDone := make(chan struct{})
	var exchangeDoneOnce sync.Once
	markWorkerExchangeDone := func() {
		exchangeDoneOnce.Do(func() {
			close(workerExchangeDone)
		})
	}
	defer markWorkerExchangeDone()
	go func() {
		select {
		case <-ctx.Done():
			workerProcess.Terminate()
			_ = pipeConn.Close()
		case <-workerExchangeDone:
		}
	}()

	response, err := exchangeElevatedWorkerRequest(ctx, pipeConn, authContext, workerRequest, invocation.Progress)
	markWorkerExchangeDone()
	if err != nil {
		workerProcess.Terminate()
		return elevatedWorkerResponse{}, err
	}
	return response, nil
}

func acceptElevatedWorkerConnection(ctx context.Context, pipeServer *elevatedWorkerPipeServer, workerProcess elevatedWorkerProcess) (io.ReadWriteCloser, error) {
	type acceptResult struct {
		pipeConn io.ReadWriteCloser
		err      error
	}
	acceptResultCh := make(chan acceptResult, 1)
	go func() {
		pipeConn, err := pipeServer.Accept(ctx)
		acceptResultCh <- acceptResult{pipeConn: pipeConn, err: err}
	}()

	var processExitCh chan elevatedWorkerExit
	if workerProcess.handle != 0 {
		processExitCh = make(chan elevatedWorkerExit, 1)
		go func() {
			processExitCh <- workerProcess.Wait()
		}()
	}

	select {
	case accepted := <-acceptResultCh:
		return accepted.pipeConn, accepted.err
	case exited := <-processExitCh:
		pipeServer.Close()
		if exited.Err != nil {
			return nil, fmt.Errorf("elevated worker exited before connecting: %w", exited.Err)
		}
		return nil, fmt.Errorf("elevated worker exited before connecting with code %d", exited.Code)
	case <-ctx.Done():
		pipeServer.Close()
		return nil, ctx.Err()
	}
}

func validateWorkerOperationPayloadFromAny(operation string, payload any) error {
	raw, err := marshalWorkerPayload(payload)
	if err != nil {
		return err
	}
	return validateWorkerOperationPayload(operation, raw)
}

func exchangeElevatedWorkerRequest(ctx context.Context, pipeConn io.ReadWriteCloser, _ elevatedWorkerAuthContext, request elevatedWorkerMessage, progress func(elevatedWorkerProgress)) (elevatedWorkerResponse, error) {
	encoder := json.NewEncoder(pipeConn)
	decoder := json.NewDecoder(pipeConn)
	decoder.DisallowUnknownFields()

	if err := encoder.Encode(request); err != nil {
		return elevatedWorkerResponse{}, fmt.Errorf("send elevated worker request: %w", err)
	}

	finalResponseCh := make(chan elevatedWorkerResponse, 1)
	decodeErrCh := make(chan error, 1)
	go func() {
		for {
			var workerResponse elevatedWorkerResponse
			if err := decoder.Decode(&workerResponse); err != nil {
				decodeErrCh <- err
				return
			}
			if err := validateElevatedWorkerResponse(workerResponse, request.RequestID); err != nil {
				decodeErrCh <- err
				return
			}
			if workerResponse.Type == workerResponseProgress {
				if workerResponse.Progress != nil && progress != nil {
					progress(*workerResponse.Progress)
				}
				continue
			}
			finalResponseCh <- workerResponse
			return
		}
	}()

	select {
	case workerResponse := <-finalResponseCh:
		if !workerResponse.OK && workerResponse.Error != "" && workerResponse.Result.Stderr == "" {
			workerResponse.Result.Stderr = workerResponse.Error
		}
		return workerResponse, nil
	case err := <-decodeErrCh:
		return elevatedWorkerResponse{}, fmt.Errorf("read elevated worker response: %w", err)
	case <-ctx.Done():
		return elevatedWorkerResponse{
			Version:   elevatedWorkerProtocolVersion,
			RequestID: request.RequestID,
			OK:        false,
			Result:    CommandResult{Code: commandCancelledCode, Command: request.Operation, Stderr: "Cancelled."},
		}, nil
	}
}

func validateElevatedWorkerResponse(response elevatedWorkerResponse, requestID string) error {
	if response.Version != elevatedWorkerProtocolVersion {
		return fmt.Errorf("unsupported elevated worker response version %d", response.Version)
	}
	if response.RequestID != requestID {
		return errors.New("elevated worker response request_id mismatch")
	}
	if response.Type != "" && response.Type != workerResponseProgress {
		return fmt.Errorf("unknown elevated worker response type %q", response.Type)
	}
	return nil
}

type elevatedWorkerPipeServer struct {
	handle windows.Handle
}

func newElevatedWorkerPipeServer(pipeName, userSID string) (*elevatedWorkerPipeServer, error) {
	pipeNameUTF16, err := windows.UTF16PtrFromString(pipeName)
	if err != nil {
		return nil, err
	}
	securityAttributes, cleanup, err := namedPipeSecurityAttributes(userSID)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	pipeHandle, err := windows.CreateNamedPipe(
		pipeNameUTF16,
		windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_FIRST_PIPE_INSTANCE,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
		1,
		elevatedWorkerPipeBufferSize,
		elevatedWorkerPipeBufferSize,
		uint32(elevatedWorkerStartupTimeout/time.Millisecond),
		securityAttributes,
	)
	if err != nil {
		return nil, err
	}
	return &elevatedWorkerPipeServer{handle: pipeHandle}, nil
}

func (server *elevatedWorkerPipeServer) Accept(ctx context.Context) (io.ReadWriteCloser, error) {
	errCh := make(chan error, 1)
	go func() {
		err := windows.ConnectNamedPipe(server.handle, nil)
		if errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
			err = nil
		}
		errCh <- err
	}()
	select {
	case err := <-errCh:
		if err != nil {
			return nil, err
		}
		file := os.NewFile(uintptr(server.handle), "elevated-worker-pipe")
		server.handle = 0
		return file, nil
	case <-ctx.Done():
		server.Close()
		return nil, ctx.Err()
	}
}

func (server *elevatedWorkerPipeServer) Close() {
	if server.handle != 0 {
		_ = windows.CloseHandle(server.handle)
		server.handle = 0
	}
}

func namedPipeSecurityAttributes(userSID string) (*windows.SecurityAttributes, func(), error) {
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;" + userSID + ")")
	if err != nil {
		return nil, func() {}, err
	}
	attributes := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	return attributes, func() {}, nil
}

func runElevatedWorkerFromArgs() error {
	pipeName, _ := argValue("--worker-pipe")
	capability, _ := argValue("--worker-capability")
	userSID, _ := argValue("--worker-user-sid")
	sessionIDRaw, _ := argValue("--worker-session-id")
	sessionID, err := parseRequiredUint32(sessionIDRaw)
	if err != nil {
		return err
	}
	authContext := elevatedWorkerAuthContext{Capability: capability, UserSID: userSID, SessionID: sessionID}
	if pipeName == "" || capability == "" || userSID == "" {
		return errors.New("worker pipe, capability, and user SID are required")
	}
	pipeConn, err := connectElevatedWorkerPipe(pipeName, elevatedWorkerStartupTimeout)
	if err != nil {
		return err
	}
	defer pipeConn.Close()
	return serveElevatedWorkerConnection(pipeConn, authContext)
}

func connectElevatedWorkerPipe(pipeName string, timeout time.Duration) (io.ReadWriteCloser, error) {
	deadline := time.Now().Add(timeout)
	pipeNameUTF16, err := windows.UTF16PtrFromString(pipeName)
	if err != nil {
		return nil, err
	}
	for {
		pipeHandle, err := windows.CreateFile(
			pipeNameUTF16,
			windows.GENERIC_READ|windows.GENERIC_WRITE,
			0,
			nil,
			windows.OPEN_EXISTING,
			windows.FILE_ATTRIBUTE_NORMAL,
			0,
		)
		if err == nil {
			return os.NewFile(uintptr(pipeHandle), "elevated-worker-pipe"), nil
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func serveElevatedWorkerConnection(pipeConn io.ReadWriter, authContext elevatedWorkerAuthContext) error {
	decoder := json.NewDecoder(pipeConn)
	decoder.DisallowUnknownFields()
	encoder := json.NewEncoder(pipeConn)
	var workerRequest elevatedWorkerMessage
	if err := decoder.Decode(&workerRequest); err != nil {
		return err
	}
	if err := validateElevatedWorkerMessage(workerRequest, authContext); err != nil {
		_ = encoder.Encode(elevatedWorkerResponse{
			Version:   elevatedWorkerProtocolVersion,
			RequestID: workerRequest.RequestID,
			OK:        false,
			Error:     err.Error(),
			Result:    validationCommandResult(workerRequest.Operation, err),
		})
		return err
	}
	if workerRequest.Type != workerMessageRequest {
		return errors.New("first worker message must be a request")
	}

	ctx := context.Background()
	batchProgressTotal := packageUpdateBatchProgressTotal(workerRequest.Operation, workerRequest.Payload)

	progress := func(index int, pkg Package) {
		_ = encoder.Encode(elevatedWorkerResponse{
			Version:   elevatedWorkerProtocolVersion,
			Type:      workerResponseProgress,
			RequestID: workerRequest.RequestID,
			Progress: &elevatedWorkerProgress{
				CurrentIndex:   index,
				Total:          batchProgressTotal,
				PackageKey:     normalizedJobPackageKey(pkg),
				PackageName:    updateJobPackageName(pkg),
				PackageID:      pkg.ID,
				PackageManager: pkg.Manager,
			},
		})
	}
	operationResult := executeElevatedWorkerOperation(ctx, workerRequest.Operation, workerRequest.Payload, authContext, progress)
	workerResponse := elevatedWorkerResponse{
		Version:   elevatedWorkerProtocolVersion,
		RequestID: workerRequest.RequestID,
		OK:        operationResult.Result.OK,
		Result:    operationResult.Result,
		Results:   operationResult.Results,
	}
	if !operationResult.Result.OK {
		workerResponse.Error = strings.TrimSpace(operationResult.Result.Stderr)
	}
	_ = encoder.Encode(workerResponse)
	return nil
}

func executeElevatedWorkerOperation(ctx context.Context, operation string, payload json.RawMessage, auth elevatedWorkerAuthContext, progress func(int, Package)) elevatedWorkerOperationResult {
	if err := validateWorkerOperationPayload(operation, payload); err != nil {
		return elevatedWorkerOperationResult{Result: validationCommandResult(operation, err)}
	}
	switch operation {
	case workerOperationPackageInstall:
		var decoded elevatedWorkerPackageInstallPayload
		if err := decodeWorkerPayload(payload, &decoded); err != nil {
			return elevatedWorkerOperationResult{Result: validationCommandResult(operation, err)}
		}
		return elevatedWorkerOperationResult{Result: installPackageContext(ctx, decoded.Manager, decoded.PackageID)}
	case workerOperationPackageUpdate:
		var decoded elevatedWorkerPackageUpdatePayload
		if err := decodeWorkerPayload(payload, &decoded); err != nil {
			return elevatedWorkerOperationResult{Result: validationCommandResult(operation, err)}
		}
		pkg := decoded.Package
		pkg.AllowUnknownVersionUpdate = decoded.AllowUnknownVersion
		pkg.AllowPinnedUpdate = decoded.AllowPinned
		return elevatedWorkerOperationResult{Result: updatePackageWithMetadataContext(ctx, pkg)}
	case workerOperationPackageUpdateBatch:
		var decoded elevatedWorkerPackageUpdateBatchPayload
		if err := decodeWorkerPayload(payload, &decoded); err != nil {
			return elevatedWorkerOperationResult{Result: validationCommandResult(operation, err)}
		}
		if err := validateBatchWorkerIdentity(auth, decoded.Packages); err != nil {
			return elevatedWorkerOperationResult{Result: validationCommandResult(operation, err)}
		}
		return executeElevatedPackageUpdateBatch(ctx, decoded.Packages, progress)
	case workerOperationManagerInstall:
		var decoded elevatedWorkerManagerInstallPayload
		if err := decodeWorkerPayload(payload, &decoded); err != nil {
			return elevatedWorkerOperationResult{Result: validationCommandResult(operation, err)}
		}
		return elevatedWorkerOperationResult{Result: installManagerContext(ctx, decoded.Manager)}
	case workerOperationStartupTask:
		var decoded elevatedWorkerTaskPayload
		if err := decodeWorkerPayload(payload, &decoded); err != nil {
			return elevatedWorkerOperationResult{Result: validationCommandResult(operation, err)}
		}
		return elevatedWorkerOperationResult{Result: setStartupTaskDirect(decoded.Enabled)}
	case workerOperationAutoUpdateTask:
		var decoded elevatedWorkerTaskPayload
		if err := decodeWorkerPayload(payload, &decoded); err != nil {
			return elevatedWorkerOperationResult{Result: validationCommandResult(operation, err)}
		}
		return elevatedWorkerOperationResult{Result: setAutoUpdateTaskDirect(decoded.Enabled)}
	default:
		return elevatedWorkerOperationResult{Result: validationCommandResult(operation, fmt.Errorf("unknown worker operation %q", operation))}
	}
}

func validateBatchWorkerIdentity(auth elevatedWorkerAuthContext, packages []Package) error {
	if !packageUpdateBatchIncludesManager(packages, managerWinget) {
		return nil
	}
	actualSID, err := currentUserSID()
	if err != nil {
		return fmt.Errorf("validate elevated WinGet worker user: %w", err)
	}
	if !strings.EqualFold(actualSID, auth.UserSID) {
		return fmt.Errorf("elevated WinGet batch refused because worker user SID %s does not match requester %s", actualSID, auth.UserSID)
	}
	actualSession, err := currentSessionID()
	if err != nil {
		return fmt.Errorf("validate elevated WinGet worker session: %w", err)
	}
	if actualSession != auth.SessionID {
		return fmt.Errorf("elevated WinGet batch refused because worker session %d does not match requester session %d", actualSession, auth.SessionID)
	}
	return nil
}

func packageUpdateBatchProgressTotal(operation string, payload json.RawMessage) int {
	if operation != workerOperationPackageUpdateBatch {
		return 0
	}
	var decoded elevatedWorkerPackageUpdateBatchPayload
	if err := decodeWorkerPayload(payload, &decoded); err != nil {
		return 0
	}
	return len(decoded.Packages)
}

func executeElevatedPackageUpdateBatch(ctx context.Context, packages []Package, progress func(int, Package)) elevatedWorkerOperationResult {
	results := make([]UpdateResult, 0, len(packages))
	for index, pkg := range packages {
		if ctx.Err() != nil {
			break
		}
		key := normalizedJobPackageKey(pkg)
		if key == "" {
			key = packageKey(pkg.Manager, pkg.ID)
		}
		pkg.Key = key
		if progress != nil {
			progress(index+1, pkg)
		}
		result := updatePackageWithMetadataContext(ctx, pkg)
		results = append(results, UpdateResult{Key: key, Result: result})
		if ctx.Err() != nil || result.Code == commandCancelledCode {
			break
		}
	}
	return elevatedWorkerOperationResult{
		Result:  aggregatePackageUpdateBatchResult(results, ctx.Err()),
		Results: results,
	}
}

func aggregatePackageUpdateBatchResult(results []UpdateResult, err error) CommandResult {
	result := CommandResult{OK: true, Command: workerOperationPackageUpdateBatch}
	if err != nil {
		result.OK = false
		result.Code = commandCancelledCode
		result.Stderr = "Cancelled."
	}
	if len(results) == 0 && err == nil {
		return validationCommandResult(workerOperationPackageUpdateBatch, errors.New("package update batch returned no results"))
	}
	var stdout []string
	var stderr []string
	for _, item := range results {
		if item.Result.Stdout != "" {
			stdout = append(stdout, strings.TrimSpace(item.Result.Stdout))
		}
		if item.Result.Stderr != "" {
			stderr = append(stderr, strings.TrimSpace(item.Result.Stderr))
		}
		if !item.Result.OK && result.OK {
			result.OK = false
			result.Code = item.Result.Code
		}
	}
	if result.OK {
		result.Stdout = fmt.Sprintf("Elevated package batch updated %d package(s).", len(results))
	} else if result.Code == 0 {
		result.Code = 1
	}
	if len(stdout) > 0 {
		result.Stdout = strings.TrimSpace(strings.Join(append([]string{result.Stdout}, stdout...), "\n"))
	}
	if len(stderr) > 0 {
		result.Stderr = strings.TrimSpace(strings.Join(append([]string{result.Stderr}, stderr...), "\n"))
	}
	return result
}

func runElevatedPackageUpdateBatch(ctx context.Context, packages []Package, progress func(int, Package)) ([]UpdateResult, CommandResult) {
	payload := elevatedWorkerPackageUpdateBatchPayload{Packages: packages}
	if err := validateWorkerOperationPayloadFromAny(workerOperationPackageUpdateBatch, payload); err != nil {
		return nil, validationCommandResult(workerOperationPackageUpdateBatch, err)
	}
	response, err := runElevatedWorkerInvocation(ctx, elevatedWorkerInvocation{
		Operation: workerOperationPackageUpdateBatch,
		Payload:   payload,
		Progress: func(workerProgress elevatedWorkerProgress) {
			index := workerProgress.CurrentIndex
			if index < 1 || index > len(packages) || progress == nil {
				return
			}
			pkg := packages[index-1]
			if pkg.Key == "" {
				pkg.Key = normalizedJobPackageKey(pkg)
			}
			progress(index, pkg)
		},
	})
	if err != nil {
		return nil, workerCommandResultError(workerOperationPackageUpdateBatch, err)
	}
	appendWorkerResultLogsContext(ctx, response.Result)
	return response.Results, response.Result
}

func packageUpdateBatchIncludesManager(packages []Package, manager string) bool {
	for _, pkg := range packages {
		if pkg.Manager == manager {
			return true
		}
	}
	return false
}

func appendWorkerResultLogs(result CommandResult) {
	appendWorkerResultLogsContext(context.Background(), result)
}

func appendWorkerResultLogsContext(ctx context.Context, result CommandResult) {
	categories := logCategoriesForCommandLine(result.Command)
	if result.Command != "" {
		sessionLogs.AppendContext(ctx, "command", result.Command, categories)
	}
	for _, line := range strings.Split(strings.TrimRight(result.Stdout, "\r\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			appendLogLineContext(ctx, "stdout", line, categories)
		}
	}
	for _, line := range strings.Split(strings.TrimRight(result.Stderr, "\r\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			appendLogLineContext(ctx, "stderr", line, categories)
		}
	}
	if result.Command != "" {
		sessionLogs.AppendContext(ctx, "exit", fmt.Sprintf("%s exited with code %d", result.Command, result.Code), categories)
	}
}

func parseRequiredUint32(value string) (uint32, error) {
	var parsed uint64
	if value == "" {
		return 0, errors.New("uint32 value is required")
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("invalid uint32 value %q", value)
		}
		parsed = parsed*10 + uint64(r-'0')
		if parsed > ^uint64(0)>>32 {
			return 0, fmt.Errorf("uint32 value %q overflows", value)
		}
	}
	return uint32(parsed), nil
}

func privilegedPackageActionRequired(manager string) bool {
	return manager == managerChoco
}

func runPrivilegedPackageInstall(ctx context.Context, manager, id string) CommandResult {
	if isAdmin() || !privilegedPackageActionRequired(manager) {
		return CommandResult{}
	}
	return runElevatedWorkerOperation(ctx, elevatedWorkerInvocation{
		Operation: workerOperationPackageInstall,
		Payload: elevatedWorkerPackageInstallPayload{
			Manager:   manager,
			PackageID: id,
		},
	})
}

func runPrivilegedPackageUpdate(ctx context.Context, pkg Package) CommandResult {
	if isAdmin() || !privilegedPackageActionRequired(pkg.Manager) {
		return CommandResult{}
	}
	return runElevatedWorkerOperation(ctx, elevatedWorkerInvocation{
		Operation: workerOperationPackageUpdate,
		Payload: elevatedWorkerPackageUpdatePayload{
			Package:             pkg,
			AllowUnknownVersion: pkg.AllowUnknownVersionUpdate,
			AllowPinned:         pkg.AllowPinnedUpdate,
		},
	})
}
