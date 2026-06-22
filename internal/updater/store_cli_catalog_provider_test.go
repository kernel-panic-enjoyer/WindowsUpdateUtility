package updater

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestParseStoreCLIShowMetadataRequiresExactPFNAndProductID(t *testing.T) {
	output := `Basic Information
  Name            : VP9-Videoerweiterungen

Technical Information
  Product ID      : 9N4D0MSMP0PT
  Publisher ID    : 10100100
  PFN             : Microsoft.VP9VideoExtensions_8wekyb3d8bbwe`
	got, err := parseStoreCLIShowMetadata(output)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProductID != "9N4D0MSMP0PT" || got.PFN != "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe" {
		t.Fatalf("metadata=%#v", got)
	}
	if _, err := parseStoreCLIShowMetadata("Name: VP9"); err == nil {
		t.Fatal("expected missing PFN/Product ID to be rejected")
	}
}

func TestParseStoreCLIUpdateCheck(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		want    StoreObservationKind
		wantErr bool
	}{
		{
			name: "positive",
			output: `Checking updates...
Checking updates for VP9-Videoerweiterungen...

Update available for 'VP9-Videoerweiterungen'

Would you like to apply the update? [y/n] (y):
Failed to read input in non-interactive mode.`,
			want: StoreObservationPositiveUpdateOffer,
		},
		{
			name:   "english prompt only",
			output: "Checking updates...\nWould you like to apply the update? [y/n] (y):\nFailed to read input in non-interactive mode.",
			want:   StoreObservationPositiveUpdateOffer,
		},
		{
			name:   "english install prompt only",
			output: "Checking updates...\nWould you like to install the update now? [y/n] (y):",
			want:   StoreObservationPositiveUpdateOffer,
		},
		{
			name:   "english single update count prompt",
			output: "Checking updates...\nWould you like to install the 1 Store update(s) now? [y/n] (y):",
			want:   StoreObservationPositiveUpdateOffer,
		},
		{
			name:    "generic multi update count prompt",
			output:  "Checking updates...\nWould you like to install the 2 Store update(s) now? [y/n] (y):",
			want:    StoreObservationIncompleteResult,
			wantErr: true,
		},
		{
			name:   "german prompt only",
			output: "Updates werden gesucht...\nMöchten Sie das Update jetzt installieren? [j/n] (j):",
			want:   StoreObservationPositiveUpdateOffer,
		},
		{
			name:   "current",
			output: "Checking updates...\n'Windows-Rechner' is already up to date",
			want:   StoreObservationAuthoritativeNegative,
		},
		{
			name:    "current mixed with failure is incomplete",
			output:  "Checking updates...\nNo updates found.\nFailed to read input in non-interactive mode.",
			want:    StoreObservationIncompleteResult,
			wantErr: true,
		},
		{
			name:   "inapplicable",
			output: "Checking updates...\nA newer version exists, but no applicable installer is available.",
			want:   StoreObservationNewerCatalogNoApplicableInstaller,
		},
		{
			name:    "error",
			output:  "Checking updates...\nError: Could not find installed product metadata.",
			want:    StoreObservationIncompleteResult,
			wantErr: true,
		},
		{
			name:    "empty",
			output:  "Checking updates...\n",
			want:    StoreObservationEmptyResult,
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseStoreCLIUpdateCheck(tc.output)
			if got != tc.want || (err != nil) != tc.wantErr {
				t.Fatalf("got=%s err=%v, want=%s err=%v", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestParseStoreCLIUpdatesOutput(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		wantNone  bool
		wantCount int
		wantErr   bool
	}{
		{
			name:     "explicit no updates",
			output:   "Checking for updates...\n\nNo updates found.",
			wantNone: true,
		},
		{
			name:     "no updates mixed with failure",
			output:   "Checking for updates...\n\nNo updates found.\nFailed to read input in non-interactive mode.",
			wantNone: true,
			wantErr:  true,
		},
		{
			name: "exact offer",
			output: `Checking for updates...

Update available
Product ID : 9N4D0MSMP0PT
PFN : Microsoft.VP9VideoExtensions_8wekyb3d8bbwe
Available Version : 1.2.20.0`,
			wantCount: 1,
		},
		{
			name: "contradictory no updates with exact offer",
			output: `Checking for updates...

No updates found.

Update available
Product ID : 9N4D0MSMP0PT
PFN : Microsoft.VP9VideoExtensions_8wekyb3d8bbwe
Available Version : 1.2.20.0`,
			wantNone:  true,
			wantCount: 1,
			wantErr:   true,
		},
		{
			name: "prompt count exceeding exact rows",
			output: `Would you like to install the 2 Store update(s) now? [y/n] (y):
Product ID : 9N4D0MSMP0PT
PFN : Microsoft.VP9VideoExtensions_8wekyb3d8bbwe
Available Version : 1.2.20.0`,
			wantCount: 1,
			wantErr:   true,
		},
		{
			name:    "positive hint without exact identifiers",
			output:  "Would you like to install the 2 Store update(s) now? [y/n] (y):",
			wantErr: true,
		},
		{
			name:    "empty output",
			output:  "Checking for updates...\n",
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseStoreCLIUpdatesOutput(tc.output)
			if got.NoUpdates != tc.wantNone || len(got.Offers) != tc.wantCount || (err != nil) != tc.wantErr {
				t.Fatalf("got=%#v err=%v", got, err)
			}
		})
	}
}

func TestParseStoreCLIUpdatesOutputInapplicableExactOffer(t *testing.T) {
	got, err := parseStoreCLIUpdatesOutput(`Update available
Product ID : 9N4D0MSMP0PT
PFN : Microsoft.VP9VideoExtensions_8wekyb3d8bbwe
Available Version : 1.2.20.0
Applicability : No applicable installer`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Offers) != 1 || !got.Offers[0].Inapplicable {
		t.Fatalf("expected exact inapplicable offer, got %#v", got)
	}
}

func TestStoreCLIExactProviderVP9PositiveOffer(t *testing.T) {
	restore := replacePackageActionManagerAvailable(func(manager string) bool { return manager == managerStore })
	defer restore()
	scan := StoreScanGeneration{
		ScanID:           "scan-vp9",
		UserSID:          "S-1-5-21-vp9",
		StartedAt:        time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC),
		CompletedAt:      time.Date(2026, 6, 21, 12, 0, 1, 0, time.UTC),
		CompletionStatus: StoreScanCompleted,
	}
	pfn := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	provider := storeCLIExactCatalogProvider{
		Concurrency: 1,
		Now:         fixedPipelineTimes(scan.StartedAt, scan.StartedAt.Add(time.Second), scan.StartedAt.Add(2*time.Second), scan.StartedAt.Add(3*time.Second)),
		Run: func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			command := strings.Join(args, " ")
			switch {
			case strings.Contains(command, " show "+pfn):
				return CommandResult{OK: true, Command: command, Stdout: "Product ID : 9N4D0MSMP0PT\nPFN : " + pfn}
			case strings.Contains(command, " update "+pfn):
				return CommandResult{OK: true, Command: command, Stdout: "Update available for 'VP9-Videoerweiterungen'\nFailed to read input in non-interactive mode."}
			default:
				return CommandResult{Command: command, Code: 1, Stderr: "unexpected command"}
			}
		},
	}
	run := provider.Observe(context.Background(), scan, testStoreInventory(scan, pfn, "1.2.13.0").Families)
	if run.Health != StoreProviderHealthy || len(run.Observations) != 1 || len(run.Mappings) != 1 {
		t.Fatalf("run=%#v", run)
	}
	observation := run.Observations[0]
	if observation.Kind != StoreObservationPositiveUpdateOffer || observation.Target == nil {
		t.Fatalf("observation=%#v", observation)
	}
	if observation.Target.ProductID != "9N4D0MSMP0PT" || observation.Target.UpdateID != pfn || !observation.Target.ExactFor(observation.Identity) {
		t.Fatalf("target=%#v", observation.Target)
	}
}

