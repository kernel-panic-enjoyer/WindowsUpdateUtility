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
	"strings"
	"time"
	"unicode"
)

const (
	storeInventoryWorkerFlag            = "--store-inventory-worker"
	storeInventoryWorkerProtocolVersion = 1
	storeInventoryWorkerRequestLimit    = 32 * 1024
	storeInventoryWorkerResponseLimit   = 8 * 1024 * 1024
	storeInventoryWorkerMaxFamilies     = 12000
	storeInventoryWorkerMaxInstances    = 24000
	storeInventoryWorkerMaxErrors       = 32
	storeInventoryWorkerMaxErrorBytes   = 1024
	storeInventoryWorkerMaxStringBytes  = 4096
)

// storeInventoryWorkerProvider isolates current-user WinRT packaged-app
// enumeration in a same-binary child process. Some WinRT/COM calls can block an
// OS thread; the parent owns the child with a kill-on-close Job Object so
// cancellation does not strand the long-running WebUI process.
type storeInventoryWorkerProvider struct {
	Executable         string
	Args               []string
	Env                []string
	Timeout            time.Duration
	SkipElevationCheck bool
}

type storeInventoryWorkerRequest struct {
	ProtocolVersion int       `json:"protocol_version"`
	ScanID          string    `json:"scan_id"`
	UserSID         string    `json:"user_sid"`
	Deadline        time.Time `json:"deadline"`
}

type storeInventoryWorkerResponse struct {
	ProtocolVersion int                      `json:"protocol_version"`
	ScanID          string                   `json:"scan_id"`
	UserSID         string                   `json:"user_sid"`
	Completed       bool                     `json:"completed"`
	Partial         bool                     `json:"partial"`
	PackageFamilies []StorePackagedAppFamily `json:"package_families"`
	Errors          []string                 `json:"errors,omitempty"`
}

func (provider storeInventoryWorkerProvider) Inventory(ctx context.Context, scan StoreScanGeneration) (StorePackagedAppInventory, CommandResult) {
	timeout := provider.Timeout
	if timeout <= 0 {
		timeout = storePackagedInventoryTimeout
	}
	if !provider.SkipElevationCheck && isAdmin() {
		err := errors.New("Store inventory worker requires the non-elevated interactive user context")
		result := CommandResult{Command: winrtInventoryCommand, Code: 1, Stderr: err.Error()}
		return incompleteStorePackagedInventory(scan, err), result
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	executable := provider.Executable
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			result := CommandResult{Command: winrtInventoryCommand, Code: 127, Stderr: err.Error()}
			return incompleteStorePackagedInventory(scan, err), result
		}
	}
	args := provider.Args
	if len(args) == 0 {
		args = []string{storeInventoryWorkerFlag}
	}

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().UTC().Add(timeout)
	}
	request := storeInventoryWorkerRequest{
		ProtocolVersion: storeInventoryWorkerProtocolVersion,
		ScanID:          scan.ScanID,
		UserSID:         scan.UserSID,
		Deadline:        deadline.UTC(),
	}
	requestBytes, err := encodeStoreInventoryWorkerRequest(request)
	if err != nil {
		result := CommandResult{Command: winrtInventoryCommand, Code: 2, Stderr: err.Error()}
		return incompleteStorePackagedInventory(scan, err), result
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
		return incompleteStorePackagedInventory(scan, err), result
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		result.Code = 127
		result.Stderr = err.Error()
		return incompleteStorePackagedInventory(scan, err), result
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		result.Code = 127
		result.Stderr = err.Error()
		return incompleteStorePackagedInventory(scan, err), result
	}

	owner, err := newCommandProcessOwner(true)
	if err != nil {
		result.Code = 127
		result.Stderr = err.Error()
		return incompleteStorePackagedInventory(scan, err), result
	}
	defer owner.Close()

	if err := cmd.Start(); err != nil {
		result.Code = 127
		result.Stderr = err.Error()
		return incompleteStorePackagedInventory(scan, err), result
	}
	if err := owner.Assign(cmd); err != nil {
		terminateStartedCommand(cmd, owner)
		_ = cmd.Wait()
		result.Code = 127
		result.Stderr = err.Error()
		return incompleteStorePackagedInventory(scan, err), result
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
		stdoutDone <- readBoundedPipe(stdout, storeInventoryWorkerResponseLimit)
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
		err := errors.New("Store inventory worker timed out and was terminated")
		result.Code = 124
		result.Stderr = appendDiagnostic(result.Stderr, err.Error())
		return incompleteStorePackagedInventory(scan, err), result
	}
	if ctx.Err() == context.Canceled {
		err := errors.New("Store inventory worker was cancelled and terminated")
		result.Code = commandCancelledCode
		result.Stderr = appendDiagnostic(result.Stderr, err.Error())
		return incompleteStorePackagedInventory(scan, err), result
	}

	response, parseErr := decodeStoreInventoryWorkerResponse(stdoutBytes)
	var exitCode int
	if waitErr != nil {
		exitCode = commandExitCode(waitErr)
		result.Code = exitCode
		result.Stderr = appendDiagnostic(result.Stderr, waitErr.Error())
	}
	if parseErr != nil {
		result.Code = firstNonZero(result.Code, 2)
		err := fmt.Errorf("invalid Store inventory worker response: %w", parseErr)
		result.Stderr = appendDiagnostic(result.Stderr, err.Error())
		return incompleteStorePackagedInventory(scan, err), result
	}

	inventory, validationErr := inventoryFromStoreInventoryWorkerResponse(scan, response)
	if validationErr != nil {
		result.Code = firstNonZero(result.Code, 2)
		result.Stderr = appendDiagnostic(result.Stderr, validationErr.Error())
		return incompleteStorePackagedInventory(scan, validationErr), result
	}
	result.Stdout = fmt.Sprintf("Store inventory worker returned %d package record(s), %d family group(s).", len(inventory.Records), len(inventory.Families))
	if waitErr != nil {
		err := fmt.Errorf("Store inventory worker exited with code %d", exitCode)
		inventory.Partial = true
		inventory.Errors = appendBoundedDiagnostics(inventory.Errors, err.Error())
		inventory.Scan.CompletionStatus = StoreScanIncomplete
		result.Stderr = appendDiagnostic(result.Stderr, strings.Join(response.Errors, "\n"))
		return inventory, result
	}
	if response.Partial || !response.Completed || len(response.Errors) > 0 {
		result.Code = 1
		result.Stderr = appendDiagnostic(result.Stderr, strings.Join(response.Errors, "\n"))
		inventory.Partial = true
		inventory.Scan.CompletionStatus = StoreScanIncomplete
		return inventory, result
	}
	result.OK = true
	return inventory, result
}

