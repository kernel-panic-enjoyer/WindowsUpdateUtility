package updater

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

func TestElevatedWorkerProtocolEncodingAndAuthorization(t *testing.T) {
	auth := elevatedWorkerAuthContext{Capability: "capability", UserSID: "S-1-5-21-test-1001", SessionID: 7}
	message, err := newElevatedWorkerRequest(auth, "request-1", workerOperationPackageInstall, elevatedWorkerPackageInstallPayload{
		Manager:   managerWinget,
		PackageID: "Git.Git",
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var decoded elevatedWorkerMessage
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if err := validateElevatedWorkerMessage(decoded, auth); err != nil {
		t.Fatalf("valid message rejected: %v", err)
	}
}

func TestElevatedWorkerAuthorizationRejectsWrongCapabilityUserAndSession(t *testing.T) {
	auth := elevatedWorkerAuthContext{Capability: "capability", UserSID: "S-1-5-21-test-1001", SessionID: 7}
	valid, err := newElevatedWorkerRequest(auth, "request-1", workerOperationPackageInstall, elevatedWorkerPackageInstallPayload{
		Manager:   managerWinget,
		PackageID: "Git.Git",
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*elevatedWorkerMessage)
	}{
		{"wrong capability", func(message *elevatedWorkerMessage) { message.Capability = "other" }},
		{"wrong user", func(message *elevatedWorkerMessage) { message.UserSID = "S-1-5-21-test-1002" }},
		{"wrong session", func(message *elevatedWorkerMessage) { message.SessionID = 8 }},
		{"wrong version", func(message *elevatedWorkerMessage) { message.Version = 99 }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			message := valid
			tt.mutate(&message)
			if err := validateElevatedWorkerMessage(message, auth); err == nil {
				t.Fatal("expected authorization failure")
			}
		})
	}
}

func TestElevatedWorkerRejectsUnknownOperationAndUnexpectedFields(t *testing.T) {
	auth := elevatedWorkerAuthContext{Capability: "capability", UserSID: "S-1-5-21-test-1001", SessionID: 7}
	message := elevatedWorkerMessage{
		Version:    elevatedWorkerProtocolVersion,
		Type:       workerMessageRequest,
		RequestID:  "request-1",
		Capability: auth.Capability,
		UserSID:    auth.UserSID,
		SessionID:  auth.SessionID,
		Operation:  "run_command",
		Payload:    json.RawMessage(`{"exe":"cmd.exe"}`),
	}
	if err := validateElevatedWorkerMessage(message, auth); err == nil || !strings.Contains(err.Error(), "unknown worker operation") {
		t.Fatalf("expected unknown operation rejection, got %v", err)
	}

	message.Operation = workerOperationPackageInstall
	message.Payload = json.RawMessage(`{"manager":"winget","package_id":"Git.Git","exe":"cmd.exe"}`)
	if err := validateElevatedWorkerMessage(message, auth); err == nil {
		t.Fatal("expected unexpected field rejection")
	}
}

func TestElevatedWorkerOperationAllowlist(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		payload   any
		wantError bool
	}{
		{
			name:      "winget package install allowed",
			operation: workerOperationPackageInstall,
			payload:   elevatedWorkerPackageInstallPayload{Manager: managerWinget, PackageID: "Git.Git"},
		},
		{
			name:      "choco package install allowed",
			operation: workerOperationPackageInstall,
			payload:   elevatedWorkerPackageInstallPayload{Manager: managerChoco, PackageID: "git"},
		},
		{
			name:      "store package install rejected",
			operation: workerOperationPackageInstall,
			payload:   elevatedWorkerPackageInstallPayload{Manager: managerStore, PackageID: "Codex"},
			wantError: true,
		},
		{
			name:      "choco manager install allowed",
			operation: workerOperationManagerInstall,
			payload:   elevatedWorkerManagerInstallPayload{Manager: managerChoco},
		},
		{
			name:      "winget manager install rejected",
			operation: workerOperationManagerInstall,
			payload:   elevatedWorkerManagerInstallPayload{Manager: managerWinget},
			wantError: true,
		},
		{
			name:      "auto update task allowed",
			operation: workerOperationAutoUpdateTask,
			payload:   elevatedWorkerTaskPayload{Enabled: true},
		},
		{
			name:      "winget choco package update batch allowed",
			operation: workerOperationPackageUpdateBatch,
			payload: elevatedWorkerPackageUpdateBatchPayload{Packages: []Package{
				{Manager: managerWinget, ID: "Git.Git"},
				{Manager: managerChoco, ID: "gh"},
			}},
		},
		{
			name:      "store package update batch rejected",
			operation: workerOperationPackageUpdateBatch,
			payload: elevatedWorkerPackageUpdateBatchPayload{Packages: []Package{
				{Manager: managerStore, ID: "OpenAI.Codex_abc123"},
			}},
			wantError: true,
		},
		{
			name:      "empty package update batch rejected",
			operation: workerOperationPackageUpdateBatch,
			payload:   elevatedWorkerPackageUpdateBatchPayload{},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := marshalWorkerPayload(tt.payload)
			if err != nil {
				t.Fatal(err)
			}
			err = validateWorkerOperationPayload(tt.operation, raw)
			if tt.wantError && err == nil {
				t.Fatal("expected allowlist rejection")
			}
			if !tt.wantError && err != nil {
				t.Fatalf("unexpected allowlist rejection: %v", err)
			}
		})
	}
}

