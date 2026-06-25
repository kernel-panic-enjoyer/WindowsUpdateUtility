package updater

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
)

type LogEntry struct {
	ID         int64    `json:"id"`
	Timestamp  string   `json:"timestamp"`
	Stream     string   `json:"stream"`
	Message    string   `json:"message"`
	Categories []string `json:"categories,omitempty"`
	JobID      string   `json:"job_id,omitempty"`
	JobType    string   `json:"job_type,omitempty"`
	PackageKey string   `json:"package_key,omitempty"`
	Manager    string   `json:"manager,omitempty"`
	Activity   string   `json:"activity,omitempty"`
	CommandID  string   `json:"command_id,omitempty"`
	ScanID     string   `json:"scan_id,omitempty"`
}

type LogBuffer struct {
	mu         sync.Mutex
	nextID     int64
	entries    []LogEntry
	totalBytes int
	maxEntries int
	maxBytes   int

	global     logRing
	categories map[string]*logRing
	jobs       map[string]*logRing
}

type LogQueryResult struct {
	Entries      []LogEntry `json:"entries"`
	OldestID     int64      `json:"oldest_id"`
	LatestID     int64      `json:"latest_id"`
	DroppedCount int64      `json:"dropped_count"`
	DroppedBytes int64      `json:"dropped_bytes"`
	GapDetected  bool       `json:"gap_detected"`
}

type LogStoreSnapshot struct {
	Global     LogQueryResult
	Categories map[string]LogQueryResult
	Jobs       map[string]LogQueryResult
}

type logMetadata struct {
	JobID      string
	JobType    string
	PackageKey string
	Manager    string
	Activity   string
	CommandID  string
	ScanID     string
}

type logMetadataContextKey struct{}

type logRing struct {
	entries      []LogEntry
	head         int
	length       int
	totalBytes   int
	maxEntries   int
	maxBytes     int
	droppedCount int64
	droppedBytes int64
}

var sessionLogs = &LogBuffer{}
var commandLogSeq atomic.Int64

const (
	defaultLogMaxEntries         = 5000
	defaultLogMaxBytes           = 2 * 1024 * 1024
	defaultCategoryLogMaxEntries = 2500
	defaultCategoryLogMaxBytes   = 1024 * 1024
	defaultJobLogMaxEntries      = 1200
	defaultJobLogMaxBytes        = 512 * 1024
	maxLogEntryBytes             = 16 * 1024

	logCategoryAll         = "all"
	logCategoryApplication = "application"
	logCategorySearches    = "searches"
	logCategoryUpdates     = "updates"
	logCategoryWinget      = "winget"
	logCategoryStore       = "store"
	logCategoryChocolatey  = "chocolatey"
	logCategoryStoreScan   = "store-scan"
	logCategoryMutations   = "mutations"
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
	{logCategoryMutations, "mutations.txt", "Mutations"},
	{logCategoryStoreScan, "store-scan.txt", "Store Scan"},
	{logCategoryWinget, "winget.txt", "winget"},
	{logCategoryStore, "store.txt", "Store"},
	{logCategoryChocolatey, "chocolatey.txt", "Chocolatey"},
}

func nextCommandLogID() string {
	return fmt.Sprintf("cmd-%d", commandLogSeq.Add(1))
}

func withLogMetadata(ctx context.Context, metadata logMetadata) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	current := logMetadataFromContext(ctx)
	if metadata.JobID != "" {
		current.JobID = metadata.JobID
	}
	if metadata.JobType != "" {
		current.JobType = metadata.JobType
	}
	if metadata.PackageKey != "" {
		current.PackageKey = metadata.PackageKey
	}
	if metadata.Manager != "" {
		current.Manager = metadata.Manager
	}
	if metadata.Activity != "" {
		current.Activity = metadata.Activity
	}
	if metadata.CommandID != "" {
		current.CommandID = metadata.CommandID
	}
	if metadata.ScanID != "" {
		current.ScanID = metadata.ScanID
	}
	return context.WithValue(ctx, logMetadataContextKey{}, current)
}

func logMetadataFromContext(ctx context.Context) logMetadata {
	if ctx == nil {
		return logMetadata{}
	}
	metadata, _ := ctx.Value(logMetadataContextKey{}).(logMetadata)
	return metadata
}

func logCategoriesForCommand(args []string) []string {
	manager, verb := packageManagerCommandVerb(args)
	categories := logCategoriesForManagerVerb(manager, verb)
	if isPackageManagerMutationCommand(args) {
		categories = append(categories, logCategoryUpdates, logCategoryMutations)
	} else if isStoreDetectionCommand(args) {
		categories = append(categories, logCategoryStoreScan)
	}
	return normalizeLogCategories(categories)
}

