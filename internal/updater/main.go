package updater

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
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
	appName         = "Windows Updater WebUI"
	appDirName      = "WindowsUpdaterWebUI"
	defaultHost     = "127.0.0.1"
	defaultPort     = 4183
	portSearchLimit = 50

	flagHelp           = "--help"
	flagNoBrowser      = "--no-browser"
	flagPort           = "--port"
	flagToken          = "--token"
	flagTask           = "--task"
	flagElevatedWorker = "--elevated-worker"
	flagNoElevate      = "--no-elevate"
)

var cryptoRandomRead = rand.Read

func randomToken() (string, error) {
	b := make([]byte, 24)
	if _, err := cryptoRandomRead(b); err != nil {
		return "", fmt.Errorf("generate secure token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

type cliMode string

const (
	cliModeServer         cliMode = "server"
	cliModeHelp           cliMode = "help"
	cliModeAutoUpdate     cliMode = "auto-update"
	cliModeElevatedWorker cliMode = "elevated-worker"
	cliModeStoreInventory cliMode = "store-inventory-worker"
)

type cliOptions struct {
	Mode      cliMode
	NoBrowser bool
	Token     string
	Port      int
	PortSet   bool
}

type trayController interface {
	Stop()
}

type serverHooks struct {
	startTray func(*App, string) (trayController, error)
	openURL   func(string) error
}

var productionServerHooks = serverHooks{
	startTray: func(app *App, url string) (trayController, error) {
		return startTray(app, url)
	},
	openURL: openURL,
}

func parseCLI(args []string) (cliOptions, error) {
	options := cliOptions{Mode: cliModeServer}
	set := flag.NewFlagSet("WindowsUpdaterWebUI", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	help := set.Bool("help", false, "")
	set.BoolVar(help, "h", false, "")
	noBrowser := set.Bool(strings.TrimPrefix(flagNoBrowser, "--"), false, "")
	token := set.String(strings.TrimPrefix(flagToken, "--"), "", "")
	port := set.Int(strings.TrimPrefix(flagPort, "--"), 0, "")
	task := set.String(strings.TrimPrefix(flagTask, "--"), "", "")
	elevatedWorker := set.Bool(strings.TrimPrefix(flagElevatedWorker, "--"), false, "")
	storeInventoryWorker := set.Bool(strings.TrimPrefix(storeInventoryWorkerFlag, "--"), false, "")
	noElevate := set.Bool(strings.TrimPrefix(flagNoElevate, "--"), false, "")
	if err := set.Parse(args); err != nil {
		return options, err
	}
	if *noElevate {
		return options, fmt.Errorf("%s is not supported; the WebUI starts asInvoker and elevates only individual privileged actions", flagNoElevate)
	}
	if *help {
		options.Mode = cliModeHelp
		return options, nil
	}
	if *storeInventoryWorker {
		options.Mode = cliModeStoreInventory
		return options, nil
	}
	if *elevatedWorker {
		options.Mode = cliModeElevatedWorker
		return options, nil
	}
	if strings.EqualFold(strings.TrimSpace(*task), "auto-update") {
		options.Mode = cliModeAutoUpdate
		return options, nil
	}
	if strings.TrimSpace(*task) != "" {
		return options, fmt.Errorf("unsupported task %q", *task)
	}
	options.NoBrowser = *noBrowser
	options.Token = strings.TrimSpace(*token)
	if *port != 0 {
		if *port < 1 || *port > 65535 {
			return options, fmt.Errorf("port must be between 1 and 65535")
		}
		options.Port = *port
		options.PortSet = true
	}
	return options, nil
}

func helpText() string {
	return strings.TrimSpace(`WindowsUpdaterWebUI

Usage:
  WindowsUpdaterWebUI.exe [--no-browser] [--port PORT] [--token TOKEN]
  WindowsUpdaterWebUI.exe --task auto-update

Options:
  --no-browser   Start the local WebUI without opening a browser. Prints the URL.
  --port PORT    Bind the WebUI to this local TCP port. Fails if unavailable.
  --token TOKEN  Use a caller-provided bootstrap token instead of generating one.
  --help, -h     Show this help.

Internal unsupported modes:
  --elevated-worker and --store-inventory-worker are implementation details for
  privileged package actions and isolated current-user Store inventory.`) + "\n"
}

func listenerPort(listener net.Listener) int {
	if listener == nil {
		return 0
	}
	if tcp, ok := listener.Addr().(*net.TCPAddr); ok {
		return tcp.Port
	}
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return 0
	}
	port, _ := strconv.Atoi(portText)
	return port
}

func listenForServer(host string, requestedPort int, explicit bool) (net.Listener, error) {
	if explicit {
		listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(requestedPort)))
		if err != nil {
			return nil, fmt.Errorf("bind %s:%d: %w", host, requestedPort, err)
		}
		return listener, nil
	}
	for port := requestedPort; port < requestedPort+portSearchLimit; port++ {
		listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err == nil {
			return listener, nil
		}
	}
	return nil, fmt.Errorf("no available local port in %d-%d", requestedPort, requestedPort+portSearchLimit-1)
}

func serverURL(host string, port int, token string) string {
	return fmt.Sprintf("http://%s/?token=%s", net.JoinHostPort(host, strconv.Itoa(port)), token)
}

func runServer(noBrowser bool) error {
	options, err := parseCLI(os.Args[1:])
	if err != nil {
		return err
	}
	options.NoBrowser = noBrowser
	return runServerWithOptions(options, productionServerHooks)
}

func runServerWithOptions(options cliOptions, hooks serverHooks) error {
	token := options.Token
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
	port := options.Port
	portSet := options.PortSet
	if !portSet {
		if override := os.Getenv("UPDATER_PORT"); override != "" {
			parsed, err := strconv.Atoi(override)
			if err != nil || parsed < 1 || parsed > 65535 {
				return fmt.Errorf("invalid UPDATER_PORT %q", override)
			}
			port = parsed
			portSet = true
		}
	}
	if port == 0 {
		port = defaultPort
	}
	listener, err := listenForServer(defaultHost, port, portSet)
	if err != nil {
		return err
	}
	defer listener.Close()
	actualPort := listenerPort(listener)
	sessionToken, err := randomToken()
	if err != nil {
		return err
	}
	app := &App{token: token, sessionToken: sessionToken, listenHost: defaultHost, listenPort: actualPort, storeBackgroundScanEnabled: true}
	defer func() {
		app.beginShutdown()
		if !app.waitForBackgroundWork(gracefulShutdownTimeout) {
			appLog("Server exit timed out waiting for background work.")
		}
		app.runShutdownCleanups()
	}()
	stopSignalWatcher := app.startShutdownSignalWatcher()
	defer stopSignalWatcher()

	mux := http.NewServeMux()
	mux.HandleFunc("/", app.serveHTTP)
	server := &http.Server{
		Addr:              listener.Addr().String(),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	app.server = server
	app.refreshStatus(true)
	app.refreshInventory(true)

	url := serverURL(defaultHost, actualPort, token)
	appLog("Server listening on http://%s.", net.JoinHostPort(defaultHost, strconv.Itoa(actualPort)))
	if hooks.startTray != nil {
		tray, err := hooks.startTray(app, url)
		if err != nil {
			appLog("Tray icon could not be started: %s", err)
		} else {
			app.addShutdownCleanup(tray.Stop)
		}
	}
	if !options.NoBrowser {
		if hooks.openURL != nil {
			_ = hooks.openURL(url)
		}
	} else {
		fmt.Println(url)
	}
	return server.Serve(listener)
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

	options, err := parseCLI(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	switch options.Mode {
	case cliModeHelp:
		fmt.Print(helpText())
		return
	case cliModeStoreInventory:
		os.Exit(runStoreInventoryWorkerFromArgs())
	case cliModeElevatedWorker:
		if err := runElevatedWorkerFromArgs(); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		return
	case cliModeAutoUpdate:
		results := runAutoUpdate()
		data, _ := json.MarshalIndent(results, "", "  ")
		fmt.Println(string(data))
		return
	}

	if err := runServerWithOptions(options, productionServerHooks); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, err)
	}
}