func TestStoreCLIUpdatesProviderNoUpdatesReturnsAuthoritativeNegatives(t *testing.T) {
	restore := replacePackageActionManagerAvailable(func(manager string) bool { return manager == managerStore })
	defer restore()
	scan := StoreScanGeneration{
		ScanID:           "scan-store-updates-none",
		UserSID:          "S-1-5-21-store-updates",
		StartedAt:        time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC),
		CompletedAt:      time.Date(2026, 6, 21, 12, 0, 1, 0, time.UTC),
		CompletionStatus: StoreScanCompleted,
	}
	pfn := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	provider := storeCLIUpdatesCatalogProvider{
		Now: fixedPipelineTimes(scan.StartedAt, scan.StartedAt.Add(time.Second)),
		Run: func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			command := strings.Join(args, " ")
			if !strings.Contains(command, "updates --apply false") {
				t.Fatalf("aggregate provider used wrong command: %s", command)
			}
			return CommandResult{OK: true, Command: command, Stdout: "Checking for updates...\n\nNo updates found."}
		},
	}
	run := provider.Observe(context.Background(), scan, testStoreInventory(scan, pfn, "1.2.20.0").Families)
	if run.Health != StoreProviderHealthy || len(run.Observations) != 1 {
		t.Fatalf("run=%#v", run)
	}
	if got := run.Observations[0]; got.Kind != StoreObservationAuthoritativeNegative || got.Identity.PackageFamilyName != pfn {
		t.Fatalf("observation=%#v", got)
	}
}

