package updater

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestManagerCommandOverride(t *testing.T) {
	t.Setenv("UPDATER_WINGET_PATH", filepath.Join("C:", "Tools", "winget.exe"))
	got := managerCommand("winget", "--version")
	if len(got) != 2 || got[0] != filepath.Join("C:", "Tools", "winget.exe") || got[1] != "--version" {
		t.Fatalf("unexpected manager command: %#v", got)
	}

	t.Setenv("UPDATER_STORE_PATH", filepath.Join("C:", "Tools", "store.exe"))
	got = managerCommand("store", "--help")
	if len(got) != 2 || got[0] != filepath.Join("C:", "Tools", "store.exe") || got[1] != "--help" {
		t.Fatalf("unexpected store manager command: %#v", got)
	}
}

func readZipTextFiles(t *testing.T, data []byte) map[string]string {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, file := range reader.File {
		handle, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(handle)
		_ = handle.Close()
		if err != nil {
			t.Fatal(err)
		}
		files[file.Name] = string(content)
	}
	return files
}

func TestLaunchPathAddsChocolateyBinWhenPresent(t *testing.T) {
	root := t.TempDir()
	chocoBin := filepath.Join(root, "chocolatey", "bin")
	if err := os.MkdirAll(chocoBin, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ProgramData", root)
	t.Setenv("ChocolateyInstall", "")

	path := launchPath(filepath.Join("C:", "Windows", "System32"))
	entries := filepath.SplitList(path)
	if len(entries) == 0 || entries[0] != chocoBin {
		t.Fatalf("expected Chocolatey bin to be prepended, path=%q entries=%#v", path, entries)
	}
}

func TestRegistryEnvironmentValueParsing(t *testing.T) {
	output := `
HKEY_LOCAL_MACHINE\SYSTEM\CurrentControlSet\Control\Session Manager\Environment
    Path    REG_EXPAND_SZ    %SystemRoot%\system32;%ProgramData%\chocolatey\bin
`
	got := parseRegistryQueryValue(output, "Path")
	if got != `%SystemRoot%\system32;%ProgramData%\chocolatey\bin` {
		t.Fatalf("unexpected parsed registry value: %q", got)
	}
}

func TestExpandWindowsEnvRefs(t *testing.T) {
	t.Setenv("ProgramData", `C:\ProgramData`)
	got := expandWindowsEnvRefs(`%ProgramData%\chocolatey\bin;%UnknownUpdaterVar%\bin`)
	want := `C:\ProgramData\chocolatey\bin;%UnknownUpdaterVar%\bin`
	if got != want {
		t.Fatalf("unexpected expanded env refs: got %q want %q", got, want)
	}
}

func TestIsWingetCommand(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{filepath.Join("C:", "Users", "User", "AppData", "Local", "Microsoft", "WindowsApps", "winget.exe"), "--version"}, true},
		{[]string{"winget", "--version"}, true},
		{[]string{"cmd.exe", "/d", "/c", "winget", "--version"}, true},
		{[]string{"choco", "--version"}, false},
		{[]string{"cmd.exe", "/c", "winget", "--version"}, false},
	}
	for _, tc := range cases {
		if got := isWingetCommand(tc.args); got != tc.want {
			t.Fatalf("isWingetCommand(%#v) = %t, want %t", tc.args, got, tc.want)
		}
	}
}

func TestIsStoreCommand(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{filepath.Join("C:", "Users", "User", "AppData", "Local", "Microsoft", "WindowsApps", "store.exe"), "--help"}, true},
		{[]string{"store", "--help"}, true},
		{[]string{"cmd.exe", "/d", "/c", "store", "--help"}, true},
		{[]string{"winget", "--version"}, false},
		{[]string{"cmd.exe", "/c", "store", "--help"}, false},
	}
	for _, tc := range cases {
		if got := isStoreCommand(tc.args); got != tc.want {
			t.Fatalf("isStoreCommand(%#v) = %t, want %t", tc.args, got, tc.want)
		}
	}
}

