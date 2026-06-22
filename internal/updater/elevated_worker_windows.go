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
}

type elevatedWorkerProcess struct {
	handle windows.Handle
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

func runElevatedWorkerOperation(ctx context.Context, invocation elevatedWorkerInvocation) CommandResult {
	if err := validateWorkerOperationPayloadFromAny(invocation.Operation, invocation.Payload); err != nil {
		return validationCommandResult(invocation.Operation, err)
	}
	requestID, err := randomToken()
	if err != nil {
		return workerCommandResultError(invocation.Operation, err)
	}
	capability, err := randomToken()
	if err != nil {
		return workerCommandResultError(invocation.Operation, err)
	}
	userSID, err := currentUserSID()
	if err != nil {
		return workerCommandResultError(invocation.Operation, err)
	}
	sessionID, err := currentSessionID()
	if err != nil {
		return workerCommandResultError(invocation.Operation, err)
	}
	auth := elevatedWorkerAuthContext{Capability: capability, UserSID: userSID, SessionID: sessionID}
	request, err := newElevatedWorkerRequest(auth, requestID, invocation.Operation, invocation.Payload)
	if err != nil {
		return workerCommandResultError(invocation.Operation, err)
	}

	pipeName := `\\.\pipe\WindowsUpdaterWebUI-` + requestID
	server, err := newElevatedWorkerPipeServer(pipeName, userSID)
	if err != nil {
		return workerCommandResultError(invocation.Operation, err)
	}
	defer server.Close()

	process, err := launchElevatedWorkerProcess(pipeName, capability, userSID, sessionID)
	if err != nil {
		return workerCommandResultError(invocation.Operation, err)
	}
	defer process.Close()

	startupCtx, cancelStartup := context.WithTimeout(ctx, elevatedWorkerStartupTimeout)
	conn, err := server.Accept(startupCtx)
	cancelStartup()
	if err != nil {
		process.Terminate()
		return workerCommandResultError(invocation.Operation, fmt.Errorf("elevated worker did not connect: %w", err))
	}
	defer conn.Close()

	result, err := exchangeElevatedWorkerRequest(ctx, conn, auth, request)
	if err != nil {
		process.Terminate()
		return workerCommandResultError(invocation.Operation, err)
	}
	appendWorkerResultLogs(result)
	return result
}

func validateWorkerOperationPayloadFromAny(operation string, payload any) error {
	raw, err := marshalWorkerPayload(payload)
	if err != nil {
		return err
	}
	return validateWorkerOperationPayload(operation, raw)
}

func exchangeElevatedWorkerRequest(ctx context.Context, conn io.ReadWriteCloser, auth elevatedWorkerAuthContext, request elevatedWorkerMessage) (CommandResult, error) {
	encoder := json.NewEncoder(conn)
	decoder := json.NewDecoder(conn)
	decoder.DisallowUnknownFields()

	var writeMu sync.Mutex
	if err := encoder.Encode(request); err != nil {
		return CommandResult{}, fmt.Errorf("send elevated worker request: %w", err)
	}

	responseCh := make(chan elevatedWorkerResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		var response elevatedWorkerResponse
		if err := decoder.Decode(&response); err != nil {
			errCh <- err
			return
		}
		responseCh <- response
	}()

	select {
	case response := <-responseCh:
		if err := validateElevatedWorkerResponse(response, request.RequestID); err != nil {
			return CommandResult{}, err
		}
		if !response.OK && response.Error != "" && response.Result.Stderr == "" {
			response.Result.Stderr = response.Error
		}
		return response.Result, nil
	case err := <-errCh:
		return CommandResult{}, fmt.Errorf("read elevated worker response: %w", err)
	case <-ctx.Done():
		cancelMessage := newElevatedWorkerCancel(auth, request.RequestID)
		writeMu.Lock()
		_ = encoder.Encode(cancelMessage)
		writeMu.Unlock()
		select {
		case response := <-responseCh:
			if err := validateElevatedWorkerResponse(response, request.RequestID); err != nil {
				return CommandResult{}, err
			}
			return response.Result, nil
		case <-errCh:
			return CommandResult{Code: commandCancelledCode, Command: request.Operation, Stderr: "Cancelled."}, nil
		case <-time.After(elevatedWorkerCancelTimeout):
			return CommandResult{Code: commandCancelledCode, Command: request.Operation, Stderr: "Cancelled; elevated worker did not stop before timeout."}, nil
		}
	}
}

func validateElevatedWorkerResponse(response elevatedWorkerResponse, requestID string) error {
	if response.Version != elevatedWorkerProtocolVersion {
		return fmt.Errorf("unsupported elevated worker response version %d", response.Version)
	}
	if response.RequestID != requestID {
		return errors.New("elevated worker response request_id mismatch")
	}
	return nil
}

type elevatedWorkerPipeServer struct {
	handle windows.Handle
}

func newElevatedWorkerPipeServer(pipeName, userSID string) (*elevatedWorkerPipeServer, error) {
	name, err := windows.UTF16PtrFromString(pipeName)
	if err != nil {
		return nil, err
	}
	securityAttributes, cleanup, err := namedPipeSecurityAttributes(userSID)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	handle, err := windows.CreateNamedPipe(
		name,
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
	return &elevatedWorkerPipeServer{handle: handle}, nil
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
	sessionRaw, _ := argValue("--worker-session-id")
	sessionID, err := parseUint32(sessionRaw)
	if err != nil {
		return err
	}
	auth := elevatedWorkerAuthContext{Capability: capability, UserSID: userSID, SessionID: sessionID}
	if pipeName == "" || capability == "" || userSID == "" {
		return errors.New("worker pipe, capability, and user SID are required")
	}
	conn, err := connectElevatedWorkerPipe(pipeName, elevatedWorkerStartupTimeout)
	if err != nil {
		return err
	}
	defer conn.Close()
	return serveElevatedWorkerConnection(conn, auth)
}

func connectElevatedWorkerPipe(pipeName string, timeout time.Duration) (io.ReadWriteCloser, error) {
	deadline := time.Now().Add(timeout)
	name, err := windows.UTF16PtrFromString(pipeName)
	if err != nil {
		return nil, err
	}
	for {
		handle, err := windows.CreateFile(
			name,
			windows.GENERIC_READ|windows.GENERIC_WRITE,
			0,
			nil,
			windows.OPEN_EXISTING,
			windows.FILE_ATTRIBUTE_NORMAL,
			0,
		)
		if err == nil {
			return os.NewFile(uintptr(handle), "elevated-worker-pipe"), nil
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func serveElevatedWorkerConnection(conn io.ReadWriter, auth elevatedWorkerAuthContext) error {
	decoder := json.NewDecoder(conn)
	decoder.DisallowUnknownFields()
	encoder := json.NewEncoder(conn)
	var request elevatedWorkerMessage
	if err := decoder.Decode(&request); err != nil {
		return err
	}
	if err := validateElevatedWorkerMessage(request, auth); err != nil {
		_ = encoder.Encode(elevatedWorkerResponse{
			Version:   elevatedWorkerProtocolVersion,
			RequestID: request.RequestID,
			OK:        false,
			Error:     err.Error(),
			Result:    validationCommandResult(request.Operation, err),
		})
		return err
	}
	if request.Type != workerMessageRequest {
		return errors.New("first worker message must be a request")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelDone := make(chan struct{})
	go func() {
		defer close(cancelDone)
		for {
			var message elevatedWorkerMessage
			if err := decoder.Decode(&message); err != nil {
				cancel()
				return
			}
			if err := validateElevatedWorkerMessage(message, auth); err != nil {
				cancel()
				return
			}
			if message.Type == workerMessageCancel && message.RequestID == request.RequestID {
				cancel()
				return
			}
		}
	}()

	result := executeElevatedWorkerOperation(ctx, request.Operation, request.Payload)
	response := elevatedWorkerResponse{
		Version:   elevatedWorkerProtocolVersion,
		RequestID: request.RequestID,
		OK:        result.OK,
		Result:    result,
	}
	if !result.OK {
		response.Error = strings.TrimSpace(result.Stderr)
	}
	_ = encoder.Encode(response)
	cancel()
	select {
	case <-cancelDone:
	case <-time.After(elevatedWorkerShutdownTimeout):
	}
	return nil
}

func executeElevatedWorkerOperation(ctx context.Context, operation string, payload json.RawMessage) CommandResult {
	if err := validateWorkerOperationPayload(operation, payload); err != nil {
		return validationCommandResult(operation, err)
	}
	switch operation {
	case workerOperationPackageInstall:
		var decoded elevatedWorkerPackageInstallPayload
		if err := decodeWorkerPayload(payload, &decoded); err != nil {
			return validationCommandResult(operation, err)
		}
		return installPackageContext(ctx, decoded.Manager, decoded.PackageID)
	case workerOperationPackageUpdate:
		var decoded elevatedWorkerPackageUpdatePayload
		if err := decodeWorkerPayload(payload, &decoded); err != nil {
			return validationCommandResult(operation, err)
		}
		pkg := decoded.Package
		pkg.AllowUnknownVersionUpdate = decoded.AllowUnknownVersion
		pkg.AllowPinnedUpdate = decoded.AllowPinned
		return updatePackageWithMetadataContext(ctx, pkg)
	case workerOperationManagerInstall:
		var decoded elevatedWorkerManagerInstallPayload
		if err := decodeWorkerPayload(payload, &decoded); err != nil {
			return validationCommandResult(operation, err)
		}
		return installManagerContext(ctx, decoded.Manager)
	case workerOperationStartupTask:
		var decoded elevatedWorkerTaskPayload
		if err := decodeWorkerPayload(payload, &decoded); err != nil {
			return validationCommandResult(operation, err)
		}
		return setStartupTaskDirect(decoded.Enabled)
	case workerOperationAutoUpdateTask:
		var decoded elevatedWorkerTaskPayload
		if err := decodeWorkerPayload(payload, &decoded); err != nil {
			return validationCommandResult(operation, err)
		}
		return setAutoUpdateTaskDirect(decoded.Enabled)
	default:
		return validationCommandResult(operation, fmt.Errorf("unknown worker operation %q", operation))
	}
}

func appendWorkerResultLogs(result CommandResult) {
	categories := logCategoriesForCommandLine(result.Command)
	if result.Command != "" {
		sessionLogs.AppendCategorized("command", result.Command, categories)
	}
	for _, line := range strings.Split(strings.TrimRight(result.Stdout, "\r\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			appendLogLineCategorized("stdout", line, categories)
		}
	}
	for _, line := range strings.Split(strings.TrimRight(result.Stderr, "\r\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			appendLogLineCategorized("stderr", line, categories)
		}
	}
	if result.Command != "" {
		sessionLogs.AppendCategorized("exit", fmt.Sprintf("%s exited with code %d", result.Command, result.Code), categories)
	}
}

func parseUint32(value string) (uint32, error) {
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
