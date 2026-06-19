package updater

import (
	"strings"
	"testing"
	"time"
)

func TestResolveStoreAppxPackagesMapsCodex(t *testing.T) {
	state := defaultState()
	appx := []Package{{
		ID:              "OpenAI.Codex_1.0.0.0_x64__abc123",
		Name:            "Codex",
		Version:         "1.0.0.0",
		Manager:         "store",
		Source:          "appx",
		UpdateSupported: false,
		ActionBackend:   "appx-inventory",
	}}

	calls := 0
	got, results, changed := resolveStoreAppxPackages(&state, appx, true, func(query string) ([]Package, CommandResult) {
		calls++
		if query != "Codex" {
			t.Fatalf("unexpected query %q", query)
		}
		return []Package{{Name: "Codex", ID: "OpenAI.Codex", Manager: "store"}}, CommandResult{OK: true, Command: "store search Codex"}
	})

	if !changed || calls != 1 || len(results) != 1 {
		t.Fatalf("expected one resolver search and cache change, calls=%d changed=%t results=%#v", calls, changed, results)
	}
	if got[0].ID != "OpenAI.Codex" || !got[0].UpdateSupported || got[0].ActionBackend != "store-cli-resolved" {
		t.Fatalf("expected resolved Store CLI target, got %#v", got[0])
	}
	entry := state.StoreResolveCache[strings.ToLower("OpenAI.Codex_1.0.0.0_x64__abc123")]
	if !entry.Resolved || entry.StoreID != "OpenAI.Codex" || entry.AppXVersion != "1.0.0.0" {
		t.Fatalf("unexpected resolver cache entry: %#v", entry)
	}
}

func TestResolveStoreAppxPackagesMarksUpdateFromStoreVersion(t *testing.T) {
	state := defaultState()
	appx := []Package{{
		ID:              "OpenAI.Codex_1.0.0.0_x64__abc123",
		Name:            "Codex",
		Version:         "1.0.0.0",
		Manager:         "store",
		Source:          "appx",
		UpdateSupported: false,
		ActionBackend:   "appx-inventory",
	}}

	got, _, changed := resolveStoreAppxPackages(&state, appx, true, func(query string) ([]Package, CommandResult) {
		return []Package{{Name: "Codex", ID: "OpenAI.Codex", Version: "1.1.0", Manager: "store"}}, CommandResult{OK: true}
	})

	if !changed {
		t.Fatal("expected resolver cache change")
	}
	if !got[0].UpdateAvailable || got[0].AvailableVersion != "1.1.0" {
		t.Fatalf("expected Store search version to mark update available, got %#v", got[0])
	}
	entry := state.StoreResolveCache[strings.ToLower("OpenAI.Codex_1.0.0.0_x64__abc123")]
	if entry.StoreVersion != "1.1.0" {
		t.Fatalf("expected Store version in cache, got %#v", entry)
	}
}

func TestResolveStoreAppxPackagesKeepsCurrentWhenStoreVersionMatches(t *testing.T) {
	state := defaultState()
	appx := []Package{{
		ID:              "OpenAI.Codex_1.0.0.0_x64__abc123",
		Name:            "Codex",
		Version:         "1.0.0.0",
		Manager:         "store",
		Source:          "appx",
		UpdateSupported: false,
		ActionBackend:   "appx-inventory",
	}}

	got, _, _ := resolveStoreAppxPackages(&state, appx, true, func(query string) ([]Package, CommandResult) {
		return []Package{{Name: "Codex", ID: "OpenAI.Codex", Version: "1.0.0.0", Manager: "store"}}, CommandResult{OK: true}
	})

	if got[0].UpdateAvailable || got[0].AvailableVersion != "" {
		t.Fatalf("matching Store version should stay current, got %#v", got[0])
	}
}

func TestResolveStoreAppxPackagesKeepsMismatchInventoryOnly(t *testing.T) {
	state := defaultState()
	appx := []Package{{
		ID:              "OpenAI.Codex_1.0.0.0_x64__abc123",
		Name:            "Codex",
		Version:         "1.0.0.0",
		Manager:         "store",
		Source:          "appx",
		UpdateSupported: false,
		ActionBackend:   "appx-inventory",
	}}

	got, _, changed := resolveStoreAppxPackages(&state, appx, true, func(query string) ([]Package, CommandResult) {
		return []Package{{Name: "Notepad", ID: "Microsoft.WindowsNotepad", Manager: "store"}}, CommandResult{OK: true}
	})

	if !changed {
		t.Fatal("expected unresolved lookup to be cached")
	}
	if got[0].UpdateSupported || got[0].ActionBackend != "appx-inventory" {
		t.Fatalf("mismatch should stay inventory-only, got %#v", got[0])
	}
	entry := state.StoreResolveCache[strings.ToLower("OpenAI.Codex_1.0.0.0_x64__abc123")]
	if entry.Resolved {
		t.Fatalf("mismatch should cache unresolved entry, got %#v", entry)
	}
}