func TestPackageManagerMutationCommandDetection(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"store", "search", "Codex"}, false},
		{[]string{"store", "install", "OpenAI.Codex"}, true},
		{[]string{"cmd.exe", "/d", "/c", "store", "updates"}, true},
		{[]string{"winget", "list"}, false},
		{[]string{"winget", "upgrade", "--all"}, true},
		{[]string{"cmd.exe", "/d", "/c", "winget", "search", "git"}, false},
		{[]string{"choco", "outdated"}, false},
		{[]string{"choco", "upgrade", "all"}, true},
	}
	for _, tc := range cases {
		if got := isPackageManagerMutationCommand(tc.args); got != tc.want {
			t.Fatalf("isPackageManagerMutationCommand(%#v) = %t, want %t", tc.args, got, tc.want)
		}
	}
}

func TestLogBufferAppendAndSince(t *testing.T) {
	buffer := &LogBuffer{}
	first := buffer.Append("app", "one")
	second := buffer.Append("stdout", "two")
	third := buffer.Append("stderr", "three")
	fourth := buffer.Append("exit", "four")

	if first.ID != 1 || second.ID != 2 || third.ID != 3 || fourth.ID != 4 {
		t.Fatalf("unexpected log ids: %d %d %d %d", first.ID, second.ID, third.ID, fourth.ID)
	}
	if buffer.LatestID() != 4 {
		t.Fatalf("expected latest id 4, got %d", buffer.LatestID())
	}

	all := buffer.Since(0)
	if len(all) != 4 || all[0].Message != "one" || all[3].Message != "four" {
		t.Fatalf("unexpected log entries: %#v", all)
	}

	newer := buffer.Since(2)
	if len(newer) != 2 || newer[0].ID != 3 || newer[1].ID != 4 {
		t.Fatalf("unexpected since entries: %#v", newer)
	}
}

func TestLogEntryCategoryMetadata(t *testing.T) {
	cases := []struct {
		name       string
		categories []string
		want       []string
	}{
		{"app", logCategoriesForManagerVerb("", ""), []string{logCategoryApplication}},
		{"winget search", logCategoriesForCommand([]string{"winget", "search", "gh"}), []string{logCategoryWinget, logCategorySearches}},
		{"choco upgrade", logCategoriesForCommand([]string{"choco", "upgrade", "git"}), []string{logCategoryChocolatey, logCategoryUpdates}},
		{"store update", logCategoriesForCommand([]string{"store", "update", "Codex"}), []string{logCategoryStore, logCategoryUpdates}},
		{"store updates", logCategoriesForCommand([]string{"store", "updates"}), []string{logCategoryStore, logCategoryUpdates}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry := LogEntry{Stream: "command", Categories: tc.categories}
			if !logEntryInCategory(entry, logCategoryAll) {
				t.Fatalf("expected %q in all category: %#v", tc.name, tc.categories)
			}
			for _, category := range tc.want {
				if !logEntryInCategory(entry, category) {
					t.Fatalf("expected %q in category %q: %#v", tc.name, category, tc.categories)
				}
			}
		})
	}
}

func TestLogArchiveDuplicatesOverlappingCategories(t *testing.T) {
	entries := []LogEntry{{
		ID:         1,
		Timestamp:  "2026-06-17T12:00:00Z",
		Stream:     "command",
		Message:    "winget search gh",
		Categories: logCategoriesForCommand([]string{"winget", "search", "gh"}),
	}}
	data, err := buildLogArchive(entries)
	if err != nil {
		t.Fatal(err)
	}
	files := readZipTextFiles(t, data)
	for _, name := range []string{"all.txt", "winget.txt", "searches.txt"} {
		if !strings.Contains(files[name], "winget search gh") {
			t.Fatalf("expected duplicated entry in %s, files=%#v", name, files)
		}
	}
	if strings.Contains(files["updates.txt"], "winget search gh") {
		t.Fatalf("search entry should not be in updates.txt: %q", files["updates.txt"])
	}
}

