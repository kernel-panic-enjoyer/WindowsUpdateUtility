package updater

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

func buildLogArchive(entries []LogEntry) ([]byte, error) {
	buffer := &LogBuffer{}
	for _, entry := range entries {
		buffer.mu.Lock()
		buffer.ensureLocked()
		buffer.nextID = maxInt64(buffer.nextID, entry.ID-1)
		buffer.mu.Unlock()
		buffer.AppendContext(logMetadataContext(entry), entry.Stream, entry.Message, entry.Categories)
	}
	return buildLogArchiveFromSnapshot(buffer.ExportSnapshot())
}

func buildLogArchiveFromSnapshot(snapshot LogStoreSnapshot) ([]byte, error) {
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for _, file := range logCategorySpecs {
		query := snapshot.Categories[file.Category]
		if file.Category == logCategoryAll {
			query = snapshot.Global
		}
		writer, err := archive.Create(file.Filename)
		if err != nil {
			_ = archive.Close()
			return nil, err
		}
		for _, entry := range query.Entries {
			if _, err := writer.Write([]byte(formatLogEntryText(entry))); err != nil {
				_ = archive.Close()
				return nil, err
			}
		}
	}
	for _, jobID := range sortedLogJobIDs(snapshot.Jobs) {
		query := snapshot.Jobs[jobID]
		writer, err := archive.Create(path.Join("jobs", sanitizeArchiveFilename(jobID)+".txt"))
		if err != nil {
			_ = archive.Close()
			return nil, err
		}
		for _, entry := range query.Entries {
			if _, err := writer.Write([]byte(formatLogEntryText(entry))); err != nil {
				_ = archive.Close()
				return nil, err
			}
		}
	}
	manifest, err := json.MarshalIndent(buildLogArchiveManifest(snapshot), "", "  ")
	if err != nil {
		_ = archive.Close()
		return nil, err
	}
	if writer, err := archive.Create("manifest.json"); err != nil {
		_ = archive.Close()
		return nil, err
	} else if _, err := writer.Write(manifest); err != nil {
		_ = archive.Close()
		return nil, err
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

type logArchiveManifest struct {
	ExportedAt  string                     `json:"exported_at"`
	Global      logArchiveManifestScope    `json:"global"`
	Categories  map[string]logArchiveScope `json:"categories"`
	Jobs        map[string]logArchiveScope `json:"jobs"`
	GapsPresent bool                       `json:"gaps_present"`
}

type logArchiveManifestScope struct {
	OldestID      int64 `json:"oldest_id"`
	LatestID      int64 `json:"latest_id"`
	RetainedCount int   `json:"retained_count"`
	RetainedBytes int   `json:"retained_bytes"`
	DroppedCount  int64 `json:"dropped_count"`
	DroppedBytes  int64 `json:"dropped_bytes"`
	GapDetected   bool  `json:"gap_detected"`
}

type logArchiveScope = logArchiveManifestScope

func buildLogArchiveManifest(snapshot LogStoreSnapshot) logArchiveManifest {
	manifest := logArchiveManifest{
		ExportedAt: utcNow(),
		Global:     logArchiveScopeFromQuery(snapshot.Global),
		Categories: map[string]logArchiveScope{},
		Jobs:       map[string]logArchiveScope{},
	}
	manifest.GapsPresent = manifest.Global.DroppedCount > 0 || manifest.Global.GapDetected
	for _, spec := range logCategorySpecs {
		scope := logArchiveScopeFromQuery(snapshot.Categories[spec.Category])
		if spec.Category == logCategoryAll {
			scope = logArchiveScopeFromQuery(snapshot.Global)
		}
		manifest.Categories[spec.Category] = scope
		if scope.DroppedCount > 0 || scope.GapDetected {
			manifest.GapsPresent = true
		}
	}
	for _, jobID := range sortedLogJobIDs(snapshot.Jobs) {
		scope := logArchiveScopeFromQuery(snapshot.Jobs[jobID])
		manifest.Jobs[jobID] = scope
		if scope.DroppedCount > 0 || scope.GapDetected {
			manifest.GapsPresent = true
		}
	}
	return manifest
}

func logArchiveScopeFromQuery(query LogQueryResult) logArchiveScope {
	bytes := 0
	for _, entry := range query.Entries {
		bytes += logEntrySize(entry)
	}
	return logArchiveScope{
		OldestID:      query.OldestID,
		LatestID:      query.LatestID,
		RetainedCount: len(query.Entries),
		RetainedBytes: bytes,
		DroppedCount:  query.DroppedCount,
		DroppedBytes:  query.DroppedBytes,
		GapDetected:   query.GapDetected,
	}
}

func sortedLogJobIDs(jobs map[string]LogQueryResult) []string {
	ids := make([]string, 0, len(jobs))
	for id := range jobs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func formatLogEntryText(entry LogEntry) string {
	return fmt.Sprintf("[%s] %s%s %s\n", logEntryDisplayTime(entry.Timestamp), strings.ToUpper(entry.Stream), formatLogEntryMetadata(entry), sanitizeLogText(entry.Message))
}

func formatLogEntryMetadata(entry LogEntry) string {
	var parts []string
	if entry.JobID != "" {
		parts = append(parts, "job="+sanitizeLogText(entry.JobID))
	}
	if entry.PackageKey != "" {
		parts = append(parts, "package="+sanitizeLogText(entry.PackageKey))
	}
	if entry.Manager != "" {
		parts = append(parts, "manager="+sanitizeLogText(entry.Manager))
	}
	if entry.CommandID != "" {
		parts = append(parts, "command="+sanitizeLogText(entry.CommandID))
	}
	if len(parts) == 0 {
		return ""
	}
	return " [" + strings.Join(parts, " ") + "]"
}

func logEntryDisplayTime(timestamp string) string {
	parsed, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return timestamp
	}
	return parsed.Local().Format("15:04:05")
}

var (
	windowsUserPathPattern = regexp.MustCompile(`(?i)\b[A-Z]:\\Users\\[^\\\s]+`)
	rawSIDPattern          = regexp.MustCompile(`\bS-\d-\d+(?:-\d+){2,}\b`)
	secretPairPattern      = regexp.MustCompile(`(?i)\b(token|capability|password|secret|credential)=([^\s&]+)`)
)

func sanitizeLogText(text string) string {
	text = windowsUserPathPattern.ReplaceAllString(text, `%USERPROFILE%`)
	text = rawSIDPattern.ReplaceAllString(text, `[sid]`)
	text = secretPairPattern.ReplaceAllString(text, `$1=[redacted]`)
	return text
}

func sanitizeArchiveFilename(name string) string {
	name = sanitizeLogText(name)
	name = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.':
			return r
		default:
			return '_'
		}
	}, name)
	name = strings.Trim(name, "._-")
	if name == "" {
		return "job"
	}
	return name
}

func logMetadataContext(entry LogEntry) context.Context {
	return withLogMetadata(context.Background(), logMetadata{
		JobID:      entry.JobID,
		JobType:    entry.JobType,
		PackageKey: entry.PackageKey,
		Manager:    entry.Manager,
		Activity:   entry.Activity,
		CommandID:  entry.CommandID,
		ScanID:     entry.ScanID,
	})
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