func TestResolveStoreAppxPackagesRejectsGenericContainedStoreResult(t *testing.T) {
	appx := Package{
		ID:              "Microsoft.Example.WindowsAppHelper_1.0.0.0_x64__abc123",
		Name:            "Windows App Runtime Singleton",
		Version:         "1.0.0.0",
		Manager:         "store",
		Source:          "appx",
		UpdateSupported: false,
		ActionBackend:   "appx-inventory",
	}
	if match, ok := chooseStoreResolution(appx, []Package{{Name: "Windows App", ID: "9MVJJ5Q28CJ2", Manager: "store"}}); ok {
		t.Fatalf("generic contained Store result should not resolve, got %#v", match)
	}
}

func TestResolveStoreAppxPackagesRejectsGenericCachedContainedStoreResult(t *testing.T) {
	state := defaultState()
	appxID := "MicrosoftCorporationII.WinAppRuntime.Singleton_8002.1.3.0_x64__8wekyb3d8bbwe"
	state.StoreResolveCache[strings.ToLower(appxID)] = StoreResolveCacheEntry{
		AppXVersion: "8002.1.3.0",
		StoreID:     "9MVJJ5Q28CJ2",
		StoreName:   "Windows App",
		Resolved:    true,
		ResolvedAt:  utcNow(),
	}
	appx := []Package{{
		ID:              appxID,
		Name:            "Windows App Runtime Singleton",
		Version:         "8002.1.3.0",
		Manager:         "store",
		Source:          "appx",
		UpdateSupported: false,
		ActionBackend:   "appx-inventory",
	}}

	calls := 0
	got, _, changed := resolveStoreAppxPackages(&state, appx, true, func(query string) ([]Package, CommandResult) {
		calls++
		return []Package{{Name: "Windows App", ID: "9MVJJ5Q28CJ2", Manager: "store"}}, CommandResult{OK: true}
	})

	if calls != 1 || !changed {
		t.Fatalf("expected stale generic cache to be discarded and refreshed, calls=%d changed=%t", calls, changed)
	}
	if got[0].UpdateSupported || got[0].ActionBackend != backendAppXInventory {
		t.Fatalf("generic cached Store result should not resolve, got %#v", got[0])
	}
	entry := state.StoreResolveCache[strings.ToLower(appxID)]
	if entry.Resolved {
		t.Fatalf("generic cached Store result should be replaced with unresolved cache, got %#v", entry)
	}
}

func TestResolveStoreAppxPackagesRetriesStaleUnresolvedCache(t *testing.T) {
	state := defaultState()
	appxID := "OpenAI.Codex_1.0.0.0_x64__abc123"
	state.StoreResolveCache[strings.ToLower(appxID)] = StoreResolveCacheEntry{
		AppXVersion: "1.0.0.0",
		Resolved:    false,
		ResolvedAt:  time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339),
	}
	appx := []Package{{
		ID:              appxID,
		Name:            "Codex",
		Version:         "1.0.0.0",
		Manager:         "store",
		Source:          "appx",
		UpdateSupported: false,
		ActionBackend:   "appx-inventory",
	}}

	calls := 0
	got, _, changed := resolveStoreAppxPackages(&state, appx, true, func(query string) ([]Package, CommandResult) {
		calls++
		return []Package{{Name: "Codex", ID: "OpenAI.Codex", Version: "1.1.0.0", Manager: "store"}}, CommandResult{OK: true}
	})

	if calls != 1 || !changed {
		t.Fatalf("expected stale unresolved cache to be retried, calls=%d changed=%t", calls, changed)
	}
	if !got[0].UpdateAvailable || got[0].ID != "OpenAI.Codex" {
		t.Fatalf("expected retry to resolve Codex update, got %#v", got[0])
	}
}

func TestResolveStoreAppxPackagesRetriesFreshUnresolvedCache(t *testing.T) {
	state := defaultState()
	appxID := "OpenAI.Codex_1.0.0.0_x64__abc123"
	state.StoreResolveCache[strings.ToLower(appxID)] = StoreResolveCacheEntry{
		AppXVersion: "1.0.0.0",
		Resolved:    false,
		ResolvedAt:  utcNow(),
	}
	appx := []Package{{
		ID:              appxID,
		Name:            "Codex",
		Version:         "1.0.0.0",
		Manager:         "store",
		Source:          "appx",
		UpdateSupported: false,
		ActionBackend:   "appx-inventory",
	}}

	calls := 0
	got, _, changed := resolveStoreAppxPackages(&state, appx, true, func(query string) ([]Package, CommandResult) {
		calls++
		return []Package{{Name: "Codex", ID: "9PLM9XGG6VKS", Manager: "store"}}, CommandResult{OK: true}
	})

	if calls != 1 || !changed {
		t.Fatalf("expected fresh unresolved cache to be retried, calls=%d changed=%t", calls, changed)
	}
	if got[0].ID != "9PLM9XGG6VKS" || got[0].ActionBackend != backendStoreCLIResolved || !got[0].UpdateSupported {
		t.Fatalf("expected retry to resolve Codex Store product ID, got %#v", got[0])
	}
}