func runStoreInventoryWorkerFromArgs() int {
	if len(os.Args) != 2 || os.Args[1] != storeInventoryWorkerFlag {
		fmt.Fprintln(os.Stderr, "internal unsupported mode requires exactly --store-inventory-worker")
		return 2
	}
	return runStoreInventoryWorker(os.Stdin, os.Stdout, os.Stderr, winrtStorePackagedAppInventoryProvider{})
}

// runStoreInventoryWorker accepts only the versioned inventory protocol over
// stdin/stdout. It intentionally has no arguments for commands, package
// operations, paths, or generic COM activation.
func runStoreInventoryWorker(input io.Reader, output io.Writer, diagnostics io.Writer, provider winrtStorePackagedAppInventoryProvider) int {
	request, err := decodeStoreInventoryWorkerRequest(input)
	if err != nil {
		fmt.Fprintln(diagnostics, err)
		return 2
	}
	if err := validateStoreInventoryWorkerRequest(request); err != nil {
		_ = encodeStoreInventoryWorkerResponse(output, storeInventoryWorkerResponse{
			ProtocolVersion: storeInventoryWorkerProtocolVersion,
			ScanID:          request.ScanID,
			UserSID:         request.UserSID,
			Partial:         true,
			Errors:          []string{err.Error()},
		})
		return 2
	}
	now := time.Now().UTC()
	if request.Deadline.Before(now) {
		_ = encodeStoreInventoryWorkerResponse(output, storeInventoryWorkerResponse{
			ProtocolVersion: storeInventoryWorkerProtocolVersion,
			ScanID:          request.ScanID,
			UserSID:         request.UserSID,
			Partial:         true,
			Errors:          []string{"Store inventory worker deadline already expired"},
		})
		return 1
	}
	ctx, cancel := context.WithDeadline(context.Background(), request.Deadline)
	defer cancel()
	scan := newStorePackagedAppScan(request.UserSID)
	scan.ScanID = request.ScanID
	inventory, result := provider.Inventory(ctx, scan)
	response := storeInventoryWorkerResponse{
		ProtocolVersion: storeInventoryWorkerProtocolVersion,
		ScanID:          request.ScanID,
		UserSID:         request.UserSID,
		Completed:       result.OK && !inventory.Partial && inventory.Scan.CompletionStatus == StoreScanCompleted,
		Partial:         inventory.Partial || !result.OK,
		PackageFamilies: inventory.Families,
		Errors:          appendBoundedDiagnostics(inventory.Errors, result.Stderr),
	}
	if err := encodeStoreInventoryWorkerResponse(output, response); err != nil {
		fmt.Fprintln(diagnostics, err)
		return 2
	}
	if !response.Completed || response.Partial || !result.OK {
		return 1
	}
	return 0
}