func TestStoreCLIUpdatesProviderNoUpdatesWithFailureIsIncomplete(t *testing.T) {
	restore := replacePackageActionManagerAvailable(func(manager string) bool { return manager == managerStore })
	defer restore()
	scan := StoreScanGeneration{
		ScanID:           "scan-store-updates-failed-none",
		UserSID:          "S-1-5-21-store-updates",
		StartedAt:        time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC),
		CompletedAt:      time.Date(2026, 6, 21, 12, 0, 1, 0, time.UTC),
		CompletionStatus: StoreScanCompleted,
	}
	pfn := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	provider := storeCLIUpdatesCatalogProvider{
		Now: fixedPipelineTimes(scan.StartedAt, scan.StartedAt.Add(time.Second)),
		Run: func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			return CommandResult{OK: true, Command: strings.Join(args, " "), Stdout: "Checking for updates...\n\nNo updates found.\nFailed to read input in non-interactive mode."}
		},
	}
	run := provider.Observe(context.Background(), scan, testStoreInventory(scan, pfn, "1.2.20.0").Families)
	if run.Health != StoreProviderIncomplete || len(run.Observations) != 0 {
		t.Fatalf("failure-tainted no-updates output must not manufacture negatives: %#v", run)
	}
	if !strings.Contains(strings.ToLower(run.Error), "failure") && !strings.Contains(strings.ToLower(run.Error), "failed") {
		t.Fatalf("run error should explain failure-tainted aggregate output: %#v", run)
	}
}

func TestStoreCLIUpdatesProviderRequiresExactIdentifiersForPositive(t *testing.T) {
	restore := replacePackageActionManagerAvailable(func(manager string) bool { return manager == managerStore })
	defer restore()
	scan := StoreScanGeneration{
		ScanID:           "scan-store-updates-positive",
		UserSID:          "S-1-5-21-store-updates",
		StartedAt:        time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC),
		CompletedAt:      time.Date(2026, 6, 21, 12, 0, 1, 0, time.UTC),
		CompletionStatus: StoreScanCompleted,
	}
	pfn := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	provider := storeCLIUpdatesCatalogProvider{
		Now: fixedPipelineTimes(scan.StartedAt, scan.StartedAt.Add(time.Second)),
		Run: func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			return CommandResult{OK: true, Command: strings.Join(args, " "), Stdout: `Update available
Product ID : 9N4D0MSMP0PT
PFN : Microsoft.VP9VideoExtensions_8wekyb3d8bbwe
Available Version : 1.2.20.0`}
		},
	}
	run := provider.Observe(context.Background(), scan, testStoreInventory(scan, pfn, "1.2.13.0").Families)
	if run.Health != StoreProviderHealthy || len(run.Observations) != 1 || len(run.Mappings) != 1 {
		t.Fatalf("run=%#v", run)
	}
	observation := run.Observations[0]
	if observation.Kind != StoreObservationPositiveUpdateOffer || observation.Target == nil {
		t.Fatalf("observation=%#v", observation)
	}
	if observation.Target.ProductID != "9N4D0MSMP0PT" || observation.Target.UpdateID != pfn || observation.AvailableVersion != "1.2.20.0" {
		t.Fatalf("observation=%#v", observation)
	}
}

