package updater

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	storePackagedInventoryProtocolVersion = 1
	storePackagedInventoryTimeout         = 90 * time.Second

	sourceNativeAppX = "native-appx"

	storePackageClassMain      = "main"
	storePackageClassBundle    = "bundle"
	storePackageClassFramework = "framework"
	storePackageClassResource  = "resource"
	storePackageClassOptional  = "optional"
	storePackageClassUnknown   = "unknown"
)

type StorePackagedAppInventoryProvider interface {
	Inventory(context.Context, StoreScanGeneration) (StorePackagedAppInventory, CommandResult)
}

type StorePackagedAppInventory struct {
	Scan     StoreScanGeneration      `json:"scan"`
	Records  []StorePackagedAppRecord `json:"records"`
	Families []StorePackagedAppFamily `json:"families"`
	Partial  bool                     `json:"partial,omitempty"`
	Errors   []string                 `json:"errors,omitempty"`
}

type StorePackagedAppRecord struct {
	UserSID               string              `json:"user_sid"`
	PackageFamilyName     string              `json:"package_family_name"`
	PackageFullName       string              `json:"package_full_name"`
	IdentityName          string              `json:"identity_name"`
	Publisher             string              `json:"publisher,omitempty"`
	PublisherID           string              `json:"publisher_id,omitempty"`
	Version               StorePackageVersion `json:"version"`
	ProcessorArchitecture string              `json:"processor_architecture,omitempty"`
	InstallLocation       string              `json:"install_location,omitempty"`
	PackageType           string              `json:"package_type,omitempty"`
	Classification        string              `json:"classification,omitempty"`
	IsFramework           bool                `json:"is_framework,omitempty"`
	IsResourcePackage     bool                `json:"is_resource_package,omitempty"`
	IsOptional            bool                `json:"is_optional,omitempty"`
	IsBundle              bool                `json:"is_bundle,omitempty"`
	IsDevelopmentMode     bool                `json:"is_development_mode,omitempty"`
	IsStaged              bool                `json:"is_staged,omitempty"`
	Status                StorePackageStatus  `json:"status"`
	DisplayName           string              `json:"display_name,omitempty"`
}

type StorePackageVersion struct {
	Major    uint16 `json:"major"`
	Minor    uint16 `json:"minor"`
	Build    uint16 `json:"build"`
	Revision uint16 `json:"revision"`
}

func (version StorePackageVersion) String() string {
	return fmt.Sprintf("%d.%d.%d.%d", version.Major, version.Minor, version.Build, version.Revision)
}

type StorePackageStatus struct {
	OK                   bool   `json:"ok"`
	Raw                  string `json:"raw,omitempty"`
	VerifyError          string `json:"verify_error,omitempty"`
	DataOffline          bool   `json:"data_offline,omitempty"`
	DependencyIssue      bool   `json:"dependency_issue,omitempty"`
	DeploymentInProgress bool   `json:"deployment_in_progress,omitempty"`
	Disabled             bool   `json:"disabled,omitempty"`
	IsPartiallyStaged    bool   `json:"is_partially_staged,omitempty"`
	LicenseIssue         bool   `json:"license_issue,omitempty"`
	Modified             bool   `json:"modified,omitempty"`
	NeedsRemediation     bool   `json:"needs_remediation,omitempty"`
	NotAvailable         bool   `json:"not_available,omitempty"`
	PackageOffline       bool   `json:"package_offline,omitempty"`
	Servicing            bool   `json:"servicing,omitempty"`
	Tampered             bool   `json:"tampered,omitempty"`
}

type StorePackagedAppFamily struct {
	Identity    StoreInstalledIdentity   `json:"identity"`
	Primary     StorePackagedAppRecord   `json:"primary"`
	Instances   []StorePackagedAppRecord `json:"instances"`
	DisplayName string                   `json:"display_name,omitempty"`
	ProductLike bool                     `json:"product_like"`
}

type storePackagedInventoryRequest struct {
	ProtocolVersion int    `json:"protocol_version"`
	ScanID          string `json:"scan_id"`
	UserSID         string `json:"user_sid"`
}

type storePackagedInventoryResponse struct {
	ProtocolVersion int                      `json:"protocol_version"`
	ScanID          string                   `json:"scan_id"`
	UserSID         string                   `json:"user_sid"`
	StartedAt       string                   `json:"started_at,omitempty"`
	CompletedAt     string                   `json:"completed_at,omitempty"`
	Complete        bool                     `json:"complete"`
	Partial         bool                     `json:"partial,omitempty"`
	Error           string                   `json:"error,omitempty"`
	Records         []StorePackagedAppRecord `json:"records"`
}

