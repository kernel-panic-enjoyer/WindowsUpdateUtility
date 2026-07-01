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

	globalRing    logEntryRing
	categoryRings map[string]*logEntryRing
	jobRings      map[string]*logEntryRing
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

type logEntryRing struct {
	entries      []LogEntry
	oldestIndex  int
	entryCount   int
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
	commandFields := strings.Fields(command)
	if len(commandFields) == 0 {
		return nil
	}
	firstCommandName := strings.TrimSuffix(packageManagerNameFromArg(commandFields[0]), ".exe")
	if firstCommandName == managerWinget || firstCommandName == managerStore || firstCommandName == managerChoco {
		return commandFields
	}
	if strings.EqualFold(packageManagerNameFromArg(commandFields[0]), "cmd.exe") {
		return commandFields
	}
	for commandIndex, field := range commandFields {
		managerName := strings.TrimSuffix(packageManagerNameFromArg(field), ".exe")
		if managerName == managerWinget || managerName == managerStore || managerName == managerChoco {
			return commandFields[commandIndex:]
		}
	}
	return commandFields
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

func commandHasArg(args []string, expectedArg string) bool {
	for _, arg := range args {
		if strings.EqualFold(strings.TrimSpace(arg), expectedArg) {
			return true
		}
	}
	return false
}

func normalizeLogCategories(categories []string) []string {
	seenCategories := map[string]bool{}
	normalizedCategories := []string{}
	add := func(category string) {
		category = strings.TrimSpace(strings.ToLower(category))
		if category == "" || seenCategories[category] {
			return
		}
		seenCategories[category] = true
		normalizedCategories = append(normalizedCategories, category)
	}

	add(logCategoryAll)
	for _, category := range categories {
		add(category)
	}
	if len(normalizedCategories) == 1 {
		add(logCategoryApplication)
	}
	return normalizedCategories
}

func logCategoriesWithMetadata(categories []string, metadata logMetadata) []string {
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
		Categories: logCategoriesWithMetadata(categories, metadata),
		JobID:      metadata.JobID,
		JobType:    metadata.JobType,
		PackageKey: metadata.PackageKey,
		Manager:    metadata.Manager,
		Activity:   metadata.Activity,
		CommandID:  metadata.CommandID,
		ScanID:     metadata.ScanID,
	}
	entry = clampLogEntry(entry, maxLogEntryBytes)
	buffer.globalRing.append(entry)
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
	if buffer.globalRing.maxEntries == 0 {
		buffer.globalRing.maxEntries = maxEntries
		buffer.globalRing.maxBytes = maxBytes
	}
	if buffer.categoryRings == nil {
		buffer.categoryRings = map[string]*logEntryRing{}
	}
	if buffer.jobRings == nil {
		buffer.jobRings = map[string]*logEntryRing{}
	}
}

func (buffer *LogBuffer) categoryRingLocked(category string) *logEntryRing {
	ring := buffer.categoryRings[category]
	if ring == nil {
		ring = &logEntryRing{maxEntries: defaultCategoryLogMaxEntries, maxBytes: defaultCategoryLogMaxBytes}
		buffer.categoryRings[category] = ring
	}
	return ring
}

