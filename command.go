package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	createNoWindow = 0x08000000
	logEntryLimit  = 2000
)

type CommandResult struct {
	OK      bool   `json:"ok"`
	Code    int    `json:"code"`
	Stdout  string `json:"stdout"`
	Stderr  string `json:"stderr"`
	Command string `json:"command"`
}

type LogEntry struct {
	ID        int64  `json:"id"`
	Timestamp string `json:"timestamp"`
	Stream    string `json:"stream"`
	Message   string `json:"message"`
}

type LogBuffer struct {
	mu      sync.Mutex
	nextID  int64
	max     int
	entries []LogEntry
}

var sessionLogs = newLogBuffer(logEntryLimit)
var wingetCommandMu sync.Mutex

func newLogBuffer(max int) *LogBuffer {
	if max <= 0 {
		max = logEntryLimit
	}
	return &LogBuffer{max: max}
}

func (buffer *LogBuffer) Append(stream, message string) LogEntry {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()

	buffer.nextID++
	entry := LogEntry{
		ID:        buffer.nextID,
		Timestamp: utcNow(),
		Stream:    stream,
		Message:   strings.TrimRight(message, "\r\n"),
	}
	buffer.entries = append(buffer.entries, entry)
	if len(buffer.entries) > buffer.max {
		overflow := len(buffer.entries) - buffer.max
		copy(buffer.entries, buffer.entries[overflow:])
		buffer.entries = buffer.entries[:buffer.max]
	}
	return entry
}

func (buffer *LogBuffer) Since(since int64) []LogEntry {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()

	start := 0
	if since > 0 {
		for start < len(buffer.entries) && buffer.entries[start].ID <= since {
			start++
		}
	}
	entries := make([]LogEntry, len(buffer.entries[start:]))
	copy(entries, buffer.entries[start:])
	return entries
}

func (buffer *LogBuffer) LatestID() int64 {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.nextID
}

func appLog(format string, args ...any) {
	sessionLogs.Append("app", fmt.Sprintf(format, args...))
}

func hiddenSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

func appendLogChunk(stream, pending, chunk string) string {
	normalized := strings.ReplaceAll(chunk, "\r", "\n")
	parts := strings.Split(pending+normalized, "\n")
	for _, line := range parts[:len(parts)-1] {
		sessionLogs.Append(stream, line)
	}
	return parts[len(parts)-1]
}

func streamCommandOutput(reader io.Reader, stream string, output *bytes.Buffer, wg *sync.WaitGroup) {
	defer wg.Done()

	pending := ""
	buffer := make([]byte, 4096)
	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			chunk := string(buffer[:n])
			output.WriteString(chunk)
			pending = appendLogChunk(stream, pending, chunk)
		}
		if err != nil {
			if err != io.EOF {
				appLog("Error reading %s stream: %s", stream, err)
			}
			break
		}
	}
	if pending != "" {
		sessionLogs.Append(stream, pending)
	}
}

func isWingetCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	name := strings.ToLower(filepath.Base(args[0]))
	if name == "winget" || name == "winget.exe" {
		return true
	}
	if name == "cmd.exe" && len(args) >= 4 && strings.EqualFold(args[1], "/d") && strings.EqualFold(args[2], "/c") && strings.EqualFold(args[3], "winget") {
		return true
	}
	return false
}

func isStoreCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	name := strings.ToLower(filepath.Base(args[0]))
	if name == "store" || name == "store.exe" {
		return true
	}
	return name == "cmd.exe" && len(args) >= 4 && strings.EqualFold(args[1], "/d") && strings.EqualFold(args[2], "/c") && strings.EqualFold(args[3], "store")
}

