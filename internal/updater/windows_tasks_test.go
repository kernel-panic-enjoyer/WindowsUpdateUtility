package updater

import "testing"

func TestAutoUpdateTaskExecutableRequiresTrustedRoot(t *testing.T) {
	roots := []string{`C:\Program Files`, `C:\Program Files (x86)`, `C:\Windows`}
	if !pathWithinAnyRoot(`C:\Program Files\WindowsUpdaterWebUI\WindowsUpdaterWebUI.exe`, roots) {
		t.Fatal("expected Program Files executable to be trusted")
	}
	for _, exe := range []string{
		`C:\Users\User\Downloads\WindowsUpdaterWebUI.exe`,
		`C:\Program Files Evil\WindowsUpdaterWebUI.exe`,
		`C:\Users\User\AppData\Local\Temp\WindowsUpdaterWebUI.exe`,
	} {
		if pathWithinAnyRoot(exe, roots) {
			t.Fatalf("expected user-writable executable path to be rejected: %s", exe)
		}
	}
}
