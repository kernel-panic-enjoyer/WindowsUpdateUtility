package updater

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	windowsUpdateUtilityOwner      = "kernel-panic-enjoyer"
	windowsUpdateUtilityRepository = "WindowsUpdateUtility"

	releaseAssetExecutable = "WindowsUpdaterWebUI.exe"
	releaseAssetMetadata   = "WindowsUpdaterWebUI.metadata.json"
	releaseAssetSHA256     = "WindowsUpdaterWebUI.exe.sha256"

	maxGitHubReleaseResponseBytes = 512 * 1024
	maxSelfUpdateExecutableBytes  = 100 * 1024 * 1024
	maxSelfUpdateChecksumBytes    = 4 * 1024
	appUpdateCheckTimeout         = 8 * time.Second
	appUpdateCacheTTL             = 30 * time.Minute
	selfUpdateApplyTimeout        = 2 * time.Minute
)

var sha256LinePattern = regexp.MustCompile(`(?i)\b[0-9a-f]{64}\b`)

type appUpdateChecker interface {
	Check(context.Context, string) (AppUpdateStatus, error)
}

type AppUpdateStatus struct {
	CurrentVersion string `json:"current_version"`
	LatestVersion  string `json:"latest_version,omitempty"`
	LatestTag      string `json:"latest_tag,omitempty"`
	Available      bool   `json:"available"`
	CheckedAt      string `json:"checked_at,omitempty"`
	ReleaseURL     string `json:"release_url,omitempty"`
	Error          string `json:"error,omitempty"`

	ExecutableURL  string `json:"-"`
	MetadataURL    string `json:"-"`
	SHA256URL      string `json:"-"`
	ExecutableSize int64  `json:"-"`
}

type GitHubReleaseChecker struct {
	Client           *http.Client
	LatestReleaseURL string
}

type githubReleaseResponse struct {
	TagName    string               `json:"tag_name"`
	Draft      bool                 `json:"draft"`
	Prerelease bool                 `json:"prerelease"`
	HTMLURL    string               `json:"html_url"`
	Assets     []githubReleaseAsset `json:"assets"`
}

type githubReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

type selfUpdateArtifact struct {
	Path    string
	SHA256  string
	Version string
}

func defaultGitHubReleaseChecker() GitHubReleaseChecker {
	return GitHubReleaseChecker{
		Client: http.DefaultClient,
		LatestReleaseURL: fmt.Sprintf(
			"https://api.github.com/repos/%s/%s/releases/latest",
			windowsUpdateUtilityOwner,
			windowsUpdateUtilityRepository,
		),
	}
}

func (checker GitHubReleaseChecker) Check(ctx context.Context, currentVersion string) (AppUpdateStatus, error) {
	if checker.Client == nil {
		checker.Client = http.DefaultClient
	}
	status := AppUpdateStatus{CurrentVersion: currentVersion}
	latestReleaseURL := strings.TrimSpace(checker.LatestReleaseURL)
	if latestReleaseURL == "" {
		latestReleaseURL = defaultGitHubReleaseChecker().LatestReleaseURL
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, latestReleaseURL, nil)
	if err != nil {
		return status, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "WindowsUpdaterWebUI/"+currentAppVersion())
	response, err := checker.Client.Do(request)
	if err != nil {
		return status, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return status, nil
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return status, fmt.Errorf("GitHub release check failed with HTTP %d", response.StatusCode)
	}
	releaseJSON, err := readBounded(response.Body, maxGitHubReleaseResponseBytes, "release response")
	if err != nil {
		return status, err
	}
	return parseGitHubRelease(releaseJSON, currentVersion)
}

func parseGitHubRelease(releaseJSON []byte, currentVersion string) (AppUpdateStatus, error) {
	updateStatus := AppUpdateStatus{CurrentVersion: currentVersion}
	var latestRelease githubReleaseResponse
	decoder := json.NewDecoder(bytes.NewReader(releaseJSON))
	if err := decoder.Decode(&latestRelease); err != nil {
		return updateStatus, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = errors.New("release response contains trailing JSON data")
		}
		return updateStatus, err
	}
	updateStatus.LatestTag = strings.TrimSpace(latestRelease.TagName)
	updateStatus.ReleaseURL = strings.TrimSpace(latestRelease.HTMLURL)
	latestVersion, ok := normalizeAppVersion(updateStatus.LatestTag)
	if !ok {
		if updateStatus.LatestTag == "" {
			return updateStatus, errors.New("release tag is missing")
		}
		return updateStatus, fmt.Errorf("release tag %q is not a supported semantic version", updateStatus.LatestTag)
	}
	updateStatus.LatestVersion = latestVersion
	if latestRelease.Draft || latestRelease.Prerelease || compareAppVersions(latestVersion, currentVersion) <= 0 {
		return updateStatus, nil
	}
	assetsByName := make(map[string]githubReleaseAsset, len(latestRelease.Assets))
	for _, asset := range latestRelease.Assets {
		assetsByName[asset.Name] = asset
	}
	executableAsset := assetsByName[releaseAssetExecutable]
	metadataAsset := assetsByName[releaseAssetMetadata]
	checksumAsset := assetsByName[releaseAssetSHA256]
	if executableAsset.BrowserDownloadURL == "" || metadataAsset.BrowserDownloadURL == "" || checksumAsset.BrowserDownloadURL == "" {
		return updateStatus, errors.New("newer release is missing required release assets")
	}
	if executableAsset.Size > maxSelfUpdateExecutableBytes {
		return updateStatus, fmt.Errorf("release executable exceeds %d bytes", maxSelfUpdateExecutableBytes)
	}
	updateStatus.Available = true
	updateStatus.ExecutableURL = executableAsset.BrowserDownloadURL
	updateStatus.MetadataURL = metadataAsset.BrowserDownloadURL
	updateStatus.SHA256URL = checksumAsset.BrowserDownloadURL
	updateStatus.ExecutableSize = executableAsset.Size
	return updateStatus, nil
}

