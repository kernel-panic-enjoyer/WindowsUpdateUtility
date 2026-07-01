package updater

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	elevatedWorkerProtocolVersion               = 1
	elevatedWorkerPackageUpdateBatchMaxPackages = 100

	workerMessageRequest = "request"
	workerMessageCancel  = "cancel"

	workerResponseProgress = "progress"

	workerOperationPackageInstall     = "package_install"
	workerOperationPackageUpdate      = "package_update"
	workerOperationPackageUpdateBatch = "package_update_batch"
	workerOperationManagerInstall     = "manager_install"
	workerOperationStartupTask        = "startup_task"
	workerOperationAutoUpdateTask     = "auto_update_task"
)

type elevatedWorkerMessage struct {
	Version    int             `json:"version"`
	Type       string          `json:"type"`
	RequestID  string          `json:"request_id"`
	Capability string          `json:"capability"`
	UserSID    string          `json:"user_sid"`
	SessionID  uint32          `json:"session_id"`
	Operation  string          `json:"operation,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

type elevatedWorkerResponse struct {
	Version   int                     `json:"version"`
	Type      string                  `json:"type,omitempty"`
	RequestID string                  `json:"request_id"`
	OK        bool                    `json:"ok"`
	Error     string                  `json:"error,omitempty"`
	Result    CommandResult           `json:"result"`
	Results   []UpdateResult          `json:"results,omitempty"`
	Progress  *elevatedWorkerProgress `json:"progress,omitempty"`
}

type elevatedWorkerProgress struct {
	CurrentIndex   int    `json:"current_index"`
	Total          int    `json:"total"`
	PackageKey     string `json:"package_key,omitempty"`
	PackageName    string `json:"package_name,omitempty"`
	PackageID      string `json:"package_id,omitempty"`
	PackageManager string `json:"package_manager,omitempty"`
}

type elevatedWorkerPackageInstallPayload struct {
	Manager   string `json:"manager"`
	PackageID string `json:"package_id"`
}

type elevatedWorkerPackageUpdatePayload struct {
	Package             Package `json:"package"`
	AllowUnknownVersion bool    `json:"allow_unknown_version"`
	AllowPinned         bool    `json:"allow_pinned"`
}

type elevatedWorkerPackageUpdateBatchPayload struct {
	Packages []Package `json:"packages"`
}

type elevatedWorkerManagerInstallPayload struct {
	Manager string `json:"manager"`
}

type elevatedWorkerTaskPayload struct {
	Enabled bool `json:"enabled"`
}

type elevatedWorkerAuthContext struct {
	Capability string
	UserSID    string
	SessionID  uint32
}

func newElevatedWorkerRequest(auth elevatedWorkerAuthContext, requestID, operation string, operationPayload any) (elevatedWorkerMessage, error) {
	rawPayload, err := marshalWorkerPayload(operationPayload)
	if err != nil {
		return elevatedWorkerMessage{}, err
	}
	return elevatedWorkerMessage{
		Version:    elevatedWorkerProtocolVersion,
		Type:       workerMessageRequest,
		RequestID:  requestID,
		Capability: auth.Capability,
		UserSID:    auth.UserSID,
		SessionID:  auth.SessionID,
		Operation:  operation,
		Payload:    rawPayload,
	}, nil
}

func newElevatedWorkerCancel(auth elevatedWorkerAuthContext, requestID string) elevatedWorkerMessage {
	return elevatedWorkerMessage{
		Version:    elevatedWorkerProtocolVersion,
		Type:       workerMessageCancel,
		RequestID:  requestID,
		Capability: auth.Capability,
		UserSID:    auth.UserSID,
		SessionID:  auth.SessionID,
	}
}

func marshalWorkerPayload(operationPayload any) (json.RawMessage, error) {
	if operationPayload == nil {
		return nil, nil
	}
	encodedPayload, err := json.Marshal(operationPayload)
	if err != nil {
		return nil, err
	}
	return encodedPayload, nil
}

func validateElevatedWorkerMessage(message elevatedWorkerMessage, expectedAuth elevatedWorkerAuthContext) error {
	if message.Version != elevatedWorkerProtocolVersion {
		return fmt.Errorf("unsupported worker protocol version %d", message.Version)
	}
	if message.RequestID == "" {
		return errors.New("worker request_id is required")
	}
	if message.Capability == "" || message.Capability != expectedAuth.Capability {
		return errors.New("worker capability is invalid")
	}
	if message.UserSID == "" || !strings.EqualFold(message.UserSID, expectedAuth.UserSID) {
		return errors.New("worker user SID is invalid")
	}
	if message.SessionID != expectedAuth.SessionID {
		return errors.New("worker session is invalid")
	}
	switch message.Type {
	case workerMessageRequest:
		if message.Operation == "" {
			return errors.New("worker operation is required")
		}
		return validateWorkerOperationPayload(message.Operation, message.Payload)
	case workerMessageCancel:
		if message.Operation != "" || len(message.Payload) != 0 {
			return errors.New("worker cancel message cannot include operation payload")
		}
		return nil
	default:
		return fmt.Errorf("unknown worker message type %q", message.Type)
	}
}

func validateWorkerOperationPayload(operation string, rawPayload json.RawMessage) error {
	switch operation {
	case workerOperationPackageInstall:
		var installPayload elevatedWorkerPackageInstallPayload
		if err := decodeWorkerPayload(rawPayload, &installPayload); err != nil {
			return err
		}
		if !isElevatedWorkerPackageManager(installPayload.Manager) {
			return errors.New("package install worker operation only allows winget or choco")
		}
		return validateManagerAndID(installPayload.Manager, installPayload.PackageID)
	case workerOperationPackageUpdate:
		var updatePayload elevatedWorkerPackageUpdatePayload
		if err := decodeWorkerPayload(rawPayload, &updatePayload); err != nil {
			return err
		}
		if !isElevatedWorkerPackageManager(updatePayload.Package.Manager) {
			return errors.New("package update worker operation only allows winget or choco")
		}
		return validateManagerAndID(updatePayload.Package.Manager, updatePayload.Package.ID)
	case workerOperationPackageUpdateBatch:
		var batchPayload elevatedWorkerPackageUpdateBatchPayload
		if err := decodeWorkerPayload(rawPayload, &batchPayload); err != nil {
			return err
		}
		return validateElevatedWorkerPackageUpdateBatchPayload(batchPayload)
	case workerOperationManagerInstall:
		var managerInstallPayload elevatedWorkerManagerInstallPayload
		if err := decodeWorkerPayload(rawPayload, &managerInstallPayload); err != nil {
			return err
		}
		if managerInstallPayload.Manager != managerChoco {
			return errors.New("manager install worker operation only allows choco")
		}
		return nil
	case workerOperationStartupTask, workerOperationAutoUpdateTask:
		var taskPayload elevatedWorkerTaskPayload
		return decodeWorkerPayload(rawPayload, &taskPayload)
	default:
		return fmt.Errorf("unknown worker operation %q", operation)
	}
}

func isElevatedWorkerPackageManager(manager string) bool {
	return manager == managerWinget || manager == managerChoco
}

func validateElevatedWorkerPackageUpdateBatchPayload(batchPayload elevatedWorkerPackageUpdateBatchPayload) error {
	if len(batchPayload.Packages) == 0 {
		return errors.New("package update batch requires at least one package")
	}
	if len(batchPayload.Packages) > elevatedWorkerPackageUpdateBatchMaxPackages {
		return fmt.Errorf("package update batch has %d packages; maximum is %d", len(batchPayload.Packages), elevatedWorkerPackageUpdateBatchMaxPackages)
	}
	for _, packageToUpdate := range batchPayload.Packages {
		if !isElevatedWorkerPackageManager(packageToUpdate.Manager) {
			return errors.New("package update batch only allows winget or choco packages")
		}
		if err := validateManagerAndID(packageToUpdate.Manager, packageToUpdate.ID); err != nil {
			return err
		}
	}
	return nil
}

func decodeWorkerPayload(rawPayload json.RawMessage, target any) error {
	if len(rawPayload) == 0 {
		return errors.New("worker operation payload is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(rawPayload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extraValue struct{}
	if err := decoder.Decode(&extraValue); err != io.EOF {
		return errors.New("worker operation payload contains multiple values")
	}
	return nil
}

func workerCommandResultError(command string, err error) CommandResult {
	return CommandResult{Code: 1, Command: command, Stderr: err.Error()}
}
