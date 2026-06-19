package updater

import "testing"

func TestStorePackageKeysAreValid(t *testing.T) {
	for rank, manager := range managedPackageManagers {
		if !isManagedPackageManager(manager) {
			t.Fatalf("%q should be accepted by manager validation", manager)
		}
		if managerSortRank(manager) != rank {
			t.Fatalf("%q sort rank should be %d, got %d", manager, rank, managerSortRank(manager))
		}
	}
	if isManagedPackageManager("npm") {
		t.Fatal("unexpected manager accepted")
	}
	if managerValidationError().Error() != managerValidationMessage {
		t.Fatalf("unexpected manager validation message: %q", managerValidationError().Error())
	}

	manager, id, err := splitPackageKey("store:9NBLGGH5R558")
	if err != nil {
		t.Fatal(err)
	}
	if manager != "store" || id != "9NBLGGH5R558" {
		t.Fatalf("unexpected store package key split: %q %q", manager, id)
	}
	if err := validateManagerAndID("store", "9NBLGGH5R558"); err != nil {
		t.Fatalf("store manager should validate: %v", err)
	}
	if wingetSourceArg("store") != "msstore" {
		t.Fatal("store manager should use the msstore winget source")
	}
	if err := validateManagerAndID("store", "Microsoft To Do"); err != nil {
		t.Fatalf("store queries with spaces should validate: %v", err)
	}
	if err := validateManagerAndID("store", "bad&query"); err == nil {
		t.Fatal("store queries with shell metacharacters should be rejected")
	}
	if err := validateManagerAndID("store", "bad%query"); err == nil {
		t.Fatal("store queries with cmd expansion characters should be rejected")
	}
	if err := validateManagerAndID("winget", "Long Desktop App"); err != nil {
		t.Fatalf("winget positional package targets should validate: %v", err)
	}
	if err := validateManagerAndID("winget", "bad&target"); err == nil {
		t.Fatal("winget targets with shell metacharacters should be rejected")
	}
}

func TestPackageAutoUpdateEnabledUsesEquivalentStoreKey(t *testing.T) {
	state := State{
		AutoUpdatePackages: map[string]bool{
			"store:OpenAI.Codex_1.0.0.0_x64__abc123": true,
		},
	}
	pkg := Package{
		Key:     "store:OpenAI.Codex",
		Manager: "store",
		ID:      "OpenAI.Codex",
	}
	if !packageAutoUpdateEnabled(state, pkg) {
		t.Fatalf("expected equivalent Store auto-update key to be honored")
	}
}

func TestVersionGreater(t *testing.T) {
	cases := []struct {
		candidate string
		current   string
		want      bool
	}{
		{"1.1.0", "1.0.9", true},
		{"2026.11050.1001.0", "2026.11050.1001.0", false},
		{"1.0", "1.0.1", false},
		{"v2.0.0", "1.9.9", true},
		{"latest", "1.0.0", false},
	}
	for _, tc := range cases {
		if got := versionGreater(tc.candidate, tc.current); got != tc.want {
			t.Fatalf("versionGreater(%q, %q) = %t, want %t", tc.candidate, tc.current, got, tc.want)
		}
	}
}