func TestElevatedWorkerPackageUpdateBatchRejectsUnsafePayloads(t *testing.T) {
	oversized := make([]Package, elevatedWorkerPackageUpdateBatchMaxPackages+1)
	for i := range oversized {
		oversized[i] = Package{Manager: managerWinget, ID: "Vendor.App"}
	}
	tests := []struct {
		name    string
		payload json.RawMessage
	}{
		{
			name:    "unknown field",
			payload: json.RawMessage(`{"packages":[{"manager":"winget","id":"Git.Git"}],"exe":"cmd.exe"}`),
		},
		{
			name:    "invalid package id",
			payload: json.RawMessage(`{"packages":[{"manager":"winget","id":"--all"}]}`),
		},
		{
			name:    "store package",
			payload: json.RawMessage(`{"packages":[{"manager":"store","id":"OpenAI.Codex_abc123"}]}`),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateWorkerOperationPayload(workerOperationPackageUpdateBatch, tt.payload); err == nil {
				t.Fatal("expected batch payload rejection")
			}
		})
	}
	raw, err := marshalWorkerPayload(elevatedWorkerPackageUpdateBatchPayload{Packages: oversized})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateWorkerOperationPayload(workerOperationPackageUpdateBatch, raw); err == nil {
		t.Fatal("expected oversized batch rejection")
	}
}

func TestElevatedWorkerCancelMessageValidation(t *testing.T) {
	auth := elevatedWorkerAuthContext{Capability: "capability", UserSID: "S-1-5-21-test-1001", SessionID: 7}
	cancel := newElevatedWorkerCancel(auth, "request-1")
	if err := validateElevatedWorkerMessage(cancel, auth); err != nil {
		t.Fatalf("valid cancel rejected: %v", err)
	}
	cancel.Operation = workerOperationPackageInstall
	if err := validateElevatedWorkerMessage(cancel, auth); err == nil {
		t.Fatal("expected cancel with operation payload to be rejected")
	}
}

func TestDecodeWorkerPayloadRejectsMalformedJSON(t *testing.T) {
	var payload elevatedWorkerPackageInstallPayload
	if err := decodeWorkerPayload(json.RawMessage(`{"manager":"winget","package_id":`), &payload); err == nil {
		t.Fatal("expected malformed payload rejection")
	}
	if err := decodeWorkerPayload(nil, &payload); err == nil {
		t.Fatal("expected missing payload rejection")
	}
}

