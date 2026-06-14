package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	appName     = "Windows Updater WebUI"
	appDirName  = "WindowsUpdaterWebUI"
	defaultHost = "127.0.0.1"
	defaultPort = 4183
)

func randomToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
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
	token := os.Getenv("UPDATER_TOKEN")
	if token == "" {
		token = randomToken()
	}
	port := freePort(defaultPort)
	if override := os.Getenv("UPDATER_PORT"); override != "" {
		if parsed, err := strconv.Atoi(override); err == nil && parsed > 0 && parsed < 65536 {
			port = parsed
		}
	}
	app := &App{token: token}
	mux := http.NewServeMux()
	mux.HandleFunc("/", app.serveHTTP)
	server := &http.Server{Addr: fmt.Sprintf("%s:%d", defaultHost, port), Handler: mux}
	app.server = server
	app.refreshStatus(true)
	app.refreshInventory(true)

	url := fmt.Sprintf("http://%s:%d/?token=%s", defaultHost, port, token)
	appLog("Server listening on http://%s:%d.", defaultHost, port)
	tray, err := startTray(app, url)
	if err != nil {
		appLog("Tray icon could not be started: %s", err)
	} else {
		defer tray.Stop()
	}
	if !noBrowser {
		_ = openURL(url)
	} else {
		fmt.Println(url)
	}
	return server.ListenAndServe()
}

func hasArg(name string) bool {
	for _, arg := range os.Args[1:] {
		if arg == name {
			return true
		}
	}
	return false
}

func main() {
	enableDPIAwareness()

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

	if !hasArg("--no-elevate") && !isAdmin() {
		exe, _ := os.Executable()
		var params []string
		for _, arg := range os.Args[1:] {
			if arg != "--no-elevate" {
				params = append(params, quoteArg(arg))
			}
		}
		if err := shellExecuteRunas(exe, strings.Join(params, " ")); err == nil {
			return
		}
	}

	if err := runServer(hasArg("--no-browser")); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, err)
	}
}