func (buffer *LogBuffer) jobRingLocked(jobID string) *logEntryRing {
	ring := buffer.jobRings[jobID]
	if ring == nil {
		ring = &logEntryRing{maxEntries: defaultJobLogMaxEntries, maxBytes: defaultJobLogMaxBytes}
		buffer.jobRings[jobID] = ring
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
	entryBytes := len(entry.Timestamp) + len(entry.Stream) + len(entry.Message) + 32
	for _, category := range entry.Categories {
		entryBytes += len(category) + 1
	}
	entryBytes += len(entry.JobID) + len(entry.JobType) + len(entry.PackageKey) + len(entry.Manager) + len(entry.Activity) + len(entry.CommandID) + len(entry.ScanID)
	return entryBytes
}

func clampLogEntry(entry LogEntry, maxBytes int) LogEntry {
	entryBytes := logEntrySize(entry)
	if maxBytes <= 0 || entryBytes <= maxBytes {
		return entry
	}
	nonMessageBytes := entryBytes - len(entry.Message)
	messageLimit := maxBytes - nonMessageBytes - 64
	if messageLimit < 0 {
		messageLimit = 0
	}
	if len(entry.Message) > messageLimit {
		entry.Message = validUTF8TailString([]byte(entry.Message[:messageLimit])) + fmt.Sprintf(" [log entry truncated: omitted %d bytes]", len(entry.Message)-messageLimit)
	}
	return entry
}

func (buffer *LogBuffer) trimLocked() {
	buffer.ensureLocked()
	buffer.globalRing.trim()
	buffer.syncCompatLocked()
}

func (buffer *LogBuffer) syncCompatLocked() {
	buffer.entries = buffer.globalRing.entriesInOrder()
	buffer.totalBytes = buffer.globalRing.totalBytes
}

func (buffer *LogBuffer) Since(since int64) []LogEntry {
	return buffer.Query(since).Entries
}

func (buffer *LogBuffer) Query(since int64) LogQueryResult {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	buffer.ensureLocked()
	return buffer.globalRing.query(since, buffer.nextID)
}

func (buffer *LogBuffer) CategoryQuery(category string, since int64) LogQueryResult {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	buffer.ensureLocked()
	category = strings.ToLower(strings.TrimSpace(category))
	if category == "" || category == logCategoryAll {
		return buffer.globalRing.query(since, buffer.nextID)
	}
	ring := buffer.categoryRings[category]
	if ring == nil {
		return LogQueryResult{LatestID: buffer.nextID}
	}
	return ring.query(since, buffer.nextID)
}

func (buffer *LogBuffer) JobQuery(jobID string, since int64) LogQueryResult {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	buffer.ensureLocked()
	ring := buffer.jobRings[jobID]
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
	return buffer.globalRing.entriesInOrder()
}

func (buffer *LogBuffer) ExportSnapshot() LogStoreSnapshot {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	buffer.ensureLocked()
	snapshot := LogStoreSnapshot{
		Global:     buffer.globalRing.query(0, buffer.nextID),
		Categories: map[string]LogQueryResult{},
		Jobs:       map[string]LogQueryResult{},
	}
	for _, spec := range logCategorySpecs {
		snapshot.Categories[spec.Category] = buffer.CategoryQueryLocked(spec.Category)
	}
	for jobID, ring := range buffer.jobRings {
		snapshot.Jobs[jobID] = ring.query(0, buffer.nextID)
	}
	return snapshot
}

func (buffer *LogBuffer) CategoryQueryLocked(category string) LogQueryResult {
	if category == logCategoryAll {
		return buffer.globalRing.query(0, buffer.nextID)
	}
	ring := buffer.categoryRings[category]
	if ring == nil {
		return LogQueryResult{LatestID: buffer.nextID}
	}
	return ring.query(0, buffer.nextID)
}

func (ring *logEntryRing) limits() (int, int) {
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

func (ring *logEntryRing) append(entry LogEntry) {
	maxEntries, maxBytes := ring.limits()
	if maxEntries <= 0 {
		return
	}
	entry = clampLogEntry(entry, min(maxLogEntryBytes, maxBytes))
	entryBytes := logEntrySize(entry)
	for ring.entryCount > 0 && (ring.entryCount >= maxEntries || ring.totalBytes+entryBytes > maxBytes) {
		ring.dropOldest()
	}
	if entryBytes > maxBytes {
		ring.droppedCount++
		ring.droppedBytes += int64(entryBytes)
		return
	}
	if ring.entryCount < len(ring.entries) {
		writeIndex := (ring.oldestIndex + ring.entryCount) % len(ring.entries)
		ring.entries[writeIndex] = entry
		ring.entryCount++
		ring.totalBytes += entryBytes
		return
	}
	if len(ring.entries) < maxEntries {
		ring.entries = append(ring.entries, entry)
		ring.entryCount++
		ring.totalBytes += entryBytes
		return
	}
	writeIndex := (ring.oldestIndex + ring.entryCount) % len(ring.entries)
	ring.entries[writeIndex] = entry
	ring.totalBytes += entryBytes
	ring.entryCount++
}

func (ring *logEntryRing) trim() {
	maxEntries, maxBytes := ring.limits()
	for ring.entryCount > 0 && (ring.entryCount > maxEntries || ring.totalBytes > maxBytes) {
		ring.dropOldest()
	}
}

func (ring *logEntryRing) dropOldest() {
	if ring.entryCount == 0 || len(ring.entries) == 0 {
		return
	}
	oldestEntry := ring.entries[ring.oldestIndex]
	oldestEntryBytes := logEntrySize(oldestEntry)
	ring.entries[ring.oldestIndex] = LogEntry{}
	ring.oldestIndex = (ring.oldestIndex + 1) % len(ring.entries)
	ring.entryCount--
	ring.totalBytes -= oldestEntryBytes
	ring.droppedCount++
	ring.droppedBytes += int64(oldestEntryBytes)
	if ring.totalBytes < 0 {
		ring.totalBytes = 0
	}
	if ring.entryCount == 0 {
		ring.oldestIndex = 0
	}
}

func (ring *logEntryRing) entriesInOrder() []LogEntry {
	orderedEntries := make([]LogEntry, 0, ring.entryCount)
	if ring.entryCount == 0 || len(ring.entries) == 0 {
		return orderedEntries
	}
	for offset := 0; offset < ring.entryCount; offset++ {
		orderedEntries = append(orderedEntries, ring.entries[(ring.oldestIndex+offset)%len(ring.entries)])
	}
	return orderedEntries
}

func (ring *logEntryRing) query(since int64, latestID int64) LogQueryResult {
	orderedEntries := ring.entriesInOrder()
	oldestID := int64(0)
	if len(orderedEntries) > 0 {
		oldestID = orderedEntries[0].ID
	}
	firstNewIndex := 0
	if since > 0 {
		for firstNewIndex < len(orderedEntries) && orderedEntries[firstNewIndex].ID <= since {
			firstNewIndex++
		}
	}
	matchingEntries := orderedEntries[firstNewIndex:]
	entries := make([]LogEntry, len(matchingEntries))
	copy(entries, matchingEntries)
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

func appendLogChunkCategorized(stream, pendingLine, chunk string, categories []string) string {
	return appendLogChunkContext(context.Background(), stream, pendingLine, chunk, categories, true)
}

func appendLogChunkContext(ctx context.Context, stream, pendingLine, chunk string, categories []string, emitSessionLog bool) string {
	bufferedText := pendingLine + chunk
	endsWithCarriageReturn := strings.HasSuffix(bufferedText, "\r")
	if endsWithCarriageReturn {
		bufferedText = strings.TrimSuffix(bufferedText, "\r")
	}
	bufferedText = strings.ReplaceAll(bufferedText, "\r\n", "\n")

	var currentLine strings.Builder
	for _, r := range bufferedText {
		switch r {
		case '\n':
			if emitSessionLog {
				appendLogLineContext(ctx, stream, currentLine.String(), categories)
			}
			currentLine.Reset()
		case '\r':
			currentLine.Reset()
		default:
			currentLine.WriteRune(r)
		}
	}
	if endsWithCarriageReturn {
		return currentLine.String() + "\r"
	}
	return currentLine.String()
}

func streamCommandOutputCategorized(reader io.Reader, stream string, output io.Writer, wg *sync.WaitGroup, categories []string) {
	streamCommandOutputContext(context.Background(), reader, stream, output, wg, categories, true)
}

func streamCommandOutputContext(ctx context.Context, reader io.Reader, stream string, output io.Writer, wg *sync.WaitGroup, categories []string, emitSessionLog bool) {
	defer wg.Done()

	pendingLine := ""
	readBuffer := make([]byte, 4096)
	for {
		bytesRead, readErr := reader.Read(readBuffer)
		if bytesRead > 0 {
			decodedChunk := decodeCommandOutputBytes(readBuffer[:bytesRead])
			_, _ = output.Write([]byte(decodedChunk))
			pendingLine = appendLogChunkContext(ctx, stream, pendingLine, decodedChunk, categories, emitSessionLog)
		}
		if readErr != nil {
			if readErr != io.EOF {
				appLogContext(ctx, "Error reading %s stream: %s", stream, readErr)
			}
			break
		}
	}
	if pendingLine != "" && emitSessionLog {
		appendLogLineContext(ctx, stream, strings.TrimSuffix(pendingLine, "\r"), categories)
	}
}