func TestStoreCLIUpdatesProviderExactInapplicableOffer(t *testing.T) {
	restore := replacePackageActionManagerAvailable(func(manager string) bool { return manager == managerStore })
	defer restore()
	scan := StoreScanGeneration{
		ScanID:           "scan-store-updates-inapplicable",
		UserSID:          "S-1-5-21-store-updates",
		StartedAt:        time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC),
		CompletedAt:      time.Date(2026, 6, 21, 12, 0, 1, 0, time.UTC),
		CompletionStatus: StoreScanCompleted,
	}
	pfn := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	provider := storeCLIUpdatesCatalogProvider{
		Now: fixedPipelineTimes(scan.StartedAt, scan.StartedAt.Add(time.Second)),
		Run: func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			return CommandResult{OK: true, Command: strings.Join(args, " "), Stdout: `Update available
Product ID : 9N4D0MSMP0PT
PFN : Microsoft.VP9VideoExtensions_8wekyb3d8bbwe
Available Version : 1.2.20.0
Applicability : No applicable installer`}
		},
	}
	run := provider.Observe(context.Background(), scan, testStoreInventory(scan, pfn, "1.2.13.0").Families)
	if run.Health != StoreProviderHealthy || len(run.Observations) != 1 || len(run.Mappings) != 1 {
		t.Fatalf("run=%#v", run)
	}
	observation := run.Observations[0]
	if observation.Kind != StoreObservationNewerCatalogNoApplicableInstaller || observation.Target != nil {
		t.Fatalf("expected inapplicable observation without update target, got %#v", observation)
	}
	if observation.CatalogVersion != "1.2.20.0" || observation.AvailableVersion != "1.2.20.0" {
		t.Fatalf("expected catalog version to be retained, got %#v", observation)
	}
}

func TestStoreCLIUpdatesProviderExactOffersReturnNegativesForUnlistedPFNs(t *testing.T) {
	restore := replacePackageActionManagerAvailable(func(manager string) bool { return manager == managerStore })
	defer restore()
	scan := StoreScanGeneration{
		ScanID:           "scan-store-updates-coverage",
		UserSID:          "S-1-5-21-store-updates",
		StartedAt:        time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC),
		CompletedAt:      time.Date(2026, 6, 21, 12, 0, 1, 0, time.UTC),
		CompletionStatus: StoreScanCompleted,
	}
	updatePFN := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	currentPFN := "Microsoft.WindowsCalculator_8wekyb3d8bbwe"
	inventory := testStoreInventory(scan, updatePFN, "1.2.13.0")
	current := testStoreInventory(scan, currentPFN, "11.0.0.0")
	inventory.Records = append(inventory.Records, current.Records...)
	inventory.Families = groupStorePackagedAppFamilies(inventory.Records)
	provider := storeCLIUpdatesCatalogProvider{
		Now: fixedPipelineTimes(scan.StartedAt, scan.StartedAt.Add(time.Second), scan.StartedAt.Add(2*time.Second), scan.StartedAt.Add(3*time.Second)),
		Run: func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			return CommandResult{OK: true, Command: strings.Join(args, " "), Stdout: `Update available
Product ID : 9N4D0MSMP0PT
PFN : Microsoft.VP9VideoExtensions_8wekyb3d8bbwe
Available Version : 1.2.20.0`}
		},
	}
	run := provider.Observe(context.Background(), scan, inventory.Families)
	if run.Health != StoreProviderHealthy || len(run.Observations) != 2 || len(run.Mappings) != 1 {
		t.Fatalf("run=%#v", run)
	}
	byPFN := map[string]StoreProviderObservation{}
	for _, observation := range run.Observations {
		byPFN[observation.Identity.PackageFamilyName] = observation
	}
	if got := byPFN[updatePFN]; got.Kind != StoreObservationPositiveUpdateOffer || got.Target == nil || got.Target.ProductID != "9N4D0MSMP0PT" {
		t.Fatalf("update observation=%#v", got)
	}
	if got := byPFN[currentPFN]; got.Kind != StoreObservationAuthoritativeNegative || got.Target != nil {
		t.Fatalf("current observation=%#v", got)
	}
}

