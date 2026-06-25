package updater

import (
	"errors"
	"net"
	"net/http"
	"os"
	"strconv"
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

func TestParseCLIRejectsNoElevateContract(t *testing.T) {
	_, err := parseCLI([]string{"--no-elevate"})
	if err == nil || !strings.Contains(err.Error(), "--no-elevate is not supported") {
		t.Fatalf("expected unsupported --no-elevate error, got %v", err)
	}
}

func TestParseCLIHelpAndServerOptions(t *testing.T) {
	help, err := parseCLI([]string{"--help"})
	if err != nil || help.Mode != cliModeHelp {
		t.Fatalf("unexpected help parse: %#v err=%v", help, err)
	}
	options, err := parseCLI([]string{"--no-browser", "--port", "4299", "--token=abc"})
	if err != nil {
		t.Fatalf("parse server options: %v", err)
	}
	if !options.NoBrowser || !options.PortSet || options.Port != 4299 || options.Token != "abc" {
		t.Fatalf("unexpected server options: %#v", options)
	}
	if !strings.Contains(helpText(), "--no-browser") || strings.Contains(helpText(), "--no-elevate") {
		t.Fatalf("ordinary help should document supported user options only:\n%s", helpText())
	}
}

func TestListenForServerExplicitRequestedPortOccupied(t *testing.T) {
	occupied := listenOnLocalhost(t, 0)
	defer occupied.Close()
	port := listenerTestPort(t, occupied)
	listener, err := listenForServer(defaultHost, port, true)
	if err == nil {
		listener.Close()
		t.Fatal("expected explicit occupied port to fail")
	}
	if !strings.Contains(err.Error(), strconv.Itoa(port)) {
		t.Fatalf("error should name occupied port %d: %v", port, err)
	}
}

func TestListenForServerDefaultFallbackRetainsListener(t *testing.T) {
	first := listenOnLocalhost(t, 0)
	defer first.Close()
	start := listenerTestPort(t, first)
	listener, err := listenForServer(defaultHost, start, false)
	if err != nil {
		t.Fatalf("fallback listen failed: %v", err)
	}
	defer listener.Close()
	if got := listenerTestPort(t, listener); got == start {
		t.Fatalf("fallback reused occupied start port %d", start)
	}
	if second, err := net.Listen("tcp", listener.Addr().String()); err == nil {
		second.Close()
		t.Fatalf("fallback listener was not retained on %s", listener.Addr())
	}
}

func TestServerURLUsesBoundPort(t *testing.T) {
	listener := listenOnLocalhost(t, 0)
	defer listener.Close()
	port := listenerTestPort(t, listener)
	url := serverURL(defaultHost, port, "token")
	if !strings.Contains(url, "127.0.0.1:"+strconv.Itoa(port)) {
		t.Fatalf("URL did not use bound port %d: %s", port, url)
	}
}

func TestRunServerDoesNotStartTrayOrBrowserAfterBindFailure(t *testing.T) {
	occupied := listenOnLocalhost(t, 0)
	defer occupied.Close()
	port := listenerTestPort(t, occupied)
	var trayStarted, browserOpened bool
	err := runServerWithOptions(cliOptions{Mode: cliModeServer, NoBrowser: false, Token: "abc", Port: port, PortSet: true}, serverHooks{
		startTray: func(*App, string) (trayController, error) {
			trayStarted = true
			return nil, nil
		},
		openURL: func(string) error {
			browserOpened = true
			return nil
		},
	})
	if err == nil {
		t.Fatal("expected bind failure")
	}
	if trayStarted || browserOpened {
		t.Fatalf("tray/browser started after bind failure: tray=%t browser=%t", trayStarted, browserOpened)
	}
}

func TestServerShutdownClosesRetainedListener(t *testing.T) {
	listener, err := listenForServer(defaultHost, 0, true)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := listenerTestPort(t, listener)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	if err := server.Close(); err != nil {
		t.Fatalf("server close: %v", err)
	}
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("unexpected serve error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not exit after close")
	}
	rebound, err := net.Listen("tcp", net.JoinHostPort(defaultHost, strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("listener was not released after shutdown: %v", err)
	}
	rebound.Close()
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

func listenOnLocalhost(t *testing.T, port int) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", net.JoinHostPort(defaultHost, strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("listen localhost:%d: %v", port, err)
	}
	return listener
}

func listenerTestPort(t *testing.T, listener net.Listener) int {
	t.Helper()
	port := listenerPort(listener)
	if port == 0 {
		t.Fatalf("could not resolve listener port from %s", listener.Addr())
	}
	return port
}
