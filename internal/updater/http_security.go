package updater

import (
	"crypto/subtle"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const sessionCookieName = "WindowsUpdaterWebUI"
const trustedUIRequestHeader = "X-Windows-Updater-WebUI"

func setSecurityHeaders(w http.ResponseWriter) {
	header := w.Header()
	header.Set("Cache-Control", "no-store")
	header.Set("Pragma", "no-cache")
	header.Set("Expires", "0")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'; form-action 'self'; img-src 'self' data:; connect-src 'self'; script-src 'self'; style-src 'self'")
}

func (app *App) expectedPort() int {
	if app.listenPort != 0 {
		return app.listenPort
	}
	return defaultPort
}

func (app *App) trustedHost(r *http.Request) bool {
	if app.listenHost == "" && app.listenPort == 0 {
		return true
	}
	host := strings.TrimSpace(r.Host)
	if host == "" {
		return false
	}
	name, portText, err := net.SplitHostPort(host)
	if err != nil {
		name = host
		portText = ""
	}
	name = strings.Trim(strings.ToLower(name), "[]")
	if !isLoopbackHostName(name) {
		return false
	}
	if portText == "" {
		return app.expectedPort() == 80
	}
	port, err := strconv.Atoi(portText)
	return err == nil && port == app.expectedPort()
}

func isLoopbackHostName(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (app *App) sameOrigin(raw string) bool {
	origin, err := url.Parse(raw)
	if err != nil || origin.Scheme != "http" || origin.Host == "" {
		return false
	}
	request := &http.Request{Host: origin.Host}
	return app.trustedHost(request)
}

func constantTokenEqual(got, want string) bool {
	if got == "" || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func (app *App) sessionOK(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	return constantTokenEqual(cookie.Value, app.sessionToken)
}

func (app *App) setSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    app.sessionToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

func (app *App) consumeBootstrapToken(token string) bool {
	if !constantTokenEqual(token, app.token) {
		return false
	}
	app.mu.Lock()
	defer app.mu.Unlock()
	if app.bootstrapUsed {
		return false
	}
	app.bootstrapUsed = true
	return true
}

func cleanURLWithoutToken(r *http.Request) string {
	values := r.URL.Query()
	values.Del("token")
	next := r.URL.Path
	if next == "" {
		next = "/"
	}
	if encoded := values.Encode(); encoded != "" {
		next += "?" + encoded
	}
	return next
}

func (app *App) handleBootstrap(w http.ResponseWriter, r *http.Request) bool {
	token := r.URL.Query().Get("token")
	if token == "" || r.URL.Path != "/" {
		return false
	}
	if app.sessionOK(r) {
		http.Redirect(w, r, cleanURLWithoutToken(r), http.StatusSeeOther)
		return true
	}
	if r.Method != http.MethodGet || !app.consumeBootstrapToken(token) {
		return false
	}
	app.setSessionCookie(w)
	http.Redirect(w, r, cleanURLWithoutToken(r), http.StatusSeeOther)
	return true
}

func requestIsMutating(r *http.Request) bool {
	return r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions
}

func (app *App) requestBoundaryOK(w http.ResponseWriter, r *http.Request) bool {
	if !requestIsMutating(r) {
		return true
	}
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" && !app.sameOrigin(origin) {
		if !(strings.EqualFold(origin, "null") && app.trustedUIRequest(r)) {
			writeAPIError(w, http.StatusForbidden, "forbidden origin")
			return false
		}
	}
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))) {
	case "", "same-origin", "none":
		return true
	default:
		writeAPIError(w, http.StatusForbidden, "forbidden fetch context")
		return false
	}
}

func (app *App) trustedUIRequest(r *http.Request) bool {
	return r.Header.Get(trustedUIRequestHeader) == "1" && app.trustedHost(r)
}