func TestStoreCLIUpdatesProviderUnmatchedOfferMakesCoverageIncomplete(t *testing.T) {
	restore := replacePackageActionManagerAvailable(func(manager string) bool { return manager == managerStore })
	defer restore()
	scan := StoreScanGeneration{
		ScanID:           "scan-store-updates-unmatched",
		UserSID:          "S-1-5-21-store-updates",
		StartedAt:        time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC),
		CompletedAt:      time.Date(2026, 6, 21, 12, 0, 1, 0, time.UTC),
		CompletionStatus: StoreScanCompleted,
	}
	updatePFN := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	currentPFN := "Microsoft.WindowsCalculator_8wekyb3d8bbwe"
	inventory := testStoreInventory(scan, updatePFN, "1.2.13.0")
	current := testStoreInventory(scan, currentPFN, "11.0.0.0")
	inventory.Records = append(inventory.Records, current.Records...)
	inventory.Families = groupStorePackagedAppFamilies(inventory.Records)
	provider := storeCLIUpdatesCatalogProvider{
		Now: fixedPipelineTimes(scan.StartedAt, scan.StartedAt.Add(time.Second), scan.StartedAt.Add(2*time.Second)),
		Run: func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			return CommandResult{OK: true, Command: strings.Join(args, " "), Stdout: `Update available
Product ID : 9N4D0MSMP0PT
PFN : Microsoft.VP9VideoExtensions_8wekyb3d8bbwe
Available Version : 1.2.20.0

Product ID : 9NUNMATCHED
PFN : Vendor.NotInstalled_12345
Available Version : 2.0.0`}
		},
	}
	run := provider.Observe(context.Background(), scan, inventory.Families)
	if run.Health != StoreProviderIncomplete || len(run.Observations) != 1 || len(run.Mappings) != 1 {
		t.Fatalf("run=%#v", run)
	}
	got := run.Observations[0]
	if got.Identity.PackageFamilyName != updatePFN || got.Kind != StoreObservationPositiveUpdateOffer || got.Target == nil {
		t.Fatalf("matched positive should be retained without manufacturing negatives: %#v", got)
	}
	if !strings.Contains(run.Error, "without matching installed PFN") {
		t.Fatalf("run error did not explain incomplete coverage: %#v", run)
	}
}

func TestStoreCLIUpdatesProviderPartialPromptCountKeepsMatchedPositiveIncomplete(t *testing.T) {
	restore := replacePackageActionManagerAvailable(func(manager string) bool { return manager == managerStore })
	defer restore()
	scan := StoreScanGeneration{
		ScanID:           "scan-store-updates-partial-count",
		UserSID:          "S-1-5-21-store-updates",
		StartedAt:        time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC),
		CompletedAt:      time.Date(2026, 6, 21, 12, 0, 1, 0, time.UTC),
		CompletionStatus: StoreScanCompleted,
	}
	updatePFN := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	currentPFN := "Microsoft.WindowsCalculator_8wekyb3d8bbwe"
	inventory := testStoreInventory(scan, updatePFN, "1.2.13.0")
	current := testStoreInventory(scan, currentPFN, "11.0.0.0")
	inventory.Records = append(inventory.Records, current.Records...)
	inventory.Families = groupStorePackagedAppFamilies(inventory.Records)
	provider := storeCLIUpdatesCatalogProvider{
		Now: fixedPipelineTimes(scan.StartedAt, scan.StartedAt.Add(time.Second), scan.StartedAt.Add(2*time.Second)),
		Run: func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			return CommandResult{OK: true, Command: strings.Join(args, " "), Stdout: `Would you like to install the 2 Store update(s) now? [y/n] (y):
Product ID : 9N4D0MSMP0PT
PFN : Microsoft.VP9VideoExtensions_8wekyb3d8bbwe
Available Version : 1.2.20.0`}
		},
	}
	run := provider.Observe(context.Background(), scan, inventory.Families)
	if run.Health != StoreProviderIncomplete || len(run.Observations) != 1 || len(run.Mappings) != 1 {
		t.Fatalf("run=%#v", run)
	}
	got := run.Observations[0]
	if got.Identity.PackageFamilyName != updatePFN || got.Kind != StoreObservationPositiveUpdateOffer || got.Target == nil {
		t.Fatalf("matched exact positive should be retained: %#v", got)
	}
	if !strings.Contains(run.Error, "mentioned 2 update(s) but only 1 exact") {
		t.Fatalf("run error did not explain partial exact coverage: %#v", run)
	}
}