func downloadSelfUpdateArtifact(ctx context.Context, client *http.Client, updateStatus AppUpdateStatus, downloadDir string) (selfUpdateArtifact, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if !updateStatus.Available {
		return selfUpdateArtifact{}, errors.New("no application update is available")
	}
	if updateStatus.ExecutableURL == "" || updateStatus.SHA256URL == "" {
		return selfUpdateArtifact{}, errors.New("application update release assets are incomplete")
	}
	if updateStatus.ExecutableSize > maxSelfUpdateExecutableBytes {
		return selfUpdateArtifact{}, fmt.Errorf("release executable exceeds %d bytes", maxSelfUpdateExecutableBytes)
	}
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		return selfUpdateArtifact{}, err
	}
	expectedSHA256, err := downloadExpectedSHA256(ctx, client, updateStatus.SHA256URL)
	if err != nil {
		return selfUpdateArtifact{}, err
	}
	tempFile, err := os.CreateTemp(downloadDir, "WindowsUpdaterWebUI-update-*.exe")
	if err != nil {
		return selfUpdateArtifact{}, err
	}
	artifactPath := tempFile.Name()
	removePartialDownload := true
	defer func() {
		if removePartialDownload {
			_ = os.Remove(artifactPath)
		}
	}()
	actualSHA256, err := downloadFileAndHash(ctx, client, updateStatus.ExecutableURL, tempFile, sha256.New())
	closeErr := tempFile.Close()
	if err != nil {
		return selfUpdateArtifact{}, err
	}
	if closeErr != nil {
		return selfUpdateArtifact{}, closeErr
	}
	if !strings.EqualFold(actualSHA256, expectedSHA256) {
		return selfUpdateArtifact{}, fmt.Errorf("self-update checksum mismatch: got %s want %s", actualSHA256, expectedSHA256)
	}
	_ = os.Chmod(artifactPath, 0o755)
	removePartialDownload = false
	return selfUpdateArtifact{Path: artifactPath, SHA256: strings.ToLower(actualSHA256), Version: updateStatus.LatestVersion}, nil
}

func downloadExpectedSHA256(ctx context.Context, client *http.Client, checksumURL string) (string, error) {
	checksumData, err := httpGetBounded(ctx, client, checksumURL, maxSelfUpdateChecksumBytes, "checksum")
	if err != nil {
		return "", err
	}
	digest := sha256LinePattern.FindString(string(checksumData))
	if digest == "" {
		return "", errors.New("release checksum asset does not contain a SHA-256 digest")
	}
	return strings.ToLower(digest), nil
}

func downloadFileAndHash(ctx context.Context, client *http.Client, downloadURL string, destination io.Writer, digest hash.Hash) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", "WindowsUpdaterWebUI/"+currentAppVersion())
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return "", fmt.Errorf("download failed with HTTP %d", response.StatusCode)
	}
	limitedBody := &io.LimitedReader{R: response.Body, N: maxSelfUpdateExecutableBytes + 1}
	hashingWriter := io.MultiWriter(destination, digest)
	bytesWritten, err := io.Copy(hashingWriter, limitedBody)
	if err != nil {
		return "", err
	}
	if bytesWritten > maxSelfUpdateExecutableBytes || limitedBody.N == 0 {
		return "", fmt.Errorf("downloaded executable exceeds %d bytes", maxSelfUpdateExecutableBytes)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func httpGetBounded(ctx context.Context, client *http.Client, downloadURL string, maxBytes int64, resourceLabel string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "WindowsUpdaterWebUI/"+currentAppVersion())
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("%s download failed with HTTP %d", resourceLabel, response.StatusCode)
	}
	return readBounded(response.Body, maxBytes, resourceLabel)
}

func readBounded(source io.Reader, maxBytes int64, resourceLabel string) ([]byte, error) {
	limitedReader := io.LimitReader(source, maxBytes+1)
	data, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", resourceLabel, maxBytes)
	}
	return data, nil
}

func selfUpdateDownloadDir() (string, error) {
	tempRoot, err := appTempDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(tempRoot, "self-update"), nil
}