func TestExchangeElevatedWorkerRequestSendsCancel(t *testing.T) {
	auth := elevatedWorkerAuthContext{Capability: "capability", UserSID: "S-1-5-21-test-1001", SessionID: 7}
	request, err := newElevatedWorkerRequest(auth, "request-1", workerOperationPackageInstall, elevatedWorkerPackageInstallPayload{
		Manager:   managerWinget,
		PackageID: "Git.Git",
	})
	if err != nil {
		t.Fatal(err)
	}
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	seenCancel := make(chan struct{})
	go func() {
		decoder := json.NewDecoder(server)
		decoder.DisallowUnknownFields()
		encoder := json.NewEncoder(server)
		var gotRequest elevatedWorkerMessage
		if err := decoder.Decode(&gotRequest); err != nil {
			return
		}
		var cancel elevatedWorkerMessage
		if err := decoder.Decode(&cancel); err != nil {
			return
		}
		if cancel.Type == workerMessageCancel && cancel.RequestID == gotRequest.RequestID {
			close(seenCancel)
		}
		_ = encoder.Encode(elevatedWorkerResponse{
			Version:   elevatedWorkerProtocolVersion,
			RequestID: gotRequest.RequestID,
			OK:        false,
			Result:    CommandResult{Code: commandCancelledCode, Command: gotRequest.Operation, Stderr: "Cancelled."},
		})
	}()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan CommandResult, 1)
	go func() {
		response, err := exchangeElevatedWorkerRequest(ctx, client, auth, request, nil)
		if err != nil {
			t.Errorf("exchange returned error: %v", err)
		}
		done <- response.Result
	}()
	cancel()

	select {
	case <-seenCancel:
	case <-time.After(time.Second):
		t.Fatal("expected cancel message")
	}
	select {
	case result := <-done:
		if result.Code != commandCancelledCode {
			t.Fatalf("result code = %d, want %d", result.Code, commandCancelledCode)
		}
	case <-time.After(time.Second):
		t.Fatal("expected cancelled result")
	}
}

func TestExchangeElevatedWorkerRequestReportsProgress(t *testing.T) {
	auth := elevatedWorkerAuthContext{Capability: "capability", UserSID: "S-1-5-21-test-1001", SessionID: 7}
	request, err := newElevatedWorkerRequest(auth, "request-1", workerOperationPackageUpdateBatch, elevatedWorkerPackageUpdateBatchPayload{Packages: []Package{
		{Manager: managerWinget, ID: "Git.Git"},
		{Manager: managerChoco, ID: "gh"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		decoder := json.NewDecoder(server)
		decoder.DisallowUnknownFields()
		encoder := json.NewEncoder(server)
		var gotRequest elevatedWorkerMessage
		if err := decoder.Decode(&gotRequest); err != nil {
			return
		}
		_ = encoder.Encode(elevatedWorkerResponse{
			Version:   elevatedWorkerProtocolVersion,
			Type:      workerResponseProgress,
			RequestID: gotRequest.RequestID,
			Progress: &elevatedWorkerProgress{
				CurrentIndex:   2,
				Total:          2,
				PackageKey:     "choco:gh",
				PackageName:    "GitHub CLI",
				PackageID:      "gh",
				PackageManager: managerChoco,
			},
		})
		_ = encoder.Encode(elevatedWorkerResponse{
			Version:   elevatedWorkerProtocolVersion,
			RequestID: gotRequest.RequestID,
			OK:        true,
			Result:    CommandResult{OK: true, Command: gotRequest.Operation},
		})
	}()

	progressCh := make(chan elevatedWorkerProgress, 1)
	response, err := exchangeElevatedWorkerRequest(context.Background(), client, auth, request, func(progress elevatedWorkerProgress) {
		progressCh <- progress
	})
	if err != nil {
		t.Fatal(err)
	}
	if !response.OK {
		t.Fatalf("expected final response ok, got %#v", response)
	}
	select {
	case progress := <-progressCh:
		if progress.CurrentIndex != 2 || progress.PackageKey != "choco:gh" || progress.PackageManager != managerChoco {
			t.Fatalf("unexpected progress: %#v", progress)
		}
	case <-time.After(time.Second):
		t.Fatal("expected progress callback")
	}
}