func TestStoreCLIUpdatesProviderRejectsUnstructuredUpdateHints(t *testing.T) {
	restore := replacePackageActionManagerAvailable(func(manager string) bool { return manager == managerStore })
	defer restore()
	scan := StoreScanGeneration{
		ScanID:           "scan-store-updates-unstructured",
		UserSID:          "S-1-5-21-store-updates",
		StartedAt:        time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC),
		CompletedAt:      time.Date(2026, 6, 21, 12, 0, 1, 0, time.UTC),
		CompletionStatus: StoreScanCompleted,
	}
	provider := storeCLIUpdatesCatalogProvider{
		Run: func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			return CommandResult{OK: true, Command: strings.Join(args, " "), Stdout: "Would you like to install the 2 Store update(s) now? [y/n] (y):"}
		},
	}
	run := provider.Observe(context.Background(), scan, testStoreInventory(scan, "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe", "1.2.13.0").Families)
	if run.Health != StoreProviderIncomplete || len(run.Observations) != 0 {
		t.Fatalf("run=%#v", run)
	}
}

func TestStoreCLIUpdatesProviderContradictoryNoUpdatesKeepsExactPositiveIncomplete(t *testing.T) {
	restore := replacePackageActionManagerAvailable(func(manager string) bool { return manager == managerStore })
	defer restore()
	scan := StoreScanGeneration{
		ScanID:           "scan-store-updates-contradictory",
		UserSID:          "S-1-5-21-store-updates",
		StartedAt:        time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC),
		CompletedAt:      time.Date(2026, 6, 21, 12, 0, 1, 0, time.UTC),
		CompletionStatus: StoreScanCompleted,
	}
	updatePFN := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	currentPFN := "Microsoft.WindowsCalculator_8wekyb3d8bbwe"
	inventory := testStoreInventory(scan, updatePFN, "1.2.13.0")
	current := testStoreInventory(scan, currentPFN, "11.0.0.0")
	inventory.Records = append(inventory.Records, current.Records...)
	inventory.Families = groupStorePackagedAppFamilies(inventory.Records)
	provider := storeCLIUpdatesCatalogProvider{
		Now: fixedPipelineTimes(scan.StartedAt, scan.StartedAt.Add(time.Second), scan.StartedAt.Add(2*time.Second)),
		Run: func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			return CommandResult{OK: true, Command: strings.Join(args, " "), Stdout: `No updates found.

Update available
Product ID : 9N4D0MSMP0PT
PFN : Microsoft.VP9VideoExtensions_8wekyb3d8bbwe
Available Version : 1.2.20.0`}
		},
	}
	run := provider.Observe(context.Background(), scan, inventory.Families)
	if run.Health != StoreProviderIncomplete || len(run.Observations) != 1 || len(run.Mappings) != 1 {
		t.Fatalf("run=%#v", run)
	}
	got := run.Observations[0]
	if got.Identity.PackageFamilyName != updatePFN || got.Kind != StoreObservationPositiveUpdateOffer || got.Target == nil {
		t.Fatalf("matched exact positive should be retained: %#v", got)
	}
	if !strings.Contains(strings.ToLower(run.Error), "exact update offers") {
		t.Fatalf("run error should explain contradictory aggregate evidence: %#v", run)
	}
}

func TestStoreCLIExactProviderVP9AuthoritativeNegative(t *testing.T) {
	run := runVP9StoreCLIProviderFixture(t, "9N4D0MSMP0PT", "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe", "'VP9-Videoerweiterungen' is already up to date", nil)
	if got := run.Observations[0]; got.Kind != StoreObservationAuthoritativeNegative || got.Target != nil {
		t.Fatalf("negative observation=%#v", got)
	}
}