type brokerStorePackagedAppInventoryProvider struct {
	Path   string
	Runner func(context.Context, string, []byte) ([]byte, []byte, error)
}

func nativeStoreInventoryEnabled() bool {
	return storeNewDetectorActive() || featureFlagEnabled("UPDATER_NATIVE_STORE_INVENTORY")
}

func nativeStoreInventoryDualRunEnabled() bool {
	return featureFlagEnabled("UPDATER_NATIVE_STORE_INVENTORY_DUAL_RUN")
}

func featureFlagEnabled(name string) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

func defaultStorePackagedAppInventoryProvider() StorePackagedAppInventoryProvider {
	return brokerStorePackagedAppInventoryProvider{Path: storeInventoryBrokerPath(), Runner: runStoreInventoryBroker}
}

var storePackagedAppInventoryProvider = defaultStorePackagedAppInventoryProvider

func storeInventoryBrokerPath() string {
	if override := os.Getenv("UPDATER_STORE_INVENTORY_BROKER"); override != "" {
		return override
	}
	if path, err := ensureEmbeddedStoreInventoryBroker(); err == nil {
		return path
	}
	exe, err := os.Executable()
	if err != nil {
		return "WindowsUpdater.StoreInventoryBroker.exe"
	}
	return filepath.Join(filepath.Dir(exe), "WindowsUpdater.StoreInventoryBroker.exe")
}

func ensureEmbeddedStoreInventoryBroker() (string, error) {
	dir, err := binaryExtractionDir()
	if err != nil {
		return "", err
	}
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(binDir, "WindowsUpdater.StoreInventoryBroker.exe")
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, embeddedStoreInventoryBroker) {
		return path, nil
	}
	tmp := filepath.Join(binDir, fmt.Sprintf("WindowsUpdater.StoreInventoryBroker-%d.tmp", time.Now().UnixNano()))
	if err := os.WriteFile(tmp, embeddedStoreInventoryBroker, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(path)
		if retryErr := os.Rename(tmp, path); retryErr != nil {
			_ = os.Remove(tmp)
			return "", err
		}
	}
	return path, nil
}

func binaryExtractionDir() (string, error) {
	if override := strings.TrimSpace(os.Getenv("UPDATER_BINARY_DIR")); override != "" {
		return override, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Dir(exe), nil
}

func (provider brokerStorePackagedAppInventoryProvider) Inventory(ctx context.Context, scan StoreScanGeneration) (StorePackagedAppInventory, CommandResult) {
	path := strings.TrimSpace(provider.Path)
	if path == "" {
		path = storeInventoryBrokerPath()
	}
	runner := provider.Runner
	if runner == nil {
		runner = runStoreInventoryBroker
	}
	request := storePackagedInventoryRequest{
		ProtocolVersion: storePackagedInventoryProtocolVersion,
		ScanID:          scan.ScanID,
		UserSID:         scan.UserSID,
	}
	input, err := json.Marshal(request)
	if err != nil {
		result := validationCommandResult("store native inventory", err)
		return StorePackagedAppInventory{Scan: scan, Partial: true, Errors: []string{err.Error()}}, result
	}
	ctx, cancel := context.WithTimeout(ctx, storePackagedInventoryTimeout)
	defer cancel()
	stdout, stderr, err := runner(ctx, path, input)
	result := CommandResult{Command: path + " --inventory"}
	if len(stderr) > 0 {
		result.Stderr = string(stderr)
	}
	if err != nil {
		result.Code = 1
		result.Stderr = strings.TrimSpace(result.Stderr + "\n" + err.Error())
		return StorePackagedAppInventory{Scan: scan, Partial: true, Errors: []string{result.Stderr}}, result
	}
	inventory, parseErr := parseStorePackagedInventoryResponse(stdout, scan)
	if parseErr != nil {
		result.Code = 2
		result.Stderr = parseErr.Error()
		return StorePackagedAppInventory{Scan: scan, Partial: true, Errors: []string{parseErr.Error()}}, result
	}
	result.OK = inventory.Scan.CompletionStatus == StoreScanCompleted
	if !result.OK {
		result.Code = 1
		result.Stderr = strings.Join(inventory.Errors, "\n")
	}
	result.Stdout = fmt.Sprintf("Native Store inventory returned %d package record(s), %d product-like family group(s).", len(inventory.Records), productLikeFamilyCount(inventory.Families))
	return inventory, result
}

func runStoreInventoryBroker(ctx context.Context, path string, input []byte) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, path, "--inventory")
	cmd.SysProcAttr = hiddenSysProcAttr()
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