func TestAppendLogChunkDropsCarriageReturnSpinnerFrames(t *testing.T) {
	oldLogs := sessionLogs
	sessionLogs = &LogBuffer{}
	defer func() { sessionLogs = oldLogs }()

	pending := appendLogChunkCategorized("stdout", "", "Downloading\r|\r/\r-\r", nil)
	pending = appendLogChunkCategorized("stdout", pending, `\`+"\rDone\n", nil)
	if pending != "" {
		t.Fatalf("expected no pending log text, got %q", pending)
	}

	entries := sessionLogs.Since(0)
	if len(entries) != 1 || entries[0].Message != "Done" {
		t.Fatalf("expected only final line, got %#v", entries)
	}
}

func TestStreamCommandOutputKeepsRawOutputWhileDroppingSpinnerLog(t *testing.T) {
	oldLogs := sessionLogs
	sessionLogs = &LogBuffer{}
	defer func() { sessionLogs = oldLogs }()

	raw := "Downloading\r|\r/\r-\rDone\n"
	var output bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(1)
	streamCommandOutputCategorized(strings.NewReader(raw), "stdout", &output, &wg, nil)
	wg.Wait()

	if output.String() != raw {
		t.Fatalf("raw output changed: got %q want %q", output.String(), raw)
	}
	entries := sessionLogs.Since(0)
	if len(entries) != 1 || entries[0].Message != "Done" {
		t.Fatalf("expected only final log line, got %#v", entries)
	}
}

func TestAppendLogChunkPreservesNormalLines(t *testing.T) {
	oldLogs := sessionLogs
	sessionLogs = &LogBuffer{}
	defer func() { sessionLogs = oldLogs }()

	pending := appendLogChunkCategorized("stdout", "", "first\r", nil)
	pending = appendLogChunkCategorized("stdout", pending, "\nsecond\nthird", nil)
	pending = appendLogChunkCategorized("stdout", pending, "\n", nil)
	if pending != "" {
		t.Fatalf("expected no pending log text, got %q", pending)
	}

	entries := sessionLogs.Since(0)
	if len(entries) != 3 || entries[0].Message != "first" || entries[1].Message != "second" || entries[2].Message != "third" {
		t.Fatalf("unexpected normal log lines: %#v", entries)
	}
}

func TestRunCommandContextCancellation(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows command cancellation test")
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	result := runCommandContext(ctx, 10*time.Second, "powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", "Start-Sleep -Seconds 5")

	if result.OK || result.Code != commandCancelledCode || !strings.Contains(result.Stderr, "Cancelled.") {
		t.Fatalf("expected cancelled command result, got %#v", result)
	}
}

func TestRunCommandContextCancellationWhileWaitingForMutationLock(t *testing.T) {
	packageManagerMutationMu.Lock()
	defer packageManagerMutationMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	started := time.Now()
	result := runCommandContext(ctx, 10*time.Second, "choco.exe", "upgrade", "example-package")
	elapsed := time.Since(started)

	if result.OK || result.Code != commandCancelledCode || !strings.Contains(result.Stderr, "Cancelled.") {
		t.Fatalf("expected cancelled command result, got %#v", result)
	}
	if elapsed > time.Second {
		t.Fatalf("cancel while waiting for package-manager lock took too long: %s", elapsed)
	}
	if !strings.Contains(result.Command, "choco.exe upgrade example-package") {
		t.Fatalf("unexpected command text: %q", result.Command)
	}
}

func TestRunCommandContextTimeoutWhileWaitingForMutationLock(t *testing.T) {
	packageManagerMutationMu.Lock()
	defer packageManagerMutationMu.Unlock()

	started := time.Now()
	result := runCommandContext(context.Background(), 50*time.Millisecond, "choco.exe", "upgrade", "example-package")
	elapsed := time.Since(started)

	if result.OK || result.Code != 124 || !strings.Contains(result.Stderr, "Timed out.") {
		t.Fatalf("expected timeout command result, got %#v", result)
	}
	if elapsed > time.Second {
		t.Fatalf("timeout while waiting for package-manager lock took too long: %s", elapsed)
	}
	if !strings.Contains(result.Command, "choco.exe upgrade example-package") {
		t.Fatalf("unexpected command text: %q", result.Command)
	}
}