func TestStoreCLIExactProviderPromptOnlyOfferKeepsExactTarget(t *testing.T) {
	run := runVP9StoreCLIProviderFixture(t, "9N4D0MSMP0PT", "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe", "Would you like to apply the update? [y/n] (y):\nFailed to read input in non-interactive mode.", nil)
	got := run.Observations[0]
	if got.Kind != StoreObservationPositiveUpdateOffer || got.Target == nil {
		t.Fatalf("prompt-only offer did not become exact positive: %#v", got)
	}
	if got.Target.ProductID != "9N4D0MSMP0PT" || got.Target.UpdateID != "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe" || !got.Target.ExactFor(got.Identity) {
		t.Fatalf("prompt-only offer target not exact: %#v", got.Target)
	}
}

func TestStoreCLIExactProviderRejectsGenericMultiUpdatePrompt(t *testing.T) {
	run := runVP9StoreCLIProviderFixture(t, "9N4D0MSMP0PT", "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe", "Would you like to install the 2 Store update(s) now? [y/n] (y):", nil)
	got := run.Observations[0]
	if got.Kind != StoreObservationIncompleteResult || got.Target != nil || got.Health != StoreProviderIncomplete {
		t.Fatalf("generic multi-update prompt should not become PFN-specific positive evidence: %#v", got)
	}
	if !strings.Contains(got.Diagnostics, "mentioned 2 update(s)") {
		t.Fatalf("diagnostics should explain rejected prompt count: %#v", got)
	}
}

func TestStoreCLIExactProviderVP9ProviderFailure(t *testing.T) {
	run := runVP9StoreCLIProviderFixture(t, "9N4D0MSMP0PT", "Other.App_8wekyb3d8bbwe", "Update available for 'VP9-Videoerweiterungen'", nil)
	if got := run.Observations[0]; got.Kind != StoreObservationIncompleteResult || got.Health != StoreProviderIncomplete || got.Target != nil {
		t.Fatalf("failure observation=%#v", got)
	}
}

func TestStoreCLIExactProviderVP9InapplicableOffer(t *testing.T) {
	run := runVP9StoreCLIProviderFixture(t, "9N4D0MSMP0PT", "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe", "A newer version exists, but no applicable installer is available.", nil)
	if got := run.Observations[0]; got.Kind != StoreObservationNewerCatalogNoApplicableInstaller || got.Health != StoreProviderHealthy || got.Target != nil {
		t.Fatalf("inapplicable observation=%#v", got)
	}
}

func TestStoreCLIExactProviderRejectsMismatchedPFN(t *testing.T) {
	restore := replacePackageActionManagerAvailable(func(manager string) bool { return manager == managerStore })
	defer restore()
	scan := StoreScanGeneration{ScanID: "scan-mismatch", UserSID: "S-1-5-21-mismatch", StartedAt: time.Now().UTC(), CompletedAt: time.Now().UTC(), CompletionStatus: StoreScanCompleted}
	pfn := "OpenAI.Codex_abc123"
	provider := storeCLIExactCatalogProvider{
		Concurrency: 1,
		Run: func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			return CommandResult{OK: true, Command: strings.Join(args, " "), Stdout: "Product ID : 9NWRONG\nPFN : Other.App_abc123"}
		},
	}
	run := provider.Observe(context.Background(), scan, testStoreInventory(scan, pfn, "1.0.0").Families)
	if run.Health != StoreProviderHealthy || len(run.Observations) != 1 {
		t.Fatalf("run=%#v", run)
	}
	if got := run.Observations[0]; got.Kind != StoreObservationIncompleteResult || got.Health != StoreProviderIncomplete || got.Target != nil {
		t.Fatalf("mismatch observation=%#v", got)
	}
}

