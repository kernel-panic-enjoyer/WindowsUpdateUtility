package updater

import (
	"os"
	"testing"
)

func TestArgValueParsesEqualsAndSeparatedForms(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	os.Args = []string{"updater", "--token=abc", "--port", "4299"}
	if got, ok := argValue("--token"); !ok || got != "abc" {
		t.Fatalf("unexpected token arg: %q %t", got, ok)
	}
	if got, ok := argValue("--port"); !ok || got != "4299" {
		t.Fatalf("unexpected port arg: %q %t", got, ok)
	}
	if got, ok := argValue("--missing"); ok || got != "" {
		t.Fatalf("unexpected missing arg: %q %t", got, ok)
	}
}
