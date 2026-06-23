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

func TestParseStoreCLIUpdateCheckNegativePhrasesNeverBecomePositive(t *testing.T) {
	tests := []struct {
		phrase string
		want   StoreObservationKind
	}{
		{phrase: "No update available", want: StoreObservationAuthoritativeNegative},
		{phrase: "No updates available", want: StoreObservationAuthoritativeNegative},
		{phrase: "There is no update available", want: StoreObservationAuthoritativeNegative},
		{phrase: "No available update", want: StoreObservationAuthoritativeNegative},
		{phrase: "No updates found", want: StoreObservationAuthoritativeNegative},
		{phrase: "Already up to date", want: StoreObservationAuthoritativeNegative},
		{phrase: "No applicable update available", want: StoreObservationNewerCatalogNoApplicableInstaller},
	}
	for _, tc := range tests {
		t.Run(tc.phrase, func(t *testing.T) {
			got, err := parseStoreCLIUpdateCheck(tc.phrase)
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.phrase, err)
			}
			if got != tc.want {
				t.Fatalf("got=%s, want=%s", got, tc.want)
			}
			if got == StoreObservationPositiveUpdateOffer {
				t.Fatalf("%q must not create a positive update offer", tc.phrase)
			}
		})
	}
}

func TestParseStoreCLIUpdateCheckResultCommandFailures(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		result  CommandResult
		ctxErr  bool
		want    StoreObservationKind
		wantErr bool
	}{
		{
			name:   "positive plus allowed prompt failure",
			output: "Update available for 'VP9-Videoerweiterungen'\nFailed to read input in non-interactive mode.",
			result: CommandResult{OK: true},
			want:   StoreObservationPositiveUpdateOffer,
		},
		{
			name:    "positive plus access denied",
			output:  "Update available for 'VP9-Videoerweiterungen'\nError: access denied",
			result:  CommandResult{OK: true},
			want:    StoreObservationIncompleteResult,
			wantErr: true,
		},
		{
			name:    "positive plus generic error",
			output:  "Update available for 'VP9-Videoerweiterungen'\nError: another Store product could not be inspected",
			result:  CommandResult{OK: true},
			want:    StoreObservationIncompleteResult,
			wantErr: true,
		},
		{
			name:    "positive plus timeout code",
			output:  "Update available for 'VP9-Videoerweiterungen'",
			result:  CommandResult{Code: 124},
			want:    StoreObservationIncompleteResult,
			wantErr: true,
		},
		{
			name:    "positive plus cancellation code",
			output:  "Update available for 'VP9-Videoerweiterungen'",
			result:  CommandResult{Code: commandCancelledCode},
			want:    StoreObservationIncompleteResult,
			wantErr: true,
		},
		{
			name:    "positive before context deadline",
			output:  "Update available for 'VP9-Videoerweiterungen'",
			result:  CommandResult{OK: true},
			ctxErr:  true,
			want:    StoreObservationIncompleteResult,
			wantErr: true,
		},
		{
			name:    "negative plus prompt failure",
			output:  "No updates found.\nFailed to read input in non-interactive mode.",
			result:  CommandResult{OK: true},
			want:    StoreObservationIncompleteResult,
			wantErr: true,
		},
		{
			name:    "inapplicable plus fatal failure",
			output:  "A newer version exists, but no applicable installer is available.\nError: catalog unavailable",
			result:  CommandResult{OK: true},
			want:    StoreObservationIncompleteResult,
			wantErr: true,
		},
		{
			name:   "clean negative",
			output: "'VP9-Videoerweiterungen' is already up to date",
			result: CommandResult{OK: true},
			want:   StoreObservationAuthoritativeNegative,
		},
		{
			name:   "clean inapplicable",
			output: "A newer version exists, but no applicable installer is available.",
			result: CommandResult{OK: true},
			want:   StoreObservationNewerCatalogNoApplicableInstaller,
		},
		{
			name:    "nonzero unrecognized",
			output:  "Checking updates...",
			result:  CommandResult{Code: 1, Stderr: "exit status 1"},
			want:    StoreObservationIncompleteResult,
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.ctxErr {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			got, err := parseStoreCLIUpdateCheckResult(ctx, tc.output, tc.result)
			if got != tc.want || (err != nil) != tc.wantErr {
				t.Fatalf("got=%s err=%v, want=%s err=%v", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestParseStoreCLIUpdateCheckRequiresTrustworthyCommandOutcome(t *testing.T) {
	exactCommand := "store update Microsoft.VP9VideoExtensions_8wekyb3d8bbwe --apply false"
	tests := []struct {
		name    string
		output  string
		result  CommandResult
		ctxErr  bool
		want    StoreObservationKind
		wantErr bool
	}{
		{
			name:   "clean successful positive",
			output: "Update available for 'VP9-Videoerweiterungen'",
			result: CommandResult{OK: true, Command: exactCommand},
			want:   StoreObservationPositiveUpdateOffer,
		},
		{
			name:   "clean successful negative",
			output: "No update available",
			result: CommandResult{OK: true, Command: exactCommand},
			want:   StoreObservationAuthoritativeNegative,
		},
		{
			name:   "clean successful inapplicable",
			output: "No applicable installer is available.",
			result: CommandResult{OK: true, Command: exactCommand},
			want:   StoreObservationNewerCatalogNoApplicableInstaller,
		},
		{
			name:    "nonzero plus positive text",
			output:  "Update available",
			result:  CommandResult{Command: exactCommand, Code: 1, Stdout: "Update available"},
			want:    StoreObservationIncompleteResult,
			wantErr: true,
		},
		{
			name:    "nonzero plus negative text",
			output:  "No update available",
			result:  CommandResult{Command: exactCommand, Code: 1, Stdout: "No update available"},
			want:    StoreObservationIncompleteResult,
			wantErr: true,
		},
		{
			name:    "nonzero plus inapplicable text",
			output:  "No applicable installer is available.",
			result:  CommandResult{Command: exactCommand, Code: 1, Stdout: "No applicable installer is available."},
			want:    StoreObservationIncompleteResult,
			wantErr: true,
		},
		{
			name:    "ok true with nonzero code is inconsistent",
			output:  "Update available",
			result:  CommandResult{OK: true, Command: exactCommand, Code: 1, Stdout: "Update available"},
			want:    StoreObservationIncompleteResult,
			wantErr: true,
		},
		{
			name:    "positive plus standalone access denied",
			output:  "Update available\nAccess denied",
			result:  CommandResult{OK: true, Command: exactCommand},
			want:    StoreObservationIncompleteResult,
			wantErr: true,
		},
		{
			name:    "positive plus unauthorized",
			output:  "Update available\nUnauthorized",
			result:  CommandResult{OK: true, Command: exactCommand},
			want:    StoreObservationIncompleteResult,
			wantErr: true,
		},
		{
			name:    "positive plus exception hresult",
			output:  "Update available\nException HRESULT: 0x80070005",
			result:  CommandResult{OK: true, Command: exactCommand},
			want:    StoreObservationIncompleteResult,
			wantErr: true,
		},
		{
			name:    "positive plus package not found",
			output:  "Update available\nPackage not found",
			result:  CommandResult{OK: true, Command: exactCommand},
			want:    StoreObservationIncompleteResult,
			wantErr: true,
		},
		{
			name:    "positive plus product not found",
			output:  "Update available\nProduct not found",
			result:  CommandResult{OK: true, Command: exactCommand},
			want:    StoreObservationIncompleteResult,
			wantErr: true,
		},
		{
			name:    "positive plus generic error",
			output:  "Update available\nError: catalog failed",
			result:  CommandResult{OK: true, Command: exactCommand},
			want:    StoreObservationIncompleteResult,
			wantErr: true,
		},
		{
			name:    "positive plus timeout code",
			output:  "Update available",
			result:  CommandResult{Command: exactCommand, Code: 124, Stdout: "Update available"},
			want:    StoreObservationIncompleteResult,
			wantErr: true,
		},
		{
			name:    "positive plus cancellation code",
			output:  "Update available",
			result:  CommandResult{Command: exactCommand, Code: commandCancelledCode, Stdout: "Update available"},
			want:    StoreObservationIncompleteResult,
			wantErr: true,
		},
		{
			name:    "positive before context cancellation",
			output:  "Update available",
			result:  CommandResult{OK: true, Command: exactCommand},
			ctxErr:  true,
			want:    StoreObservationIncompleteResult,
			wantErr: true,
		},
		{
			name:   "allowed exact noninteractive prompt failure",
			output: "Update available for 'VP9-Videoerweiterungen'\nFailed to read input in non-interactive mode.",
			result: CommandResult{Command: exactCommand, Code: 1, Stdout: "Update available for 'VP9-Videoerweiterungen'\nFailed to read input in non-interactive mode."},
			want:   StoreObservationPositiveUpdateOffer,
		},
		{
			name:    "negative plus prompt failure",
			output:  "No update available\nFailed to read input in non-interactive mode.",
			result:  CommandResult{Command: exactCommand, Code: 1, Stdout: "No update available\nFailed to read input in non-interactive mode."},
			want:    StoreObservationIncompleteResult,
			wantErr: true,
		},
		{
			name:    "inapplicable plus prompt failure",
			output:  "No applicable installer is available.\nFailed to read input in non-interactive mode.",
			result:  CommandResult{Command: exactCommand, Code: 1, Stdout: "No applicable installer is available.\nFailed to read input in non-interactive mode."},
			want:    StoreObservationIncompleteResult,
			wantErr: true,
		},
		{
			name:    "prompt failure plus another fatal line",
			output:  "Update available\nFailed to read input in non-interactive mode.\nAccess is denied",
			result:  CommandResult{Command: exactCommand, Code: 1, Stdout: "Update available\nFailed to read input in non-interactive mode.\nAccess is denied"},
			want:    StoreObservationIncompleteResult,
			wantErr: true,
		},
		{
			name:    "generic prompt mentioning two updates",
			output:  "Would you like to install the 2 Store update(s) now? [y/n] (y):",
			result:  CommandResult{OK: true, Command: exactCommand},
			want:    StoreObservationIncompleteResult,
			wantErr: true,
		},
		{
			name:    "prompt failure without exact command context",
			output:  "Update available\nFailed to read input in non-interactive mode.",
			result:  CommandResult{Command: "store update Codex --apply false", Code: 1, Stdout: "Update available\nFailed to read input in non-interactive mode."},
			want:    StoreObservationIncompleteResult,
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.ctxErr {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			got, err := parseStoreCLIUpdateCheckResult(ctx, tc.output, tc.result)
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

func TestParseStoreCLIUpdatesOutputFieldOrderAndMalformedRecords(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		wantIDs   []string
		wantPFNs  []string
		wantErr   bool
		wantClean bool
	}{
		{
			name:     "product id then pfn",
			output:   "Product ID : 9N4D0MSMP0PT\nPFN : Microsoft.VP9VideoExtensions_8wekyb3d8bbwe\nAvailable Version : 1.2.20.0",
			wantIDs:  []string{"9N4D0MSMP0PT"},
			wantPFNs: []string{"Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"},
		},
		{
			name:     "pfn then product id",
			output:   "PFN : Microsoft.VP9VideoExtensions_8wekyb3d8bbwe\nProduct ID : 9N4D0MSMP0PT\nAvailable Version : 1.2.20.0",
			wantIDs:  []string{"9N4D0MSMP0PT"},
			wantPFNs: []string{"Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"},
		},
		{
			name:     "version before identity",
			output:   "Available Version : 1.2.20.0\nPFN : Microsoft.VP9VideoExtensions_8wekyb3d8bbwe\nProduct ID : 9N4D0MSMP0PT",
			wantIDs:  []string{"9N4D0MSMP0PT"},
			wantPFNs: []string{"Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"},
		},
		{
			name: "adjacent exact records",
			output: `Product ID : 9N4D0MSMP0PT
PFN : Microsoft.VP9VideoExtensions_8wekyb3d8bbwe
Product ID : 9NCALC
PFN : Microsoft.WindowsCalculator_8wekyb3d8bbwe`,
			wantIDs:  []string{"9N4D0MSMP0PT", "9NCALC"},
			wantPFNs: []string{"Microsoft.VP9VideoExtensions_8wekyb3d8bbwe", "Microsoft.WindowsCalculator_8wekyb3d8bbwe"},
		},
		{
			name: "adjacent pfn-first second record",
			output: `Product ID : 9N4D0MSMP0PT
PFN : Microsoft.VP9VideoExtensions_8wekyb3d8bbwe
PFN : Microsoft.WindowsCalculator_8wekyb3d8bbwe
Product ID : 9NCALC`,
			wantIDs:  []string{"9N4D0MSMP0PT", "9NCALC"},
			wantPFNs: []string{"Microsoft.VP9VideoExtensions_8wekyb3d8bbwe", "Microsoft.WindowsCalculator_8wekyb3d8bbwe"},
		},
		{
			name: "pfn-first then product-id-first adjacent records",
			output: `PFN : Microsoft.VP9VideoExtensions_8wekyb3d8bbwe
Product ID : 9N4D0MSMP0PT
Product ID : 9NCALC
PFN : Microsoft.WindowsCalculator_8wekyb3d8bbwe`,
			wantIDs:  []string{"9N4D0MSMP0PT", "9NCALC"},
			wantPFNs: []string{"Microsoft.VP9VideoExtensions_8wekyb3d8bbwe", "Microsoft.WindowsCalculator_8wekyb3d8bbwe"},
		},
		{
			name: "adjacent record beginning with update id",
			output: `Product ID : 9N4D0MSMP0PT
PFN : Microsoft.VP9VideoExtensions_8wekyb3d8bbwe
Update ID : Microsoft.WindowsCalculator_8wekyb3d8bbwe
Product ID : 9NCALC`,
			wantIDs:  []string{"9N4D0MSMP0PT", "9NCALC"},
			wantPFNs: []string{"Microsoft.VP9VideoExtensions_8wekyb3d8bbwe", "Microsoft.WindowsCalculator_8wekyb3d8bbwe"},
		},
		{
			name: "three adjacent records mixed identity order without blanks",
			output: `Product ID : 9N4D0MSMP0PT
PFN : Microsoft.VP9VideoExtensions_8wekyb3d8bbwe
PFN : Microsoft.WindowsCalculator_8wekyb3d8bbwe
Product ID : 9NCALC
Product ID : 9NPAINT
Update ID : Microsoft.Paint_8wekyb3d8bbwe`,
			wantIDs:  []string{"9N4D0MSMP0PT", "9NCALC", "9NPAINT"},
			wantPFNs: []string{"Microsoft.VP9VideoExtensions_8wekyb3d8bbwe", "Microsoft.WindowsCalculator_8wekyb3d8bbwe", "Microsoft.Paint_8wekyb3d8bbwe"},
		},
		{
			name: "duplicate identical identity fields",
			output: `Product ID : 9N4D0MSMP0PT
Product ID : 9N4D0MSMP0PT
PFN : Microsoft.VP9VideoExtensions_8wekyb3d8bbwe
PFN : Microsoft.VP9VideoExtensions_8wekyb3d8bbwe`,
			wantIDs:  []string{"9N4D0MSMP0PT"},
			wantPFNs: []string{"Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"},
		},
		{
			name: "duplicate conflicting pfn",
			output: `Product ID : 9N4D0MSMP0PT
PFN : Microsoft.VP9VideoExtensions_8wekyb3d8bbwe
PFN : Other.App_8wekyb3d8bbwe`,
			wantIDs:  []string{"9N4D0MSMP0PT"},
			wantPFNs: []string{"Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"},
			wantErr:  true,
		},
		{
			name: "duplicate conflicting product id",
			output: `Product ID : 9N4D0MSMP0PT
Product ID : 9NWRONG
PFN : Microsoft.VP9VideoExtensions_8wekyb3d8bbwe`,
			wantErr: true,
		},
		{
			name: "conflicting product id after complete record retained as partial next record",
			output: `Product ID : 9N4D0MSMP0PT
PFN : Microsoft.VP9VideoExtensions_8wekyb3d8bbwe
Product ID : 9NPARTIAL`,
			wantIDs:  []string{"9N4D0MSMP0PT"},
			wantPFNs: []string{"Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"},
			wantErr:  true,
		},
		{
			name: "partial first then valid second",
			output: `Product ID : 9NPARTIAL

Product ID : 9N4D0MSMP0PT
PFN : Microsoft.VP9VideoExtensions_8wekyb3d8bbwe`,
			wantIDs:  []string{"9N4D0MSMP0PT"},
			wantPFNs: []string{"Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"},
			wantErr:  true,
		},
		{
			name: "valid first then malformed second",
			output: `Product ID : 9N4D0MSMP0PT
PFN : Microsoft.VP9VideoExtensions_8wekyb3d8bbwe

Product ID : 9NMALFORMED`,
			wantIDs:  []string{"9N4D0MSMP0PT"},
			wantPFNs: []string{"Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"},
			wantErr:  true,
		},
		{
			name:     "mixed line endings and noise",
			output:   "Checking for updates...\r\n\r\nUpdate available\r\nPFN : Microsoft.VP9VideoExtensions_8wekyb3d8bbwe\r\nProduct ID : 9N4D0MSMP0PT\r\n",
			wantIDs:  []string{"9N4D0MSMP0PT"},
			wantPFNs: []string{"Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseStoreCLIUpdatesOutput(tc.output)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v, wantErr=%v result=%#v", err, tc.wantErr, got)
			}
			if len(got.Offers) != len(tc.wantIDs) {
				t.Fatalf("offers=%#v, want IDs %#v", got.Offers, tc.wantIDs)
			}
			for i := range tc.wantIDs {
				if got.Offers[i].ProductID != tc.wantIDs[i] || got.Offers[i].PFN != tc.wantPFNs[i] {
					t.Fatalf("offer[%d]=%#v, want id=%s pfn=%s", i, got.Offers[i], tc.wantIDs[i], tc.wantPFNs[i])
				}
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

func TestStoreCLIExactProviderPreservesCompletedPositiveOnTimeout(t *testing.T) {
	restore := replacePackageActionManagerAvailable(func(manager string) bool { return manager == managerStore })
	defer restore()
	scan := StoreScanGeneration{
		ScanID:           "scan-exact-timeout",
		UserSID:          "S-1-5-21-timeout",
		StartedAt:        time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC),
		CompletedAt:      time.Date(2026, 6, 22, 10, 0, 1, 0, time.UTC),
		CompletionStatus: StoreScanIncomplete,
	}
	vp9 := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	blocked := "Vendor.Broken_abc123"
	inventory := testStoreInventory(scan, vp9, "1.2.13.0")
	brokenInventory := testStoreInventory(scan, blocked, "1.0.0")
	inventory.Records = append(inventory.Records, brokenInventory.Records...)
	inventory.Families = groupStorePackagedAppFamilies(inventory.Records)

	provider := storeCLIExactCatalogProvider{
		Concurrency: 1,
		Now:         fixedPipelineTimes(scan.StartedAt, scan.StartedAt.Add(time.Second), scan.StartedAt.Add(2*time.Second), scan.StartedAt.Add(3*time.Second), scan.StartedAt.Add(4*time.Second)),
		Run: func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			command := strings.Join(args, " ")
			switch {
			case strings.Contains(command, " show "+vp9):
				return CommandResult{OK: true, Command: command, Stdout: "Product ID : 9N4D0MSMP0PT\nPFN : " + vp9}
			case strings.Contains(command, " update "+vp9):
				return CommandResult{OK: true, Command: command, Stdout: "Update available for 'VP9-Videoerweiterungen'\nFailed to read input in non-interactive mode."}
			case strings.Contains(command, " show "+blocked):
				<-ctx.Done()
				return CommandResult{Command: command, Code: 124, Stderr: ctx.Err().Error()}
			default:
				return CommandResult{Command: command, Code: 1, Stderr: "unexpected command"}
			}
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	done := make(chan StoreCatalogProviderRun, 1)
	go func() {
		done <- provider.Observe(ctx, scan, inventory.Families)
	}()
	var run StoreCatalogProviderRun
	select {
	case run = <-done:
	case <-time.After(time.Second):
		t.Fatal("exact provider deadlocked after context deadline")
	}
	if run.Health != StoreProviderIncomplete || len(run.Mappings) != 1 {
		t.Fatalf("run=%#v", run)
	}
	byPFN := map[string]StoreProviderObservation{}
	for _, observation := range run.Observations {
		if _, exists := byPFN[observation.Identity.PackageFamilyName]; exists {
			t.Fatalf("duplicate observation for PFN %s: %#v", observation.Identity.PackageFamilyName, run.Observations)
		}
		byPFN[observation.Identity.PackageFamilyName] = observation
	}
	if got := byPFN[vp9]; got.Kind != StoreObservationPositiveUpdateOffer || got.Target == nil || got.Target.ProductID != "9N4D0MSMP0PT" {
		t.Fatalf("completed VP9 positive was not preserved: %#v", got)
	}
	if got := byPFN[blocked]; got.Kind != StoreObservationIncompleteResult || got.Health != StoreProviderIncomplete {
		t.Fatalf("blocked PFN should be incomplete: %#v", got)
	}
	available := ReconcileStoreUpdate(StoreReconciliationInput{
		Identity:          StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: vp9},
		Scan:              scan,
		RequiredProviders: []StoreProviderIdentity{run.Provider},
		Observations:      run.Observations,
	})
	if available.State != StoreUpdateAvailable {
		t.Fatalf("VP9 should reconcile to available, got %#v", available)
	}
	unknown := ReconcileStoreUpdate(StoreReconciliationInput{
		Identity:          StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: blocked},
		Scan:              scan,
		RequiredProviders: []StoreProviderIdentity{run.Provider},
		Observations:      run.Observations,
	})
	if unknown.State != StoreUpdateUnknown {
		t.Fatalf("blocked package should reconcile to unknown, got %#v", unknown)
	}
}

func TestStoreCLIExactProviderCancellationBeforeAnyPackageCompletes(t *testing.T) {
	restore := replacePackageActionManagerAvailable(func(manager string) bool { return manager == managerStore })
	defer restore()
	scan := StoreScanGeneration{ScanID: "scan-cancel-before", UserSID: "S-1-5-21-cancel", StartedAt: time.Now().UTC(), CompletedAt: time.Now().UTC(), CompletionStatus: StoreScanIncomplete}
	inventory := testStoreInventory(scan, "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe", "1.2.13.0")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	provider := storeCLIExactCatalogProvider{Concurrency: 1, Run: func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
		t.Fatal("no Store CLI command should run after pre-cancelled context")
		return CommandResult{}
	}}
	run := provider.Observe(ctx, scan, inventory.Families)
	if run.Health != StoreProviderIncomplete || len(run.Observations) != 1 || run.Observations[0].Kind != StoreObservationIncompleteResult {
		t.Fatalf("run=%#v", run)
	}
}

func TestStoreCLIExactProviderCancellationAfterAllPackagesComplete(t *testing.T) {
	restore := replacePackageActionManagerAvailable(func(manager string) bool { return manager == managerStore })
	defer restore()
	scan := StoreScanGeneration{ScanID: "scan-cancel-after", UserSID: "S-1-5-21-cancel", StartedAt: time.Now().UTC(), CompletedAt: time.Now().UTC(), CompletionStatus: StoreScanIncomplete}
	vp9 := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	calc := "Microsoft.WindowsCalculator_8wekyb3d8bbwe"
	inventory := testStoreInventory(scan, vp9, "1.2.20.0")
	calcInventory := testStoreInventory(scan, calc, "11.0.0.0")
	inventory.Records = append(inventory.Records, calcInventory.Records...)
	inventory.Families = groupStorePackagedAppFamilies(inventory.Records)
	ctx, cancel := context.WithCancel(context.Background())
	updateChecks := 0
	provider := storeCLIExactCatalogProvider{
		Concurrency: 1,
		Run: func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			command := strings.Join(args, " ")
			switch {
			case strings.Contains(command, " show "+vp9):
				return CommandResult{OK: true, Command: command, Stdout: "Product ID : 9N4D0MSMP0PT\nPFN : " + vp9}
			case strings.Contains(command, " update "+vp9):
				updateChecks++
				return CommandResult{OK: true, Command: command, Stdout: "'VP9-Videoerweiterungen' is already up to date"}
			case strings.Contains(command, " show "+calc):
				return CommandResult{OK: true, Command: command, Stdout: "Product ID : 9NCALC\nPFN : " + calc}
			case strings.Contains(command, " update "+calc):
				updateChecks++
				return CommandResult{OK: true, Command: command, Stdout: "'Calculator' is already up to date"}
			default:
				return CommandResult{Command: command, Code: 1, Stderr: "unexpected command"}
			}
		},
	}
	run := provider.Observe(ctx, scan, inventory.Families)
	cancel()
	if updateChecks != 2 || len(run.Observations) != 2 || len(run.Mappings) != 2 {
		t.Fatalf("completed results not preserved after late cancellation: checks=%d run=%#v", updateChecks, run)
	}
	for _, observation := range run.Observations {
		if observation.Kind != StoreObservationAuthoritativeNegative {
			t.Fatalf("all packages completed before cancellation; observation=%#v run=%#v", observation, run)
		}
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

func TestStoreCLIUpdatesProviderAdjacentPFNFirstOffers(t *testing.T) {
	restore := replacePackageActionManagerAvailable(func(manager string) bool { return manager == managerStore })
	defer restore()
	scan := StoreScanGeneration{
		ScanID:           "scan-store-updates-adjacent-pfn-first",
		UserSID:          "S-1-5-21-store-updates",
		StartedAt:        time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC),
		CompletedAt:      time.Date(2026, 6, 21, 12, 0, 1, 0, time.UTC),
		CompletionStatus: StoreScanCompleted,
	}
	vp9PFN := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	calcPFN := "Microsoft.WindowsCalculator_8wekyb3d8bbwe"
	inventory := testStoreInventory(scan, vp9PFN, "1.2.13.0")
	calc := testStoreInventory(scan, calcPFN, "11.0.0.0")
	inventory.Records = append(inventory.Records, calc.Records...)
	inventory.Families = groupStorePackagedAppFamilies(inventory.Records)
	provider := storeCLIUpdatesCatalogProvider{
		Now: fixedPipelineTimes(scan.StartedAt, scan.StartedAt.Add(time.Second), scan.StartedAt.Add(2*time.Second), scan.StartedAt.Add(3*time.Second)),
		Run: func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			return CommandResult{OK: true, Command: strings.Join(args, " "), Stdout: `Product ID : 9N4D0MSMP0PT
PFN : Microsoft.VP9VideoExtensions_8wekyb3d8bbwe
PFN : Microsoft.WindowsCalculator_8wekyb3d8bbwe
Product ID : 9NCALC`}
		},
	}
	run := provider.Observe(context.Background(), scan, inventory.Families)
	if run.Health != StoreProviderHealthy || len(run.Observations) != 2 || len(run.Mappings) != 2 {
		t.Fatalf("run=%#v", run)
	}
	byPFN := map[string]StoreProviderObservation{}
	for _, observation := range run.Observations {
		byPFN[observation.Identity.PackageFamilyName] = observation
	}
	for pfn, productID := range map[string]string{vp9PFN: "9N4D0MSMP0PT", calcPFN: "9NCALC"} {
		got := byPFN[pfn]
		if got.Kind != StoreObservationPositiveUpdateOffer || got.Target == nil || got.Target.ProductID != productID {
			t.Fatalf("offer for %s = %#v", pfn, got)
		}
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

func TestStoreCLIUpdatesProviderExactOfferWithFatalErrorKeepsPositiveWithoutNegatives(t *testing.T) {
	run := runStoreCLIUpdatesProviderTwoPFNs(t, CommandResult{OK: true, Stdout: `Update available
Product ID : 9N4D0MSMP0PT
PFN : Microsoft.VP9VideoExtensions_8wekyb3d8bbwe
Available Version : 1.2.20.0
Error: another Store product could not be inspected`})
	assertAggregatePositiveIncompleteNoNegatives(t, run)
}

func TestStoreCLIUpdatesProviderExactOfferWithTimeoutKeepsPositiveWithoutNegatives(t *testing.T) {
	run := runStoreCLIUpdatesProviderTwoPFNs(t, CommandResult{Code: 124, Stdout: `Update available
Product ID : 9N4D0MSMP0PT
PFN : Microsoft.VP9VideoExtensions_8wekyb3d8bbwe
Available Version : 1.2.20.0`, Stderr: "timeout"})
	assertAggregatePositiveIncompleteNoNegatives(t, run)
}

func TestStoreCLIUpdatesProviderExactOfferWithCancellationKeepsPositiveWithoutNegatives(t *testing.T) {
	run := runStoreCLIUpdatesProviderTwoPFNs(t, CommandResult{Code: commandCancelledCode, Stdout: `Update available
Product ID : 9N4D0MSMP0PT
PFN : Microsoft.VP9VideoExtensions_8wekyb3d8bbwe
Available Version : 1.2.20.0`, Stderr: "cancelled"})
	assertAggregatePositiveIncompleteNoNegatives(t, run)
}

func TestStoreCLIUpdatesProviderNoUpdatesWithFatalDiagnosticProducesNoNegatives(t *testing.T) {
	run := runStoreCLIUpdatesProviderTwoPFNs(t, CommandResult{OK: true, Stdout: "No updates found.\nError: Store catalog failed"})
	if run.Health != StoreProviderIncomplete || len(run.Observations) != 0 {
		t.Fatalf("failure-tainted no-updates output must not produce negatives: %#v", run)
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

func runStoreCLIUpdatesProviderTwoPFNs(t *testing.T, result CommandResult) StoreCatalogProviderRun {
	t.Helper()
	restore := replacePackageActionManagerAvailable(func(manager string) bool { return manager == managerStore })
	defer restore()
	scan := StoreScanGeneration{
		ScanID:           "scan-store-updates-two-pfns",
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
			result.Command = strings.Join(args, " ")
			return result
		},
	}
	return provider.Observe(context.Background(), scan, inventory.Families)
}

func assertAggregatePositiveIncompleteNoNegatives(t *testing.T, run StoreCatalogProviderRun) {
	t.Helper()
	if run.Health != StoreProviderIncomplete || len(run.Observations) != 1 || len(run.Mappings) != 1 {
		t.Fatalf("run=%#v", run)
	}
	got := run.Observations[0]
	if got.Identity.PackageFamilyName != "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe" || got.Kind != StoreObservationPositiveUpdateOffer || got.Target == nil {
		t.Fatalf("expected only retained exact VP9 positive, got %#v", got)
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

func TestStoreCLIExactProviderNoUpdateAvailableIsNegative(t *testing.T) {
	run := runVP9StoreCLIProviderFixture(t, "9N4D0MSMP0PT", "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe", "No update available", nil)
	if len(run.Mappings) != 1 {
		t.Fatalf("verified PFN/Product ID mapping should be retained: %#v", run)
	}
	got := run.Observations[0]
	if got.Kind != StoreObservationAuthoritativeNegative || got.Target != nil {
		t.Fatalf("No update available must not create a target: %#v", got)
	}
	assessment := ReconcileStoreUpdate(StoreReconciliationInput{
		Identity:          got.Identity,
		Scan:              StoreScanGeneration{ScanID: got.ScanID, UserSID: got.Identity.UserSID, StartedAt: run.StartedAt, CompletedAt: run.CompletedAt, CompletionStatus: StoreScanCompleted},
		RequiredProviders: []StoreProviderIdentity{run.Provider},
		Observations:      run.Observations,
	})
	if assessment.State == StoreUpdateAvailable || assessment.Target != nil {
		t.Fatalf("negative exact evidence must not reconcile to available: %#v", assessment)
	}
}

func TestStoreCLIExactProviderNonzeroPositiveIsIncomplete(t *testing.T) {
	restore := replacePackageActionManagerAvailable(func(manager string) bool { return manager == managerStore })
	defer restore()
	scan := StoreScanGeneration{
		ScanID:           "scan-vp9-nonzero-positive",
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
				return CommandResult{Command: command, Code: 1, Stdout: "Update available"}
			default:
				return CommandResult{Command: command, Code: 1, Stderr: "unexpected command"}
			}
		},
	}
	run := provider.Observe(context.Background(), scan, testStoreInventory(scan, pfn, "1.2.13.0").Families)
	if len(run.Mappings) != 1 {
		t.Fatalf("mapping should be retained after incomplete update-state command: %#v", run)
	}
	got := run.Observations[0]
	if got.Kind != StoreObservationIncompleteResult || got.Target != nil || got.Health != StoreProviderIncomplete {
		t.Fatalf("nonzero positive output must be incomplete without target: %#v", got)
	}
	assessment := ReconcileStoreUpdate(StoreReconciliationInput{
		Identity:          got.Identity,
		Scan:              scan,
		RequiredProviders: []StoreProviderIdentity{run.Provider},
		Observations:      run.Observations,
	})
	if assessment.State != StoreUpdateUnknown {
		t.Fatalf("incomplete exact evidence must reconcile to unknown, got %#v", assessment)
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

func TestStoreCLIExactCatalogQueryProviderNoUpdateAvailableReportsNoOffer(t *testing.T) {
	pfn := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	request := StoreExactUpdateRequest{Identity: StoreInstalledIdentity{UserSID: "S-1-5-21-vp9", PackageFamilyName: pfn}, UpdateID: pfn, ProductID: "9N4D0MSMP0PT"}
	provider := storeCLIExactCatalogQueryProvider{Provider: storeCLIExactCatalogProvider{
		Run: func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			command := strings.Join(args, " ")
			switch {
			case strings.Contains(command, " show "+pfn):
				return CommandResult{OK: true, Command: command, Stdout: "Product ID : 9N4D0MSMP0PT\nPFN : " + pfn}
			case strings.Contains(command, " update "+pfn):
				return CommandResult{OK: true, Command: command, Stdout: "No update available"}
			default:
				return CommandResult{Command: command, Code: 1, Stderr: "unexpected command"}
			}
		},
	}}
	got, result := provider.QueryExact(context.Background(), request)
	if !result.OK || !got.Authoritative || got.OfferAvailable {
		t.Fatalf("No update available should be authoritative no-offer, catalog=%#v result=%#v", got, result)
	}
}

func TestStoreCLIExactCatalogQueryProviderNonzeroPositiveIsNotAuthoritative(t *testing.T) {
	pfn := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	request := StoreExactUpdateRequest{Identity: StoreInstalledIdentity{UserSID: "S-1-5-21-vp9", PackageFamilyName: pfn}, UpdateID: pfn, ProductID: "9N4D0MSMP0PT"}
	provider := storeCLIExactCatalogQueryProvider{Provider: storeCLIExactCatalogProvider{
		Run: func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			command := strings.Join(args, " ")
			switch {
			case strings.Contains(command, " show "+pfn):
				return CommandResult{OK: true, Command: command, Stdout: "Product ID : 9N4D0MSMP0PT\nPFN : " + pfn}
			case strings.Contains(command, " update "+pfn):
				return CommandResult{Command: command, Code: 1, Stdout: "Update available"}
			default:
				return CommandResult{Command: command, Code: 1, Stderr: "unexpected command"}
			}
		},
	}}
	got, result := provider.QueryExact(context.Background(), request)
	if got.Authoritative || got.OfferAvailable || result.OK {
		t.Fatalf("nonzero positive output must not be authoritative, catalog=%#v result=%#v", got, result)
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
