package updater

import (
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
	backendStoreCLIResolved      = "store-cli-resolved"
	backendWingetMSStoreFallback = "winget-msstore-fallback"
	inventoryBackendAppX         = "AppX"
)

var managedPackageManagers = []string{managerWinget, managerStore, managerChoco}

const managerValidationMessage = "manager must be winget, store, or choco"
const storeActionUnavailableMessage = "native Store CLI is unavailable and winget msstore fallback is unavailable"

func isManagedPackageManager(manager string) bool {
	for _, supported := range managedPackageManagers {
		if manager == supported {
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
	for index, supported := range managedPackageManagers {
		if manager == supported {
			return index
		}
	}
	return len(managedPackageManagers)
}

func versionGreater(candidate, current string) bool {
	candidateParts := versionParts(candidate)
	currentParts := versionParts(current)
	if len(candidateParts) == 0 || len(currentParts) == 0 {
		return false
	}
	maxParts := len(candidateParts)
	if len(currentParts) > maxParts {
		maxParts = len(currentParts)
	}
	for i := 0; i < maxParts; i++ {
		candidatePart := 0
		currentPart := 0
		if i < len(candidateParts) {
			candidatePart = candidateParts[i]
		}
		if i < len(currentParts) {
			currentPart = currentParts[i]
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

func versionParts(value string) []int {
	parts := []int{}
	var current strings.Builder
	flush := func() bool {
		if current.Len() == 0 {
			return true
		}
		part, err := strconv.Atoi(current.String())
		current.Reset()
		if err != nil {
			return false
		}
		parts = append(parts, part)
		return true
	}
	for _, r := range strings.TrimSpace(value) {
		if r >= '0' && r <= '9' {
			current.WriteRune(r)
			continue
		}
		if !flush() {
			return nil
		}
	}
	if !flush() {
		return nil
	}
	return parts
}

func normalizePackageIdentity(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimSuffix(value, "_8wekyb3d8bbwe")
	var normalized strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			normalized.WriteRune(r)
		}
	}
	return normalized.String()
}

func packageKey(manager, id string) string {
	return manager + ":" + id
}

func packageAutoUpdateEnabled(state State, pkg Package) bool {
	if pkg.Manager == managerStore && storeNewDetectorActive() {
		key := storePackageAutoUpdateKey(pkg)
		return key != "" && state.AutoUpdatePackages[key]
	}
	if state.AutoUpdatePackages[pkg.Key] {
		return true
	}
	normalizedKey := normalizeAutoUpdatePackageKey(pkg.Key)
	if state.AutoUpdatePackages[normalizedKey] {
		return true
	}
	for key, enabled := range state.AutoUpdatePackages {
		if enabled && equivalentPackageKeys(pkg.Key, key) {
			return true
		}
	}
	return false
}

func equivalentPackageKeys(left, right string) bool {
	leftManager, leftID, leftErr := splitPackageKey(left)
	rightManager, rightID, rightErr := splitPackageKey(right)
	if leftErr != nil || rightErr != nil || leftManager != rightManager {
		return false
	}
	if leftManager == managerStore {
		if storeNewDetectorActive() {
			leftNormalized := normalizeAutoUpdatePackageKey(left)
			rightNormalized := normalizeAutoUpdatePackageKey(right)
			return leftNormalized != "" && strings.EqualFold(leftNormalized, rightNormalized)
		}
		return strings.EqualFold(stableStoreActionID(leftID), stableStoreActionID(rightID))
	}
	return leftID == rightID
}

func splitPackageKey(key string) (string, string, error) {
	parts := strings.SplitN(key, ":", 2)
	if len(parts) != 2 || parts[1] == "" || !isManagedPackageManager(parts[0]) {
		return "", "", errors.New("package key must be manager:id")
	}
	return parts[0], parts[1], nil
}
