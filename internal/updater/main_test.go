package updater

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
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

func TestRandomTokenFailsClosedWhenCryptoRandomFails(t *testing.T) {
	oldRead := cryptoRandomRead
	cryptoRandomRead = func([]byte) (int, error) {
		return 0, errors.New("entropy unavailable")
	}
	defer func() { cryptoRandomRead = oldRead }()

	token, err := randomToken()
	if err == nil || !strings.Contains(err.Error(), "entropy unavailable") {
		t.Fatalf("expected crypto failure, token=%q err=%v", token, err)
	}
	if token != "" {
		t.Fatalf("expected no fallback token, got %q", token)
	}
}

func TestShutdownSignalWatcherRunsCleanup(t *testing.T) {
	app := &App{}
	cleanupDone := make(chan struct{})
	app.addShutdownCleanup(func() {
		close(cleanupDone)
	})
	signals := make(chan os.Signal, 1)
	stop := app.watchShutdownSignals(signals)
	defer stop()

	signals <- os.Interrupt

	select {
	case <-cleanupDone:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown signal did not run registered cleanup")
	}
}
