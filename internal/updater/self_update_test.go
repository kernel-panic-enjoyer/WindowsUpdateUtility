package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompareAppVersions(t *testing.T) {
	tests := []struct {
		name  string
		left  string
		right string
		want  int
	}{
		{name: "newer", left: "0.0.2", right: "0.0.1", want: 1},
		{name: "same with v prefix", left: "v0.0.1", right: "0.0.1", want: 0},
		{name: "older", left: "0.0.1", right: "0.1.0", want: -1},
		{name: "dev version is older", left: "0.0.0-dev", right: "0.0.1", want: -1},
		{name: "malformed is rejected low", left: "not-a-version", right: "0.0.1", want: -1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := compareAppVersions(test.left, test.right)
			if got != test.want {
				t.Fatalf("compareAppVersions(%q, %q)=%d, want %d", test.left, test.right, got, test.want)
			}
		})
	}
}

func TestParseGitHubReleaseRequiresStableNewerAssets(t *testing.T) {
	status, err := parseGitHubRelease([]byte(`{
		"tag_name": "v0.0.2",
		"draft": false,
		"prerelease": false,
		"html_url": "https://github.example/release",
		"assets": [
			{"name":"WindowsUpdaterWebUI.exe","browser_download_url":"https://github.example/app.exe","size":1234},
			{"name":"WindowsUpdaterWebUI.metadata.json","browser_download_url":"https://github.example/app.metadata.json","size":321},
			{"name":"WindowsUpdaterWebUI.exe.sha256","browser_download_url":"https://github.example/app.exe.sha256","size":64}
		]
	}`), "0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if !status.Available || status.LatestVersion != "0.0.2" || status.ExecutableURL == "" || status.SHA256URL == "" {
		t.Fatalf("release was not parsed as available with required assets: %#v", status)
	}
}

func TestParseGitHubReleaseIgnoresPrereleaseAndSameVersion(t *testing.T) {
	for _, body := range []string{
		`{"tag_name":"v0.0.2","prerelease":true,"assets":[]}`,
		`{"tag_name":"v0.0.1","draft":false,"prerelease":false,"assets":[]}`,
	} {
		status, err := parseGitHubRelease([]byte(body), "0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		if status.Available {
			t.Fatalf("release should not be available: %#v", status)
		}
	}
}

func TestParseGitHubReleaseRejectsMissingAssets(t *testing.T) {
	_, err := parseGitHubRelease([]byte(`{
		"tag_name": "v0.0.2",
		"assets": [{"name":"WindowsUpdaterWebUI.exe","browser_download_url":"https://github.example/app.exe","size":1234}]
	}`), "0.0.1")
	if err == nil || !strings.Contains(err.Error(), "required release assets") {
		t.Fatalf("expected missing asset error, got %v", err)
	}
}

func TestGitHubReleaseCheckerRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxGitHubReleaseResponseBytes+1)))
	}))
	defer server.Close()

	checker := GitHubReleaseChecker{Client: server.Client(), LatestReleaseURL: server.URL}
	status, err := checker.Check(context.Background(), "0.0.1")
	if err == nil || !strings.Contains(err.Error(), "release response exceeds") {
		t.Fatalf("expected oversized response error, got status=%#v err=%v", status, err)
	}
}

func TestDownloadSelfUpdateVerifiesChecksum(t *testing.T) {
	payload := []byte("new executable")
	sum := sha256.Sum256(payload)
	shaText := hex.EncodeToString(sum[:]) + "  WindowsUpdaterWebUI.exe\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app.exe":
			_, _ = w.Write(payload)
		case "/app.exe.sha256":
			_, _ = w.Write([]byte(shaText))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	artifact, err := downloadSelfUpdateArtifact(context.Background(), server.Client(), AppUpdateStatus{
		Available:      true,
		LatestVersion:  "0.0.2",
		ExecutableURL:  server.URL + "/app.exe",
		SHA256URL:      server.URL + "/app.exe.sha256",
		ExecutableSize: int64(len(payload)),
	}, dir)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(artifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(payload) || artifact.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("downloaded artifact mismatch: %#v data=%q", artifact, data)
	}
}

func TestDownloadSelfUpdateRejectsChecksumMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app.exe":
			_, _ = w.Write([]byte("new executable"))
		case "/app.exe.sha256":
			_, _ = w.Write([]byte(strings.Repeat("0", 64)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	_, err := downloadSelfUpdateArtifact(context.Background(), server.Client(), AppUpdateStatus{
		Available:     true,
		LatestVersion: "0.0.2",
		ExecutableURL: server.URL + "/app.exe",
		SHA256URL:     server.URL + "/app.exe.sha256",
	}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}
}

func TestApplySelfUpdateCopiesExecutableAndKeepsBackup(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "WindowsUpdaterWebUI-new.exe")
	target := filepath.Join(dir, "WindowsUpdaterWebUI.exe")
	if err := os.WriteFile(source, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("new"))
	err := replaceExecutableForSelfUpdate(selfUpdateApplyRequest{
		SourcePath:     source,
		TargetPath:     target,
		ExpectedSHA256: hex.EncodeToString(sum[:]),
	})
	if err != nil {
		t.Fatal(err)
	}
	targetData, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	backupData, err := os.ReadFile(target + ".bak")
	if err != nil {
		t.Fatal(err)
	}
	if string(targetData) != "new" || string(backupData) != "old" {
		t.Fatalf("unexpected replacement target=%q backup=%q", targetData, backupData)
	}
}
