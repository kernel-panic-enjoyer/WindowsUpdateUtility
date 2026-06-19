package updater

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestParseChocoListIgnoresWarnings(t *testing.T) {
	output := `
This is try 1/3. Retrying after 300 milliseconds.
Error converted to warning:
git|2.54.0
python314|3.14.5
`
	got := parseChocoList(output)
	if len(got) != 2 {
		t.Fatalf("expected 2 packages, got %d: %#v", len(got), got)
	}
	if got[0].ID != "git" || got[1].ID != "python314" {
		t.Fatalf("unexpected packages: %#v", got)
	}
}

func TestParseChocoOutdated(t *testing.T) {
	got := parseChocoOutdated("git|2.53.0|2.54.0|false\npython314|3.14.4|3.14.5|false\n")
	if got["git"] != "2.54.0" || got["python314"] != "3.14.5" {
		t.Fatalf("unexpected outdated map: %#v", got)
	}
}

func TestChocoUpdateTriesInstallPackageVariant(t *testing.T) {
	var targets []string
	restore := replacePackageActionHooks(
		func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			target := packageActionTargetFromArgs(args)
			targets = append(targets, target)
			if target == "example.install" {
				return CommandResult{OK: true, Command: strings.Join(args, " "), Stdout: "Chocolatey upgraded 1/1 packages."}
			}
			return CommandResult{Code: 1, Command: strings.Join(args, " "), Stderr: "Unable to find package 'example'."}
		},
		func(manager string) bool { return manager == managerChoco },
	)
	defer restore()

	pkg := Package{Manager: managerChoco, ID: "example", Name: "Example"}
	result := runChocoUpgradePackageWithFallbackContext(context.Background(), pkg)

	if !result.OK {
		t.Fatalf("expected Chocolatey .install fallback to succeed, got %#v", result)
	}
	if !containsString(targets, "example") || !containsString(targets, "example.install") {
		t.Fatalf("expected base and .install targets, got %#v", targets)
	}
}

func TestChocoUpdateTriesBasePackageVariant(t *testing.T) {
	var targets []string
	restore := replacePackageActionHooks(
		func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			target := packageActionTargetFromArgs(args)
			targets = append(targets, target)
			if target == "example" {
				return CommandResult{OK: true, Command: strings.Join(args, " "), Stdout: "Chocolatey upgraded 1/1 packages."}
			}
			return CommandResult{Code: 1, Command: strings.Join(args, " "), Stderr: "Unable to find package 'example.install'."}
		},
		func(manager string) bool { return manager == managerChoco },
	)
	defer restore()

	pkg := Package{Manager: managerChoco, ID: "example.install", Name: "Example"}
	result := runChocoUpgradePackageWithFallbackContext(context.Background(), pkg)

	if !result.OK {
		t.Fatalf("expected Chocolatey base fallback to succeed, got %#v", result)
	}
	if strings.Join(targets, "|") != "example.install|example" {
		t.Fatalf("unexpected Chocolatey fallback targets: %#v", targets)
	}
}
