package updater

import (
	"strings"
	"testing"
)

func TestSearchQueryVariantsNormalizePunctuation(t *testing.T) {
	cases := map[string][]string{
		"github-cli":  {"github-cli", "github cli", "githubcli"},
		"build tools": {"build tools", "buildtools"},
		"GitHub.cli":  {"GitHub.cli", "GitHub cli", "GitHubcli"},
		"gh":          {"gh"},
	}
	for query, want := range cases {
		got := searchQueryVariants(query)
		if len(got) != len(want) {
			t.Fatalf("searchQueryVariants(%q) = %#v, want %#v", query, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("searchQueryVariants(%q) = %#v, want %#v", query, got, want)
			}
		}
	}
}

func TestSearchPackagesRejectsShellAndOptionQueries(t *testing.T) {
	for _, query := range []string{
		`codex" & echo injected`,
		"codex|calc",
		"%TEMP%",
		"--source msstore",
	} {
		if _, err := searchPackages(query); err == nil {
			t.Fatalf("expected unsafe search query %q to be rejected", query)
		}
	}
	if err := validatePackageSearchQuery("Visual Studio Code"); err != nil {
		t.Fatalf("normal search query should validate: %v", err)
	}
}

func TestCombineWingetSearchResultsKeepsVariantOutput(t *testing.T) {
	result := combineWingetSearchResults([]CommandResult{
		{OK: true, Command: "winget search build tools", Stdout: "Microsoft Build Tools 2015"},
		{OK: true, Command: "winget search buildtools", Stdout: "Visual Studio BuildTools 2022"},
	})
	if !result.OK || result.Code != 0 {
		t.Fatalf("combined successful variant searches should be successful: %#v", result)
	}
	if !strings.Contains(result.Command, "build tools") || !strings.Contains(result.Command, "buildtools") {
		t.Fatalf("combined command should preserve variant commands: %#v", result)
	}
	if !strings.Contains(result.Stdout, "Microsoft Build Tools 2015") || !strings.Contains(result.Stdout, "Visual Studio BuildTools 2022") {
		t.Fatalf("combined stdout should preserve variant output: %#v", result)
	}
}

func TestSortSearchPackagesPrioritizesExactIdentityBeforePrefixAndMoniker(t *testing.T) {
	packages := []Package{
		{Name: "Ghidra", ID: "NationalSecurityAgency.Ghidra", Manager: managerWinget},
		{Name: "Ghostscript", ID: "ArtifexSoftware.GhostScript", Manager: managerWinget},
		{Name: "ghx", ID: "ghx", Manager: managerWinget},
		{Name: "GitHub CLI", ID: "GitHub.cli", Match: "Moniker: gh", Manager: managerWinget},
		{Name: "gh", ID: "gh", Manager: managerChoco},
	}

	sortSearchPackages("gh", packages)

	if packages[0].ID != "gh" {
		t.Fatalf("expected exact package id before alias/prefix matches, got %#v", packages)
	}
	if packages[1].Name != "GitHub CLI" || packages[1].Match != "Moniker: gh" {
		t.Fatalf("expected exact moniker match before prefix matches, got %#v", packages)
	}
	if packages[2].ID != "ghx" {
		t.Fatalf("expected ghx prefix match after exact identity and exact moniker, got %#v", packages)
	}
}

func TestAnnotateSearchPackageAddsSourceBackendAndMatchReason(t *testing.T) {
	storeViaWinget := Package{
		Manager: managerStore,
		Source:  sourceMSStore,
		ID:      "9NKSQGP7F2NH",
		Name:    "Codex",
	}
	annotateSearchPackage("codex", &storeViaWinget)
	if storeViaWinget.ActionBackend != backendWingetMSStoreFallback {
		t.Fatalf("expected winget Store fallback backend, got %#v", storeViaWinget)
	}
	if storeViaWinget.MatchReason != "Exact package name match." {
		t.Fatalf("expected exact name match reason, got %#v", storeViaWinget)
	}

	choco := Package{Manager: managerChoco, ID: "gh", Name: "gh"}
	annotateSearchPackage("github-cli", &choco)
	if choco.Source != managerChoco {
		t.Fatalf("expected Chocolatey source, got %#v", choco)
	}
	if choco.MatchReason != "Returned by Chocolatey search for this query." {
		t.Fatalf("expected Chocolatey fallback match reason, got %#v", choco)
	}
}

func TestSearchMatchReasonUsesWingetMatchMetadata(t *testing.T) {
	pkg := Package{
		Manager: managerWinget,
		ID:      "GitHub.cli",
		Name:    "GitHub CLI",
		Match:   "Moniker: gh",
	}
	if got := searchMatchReason("gh", pkg); got != "Matched moniker gh." {
		t.Fatalf("expected winget moniker match reason, got %q", got)
	}
}

func TestRunPackageSearchesPreservesPartialFailureResults(t *testing.T) {
	oldRunners := packageSearchRunners
	defer func() { packageSearchRunners = oldRunners }()

	packageSearchRunners = []packageSearchRunner{
		{
			Manager: managerWinget,
			Run: func(query string) packageSearchResult {
				return packageSearchResult{
					ResultKey:     managerWinget,
					CommandResult: CommandResult{Command: "winget search gh", Code: 1, Stderr: "source failed"},
				}
			},
		},
		{
			Manager: managerChoco,
			Run: func(query string) packageSearchResult {
				return packageSearchResult{
					ResultKey: managerChoco,
					Packages:  []Package{{Manager: managerChoco, ID: "gh", Name: "gh", Version: "2.0.0"}},
					CommandResult: CommandResult{
						OK:      true,
						Command: "choco search gh",
						Stdout:  "gh|2.0.0",
					},
				}
			},
		},
	}

	results := runPackageSearches("gh", map[string]ManagerStatus{
		managerWinget: {Available: true},
		managerChoco:  {Available: true},
	})
	if len(results) != 2 {
		t.Fatalf("expected failed and successful search result, got %#v", results)
	}
	var sawWingetFailure, sawChocoPackage bool
	for _, result := range results {
		if result.ResultKey == managerWinget && !result.CommandResult.OK && result.CommandResult.Stderr == "source failed" {
			sawWingetFailure = true
		}
		if result.ResultKey == managerChoco && len(result.Packages) == 1 && result.Packages[0].ID == "gh" {
			sawChocoPackage = true
		}
	}
	if !sawWingetFailure || !sawChocoPackage {
		t.Fatalf("partial failure metadata was not preserved: %#v", results)
	}
}
