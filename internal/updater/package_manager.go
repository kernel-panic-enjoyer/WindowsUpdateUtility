package updater

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

type ManagerStatus struct {
	Available          bool   `json:"available"`
	Version            string `json:"version,omitempty"`
	Path               string `json:"path,omitempty"`
	Error              string `json:"error,omitempty"`
	InventoryAvailable bool   `json:"inventory_available,omitempty"`
	InventoryBackend   string `json:"inventory_backend,omitempty"`
	ActionBackend      string `json:"action_backend,omitempty"`
}

type Package struct {
	Key              string `json:"key"`
	Manager          string `json:"manager"`
	ID               string `json:"id"`
	Name             string `json:"name"`
	Version          string `json:"version"`
	AvailableVersion string `json:"available_version"`
	UpdateAvailable  bool   `json:"update_available"`
	UpdateSupported  bool   `json:"update_supported"`
	UnknownVersion   bool   `json:"unknown_version,omitempty"`
	Pinned           bool   `json:"pinned,omitempty"`
	Installed        bool   `json:"installed"`
	AutoUpdate       bool   `json:"auto_update"`
	Source           string `json:"source,omitempty"`
	Match            string `json:"match,omitempty"`
	MatchReason      string `json:"match_reason,omitempty"`
	ActionBackend    string `json:"action_backend,omitempty"`

	UpdateState                string                        `json:"update_state,omitempty"`
	UpdateReason               string                        `json:"update_reason,omitempty"`
	ObservedAt                 string                        `json:"observed_at,omitempty"`
	Stale                      bool                          `json:"stale,omitempty"`
	ScanID                     string                        `json:"scan_id,omitempty"`
	ExactIdentityAvailable     bool                          `json:"exact_identity_available,omitempty"`
	ExactActionTargetAvailable bool                          `json:"exact_action_target_available,omitempty"`
	ProviderSummaries          []StorePackageProviderSummary `json:"provider_summaries,omitempty"`
	InstalledPackageFamilyName string                        `json:"installed_package_family_name,omitempty"`
	StoreProductID             string                        `json:"store_product_id,omitempty"`
	StoreUpdateID              string                        `json:"store_update_id,omitempty"`
	InstalledVersion           string                        `json:"installed_version,omitempty"`
	OfferedVersion             string                        `json:"offered_version,omitempty"`
	Applicability              string                        `json:"applicability,omitempty"`
	PreferenceEligible         bool                          `json:"preference_eligible"`
	CanUpdateNow               bool                          `json:"can_update_now"`
	CannotUpdateReason         string                        `json:"cannot_update_reason,omitempty"`
	ExactTargetKind            string                        `json:"exact_target_kind,omitempty"`

	AllowUnknownVersionUpdate bool `json:"-"`
	AllowPinnedUpdate         bool `json:"-"`
}

type StorePackageProviderSummary struct {
	Name       string `json:"name"`
	Version    string `json:"version,omitempty"`
	Health     string `json:"health"`
	Kind       string `json:"kind"`
	ObservedAt string `json:"observed_at,omitempty"`
	Error      string `json:"error,omitempty"`
}

type StoreScanHealthSummary struct {
	Active        bool                          `json:"active"`
	Healthy       bool                          `json:"healthy"`
	Authoritative bool                          `json:"authoritative"`
	ScanID        string                        `json:"scan_id,omitempty"`
	Status        string                        `json:"status,omitempty"`
	ObservedAt    string                        `json:"observed_at,omitempty"`
	Reason        string                        `json:"reason,omitempty"`
	Counts        map[string]int                `json:"counts,omitempty"`
	Providers     []StorePackageProviderSummary `json:"providers,omitempty"`
}

type PackageLookup struct {
	Packages       []Package                `json:"packages"`
	Managers       map[string]ManagerStatus `json:"managers"`
	CommandResults map[string]CommandResult `json:"command_results"`
}

type Inventory struct {
	PackageLookup
	Scan            InventoryScanSummary   `json:"scan"`
	StoreScanHealth StoreScanHealthSummary `json:"store_scan_health,omitempty"`
}

type UpdateResult struct {
	Key    string        `json:"key"`
	Result CommandResult `json:"result"`
}

type UpdateResultSummary struct {
	Key             string `json:"key"`
	Manager         string `json:"manager,omitempty"`
	PackageID       string `json:"package_id,omitempty"`
	Success         bool   `json:"success"`
	Code            int    `json:"code"`
	FinishedAt      string `json:"finished_at,omitempty"`
	RestartRequired bool   `json:"restart_required,omitempty"`
	Message         string `json:"message,omitempty"`
}

func (summary *UpdateResultSummary) UnmarshalJSON(data []byte) error {
	var legacyResultProbe struct {
		Result *CommandResult `json:"result"`
	}
	if err := json.Unmarshal(data, &legacyResultProbe); err == nil && legacyResultProbe.Result != nil {
		var legacyUpdate UpdateResult
		if err := json.Unmarshal(data, &legacyUpdate); err != nil {
			return err
		}
		*summary = summarizeUpdateResult(legacyUpdate, "")
		return nil
	}
	type alias UpdateResultSummary
	var directSummary alias
	if err := json.Unmarshal(data, &directSummary); err != nil {
		return err
	}
	*summary = UpdateResultSummary(directSummary)
	return nil
}

