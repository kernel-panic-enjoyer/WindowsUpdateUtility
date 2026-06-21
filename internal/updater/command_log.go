package updater

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"
)

type LogEntry struct {
	ID         int64    `json:"id"`
	Timestamp  string   `json:"timestamp"`
	Stream     string   `json:"stream"`
	Message    string   `json:"message"`
	Categories []string `json:"categories,omitempty"`
}

type LogBuffer struct {
	mu         sync.Mutex
	nextID     int64
	entries    []LogEntry
	totalBytes int
	maxEntries int
	maxBytes   int
}

var sessionLogs = &LogBuffer{}

const (
	defaultLogMaxEntries = 5000
	defaultLogMaxBytes   = 2 * 1024 * 1024

	logCategoryAll         = "all"
	logCategoryApplication = "application"
	logCategorySearches    = "searches"
	logCategoryUpdates     = "updates"
	logCategoryWinget      = "winget"
	logCategoryStore       = "store"
	logCategoryChocolatey  = "chocolatey"
)

type LogCategorySpec struct {
	Category string
	Filename string
	Label    string
}

var logCategorySpecs = []LogCategorySpec{
	{logCategoryAll, "all.txt", "All"},
	{logCategoryApplication, "application.txt", "Application"},
	{logCategorySearches, "searches.txt", "Searches"},
	{logCategoryUpdates, "updates.txt", "Updates"},
	{logCategoryWinget, "winget.txt", "winget"},
	{logCategoryStore, "store.txt", "Store"},
	{logCategoryChocolatey, "chocolatey.txt", "Chocolatey"},
}

func logCategoriesForCommand(args []string) []string {
	manager, verb := packageManagerCommandVerb(args)
	return logCategoriesForManagerVerb(manager, verb)
}

func logCategoriesForManagerVerb(manager, verb string) []string {
	categories := []string{logCategoryAll}
	switch strings.TrimSuffix(strings.ToLower(manager), ".exe") {
	case managerWinget:
		categories = append(categories, logCategoryWinget)
	case managerStore:
		categories = append(categories, logCategoryStore)
	case managerChoco:
		categories = append(categories, logCategoryChocolatey)
	default:
		categories = append(categories, logCategoryApplication)
	}

	switch strings.ToLower(verb) {
	case "search", "find":
		categories = append(categories, logCategorySearches)
	case "install", "upgrade", "update", "updates", "outdated", "import", "configure", "pin", "uninstall":
		categories = append(categories, logCategoryUpdates)
	}
	return normalizeLogCategories(categories)
}

func normalizeLogCategories(categories []string) []string {
	seen := map[string]bool{}
	normalized := []string{}
	add := func(category string) {
		category = strings.TrimSpace(strings.ToLower(category))
		if category == "" || seen[category] {
			return
		}
		seen[category] = true
		normalized = append(normalized, category)
	}

	add(logCategoryAll)
	for _, category := range categories {
		add(category)
	}
	if len(normalized) == 1 {
		add(logCategoryApplication)
	}
	return normalized
}

func logEntryInCategory(entry LogEntry, category string) bool {
	category = strings.ToLower(strings.TrimSpace(category))
	for _, candidate := range normalizeLogCategories(entry.Categories) {
		if candidate == category {
			return true
		}
	}
	return false
}

func (buffer *LogBuffer) Append(stream, message string) LogEntry {
	return buffer.AppendCategorized(stream, message, nil)
}

func (buffer *LogBuffer) AppendCategorized(stream, message string, categories []string) LogEntry {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()

	buffer.nextID++
	entry := LogEntry{
		ID:         buffer.nextID,
		Timestamp:  utcNow(),
		Stream:     stream,
		Message:    strings.TrimRight(message, "\r\n"),
		Categories: normalizeLogCategories(categories),
	}
	buffer.entries = append(buffer.entries, entry)
	buffer.totalBytes += logEntrySize(entry)
	buffer.trimLocked()
	return entry
}

func (buffer *LogBuffer) limits() (int, int) {
	maxEntries := buffer.maxEntries
	if maxEntries <= 0 {
		maxEntries = defaultLogMaxEntries
	}
	maxBytes := buffer.maxBytes
	if maxBytes <= 0 {
		maxBytes = defaultLogMaxBytes
	}
	return maxEntries, maxBytes
}

func logEntrySize(entry LogEntry) int {
	size := len(entry.Timestamp) + len(entry.Stream) + len(entry.Message) + 32
	for _, category := range entry.Categories {
		size += len(category) + 1
	}
	return size
}

func (buffer *LogBuffer) trimLocked() {
	maxEntries, maxBytes := buffer.limits()
	for len(buffer.entries) > 0 && (len(buffer.entries) > maxEntries || buffer.totalBytes > maxBytes) {
		buffer.totalBytes -= logEntrySize(buffer.entries[0])
		copy(buffer.entries[0:], buffer.entries[1:])
		buffer.entries = buffer.entries[:len(buffer.entries)-1]
	}
	if buffer.totalBytes < 0 {
		buffer.totalBytes = 0
	}
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

func (buffer *LogBuffer) Snapshot() []LogEntry {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()

	entries := make([]LogEntry, len(buffer.entries))
	copy(entries, buffer.entries)
	return entries
}

func appLog(format string, args ...any) {
	sessionLogs.Append("app", fmt.Sprintf(format, args...))
}

func appendLogLineCategorized(stream, line string, categories []string) {
	line = strings.TrimRight(line, "\r\n")
	if isTransientLogFrame(line) {
		return
	}
	sessionLogs.AppendCategorized(stream, line, categories)
}

func isTransientLogFrame(line string) bool {
	switch strings.TrimSpace(line) {
	case "|", "/", `\`, "-":
		return true
	default:
		return false
	}
}

func appendLogChunkCategorized(stream, pending, chunk string, categories []string) string {
	text := pending + chunk
	holdCR := strings.HasSuffix(text, "\r")
	if holdCR {
		text = strings.TrimSuffix(text, "\r")
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")

	var line strings.Builder
	for _, char := range text {
		switch char {
		case '\n':
			appendLogLineCategorized(stream, line.String(), categories)
			line.Reset()
		case '\r':
			line.Reset()
		default:
			line.WriteRune(char)
		}
	}
	if holdCR {
		return line.String() + "\r"
	}
	return line.String()
}

func streamCommandOutputCategorized(reader io.Reader, stream string, output *bytes.Buffer, wg *sync.WaitGroup, categories []string) {
	defer wg.Done()

	pending := ""
	buffer := make([]byte, 4096)
	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			chunk := string(buffer[:n])
			output.WriteString(chunk)
			pending = appendLogChunkCategorized(stream, pending, chunk, categories)
		}
		if err != nil {
			if err != io.EOF {
				appLog("Error reading %s stream: %s", stream, err)
			}
			break
		}
	}
	if pending != "" {
		appendLogLineCategorized(stream, strings.TrimSuffix(pending, "\r"), categories)
	}
}