func TestResolveStoreAppxPackagesUsesFreshResolvedCacheWithoutSearch(t *testing.T) {
	state := defaultState()
	appxID := "OpenAI.Codex_1.0.0.0_x64__abc123"
	state.StoreResolveCache[strings.ToLower(appxID)] = StoreResolveCacheEntry{
		AppXVersion: "1.0.0.0",
		StoreID:     "OpenAI.Codex",
		StoreName:   "Codex",
		Resolved:    true,
		ResolvedAt:  utcNow(),
	}
	appx := []Package{{
		ID:              appxID,
		Name:            "Codex",
		Version:         "1.0.0.0",
		Manager:         "store",
		Source:          "appx",
		UpdateSupported: false,
		ActionBackend:   "appx-inventory",
	}}

	calls := 0
	got, results, changed := resolveStoreAppxPackages(&state, appx, true, func(query string) ([]Package, CommandResult) {
		calls++
		return []Package{{Name: "Codex", ID: "OpenAI.Codex", Manager: "store"}}, CommandResult{OK: true}
	})

	if calls != 0 || changed || len(results) != 0 {
		t.Fatalf("fresh resolved cache should avoid store search, calls=%d changed=%t results=%#v", calls, changed, results)
	}
	if got[0].ID != "OpenAI.Codex" || got[0].ActionBackend != "store-cli-resolved" || !got[0].UpdateSupported {
		t.Fatalf("cache hit did not resolve package: %#v", got[0])
	}
}

func TestCachedStoreResolutionUsesStoreUpdatesForAvailableVersion(t *testing.T) {
	state := defaultState()
	appxID := "OpenAI.Codex_1.0.0.0_x64__abc123"
	state.StoreResolveCache[strings.ToLower(appxID)] = StoreResolveCacheEntry{
		AppXVersion:  "1.0.0.0",
		StoreID:      "OpenAI.Codex",
		StoreName:    "Codex",
		StoreVersion: "1.0.0.0",
		Resolved:     true,
		ResolvedAt:   utcNow(),
	}
	appx := []Package{{
		ID:              appxID,
		Name:            "Codex",
		Version:         "1.0.0.0",
		Manager:         "store",
		Source:          "appx",
		UpdateSupported: false,
		ActionBackend:   "appx-inventory",
	}}

	calls := 0
	got, _, changed := resolveStoreAppxPackages(&state, appx, true, func(query string) ([]Package, CommandResult) {
		calls++
		return []Package{{Name: "Codex", ID: "OpenAI.Codex", Version: "1.1.0.0", Manager: "store"}}, CommandResult{OK: true}
	})

	if calls != 0 || changed {
		t.Fatalf("fresh cache should not search while resolving update target, calls=%d changed=%t", calls, changed)
	}
	got[0] = applyStoreUpdateVersion(got[0], map[string]string{
		packageKey(managerStore, strings.ToLower("OpenAI.Codex")): "1.1.0.0",
	}, true)
	if !got[0].UpdateAvailable || got[0].AvailableVersion != "1.1.0.0" {
		t.Fatalf("expected Store updates map to mark Codex update, got %#v", got[0])
	}
}

func TestResolveStoreAppxPackagesKeepsCachedMappingOnBadRefreshMatch(t *testing.T) {
	state := defaultState()
	appxID := "OpenAI.Codex_1.0.0.0_x64__abc123"
	state.StoreResolveCache[strings.ToLower(appxID)] = StoreResolveCacheEntry{
		AppXVersion: "1.0.0.0",
		StoreID:     "OpenAI.Codex",
		StoreName:   "Codex",
		Resolved:    true,
		ResolvedAt:  utcNow(),
	}
	appx := []Package{{
		ID:              appxID,
		Name:            "Codex",
		Version:         "1.0.0.0",
		Manager:         "store",
		Source:          "appx",
		UpdateSupported: false,
		ActionBackend:   "appx-inventory",
	}}

	got, _, _ := resolveStoreAppxPackages(&state, appx, true, func(query string) ([]Package, CommandResult) {
		return []Package{{Name: "Notepad", ID: "Microsoft.WindowsNotepad", Manager: "store"}}, CommandResult{OK: true}
	})

	if got[0].ID != "OpenAI.Codex" || got[0].ActionBackend != "store-cli-resolved" || !got[0].UpdateSupported {
		t.Fatalf("bad refresh match should keep cached Store mapping, got %#v", got[0])
	}
	entry := state.StoreResolveCache[strings.ToLower(appxID)]
	if !entry.Resolved || entry.StoreID != "OpenAI.Codex" {
		t.Fatalf("bad refresh match should not overwrite safe cache entry: %#v", entry)
	}
}

