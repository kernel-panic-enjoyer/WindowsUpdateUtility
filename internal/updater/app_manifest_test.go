package updater

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplicationManifestUsesAsInvoker(t *testing.T) {
	root := filepath.Join("..", "..")
	manifest, err := os.ReadFile(filepath.Join(root, "app.manifest"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(manifest)
	if !strings.Contains(text, `requestedExecutionLevel level="asInvoker"`) {
		t.Fatalf("app.manifest must keep the WebUI coordinator medium-integrity, got:\n%s", text)
	}
	if strings.Contains(text, "requireAdministrator") {
		t.Fatalf("app.manifest must not require administrator elevation:\n%s", text)
	}
}

func TestCompiledResourceEmbedsAsInvokerManifest(t *testing.T) {
	root := filepath.Join("..", "..")
	resource, err := os.ReadFile(filepath.Join(root, "app.syso"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(resource)
	if !strings.Contains(text, "asInvoker") {
		t.Fatal("app.syso does not contain the asInvoker execution level")
	}
	if strings.Contains(text, "requireAdministrator") {
		t.Fatal("app.syso contains requireAdministrator")
	}
}
