package updater

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDurableAutoUpdateHistoryIsSummarizedAndBounded(t *testing.T) {
	store := newTestFileStateStore(t)
	huge := strings.Repeat("stdout-", commandResultStreamLimitBytes)
	results := make([]UpdateResult, 100)
	for i := range results {
		key := "winget:Vendor.App" + string(rune('A'+i%26))
		results[i] = UpdateResult{Key: key, Result: CommandResult{Command: "winget upgrade " + key, Code: 1, Stderr: huge}}
	}
	if err := persistAutoUpdateResultsForTest(context.Background(), store, results); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(store.dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > maxStateFileBytes {
		t.Fatalf("state file grew beyond bound: %d", len(data))
	}
	if strings.Count(string(data), "stdout-") > 200 {
		t.Fatalf("state persisted raw command output: %d bytes", len(data))
	}
	loaded := mustLoadStoreState(t, store)
	if len(loaded.LastAutoUpdateResults) == 0 || len(loaded.LastAutoUpdateResults) > maxStateDurableUpdateSummaries {
		t.Fatalf("unexpected summary count: %d", len(loaded.LastAutoUpdateResults))
	}
	if len(loaded.LastAutoUpdateResults[0].Message) > maxStateSummaryMessageBytes {
		t.Fatalf("summary message was not capped: %d", len(loaded.LastAutoUpdateResults[0].Message))
	}
}

func TestStateLoadMigratesLegacyAutoUpdateResultsWithoutOutput(t *testing.T) {
	store := newTestFileStateStore(t)
	huge := strings.Repeat("legacy-output", 10000)
	raw := `{
  "created_at":"2026-06-25T12:00:00Z",
  "updated_at":"2026-06-25T12:00:00Z",
  "auto_update_global":true,
  "auto_update_packages":{"winget:Git.Git":true},
  "registry_apps":{},
  "winget_apps":{},
  "store_apps":{},
  "last_auto_update_results":[{"key":"winget:Git.Git","result":{"ok":false,"code":5,"command":"winget upgrade Git.Git","stdout":"` + huge + `","stderr":"Access denied"}}],
  "theme":"dark"
}`
	if err := os.WriteFile(filepath.Join(store.dir, "state.json"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.LastAutoUpdateResults) != 1 || loaded.LastAutoUpdateResults[0].PackageID != "Git.Git" || loaded.LastAutoUpdateResults[0].Message == "" {
		t.Fatalf("legacy result did not migrate to summary: %#v", loaded.LastAutoUpdateResults)
	}
	data, err := marshalState(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "legacy-output") || strings.Contains(string(data), `"result"`) {
		t.Fatalf("legacy raw result survived migration: %s", string(data))
	}
}

func TestStateLoadRejectsOversizedPrimaryAndRecoversBackup(t *testing.T) {
	store := newTestFileStateStore(t)
	backup := defaultState()
	backup.Theme = "light"
	data, err := marshalState(backup)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.dir, "state.json.bak"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.dir, "state.json"), []byte(strings.Repeat("x", maxStateFileBytes+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Theme != "light" {
		t.Fatalf("backup was not recovered after oversized primary: %#v", loaded)
	}
}

func TestStateLoadRejectsTrailingJSONAndReportsDoubleFailure(t *testing.T) {
	store := newTestFileStateStore(t)
	if err := os.WriteFile(filepath.Join(store.dir, "state.json"), []byte(`{"theme":"dark"} {}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.dir, "state.json.bak"), []byte(`{"theme":"light"} {}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := store.Load(context.Background())
	if err == nil {
		t.Fatal("expected visible load error when primary and backup contain trailing JSON")
	}
}

func TestStatusResponseOmitsTrackedApplicationMaps(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("UPDATER_STATE_DIR", dir)
	state := defaultState()
	state.AutoUpdateGlobal = true
	state.AutoUpdatePackages["winget:Git.Git"] = true
	state.RegistryApps["registry:big"] = ScannedApp{Name: strings.Repeat("registry", 1000)}
	state.WingetApps["winget:big"] = ScannedApp{Name: strings.Repeat("winget", 1000)}
	state.StoreApps["store:big"] = ScannedApp{Name: strings.Repeat("store", 1000)}
	if err := saveState(state); err != nil {
		t.Fatal(err)
	}
	status := (&App{}).statusSnapshot()
	data, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "registry_apps") || strings.Contains(text, "winget_apps") || strings.Contains(text, "store_apps") || strings.Contains(text, "registryregistry") {
		t.Fatalf("status response includes tracked app maps: %s", text)
	}
	if !strings.Contains(text, "auto_update_packages") || !strings.Contains(text, "winget:Git.Git") {
		t.Fatalf("status response lost UI settings: %s", text)
	}
}

func TestTerminalJobsCompactPackagesAndCommandOutput(t *testing.T) {
	app := &App{}
	huge := strings.Repeat("stderr-", terminalCommandResultStreamBytes*4)
	status := app.startOperationJobWithPackageSnapshot(jobTypeUpdate, updateJobModeSelected, 1, []string{"winget:Vendor.App"}, []Package{{
		Key: "winget:Vendor.App", Manager: managerWinget, ID: "Vendor.App", Name: strings.Repeat("Package", 1000),
	}}, func(ctx context.Context, job *OperationJob) {
		app.mutateOperationJob(job, func(status *OperationJobStatus) {
			result := CommandResult{Command: strings.Repeat("winget upgrade Vendor.App ", 2000), Code: 1, Stderr: huge}
			status.Result = &result
			status.Results = []UpdateResult{{Key: "winget:Vendor.App", Result: result}}
			status.State = jobStateFailed
			status.Notice = "failed"
		})
	})
	final, ok := waitForOperationJobState(app, status.JobID, 2*time.Second)
	if !ok {
		t.Fatal("job did not finish")
	}
	if len(final.Packages) != 0 {
		t.Fatalf("terminal job retained package snapshots: %#v", final.Packages)
	}
	if final.Result == nil || len(final.Result.Stderr) > terminalCommandResultStreamBytes+128 || len(final.Result.Command) > maxCommandResultCommandBytes+128 {
		t.Fatalf("terminal result was not compacted: %#v", final.Result)
	}
}

func TestMergeCommandAttemptsWithFinalResultAppliesFinalOutputCap(t *testing.T) {
	huge := strings.Repeat("output-", commandResultStreamLimitBytes*3)
	primary := CommandResult{Command: strings.Repeat("primary ", maxCommandResultCommandBytes), Stdout: huge, Stderr: huge}
	fallback := CommandResult{Command: strings.Repeat("fallback ", maxCommandResultCommandBytes), Stdout: huge, Stderr: huge}
	merged := mergeCommandAttemptsWithFinalResult(primary, fallback, "retry")
	if len(merged.Stdout) > commandResultStreamLimitBytes+128 || len(merged.Stderr) > commandResultStreamLimitBytes+128 || len(merged.Command) > maxCommandResultCommandBytes+128 {
		t.Fatalf("merged result exceeded caps: command=%d stdout=%d stderr=%d", len(merged.Command), len(merged.Stdout), len(merged.Stderr))
	}
	if !strings.Contains(merged.Stdout, "output truncated") || !strings.Contains(merged.Command, "command truncated") {
		t.Fatalf("merged result did not include truncation markers: %#v", merged)
	}
}

func TestEventsAPIOnlySendsJobsWhenRevisionChanges(t *testing.T) {
	app := &App{jobs: map[string]*OperationJob{
		"job-1": {status: OperationJobStatus{JobID: "job-1", Type: jobTypeUpdate, State: jobStateRunning, Running: true, Revision: 1}},
	}, jobSeq: 1}
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	writer := newFlushRecorder()
	done := make(chan struct{})
	go func() {
		app.handleEventsAPI(writer, req)
		close(done)
	}()
	time.Sleep(1200 * time.Millisecond)
	cancel()
	<-done
	if count := strings.Count(writer.String(), "event: jobs"); count != 1 {
		t.Fatalf("unchanged active job was retransmitted %d times: %s", count, writer.String())
	}

	ctx, cancel = context.WithCancel(context.Background())
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, "/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	writer = newFlushRecorder()
	done = make(chan struct{})
	go func() {
		app.handleEventsAPI(writer, req)
		close(done)
	}()
	time.Sleep(200 * time.Millisecond)
	app.jobsMu.Lock()
	app.jobs["job-1"].status.Notice = "changed"
	app.jobs["job-1"].status.Revision++
	app.jobsMu.Unlock()
	time.Sleep(1200 * time.Millisecond)
	cancel()
	<-done
	if count := strings.Count(writer.String(), "event: jobs"); count != 2 {
		t.Fatalf("changed job was not sent exactly once for new revision, count=%d body=%s", count, writer.String())
	}
}

type flushRecorder struct {
	header http.Header
	mu     sync.Mutex
	body   strings.Builder
}

func newFlushRecorder() *flushRecorder {
	return &flushRecorder{header: http.Header{}}
}

func (rec *flushRecorder) Header() http.Header { return rec.header }
func (rec *flushRecorder) WriteHeader(int)     {}
func (rec *flushRecorder) Flush()              {}
func (rec *flushRecorder) Write(data []byte) (int, error) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.body.Write(data)
}
func (rec *flushRecorder) String() string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.body.String()
}