func TestStoreCLIExactCatalogQueryProvider(t *testing.T) {
	pfn := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	request := StoreExactUpdateRequest{Identity: StoreInstalledIdentity{UserSID: "S-1-5-21-vp9", PackageFamilyName: pfn}, UpdateID: pfn, ProductID: "9N4D0MSMP0PT"}
	provider := storeCLIExactCatalogQueryProvider{Provider: storeCLIExactCatalogProvider{
		Run: func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			command := strings.Join(args, " ")
			switch {
			case strings.Contains(command, " show "+pfn):
				return CommandResult{OK: true, Command: command, Stdout: "Product ID : 9N4D0MSMP0PT\nPFN : " + pfn}
			case strings.Contains(command, " update "+pfn):
				return CommandResult{OK: true, Command: command, Stdout: "'VP9-Videoerweiterungen' is already up to date"}
			default:
				return CommandResult{Command: command, Code: 1, Stderr: "unexpected command"}
			}
		},
	}}
	got, result := provider.QueryExact(context.Background(), request)
	if !result.OK || !got.Authoritative || got.OfferAvailable || !got.InstalledHealthy {
		t.Fatalf("catalog=%#v result=%#v", got, result)
	}
	if !strings.Contains(result.Command, "Store CLI exact catalog state check") {
		t.Fatalf("expected identity check and state check to be represented in command diagnostics, got %q", result.Command)
	}
}

func TestStoreCLIExactCatalogQueryProviderRejectsProductIDMismatch(t *testing.T) {
	pfn := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	request := StoreExactUpdateRequest{Identity: StoreInstalledIdentity{UserSID: "S-1-5-21-vp9", PackageFamilyName: pfn}, UpdateID: pfn, ProductID: "9N4D0MSMP0PT"}
	updateCalled := false
	provider := storeCLIExactCatalogQueryProvider{Provider: storeCLIExactCatalogProvider{
		Run: func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			command := strings.Join(args, " ")
			if strings.Contains(command, " update "+pfn) {
				updateCalled = true
				return CommandResult{OK: true, Command: command, Stdout: "'VP9-Videoerweiterungen' is already up to date"}
			}
			return CommandResult{OK: true, Command: command, Stdout: "Product ID : 9NWRONG\nPFN : " + pfn}
		},
	}}
	got, result := provider.QueryExact(context.Background(), request)
	if got.Authoritative || result.OK || !strings.Contains(result.Stderr, "Product ID") {
		t.Fatalf("expected non-authoritative Product ID mismatch, catalog=%#v result=%#v", got, result)
	}
	if updateCalled {
		t.Fatal("Store CLI update check should not run after Product ID mismatch")
	}
}

func runVP9StoreCLIProviderFixture(t *testing.T, productID, showPFN, updateOutput string, runErr error) StoreCatalogProviderRun {
	t.Helper()
	restore := replacePackageActionManagerAvailable(func(manager string) bool { return manager == managerStore })
	defer restore()
	scan := StoreScanGeneration{
		ScanID:           "scan-vp9",
		UserSID:          "S-1-5-21-vp9",
		StartedAt:        time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC),
		CompletedAt:      time.Date(2026, 6, 21, 12, 0, 1, 0, time.UTC),
		CompletionStatus: StoreScanCompleted,
	}
	pfn := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	provider := storeCLIExactCatalogProvider{
		Concurrency: 1,
		Now:         fixedPipelineTimes(scan.StartedAt, scan.StartedAt.Add(time.Second), scan.StartedAt.Add(2*time.Second), scan.StartedAt.Add(3*time.Second)),
		Run: func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			command := strings.Join(args, " ")
			if runErr != nil {
				return CommandResult{Command: command, Code: 1, Stderr: runErr.Error()}
			}
			switch {
			case strings.Contains(command, " show "+pfn):
				return CommandResult{OK: true, Command: command, Stdout: "Product ID : " + productID + "\nPFN : " + showPFN}
			case strings.Contains(command, " update "+pfn):
				return CommandResult{OK: true, Command: command, Stdout: updateOutput}
			default:
				return CommandResult{Command: command, Code: 1, Stderr: "unexpected command"}
			}
		},
	}
	run := provider.Observe(context.Background(), scan, testStoreInventory(scan, pfn, "1.2.13.0").Families)
	if len(run.Observations) != 1 {
		t.Fatalf("run=%#v", run)
	}
	return run
}

func replacePackageActionManagerAvailable(fn func(string) bool) func() {
	old := packageActionManagerAvailable
	packageActionManagerAvailable = fn
	return func() { packageActionManagerAvailable = old }
}
