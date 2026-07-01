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

func (app *App) expectedListenPort() int {
	if app.listenPort != 0 {
		return app.listenPort
	}
	return defaultPort
}

func (app *App) trustedHost(r *http.Request) bool {
	if app.listenHost == "" && app.listenPort == 0 {
		return true
	}
	hostHeader := strings.TrimSpace(r.Host)
	if hostHeader == "" {
		return false
	}
	hostName, portText, err := net.SplitHostPort(hostHeader)
	if err != nil {
		hostName = hostHeader
		portText = ""
	}
	hostName = strings.Trim(strings.ToLower(hostName), "[]")
	if !isLoopbackHostName(hostName) {
		return false
	}
	expectedPort := app.expectedListenPort()
	if portText == "" {
		return expectedPort == 80
	}
	requestPort, err := strconv.Atoi(portText)
	return err == nil && requestPort == expectedPort
}

func isLoopbackHostName(hostName string) bool {
	switch hostName {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	ip := net.ParseIP(hostName)
	return ip != nil && ip.IsLoopback()
}

func (app *App) sameOrigin(originHeader string) bool {
	originURL, err := url.Parse(originHeader)
	if err != nil || originURL.Scheme != "http" || originURL.Host == "" {
		return false
	}
	request := &http.Request{Host: originURL.Host}
	return app.trustedHost(request)
}

func tokensEqualConstantTime(providedToken, expectedToken string) bool {
	if providedToken == "" || expectedToken == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(providedToken), []byte(expectedToken)) == 1
}

func (app *App) sessionOK(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	return tokensEqualConstantTime(cookie.Value, app.sessionToken)
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
	if !tokensEqualConstantTime(token, app.token) {
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

func redirectPathWithoutBootstrapToken(r *http.Request) string {
	query := r.URL.Query()
	query.Del("token")
	redirectPath := r.URL.Path
	if redirectPath == "" {
		redirectPath = "/"
	}
	if encodedQuery := query.Encode(); encodedQuery != "" {
		redirectPath += "?" + encodedQuery
	}
	return redirectPath
}

func (app *App) handleBootstrap(w http.ResponseWriter, r *http.Request) bool {
	bootstrapToken := r.URL.Query().Get("token")
	if bootstrapToken == "" || r.URL.Path != "/" {
		return false
	}
	redirectPath := redirectPathWithoutBootstrapToken(r)
	if app.sessionOK(r) {
		http.Redirect(w, r, redirectPath, http.StatusSeeOther)
		return true
	}
	if r.Method != http.MethodGet || !app.consumeBootstrapToken(bootstrapToken) {
		return false
	}
	app.setSessionCookie(w)
	http.Redirect(w, r, redirectPath, http.StatusSeeOther)
	return true
}

func isMutatingRequest(r *http.Request) bool {
	return r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions
}

func (app *App) requestBoundaryOK(w http.ResponseWriter, r *http.Request) bool {
	if !isMutatingRequest(r) {
		return true
	}
	if originHeader := strings.TrimSpace(r.Header.Get("Origin")); originHeader != "" && !app.sameOrigin(originHeader) {
		trustedNullOriginRequest := strings.EqualFold(originHeader, "null") && app.trustedUIRequest(r)
		if !trustedNullOriginRequest {
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
