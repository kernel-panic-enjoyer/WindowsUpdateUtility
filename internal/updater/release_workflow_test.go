package updater

import (
	"os"
	"strings"
	"testing"
)

func TestReleaseWorkflowBuildsAndPublishesWindowsExecutable(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(data)
	for _, expected := range []string{
		"workflow_dispatch:",
		"contents: write",
		"windows-latest",
		"-Version",
		"-Strip",
		"WindowsUpdaterWebUI.exe.sha256",
		"WindowsUpdaterWebUI.metadata.json",
		"gh release create",
		"v${{ inputs.version }}",
	} {
		if !strings.Contains(workflow, expected) {
			t.Fatalf("release workflow missing %q", expected)
		}
	}
}

func TestBuildWorkspaceSupportsReleaseStrippingMetadata(t *testing.T) {
	data, err := os.ReadFile("../../dev/scripts/Build-Workspace.ps1")
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	for _, expected := range []string{
		"[switch]$Strip",
		"'-s'",
		"'-w'",
		"stripped",
		"license",
		"GPL-3.0-only",
		"repository",
		"https://github.com/kernel-panic-enjoyer/WindowsUpdateUtility",
		"Get-Command node -ErrorAction SilentlyContinue",
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("build script missing %q", expected)
		}
	}
	if strings.Contains(script, "$_ -eq 'node'") {
		t.Fatal("build script should not select a literal node command without checking PATH")
	}
}