type InventoryScanSummary struct {
	LastScanAt    string `json:"last_scan_at,omitempty"`
	TrackedCount  int    `json:"tracked_count"`
	RegistryCount int    `json:"registry_count"`
	WingetCount   int    `json:"winget_count"`
	StoreCount    int    `json:"store_count"`
}

const (
	managerWinget = "winget"
	managerStore  = "store"
	managerChoco  = "choco"

	sourceWinget   = "winget"
	sourceMSStore  = "msstore"
	sourceStoreCLI = "store-cli"
	sourceAppX     = "appx"

	backendStoreCLI              = "store-cli"
	backendAppXInventory         = "appx-inventory"
	backendWingetMSStoreFallback = "winget-msstore-fallback"
	backendWinRT                 = "winrt"
	inventoryBackendAppX         = "AppX"
)

var managedPackageManagers = []string{managerWinget, managerStore, managerChoco}

const managerValidationMessage = "manager must be winget, store, or choco"
const storeActionUnavailableMessage = "native Store CLI is unavailable and winget msstore fallback is unavailable"

func isManagedPackageManager(manager string) bool {
	for _, supportedManager := range managedPackageManagers {
		if manager == supportedManager {
			return true
		}
	}
	return false
}

func managerValidationError() error {
	return errors.New(managerValidationMessage)
}

func wingetSourceManager(source string) string {
	if strings.EqualFold(strings.TrimSpace(source), sourceMSStore) {
		return managerStore
	}
	return managerWinget
}

func managerSortRank(manager string) int {
	for rank, supportedManager := range managedPackageManagers {
		if manager == supportedManager {
			return rank
		}
	}
	return len(managedPackageManagers)
}

func versionGreater(candidate, current string) bool {
	candidateVersionParts := versionParts(candidate)
	currentVersionParts := versionParts(current)
	if len(candidateVersionParts) == 0 || len(currentVersionParts) == 0 {
		return false
	}
	partCount := len(candidateVersionParts)
	if len(currentVersionParts) > partCount {
		partCount = len(currentVersionParts)
	}
	for partIndex := 0; partIndex < partCount; partIndex++ {
		candidatePart := 0
		currentPart := 0
		if partIndex < len(candidateVersionParts) {
			candidatePart = candidateVersionParts[partIndex]
		}
		if partIndex < len(currentVersionParts) {
			currentPart = currentVersionParts[partIndex]
		}
		if candidatePart > currentPart {
			return true
		}
		if candidatePart < currentPart {
			return false
		}
	}
	return false
}

func versionParts(version string) []int {
	numericParts := []int{}
	var digitRun strings.Builder
	appendDigitRun := func() bool {
		if digitRun.Len() == 0 {
			return true
		}
		part, err := strconv.Atoi(digitRun.String())
		digitRun.Reset()
		if err != nil {
			return false
		}
		numericParts = append(numericParts, part)
		return true
	}
	for _, versionRune := range strings.TrimSpace(version) {
		if versionRune >= '0' && versionRune <= '9' {
			digitRun.WriteRune(versionRune)
			continue
		}
		if !appendDigitRun() {
			return nil
		}
	}
	if !appendDigitRun() {
		return nil
	}
	return numericParts
}

func normalizePackageIdentity(identity string) string {
	normalizedInput := strings.ToLower(strings.TrimSpace(identity))
	normalizedInput = strings.TrimSuffix(normalizedInput, "_8wekyb3d8bbwe")
	var normalizedIdentity strings.Builder
	for _, identityRune := range normalizedInput {
		if (identityRune >= 'a' && identityRune <= 'z') || (identityRune >= '0' && identityRune <= '9') {
			normalizedIdentity.WriteRune(identityRune)
		}
	}
	return normalizedIdentity.String()
}

func packageKey(manager, id string) string {
	return manager + ":" + id
}

func packageAutoUpdateEnabled(state State, pkg Package) bool {
	if pkg.Manager == managerStore {
		storeAutoUpdateKey := storePackageAutoUpdateKey(pkg)
		return storeAutoUpdateKey != "" && state.AutoUpdatePackages[storeAutoUpdateKey]
	}
	if state.AutoUpdatePackages[pkg.Key] {
		return true
	}
	normalizedPackageKey := normalizeAutoUpdatePackageKey(pkg.Key)
	if state.AutoUpdatePackages[normalizedPackageKey] {
		return true
	}
	for configuredKey, enabled := range state.AutoUpdatePackages {
		if enabled && equivalentPackageKeys(pkg.Key, configuredKey) {
			return true
		}
	}
	return false
}

func equivalentPackageKeys(left, right string) bool {
	leftManager, leftPackageID, leftErr := splitPackageKey(left)
	rightManager, rightPackageID, rightErr := splitPackageKey(right)
	if leftErr != nil || rightErr != nil || leftManager != rightManager {
		return false
	}
	if leftManager == managerStore {
		leftAutoUpdateKey := normalizeAutoUpdatePackageKey(left)
		rightAutoUpdateKey := normalizeAutoUpdatePackageKey(right)
		return leftAutoUpdateKey != "" && strings.EqualFold(leftAutoUpdateKey, rightAutoUpdateKey)
	}
	return leftPackageID == rightPackageID
}

func splitPackageKey(key string) (string, string, error) {
	keyParts := strings.SplitN(key, ":", 2)
	if len(keyParts) != 2 || keyParts[1] == "" || !isManagedPackageManager(keyParts[0]) {
		return "", "", errors.New("package key must be manager:id")
	}
	return keyParts[0], keyParts[1], nil
}
