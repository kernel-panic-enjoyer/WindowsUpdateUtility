package updater

import "testing"

func TestSearchQueryVariantsNormalizePunctuation(t *testing.T) {
	cases := map[string][]string{
		"github-cli": {"github-cli", "github cli"},
		"GitHub.cli": {"GitHub.cli", "GitHub cli"},
		"gh":         {"gh"},
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
