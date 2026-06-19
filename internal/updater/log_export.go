package updater

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strings"
	"time"
)

func buildLogArchive(entries []LogEntry) ([]byte, error) {
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for _, file := range logCategorySpecs {
		writer, err := archive.Create(file.Filename)
		if err != nil {
			_ = archive.Close()
			return nil, err
		}
		for _, entry := range entries {
			if !logEntryInCategory(entry, file.Category) {
				continue
			}
			if _, err := writer.Write([]byte(formatLogEntryText(entry))); err != nil {
				_ = archive.Close()
				return nil, err
			}
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func formatLogEntryText(entry LogEntry) string {
	return fmt.Sprintf("[%s] %s %s\n", logEntryDisplayTime(entry.Timestamp), strings.ToUpper(entry.Stream), entry.Message)
}

func logEntryDisplayTime(timestamp string) string {
	parsed, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return timestamp
	}
	return parsed.Local().Format("15:04:05")
}