func encodeStoreInventoryWorkerRequest(request storeInventoryWorkerRequest) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	if err := encoder.Encode(request); err != nil {
		return nil, err
	}
	if buffer.Len() > storeInventoryWorkerRequestLimit {
		return nil, fmt.Errorf("Store inventory worker request exceeds %d bytes", storeInventoryWorkerRequestLimit)
	}
	return buffer.Bytes(), nil
}

func decodeStoreInventoryWorkerRequest(input io.Reader) (storeInventoryWorkerRequest, error) {
	var request storeInventoryWorkerRequest
	data := readBoundedPipe(input, storeInventoryWorkerRequestLimit)
	if len(data) > storeInventoryWorkerRequestLimit {
		return request, fmt.Errorf("Store inventory worker request exceeds %d bytes", storeInventoryWorkerRequestLimit)
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

func encodeStoreInventoryWorkerResponse(output io.Writer, response storeInventoryWorkerResponse) error {
	data, err := json.Marshal(response)
	if err != nil {
		return err
	}
	if len(data) > storeInventoryWorkerResponseLimit {
		return fmt.Errorf("Store inventory worker response exceeds %d bytes", storeInventoryWorkerResponseLimit)
	}
	_, err = output.Write(append(data, '\n'))
	return err
}

func decodeStoreInventoryWorkerResponse(data []byte) (storeInventoryWorkerResponse, error) {
	var response storeInventoryWorkerResponse
	if len(data) > storeInventoryWorkerResponseLimit {
		return response, fmt.Errorf("response exceeds %d bytes", storeInventoryWorkerResponseLimit)
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

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("trailing JSON data")
}

func validateStoreInventoryWorkerRequest(request storeInventoryWorkerRequest) error {
	if request.ProtocolVersion != storeInventoryWorkerProtocolVersion {
		return fmt.Errorf("unsupported Store inventory worker protocol version %d", request.ProtocolVersion)
	}
	if strings.TrimSpace(request.ScanID) == "" {
		return errors.New("Store inventory worker request missing scan ID")
	}
	if strings.TrimSpace(request.UserSID) == "" {
		return errors.New("Store inventory worker request missing user SID")
	}
	if request.Deadline.IsZero() {
		return errors.New("Store inventory worker request missing deadline")
	}
	return nil
}

func inventoryFromStoreInventoryWorkerResponse(scan StoreScanGeneration, response storeInventoryWorkerResponse) (StorePackagedAppInventory, error) {
	if response.ProtocolVersion != storeInventoryWorkerProtocolVersion {
		return StorePackagedAppInventory{}, fmt.Errorf("unsupported Store inventory worker protocol version %d", response.ProtocolVersion)
	}
	if response.ScanID != scan.ScanID {
		return StorePackagedAppInventory{}, fmt.Errorf("Store inventory worker scan ID mismatch: got %q, want %q", response.ScanID, scan.ScanID)
	}
	if !strings.EqualFold(strings.TrimSpace(response.UserSID), strings.TrimSpace(scan.UserSID)) {
		return StorePackagedAppInventory{}, fmt.Errorf("Store inventory worker user SID mismatch: got %q, want %q", response.UserSID, scan.UserSID)
	}
	if len(response.PackageFamilies) > storeInventoryWorkerMaxFamilies {
		return StorePackagedAppInventory{}, fmt.Errorf("Store inventory worker returned %d family records; limit is %d", len(response.PackageFamilies), storeInventoryWorkerMaxFamilies)
	}
	if len(response.Errors) > storeInventoryWorkerMaxErrors {
		return StorePackagedAppInventory{}, fmt.Errorf("Store inventory worker returned %d diagnostics; limit is %d", len(response.Errors), storeInventoryWorkerMaxErrors)
	}
	for _, item := range response.Errors {
		if len(item) > storeInventoryWorkerMaxErrorBytes {
			return StorePackagedAppInventory{}, errors.New("Store inventory worker diagnostic exceeds limit")
		}
	}

	seenFamilies := map[StoreInstalledIdentity]bool{}
	records := []StorePackagedAppRecord{}
	for _, family := range response.PackageFamilies {
		// Why: worker output is not trusted just because it came from our
		// executable. The parent revalidates SID, PFN, full name, architecture,
		// duplicates, and bounded strings before using any inventory evidence.
		if !strings.EqualFold(family.Identity.UserSID, scan.UserSID) {
			return StorePackagedAppInventory{}, fmt.Errorf("Store inventory worker family user SID mismatch for %q", family.Identity.PackageFamilyName)
		}
		if !validStorePackageFamilyName(family.Identity.PackageFamilyName) {
			return StorePackagedAppInventory{}, fmt.Errorf("Store inventory worker returned malformed package family name %q", family.Identity.PackageFamilyName)
		}
		if seenFamilies[family.Identity] {
			return StorePackagedAppInventory{}, fmt.Errorf("Store inventory worker returned duplicate package family %q", family.Identity.PackageFamilyName)
		}
		seenFamilies[family.Identity] = true
		if len(family.Instances) == 0 {
			return StorePackagedAppInventory{}, fmt.Errorf("Store inventory worker family %q has no instances", family.Identity.PackageFamilyName)
		}
		if len(records)+len(family.Instances) > storeInventoryWorkerMaxInstances {
			return StorePackagedAppInventory{}, fmt.Errorf("Store inventory worker returned too many package instances; limit is %d", storeInventoryWorkerMaxInstances)
		}
		for _, record := range family.Instances {
			normalized, err := normalizeStorePackagedAppRecord(record, scan.UserSID)
			if err != nil {
				return StorePackagedAppInventory{}, err
			}
			if !strings.EqualFold(normalized.UserSID, scan.UserSID) {
				return StorePackagedAppInventory{}, fmt.Errorf("Store inventory worker record user SID mismatch for %q", normalized.PackageFamilyName)
			}
			if normalized.PackageFamilyName != family.Identity.PackageFamilyName {
				return StorePackagedAppInventory{}, fmt.Errorf("Store inventory worker record PFN %q does not match family %q", normalized.PackageFamilyName, family.Identity.PackageFamilyName)
			}
			if !validStorePackageFamilyName(normalized.PackageFamilyName) {
				return StorePackagedAppInventory{}, fmt.Errorf("Store inventory worker returned malformed package family name %q", normalized.PackageFamilyName)
			}
			if !validStorePackageFullName(normalized.PackageFullName) {
				return StorePackagedAppInventory{}, fmt.Errorf("Store inventory worker returned malformed package full name %q", normalized.PackageFullName)
			}
			if !validStorePackageArchitecture(normalized.ProcessorArchitecture) {
				return StorePackagedAppInventory{}, fmt.Errorf("Store inventory worker returned unsupported architecture %q", normalized.ProcessorArchitecture)
			}
			if !validStoreInventoryString(normalized.IdentityName) || !validStoreInventoryString(normalized.DisplayName) || !validStoreInventoryString(normalized.Publisher) || !validStoreInventoryString(normalized.PublisherID) || !validStoreInventoryString(normalized.InstallLocation) {
				return StorePackagedAppInventory{}, errors.New("Store inventory worker returned a malformed string field")
			}
			records = append(records, normalized)
		}
	}
	scan.CompletedAt = time.Now().UTC()
	scan.CompletionStatus = StoreScanCompleted
	if response.Partial || !response.Completed {
		scan.CompletionStatus = StoreScanIncomplete
	}
	inventory := StorePackagedAppInventory{
		Scan:     scan,
		Records:  records,
		Families: groupStorePackagedAppFamilies(records),
		Partial:  response.Partial || !response.Completed,
		Errors:   appendBoundedDiagnostics(nil, response.Errors...),
	}
	return inventory, nil
}

func readBoundedPipe(reader io.Reader, limit int) []byte {
	data, _ := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	return data
}

func validStorePackageFamilyName(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > storeInventoryWorkerMaxStringBytes || strings.Count(value, "_") != 1 {
		return false
	}
	return validStoreIdentityToken(value)
}

func validStorePackageFullName(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > storeInventoryWorkerMaxStringBytes || strings.Count(value, "_") < 4 {
		return false
	}
	return validStoreIdentityToken(value)
}

func validStoreIdentityToken(value string) bool {
	for _, r := range value {
		if r < 0x20 || unicode.IsSpace(r) || strings.ContainsRune(`\/:*?"<>|`, r) {
			return false
		}
	}
	return true
}

func validStorePackageArchitecture(value string) bool {
	switch strings.TrimSpace(value) {
	case "", "X86", "X64", "Arm", "Neutral", "Arm64", "X86OnArm64", "Unknown":
		return true
	default:
		return false
	}
}

func validStoreInventoryString(value string) bool {
	return len(value) <= storeInventoryWorkerMaxStringBytes && !strings.ContainsFunc(value, func(r rune) bool {
		return r != '\t' && r != '\n' && r != '\r' && r < 0x20
	})
}

func appendBoundedDiagnostics(existing []string, values ...string) []string {
	for _, value := range values {
		for _, line := range strings.Split(value, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if len(line) > storeInventoryWorkerMaxErrorBytes {
				line = line[:storeInventoryWorkerMaxErrorBytes]
			}
			if len(existing) >= storeInventoryWorkerMaxErrors {
				return existing
			}
			existing = append(existing, line)
		}
	}
	return existing
}

func appendDiagnostic(existing, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return existing
	}
	if existing == "" {
		return value
	}
	return existing + "\n" + value
}

func commandExitCode(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return 127
}

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}