func logCategoriesForCommandLine(command string) []string {
	return logCategoriesForCommand(commandArgsFromLine(command))
}

func commandArgsFromLine(command string) []string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return nil
	}
	if name := strings.TrimSuffix(packageManagerNameFromArg(fields[0]), ".exe"); name == managerWinget || name == managerStore || name == managerChoco {
		return fields
	}
	if strings.EqualFold(packageManagerNameFromArg(fields[0]), "cmd.exe") {
		return fields
	}
	for index, field := range fields {
		name := strings.TrimSuffix(packageManagerNameFromArg(field), ".exe")
		if name == managerWinget || name == managerStore || name == managerChoco {
			return fields[index:]
		}
	}
	return fields
}

func packageManagerVerbFromCommandLine(command string) (string, string) {
	fields := strings.Fields(command)
	for index, field := range fields {
		manager := strings.TrimSuffix(packageManagerNameFromArg(field), ".exe")
		switch manager {
		case managerWinget, managerStore, managerChoco:
			verb := ""
			if index+1 < len(fields) {
				verb = strings.ToLower(strings.Trim(fields[index+1], `"'`))
			}
			return manager, verb
		}
	}
	return "", ""
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
	case "outdated":
		categories = append(categories, logCategoryUpdates)
	}
	return normalizeLogCategories(categories)
}

func isStoreDetectionCommand(args []string) bool {
	manager, verb, _ := packageManagerCommandParts(args)
	if manager == managerStore {
		return verb == "show" || ((verb == "update" || verb == "updates") && commandHasApplyFalse(args))
	}
	if manager == managerWinget {
		if verb == "list" && commandHasArg(args, "--upgrade-available") {
			return true
		}
		return verb == "upgrade" && !isPackageManagerMutationCommand(args)
	}
	return false
}