func runCommand(timeout time.Duration, args ...string) CommandResult {
	result := CommandResult{Command: strings.Join(args, " ")}
	if len(args) == 0 {
		result.Stderr = "empty command"
		result.Code = 127
		sessionLogs.Append("command", "<empty command>")
		sessionLogs.Append("stderr", result.Stderr)
		sessionLogs.Append("exit", "empty command exited with code 127")
		return result
	}
	if isWingetCommand(args) {
		wingetCommandMu.Lock()
		defer wingetCommandMu.Unlock()
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Env = launchEnv()
	cmd.SysProcAttr = hiddenSysProcAttr()
	var stdout, stderr bytes.Buffer
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		result.Code = 127
		result.Stderr = err.Error()
		sessionLogs.Append("command", result.Command)
		sessionLogs.Append("stderr", result.Stderr)
		sessionLogs.Append("exit", fmt.Sprintf("%s exited with code 127", result.Command))
		return result
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		result.Code = 127
		result.Stderr = err.Error()
		sessionLogs.Append("command", result.Command)
		sessionLogs.Append("stderr", result.Stderr)
		sessionLogs.Append("exit", fmt.Sprintf("%s exited with code 127", result.Command))
		return result
	}

	sessionLogs.Append("command", result.Command)
	if err := cmd.Start(); err != nil {
		result.Code = 127
		result.Stderr = err.Error()
		sessionLogs.Append("stderr", result.Stderr)
		sessionLogs.Append("exit", fmt.Sprintf("%s exited with code 127", result.Command))
		return result
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go streamCommandOutput(stdoutPipe, "stdout", &stdout, &wg)
	go streamCommandOutput(stderrPipe, "stderr", &stderr, &wg)
	err = cmd.Wait()
	wg.Wait()

	result.Stdout = stdout.String()
	result.Stderr = stderr.String()
	if ctx.Err() == context.DeadlineExceeded {
		result.Code = 124
		result.Stderr += "\nTimed out."
		sessionLogs.Append("stderr", "Timed out.")
		sessionLogs.Append("exit", fmt.Sprintf("%s exited with code 124", result.Command))
		return result
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.Code = exitErr.ExitCode()
		} else {
			result.Code = 127
			if result.Stderr == "" {
				result.Stderr = err.Error()
			}
		}
		sessionLogs.Append("exit", fmt.Sprintf("%s exited with code %d", result.Command, result.Code))
		return result
	}
	result.OK = true
	sessionLogs.Append("exit", fmt.Sprintf("%s exited with code 0", result.Command))
	return result
}

func launchEnv() []string {
	env := os.Environ()
	path := os.Getenv("PATH")
	additions := []string{}
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		additions = append(additions,
			filepath.Join(local, "Microsoft", "WindowsApps"),
			filepath.Join(local, "Microsoft", "WinGet", "Links"),
		)
	}
	for _, addition := range additions {
		if _, err := os.Stat(addition); err == nil && !strings.Contains(strings.ToLower(path), strings.ToLower(addition)) {
			path = addition + string(os.PathListSeparator) + path
		}
	}
	env = append(env, "PATH="+path)
	return env
}

func resolveExecutable(name string) string {
	if override := os.Getenv("UPDATER_" + strings.ToUpper(name) + "_PATH"); override != "" {
		return override
	}
	if found, err := exec.LookPath(name); err == nil {
		return found
	}
	if strings.EqualFold(name, "winget") || strings.EqualFold(name, "store") {
		exeName := name
		if !strings.HasSuffix(strings.ToLower(exeName), ".exe") {
			exeName += ".exe"
		}
		var candidates []string
		if root := os.Getenv("SystemRoot"); root != "" {
			candidates = append(candidates, filepath.Join(root, "System32", exeName), filepath.Join(root, "Sysnative", exeName))
		}
		for _, env := range []string{"LOCALAPPDATA", "USERPROFILE"} {
			value := os.Getenv(env)
			if value == "" {
				continue
			}
			base := value
			if env == "USERPROFILE" {
				base = filepath.Join(value, "AppData", "Local")
			}
			candidates = append(candidates,
				filepath.Join(base, "Microsoft", "WindowsApps", exeName),
				filepath.Join(base, "Microsoft", "WinGet", "Links", exeName),
			)
		}
		for _, candidate := range candidates {
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
	}
	return name
}

func managerCommand(manager string, args ...string) []string {
	resolved := resolveExecutable(manager)
	if resolved != manager {
		return append([]string{resolved}, args...)
	}
	if manager == "winget" || manager == "store" {
		return append([]string{"cmd.exe", "/d", "/c", manager}, args...)
	}
	return append([]string{manager}, args...)
}
