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

func newElevatedWorkerRequest(auth elevatedWorkerAuthContext, requestID, operation string, payload any) (elevatedWorkerMessage, error) {
	raw, err := marshalWorkerPayload(payload)
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
		Payload:    raw,
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

func marshalWorkerPayload(payload any) (json.RawMessage, error) {
	if payload == nil {
		return nil, nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func validateElevatedWorkerMessage(message elevatedWorkerMessage, expected elevatedWorkerAuthContext) error {
	if message.Version != elevatedWorkerProtocolVersion {
		return fmt.Errorf("unsupported worker protocol version %d", message.Version)
	}
	if message.RequestID == "" {
		return errors.New("worker request_id is required")
	}
	if message.Capability == "" || message.Capability != expected.Capability {
		return errors.New("worker capability is invalid")
	}
	if message.UserSID == "" || !strings.EqualFold(message.UserSID, expected.UserSID) {
		return errors.New("worker user SID is invalid")
	}
	if message.SessionID != expected.SessionID {
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

func validateWorkerOperationPayload(operation string, payload json.RawMessage) error {
	switch operation {
	case workerOperationPackageInstall:
		var decoded elevatedWorkerPackageInstallPayload
		if err := decodeWorkerPayload(payload, &decoded); err != nil {
			return err
		}
		if decoded.Manager != managerWinget && decoded.Manager != managerChoco {
			return errors.New("package install worker operation only allows winget or choco")
		}
		return validateManagerAndID(decoded.Manager, decoded.PackageID)
	case workerOperationPackageUpdate:
		var decoded elevatedWorkerPackageUpdatePayload
		if err := decodeWorkerPayload(payload, &decoded); err != nil {
			return err
		}
		if decoded.Package.Manager != managerWinget && decoded.Package.Manager != managerChoco {
			return errors.New("package update worker operation only allows winget or choco")
		}
		return validateManagerAndID(decoded.Package.Manager, decoded.Package.ID)
	case workerOperationPackageUpdateBatch:
		var decoded elevatedWorkerPackageUpdateBatchPayload
		if err := decodeWorkerPayload(payload, &decoded); err != nil {
			return err
		}
		return validateElevatedWorkerPackageUpdateBatchPayload(decoded)
	case workerOperationManagerInstall:
		var decoded elevatedWorkerManagerInstallPayload
		if err := decodeWorkerPayload(payload, &decoded); err != nil {
			return err
		}
		if decoded.Manager != managerChoco {
			return errors.New("manager install worker operation only allows choco")
		}
		return nil
	case workerOperationStartupTask, workerOperationAutoUpdateTask:
		var decoded elevatedWorkerTaskPayload
		return decodeWorkerPayload(payload, &decoded)
	default:
		return fmt.Errorf("unknown worker operation %q", operation)
	}
}

func validateElevatedWorkerPackageUpdateBatchPayload(payload elevatedWorkerPackageUpdateBatchPayload) error {
	if len(payload.Packages) == 0 {
		return errors.New("package update batch requires at least one package")
	}
	if len(payload.Packages) > elevatedWorkerPackageUpdateBatchMaxPackages {
		return fmt.Errorf("package update batch has %d packages; maximum is %d", len(payload.Packages), elevatedWorkerPackageUpdateBatchMaxPackages)
	}
	for _, pkg := range payload.Packages {
		if pkg.Manager != managerWinget && pkg.Manager != managerChoco {
			return errors.New("package update batch only allows winget or choco packages")
		}
		if err := validateManagerAndID(pkg.Manager, pkg.ID); err != nil {
			return err
		}
	}
	return nil
}

func decodeWorkerPayload(data json.RawMessage, target any) error {
	if len(data) == 0 {
		return errors.New("worker operation payload is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing struct{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("worker operation payload contains multiple values")
	}
	return nil
}

func workerCommandResultError(command string, err error) CommandResult {
	return CommandResult{Code: 1, Command: command, Stderr: err.Error()}
}