func parseStorePackagedInventoryResponse(data []byte, expectedScan StoreScanGeneration) (StorePackagedAppInventory, error) {
	var response storePackagedInventoryResponse
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return StorePackagedAppInventory{}, err
	}
	if decoder.More() {
		return StorePackagedAppInventory{}, errors.New("native Store inventory response contains multiple JSON values")
	}
	if response.ProtocolVersion != storePackagedInventoryProtocolVersion {
		return StorePackagedAppInventory{}, fmt.Errorf("unsupported native Store inventory protocol version %d", response.ProtocolVersion)
	}
	if response.ScanID != expectedScan.ScanID {
		return StorePackagedAppInventory{}, errors.New("native Store inventory scan_id mismatch")
	}
	if !strings.EqualFold(response.UserSID, expectedScan.UserSID) {
		return StorePackagedAppInventory{}, errors.New("native Store inventory user SID mismatch")
	}
	scan := expectedScan
	if started, err := time.Parse(time.RFC3339, response.StartedAt); err == nil {
		scan.StartedAt = started
	}
	if completed, err := time.Parse(time.RFC3339, response.CompletedAt); err == nil {
		scan.CompletedAt = completed
	}
	if response.Complete && !response.Partial && response.Error == "" {
		scan.CompletionStatus = StoreScanCompleted
	} else {
		scan.CompletionStatus = StoreScanIncomplete
	}
	records := make([]StorePackagedAppRecord, 0, len(response.Records))
	for _, record := range response.Records {
		normalized, err := normalizeStorePackagedAppRecord(record, response.UserSID)
		if err != nil {
			return StorePackagedAppInventory{}, err
		}
		records = append(records, normalized)
	}
	inventory := StorePackagedAppInventory{
		Scan:     scan,
		Records:  records,
		Families: groupStorePackagedAppFamilies(records),
		Partial:  response.Partial || response.Error != "",
	}
	if response.Error != "" {
		inventory.Errors = append(inventory.Errors, response.Error)
	}
	return inventory, nil
}

func normalizeStorePackagedAppRecord(record StorePackagedAppRecord, fallbackUserSID string) (StorePackagedAppRecord, error) {
	record.UserSID = strings.TrimSpace(record.UserSID)
	if record.UserSID == "" {
		record.UserSID = strings.TrimSpace(fallbackUserSID)
	}
	record.PackageFamilyName = strings.TrimSpace(record.PackageFamilyName)
	record.PackageFullName = strings.TrimSpace(record.PackageFullName)
	record.IdentityName = strings.TrimSpace(record.IdentityName)
	record.Publisher = strings.TrimSpace(record.Publisher)
	record.PublisherID = strings.TrimSpace(record.PublisherID)
	record.ProcessorArchitecture = strings.TrimSpace(record.ProcessorArchitecture)
	record.InstallLocation = strings.TrimSpace(record.InstallLocation)
	record.PackageType = strings.TrimSpace(record.PackageType)
	record.Classification = classifyStorePackagedApp(record)
	record.DisplayName = strings.TrimSpace(record.DisplayName)
	if record.UserSID == "" {
		return StorePackagedAppRecord{}, errors.New("native Store inventory record missing user SID")
	}
	if record.PackageFamilyName == "" {
		return StorePackagedAppRecord{}, errors.New("native Store inventory record missing package family name")
	}
	if record.PackageFullName == "" {
		return StorePackagedAppRecord{}, errors.New("native Store inventory record missing package full name")
	}
	if record.IdentityName == "" {
		return StorePackagedAppRecord{}, errors.New("native Store inventory record missing package identity name")
	}
	return record, nil
}

func classifyStorePackagedApp(record StorePackagedAppRecord) string {
	if record.IsResourcePackage {
		return storePackageClassResource
	}
	if record.IsFramework {
		return storePackageClassFramework
	}
	if record.IsOptional {
		return storePackageClassOptional
	}
	if record.IsBundle {
		return storePackageClassBundle
	}
	if record.Classification != "" {
		return strings.TrimSpace(record.Classification)
	}
	return storePackageClassMain
}