func TestResolveStoreAppxPackagesRefreshesStaleCacheWithoutDroppingMapping(t *testing.T) {
	state := defaultState()
	appxID := "OpenAI.Codex_1.0.0.0_x64__abc123"
	state.StoreResolveCache[strings.ToLower(appxID)] = StoreResolveCacheEntry{
		AppXVersion: "1.0.0.0",
		StoreID:     "OpenAI.Codex",
		StoreName:   "Codex",
		Resolved:    true,
		ResolvedAt:  time.Now().UTC().Add(-storeResolveCacheTTL * 2).Format(time.RFC3339),
	}
	appx := []Package{{
		ID:              appxID,
		Name:            "Codex",
		Version:         "1.0.0.0",
		Manager:         "store",
		Source:          "appx",
		UpdateSupported: false,
		ActionBackend:   "appx-inventory",
	}}

	calls := 0
	got, _, changed := resolveStoreAppxPackages(&state, appx, true, func(query string) ([]Package, CommandResult) {
		calls++
		return nil, CommandResult{Code: 1}
	})

	if calls != 1 || changed {
		t.Fatalf("expected stale cache refresh attempt without cache mutation, calls=%d changed=%t", calls, changed)
	}
	if got[0].ID != "OpenAI.Codex" || got[0].ActionBackend != "store-cli-resolved" || !got[0].UpdateSupported {
		t.Fatalf("stale cache refresh failure should keep safe mapping, got %#v", got[0])
	}
}

func TestResolveStoreAppxPackagesInvalidatesBadSearchBannerCache(t *testing.T) {
	state := defaultState()
	appxID := "Microsoft.ApplicationCompatibility_1.2511.9.0_x64__abc123"
	cacheKey := strings.ToLower(appxID)
	state.StoreResolveCache[cacheKey] = StoreResolveCacheEntry{
		AppXVersion: "1.2511.9.0",
		StoreID:     "Search Results for \"Application Compatibility Enhancements\"",
		StoreName:   "Application Compatibility Enhancements",
		Resolved:    true,
		ResolvedAt:  utcNow(),
	}
	appx := []Package{{
		ID:              appxID,
		Name:            "Application Compatibility Enhancements",
		Version:         "1.2511.9.0",
		Manager:         "store",
		Source:          "appx",
		UpdateSupported: false,
		ActionBackend:   "appx-inventory",
	}}

	calls := 0
	got, _, changed := resolveStoreAppxPackages(&state, appx, true, func(query string) ([]Package, CommandResult) {
		calls++
		return []Package{{Name: "Application Compatibility Enhancements", ID: "Microsoft.ApplicationCompatibility", Manager: "store"}}, CommandResult{OK: true}
	})

	if calls != 1 || !changed {
		t.Fatalf("expected stale cache to be invalidated and searched, calls=%d changed=%t", calls, changed)
	}
	if got[0].ID != "Microsoft.ApplicationCompatibility" || got[0].ID == "Search Results for \"Application Compatibility Enhancements\"" {
		t.Fatalf("bad cached banner target was not replaced: %#v", got[0])
	}
}

func TestResolveStoreAppxPackagesRejectsUnsafeCachedTarget(t *testing.T) {
	state := defaultState()
	appxID := "OpenAI.Codex_1.0.0.0_x64__abc123"
	cacheKey := strings.ToLower(appxID)
	state.StoreResolveCache[cacheKey] = StoreResolveCacheEntry{
		AppXVersion: "1.0.0.0",
		StoreID:     "OpenAI.%USERNAME%.Codex",
		StoreName:   "Codex",
		Resolved:    true,
		ResolvedAt:  utcNow(),
	}
	appx := []Package{{
		ID:              appxID,
		Name:            "Codex",
		Version:         "1.0.0.0",
		Manager:         "store",
		Source:          "appx",
		UpdateSupported: false,
		ActionBackend:   "appx-inventory",
	}}

	calls := 0
	got, _, changed := resolveStoreAppxPackages(&state, appx, true, func(query string) ([]Package, CommandResult) {
		calls++
		return []Package{{Name: "Codex", ID: "OpenAI.Codex", Manager: "store"}}, CommandResult{OK: true}
	})

	if calls != 1 || !changed {
		t.Fatalf("expected unsafe cache target to be invalidated and searched, calls=%d changed=%t", calls, changed)
	}
	if got[0].ID != "OpenAI.Codex" || !got[0].UpdateSupported {
		t.Fatalf("expected safe target replacement, got %#v", got[0])
	}
}
