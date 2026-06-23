package updater

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	appName     = "Windows Updater WebUI"
	appDirName  = "WindowsUpdaterWebUI"
	defaultHost = "127.0.0.1"
	defaultPort = 4183
)

var cryptoRandomRead = rand.Read

func randomToken() (string, error) {
	b := make([]byte, 24)
	if _, err := cryptoRandomRead(b); err != nil {
		return "", fmt.Errorf("generate secure token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func freePort(start int) int {
	for port := start; port < start+50; port++ {
		listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", defaultHost, port))
		if err == nil {
			_ = listener.Close()
			return port
		}
	}
	return start
}

func runServer(noBrowser bool) error {
	token, _ := argValue("--token")
	if token == "" {
		token = os.Getenv("UPDATER_TOKEN")
	}
	if token == "" {
		generated, err := randomToken()
		if err != nil {
			return err
		}
		token = generated
	}
	port := freePort(defaultPort)
	if override, ok := argValue("--port"); ok {
		if parsed, err := strconv.Atoi(override); err == nil && parsed > 0 && parsed < 65536 {
			port = parsed
		}
	} else if override := os.Getenv("UPDATER_PORT"); override != "" {
		if parsed, err := strconv.Atoi(override); err == nil && parsed > 0 && parsed < 65536 {
			port = parsed
		}
	}
	sessionToken, err := randomToken()
	if err != nil {
		return err
	}
	app := &App{token: token, sessionToken: sessionToken, listenHost: defaultHost, listenPort: port, storeBackgroundScanEnabled: true}
	defer app.runShutdownCleanups()
	stopSignalWatcher := app.startShutdownSignalWatcher()
	defer stopSignalWatcher()

	mux := http.NewServeMux()
	mux.HandleFunc("/", app.serveHTTP)
	server := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", defaultHost, port),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	app.server = server
	app.refreshStatus(true)
	app.refreshInventory(true)

	url := fmt.Sprintf("http://%s:%d/?token=%s", defaultHost, port, token)
	appLog("Server listening on http://%s:%d.", defaultHost, port)
	tray, err := startTray(app, url)
	if err != nil {
		appLog("Tray icon could not be started: %s", err)
	} else {
		app.addShutdownCleanup(tray.Stop)
	}
	if !noBrowser {
		_ = openURL(url)
	} else {
		fmt.Println(url)
	}
	return server.ListenAndServe()
}

func (app *App) startShutdownSignalWatcher() func() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	stopWatcher := app.watchShutdownSignals(signals)
	var stopOnce sync.Once
	return func() {
		stopOnce.Do(func() {
			signal.Stop(signals)
			stopWatcher()
		})
	}
}

func (app *App) watchShutdownSignals(signals <-chan os.Signal) func() {
	done := make(chan struct{})
	var stopOnce sync.Once
	go func() {
		select {
		case <-signals:
			app.requestShutdown("OS signal")
		case <-done:
		}
	}()
	return func() {
		stopOnce.Do(func() {
			close(done)
		})
	}
}

func hasArg(name string) bool {
	for _, arg := range os.Args[1:] {
		if arg == name {
			return true
		}
	}
	return false
}

func argValue(name string) (string, bool) {
	prefix := name + "="
	for i, arg := range os.Args[1:] {
		if strings.HasPrefix(arg, prefix) {
			return strings.TrimPrefix(arg, prefix), true
		}
		if arg == name && i+2 < len(os.Args) {
			return os.Args[i+2], true
		}
	}
	return "", false
}

func Main() {
	enableDPIAwareness()

	if hasArg("--elevated-worker") {
		if err := runElevatedWorkerFromArgs(); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		return
	}

	if hasArg("--task") {
		for i, arg := range os.Args {
			if arg == "--task" && i+1 < len(os.Args) && os.Args[i+1] == "auto-update" {
				results := runAutoUpdate()
				data, _ := json.MarshalIndent(results, "", "  ")
				fmt.Println(string(data))
				return
			}
		}
	}

	if err := runServer(hasArg("--no-browser")); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, err)
	}
}