func groupStorePackagedAppFamilies(records []StorePackagedAppRecord) []StorePackagedAppFamily {
	byKey := map[StoreInstalledIdentity][]StorePackagedAppRecord{}
	for _, record := range records {
		identity := StoreInstalledIdentity{UserSID: record.UserSID, PackageFamilyName: record.PackageFamilyName}
		byKey[identity] = append(byKey[identity], record)
	}
	families := make([]StorePackagedAppFamily, 0, len(byKey))
	for identity, instances := range byKey {
		sort.Slice(instances, func(i, j int) bool {
			leftRank := storePackageClassificationRank(instances[i].Classification)
			rightRank := storePackageClassificationRank(instances[j].Classification)
			if leftRank != rightRank {
				return leftRank < rightRank
			}
			return compareStorePackageVersion(instances[i].Version, instances[j].Version) > 0
		})
		primary := instances[0]
		families = append(families, StorePackagedAppFamily{
			Identity:    identity,
			Primary:     primary,
			Instances:   instances,
			DisplayName: friendlyStorePackagedAppName(primary),
			ProductLike: familyProductLike(instances),
		})
	}
	sort.Slice(families, func(i, j int) bool {
		return families[i].Identity.PackageFamilyName < families[j].Identity.PackageFamilyName
	})
	return families
}

func compareStorePackageVersion(left, right StorePackageVersion) int {
	leftParts := []uint16{left.Major, left.Minor, left.Build, left.Revision}
	rightParts := []uint16{right.Major, right.Minor, right.Build, right.Revision}
	for i := range leftParts {
		if leftParts[i] > rightParts[i] {
			return 1
		}
		if leftParts[i] < rightParts[i] {
			return -1
		}
	}
	return 0
}

func storePackageClassificationRank(classification string) int {
	switch classification {
	case storePackageClassMain:
		return 0
	case storePackageClassBundle:
		return 1
	case storePackageClassOptional:
		return 2
	case storePackageClassResource:
		return 3
	case storePackageClassFramework:
		return 4
	default:
		return 5
	}
}

func familyProductLike(records []StorePackagedAppRecord) bool {
	for _, record := range records {
		if record.Classification == storePackageClassMain || record.Classification == storePackageClassBundle {
			return true
		}
	}
	return false
}

func productLikeFamilyCount(families []StorePackagedAppFamily) int {
	count := 0
	for _, family := range families {
		if family.ProductLike {
			count++
		}
	}
	return count
}

func friendlyStorePackagedAppName(record StorePackagedAppRecord) string {
	return friendlyAppxName(record.IdentityName, record.DisplayName)
}

func packagesFromNativeStorePackagedInventory(state State, inventory StorePackagedAppInventory) []Package {
	var packages []Package
	for _, family := range inventory.Families {
		if !family.ProductLike {
			continue
		}
		pkg := Package{
			Key:                        packageKey(managerStore, family.Identity.PackageFamilyName),
			Manager:                    managerStore,
			ID:                         family.Identity.PackageFamilyName,
			Name:                       firstNonEmpty(family.DisplayName, family.Primary.IdentityName, family.Identity.PackageFamilyName),
			Version:                    family.Primary.Version.String(),
			Installed:                  true,
			Source:                     sourceNativeAppX,
			Match:                      family.Primary.PackageFullName,
			UpdateSupported:            false,
			ActionBackend:              backendAppXInventory,
			InstalledPackageFamilyName: family.Identity.PackageFamilyName,
			ExactIdentityAvailable:     true,
		}
		pkg.AutoUpdate = packageAutoUpdateEnabled(state, pkg)
		packages = append(packages, pkg)
	}
	return packages
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func newStorePackagedAppScan(userSID string) StoreScanGeneration {
	now := time.Now().UTC()
	systemContext := currentStoreScanSystemContext()
	return StoreScanGeneration{
		ScanID:           fmt.Sprintf("store-native-%d", now.UnixNano()),
		UserSID:          userSID,
		StartedAt:        now,
		WindowsVersion:   systemContext.WindowsVersion,
		WindowsBuild:     systemContext.WindowsBuild,
		Architecture:     systemContext.Architecture,
		ProviderVersions: map[string]string{"store-native-inventory": "1"},
		ProviderHealth:   map[string]StoreProviderHealth{"store-native-inventory": StoreProviderHealthy},
		CompletionStatus: StoreScanRunning,
	}
}