func commandHasArg(args []string, flag string) bool {
	for _, arg := range args {
		if strings.EqualFold(strings.TrimSpace(arg), flag) {
			return true
		}
	}
	return false
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

func categoriesWithMetadata(categories []string, metadata logMetadata) []string {
	if metadata.Activity != "" {
		categories = append(categories, metadata.Activity)
	}
	if metadata.Manager != "" {
		categories = append(categories, metadata.Manager)
	}
	return normalizeLogCategories(categories)
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
	return buffer.AppendContext(context.Background(), stream, message, categories)
}

func (buffer *LogBuffer) AppendContext(ctx context.Context, stream, message string, categories []string) LogEntry {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	buffer.ensureLocked()

	metadata := logMetadataFromContext(ctx)
	buffer.nextID++
	entry := LogEntry{
		ID:         buffer.nextID,
		Timestamp:  utcNow(),
		Stream:     stream,
		Message:    strings.TrimRight(message, "\r\n"),
		Categories: categoriesWithMetadata(categories, metadata),
		JobID:      metadata.JobID,
		JobType:    metadata.JobType,
		PackageKey: metadata.PackageKey,
		Manager:    metadata.Manager,
		Activity:   metadata.Activity,
		CommandID:  metadata.CommandID,
		ScanID:     metadata.ScanID,
	}
	entry = clampLogEntry(entry, maxLogEntryBytes)
	buffer.global.append(entry)
	for _, category := range entry.Categories {
		if category == logCategoryAll {
			continue
		}
		buffer.categoryRingLocked(category).append(entry)
	}
	if entry.JobID != "" {
		buffer.jobRingLocked(entry.JobID).append(entry)
	}
	buffer.syncCompatLocked()
	return entry
}

func (buffer *LogBuffer) ensureLocked() {
	maxEntries, maxBytes := buffer.limits()
	if buffer.global.maxEntries == 0 {
		buffer.global.maxEntries = maxEntries
		buffer.global.maxBytes = maxBytes
	}
	if buffer.categories == nil {
		buffer.categories = map[string]*logRing{}
	}
	if buffer.jobs == nil {
		buffer.jobs = map[string]*logRing{}
	}
}

func (buffer *LogBuffer) categoryRingLocked(category string) *logRing {
	ring := buffer.categories[category]
	if ring == nil {
		ring = &logRing{maxEntries: defaultCategoryLogMaxEntries, maxBytes: defaultCategoryLogMaxBytes}
		buffer.categories[category] = ring
	}
	return ring
}

func (buffer *LogBuffer) jobRingLocked(jobID string) *logRing {
	ring := buffer.jobs[jobID]
	if ring == nil {
		ring = &logRing{maxEntries: defaultJobLogMaxEntries, maxBytes: defaultJobLogMaxBytes}
		buffer.jobs[jobID] = ring
	}
	return ring
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
	size += len(entry.JobID) + len(entry.JobType) + len(entry.PackageKey) + len(entry.Manager) + len(entry.Activity) + len(entry.CommandID) + len(entry.ScanID)
	return size
}

func clampLogEntry(entry LogEntry, maxBytes int) LogEntry {
	if maxBytes <= 0 || logEntrySize(entry) <= maxBytes {
		return entry
	}
	overhead := logEntrySize(entry) - len(entry.Message)
	limit := maxBytes - overhead - 64
	if limit < 0 {
		limit = 0
	}
	if len(entry.Message) > limit {
		entry.Message = validUTF8TailString([]byte(entry.Message[:limit])) + fmt.Sprintf(" [log entry truncated: omitted %d bytes]", len(entry.Message)-limit)
	}
	return entry
}

func (buffer *LogBuffer) trimLocked() {
	buffer.ensureLocked()
	buffer.global.trim()
	buffer.syncCompatLocked()
}

func (buffer *LogBuffer) syncCompatLocked() {
	buffer.entries = buffer.global.entriesSlice()
	buffer.totalBytes = buffer.global.totalBytes
}

func (buffer *LogBuffer) Since(since int64) []LogEntry {
	return buffer.Query(since).Entries
}

func (buffer *LogBuffer) Query(since int64) LogQueryResult {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	buffer.ensureLocked()
	return buffer.global.query(since, buffer.nextID)
}

func (buffer *LogBuffer) CategoryQuery(category string, since int64) LogQueryResult {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	buffer.ensureLocked()
	category = strings.ToLower(strings.TrimSpace(category))
	if category == "" || category == logCategoryAll {
		return buffer.global.query(since, buffer.nextID)
	}
	ring := buffer.categories[category]
	if ring == nil {
		return LogQueryResult{LatestID: buffer.nextID}
	}
	return ring.query(since, buffer.nextID)
}

func (buffer *LogBuffer) JobQuery(jobID string, since int64) LogQueryResult {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	buffer.ensureLocked()
	ring := buffer.jobs[jobID]
	if ring == nil {
		return LogQueryResult{LatestID: buffer.nextID}
	}
	return ring.query(since, buffer.nextID)
}

func (buffer *LogBuffer) LatestID() int64 {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.nextID
}

func (buffer *LogBuffer) Snapshot() []LogEntry {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	buffer.ensureLocked()
	return buffer.global.entriesSlice()
}

func (buffer *LogBuffer) ExportSnapshot() LogStoreSnapshot {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	buffer.ensureLocked()
	snapshot := LogStoreSnapshot{
		Global:     buffer.global.query(0, buffer.nextID),
		Categories: map[string]LogQueryResult{},
		Jobs:       map[string]LogQueryResult{},
	}
	for _, spec := range logCategorySpecs {
		snapshot.Categories[spec.Category] = buffer.CategoryQueryLocked(spec.Category)
	}
	for jobID, ring := range buffer.jobs {
		snapshot.Jobs[jobID] = ring.query(0, buffer.nextID)
	}
	return snapshot
}

func (buffer *LogBuffer) CategoryQueryLocked(category string) LogQueryResult {
	if category == logCategoryAll {
		return buffer.global.query(0, buffer.nextID)
	}
	ring := buffer.categories[category]
	if ring == nil {
		return LogQueryResult{LatestID: buffer.nextID}
	}
	return ring.query(0, buffer.nextID)
}

func (ring *logRing) limits() (int, int) {
	maxEntries := ring.maxEntries
	if maxEntries <= 0 {
		maxEntries = defaultLogMaxEntries
	}
	maxBytes := ring.maxBytes
	if maxBytes <= 0 {
		maxBytes = defaultLogMaxBytes
	}
	return maxEntries, maxBytes
}

func (ring *logRing) append(entry LogEntry) {
	maxEntries, maxBytes := ring.limits()
	if maxEntries <= 0 {
		return
	}
	entry = clampLogEntry(entry, minInt(maxLogEntryBytes, maxBytes))
	entrySize := logEntrySize(entry)
	for ring.length > 0 && (ring.length >= maxEntries || ring.totalBytes+entrySize > maxBytes) {
		ring.dropOldest()
	}
	if entrySize > maxBytes {
		ring.droppedCount++
		ring.droppedBytes += int64(entrySize)
		return
	}
	if ring.length < len(ring.entries) {
		index := (ring.head + ring.length) % len(ring.entries)
		ring.entries[index] = entry
		ring.length++
		ring.totalBytes += entrySize
		return
	}
	if len(ring.entries) < maxEntries {
		ring.entries = append(ring.entries, entry)
		ring.length++
		ring.totalBytes += entrySize
		return
	}
	index := (ring.head + ring.length) % len(ring.entries)
	ring.entries[index] = entry
	ring.totalBytes += entrySize
	ring.length++
}

func (ring *logRing) trim() {
	maxEntries, maxBytes := ring.limits()
	for ring.length > 0 && (ring.length > maxEntries || ring.totalBytes > maxBytes) {
		ring.dropOldest()
	}
}

func (ring *logRing) dropOldest() {
	if ring.length == 0 || len(ring.entries) == 0 {
		return
	}
	entry := ring.entries[ring.head]
	size := logEntrySize(entry)
	ring.entries[ring.head] = LogEntry{}
	ring.head = (ring.head + 1) % len(ring.entries)
	ring.length--
	ring.totalBytes -= size
	ring.droppedCount++
	ring.droppedBytes += int64(size)
	if ring.totalBytes < 0 {
		ring.totalBytes = 0
	}
	if ring.length == 0 {
		ring.head = 0
	}
}

func (ring *logRing) entriesSlice() []LogEntry {
	entries := make([]LogEntry, 0, ring.length)
	if ring.length == 0 || len(ring.entries) == 0 {
		return entries
	}
	for i := 0; i < ring.length; i++ {
		entries = append(entries, ring.entries[(ring.head+i)%len(ring.entries)])
	}
	return entries
}

func (ring *logRing) query(since int64, latestID int64) LogQueryResult {
	all := ring.entriesSlice()
	oldestID := int64(0)
	if len(all) > 0 {
		oldestID = all[0].ID
	}
	start := 0
	if since > 0 {
		for start < len(all) && all[start].ID <= since {
			start++
		}
	}
	entries := make([]LogEntry, len(all[start:]))
	copy(entries, all[start:])
	return LogQueryResult{
		Entries:      entries,
		OldestID:     oldestID,
		LatestID:     latestID,
		DroppedCount: ring.droppedCount,
		DroppedBytes: ring.droppedBytes,
		GapDetected:  since > 0 && oldestID > 0 && since < oldestID-1,
	}
}

func appLog(format string, args ...any) {
	appLogContext(context.Background(), format, args...)
}

func appLogContext(ctx context.Context, format string, args ...any) {
	sessionLogs.AppendContext(ctx, "app", fmt.Sprintf(format, args...), nil)
}

func appendLogLineCategorized(stream, line string, categories []string) {
	appendLogLineContext(context.Background(), stream, line, categories)
}

func appendLogLineContext(ctx context.Context, stream, line string, categories []string) {
	line = strings.TrimRight(line, "\r\n")
	if isTransientLogFrame(line) {
		return
	}
	sessionLogs.AppendContext(ctx, stream, line, categories)
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
	return appendLogChunkContext(context.Background(), stream, pending, chunk, categories, true)
}

func appendLogChunkContext(ctx context.Context, stream, pending, chunk string, categories []string, emit bool) string {
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
			if emit {
				appendLogLineContext(ctx, stream, line.String(), categories)
			}
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

func streamCommandOutputCategorized(reader io.Reader, stream string, output io.Writer, wg *sync.WaitGroup, categories []string) {
	streamCommandOutputContext(context.Background(), reader, stream, output, wg, categories, true)
}

func streamCommandOutputContext(ctx context.Context, reader io.Reader, stream string, output io.Writer, wg *sync.WaitGroup, categories []string, emit bool) {
	defer wg.Done()

	pending := ""
	buffer := make([]byte, 4096)
	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			chunk := string(buffer[:n])
			_, _ = output.Write(buffer[:n])
			pending = appendLogChunkContext(ctx, stream, pending, chunk, categories, emit)
		}
		if err != nil {
			if err != io.EOF {
				appLogContext(ctx, "Error reading %s stream: %s", stream, err)
			}
			break
		}
	}
	if pending != "" && emit {
		appendLogLineContext(ctx, stream, strings.TrimSuffix(pending, "\r"), categories)
	}
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
