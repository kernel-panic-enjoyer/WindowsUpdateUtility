package main

import (
	"strings"
	"unicode"
)

func splitPackageTableColumns(line string) []string {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	var cols []string
	var field strings.Builder
	pendingSpace := 0
	flush := func() {
		value := strings.TrimSpace(field.String())
		if value != "" {
			cols = append(cols, value)
		}
		field.Reset()
	}
	for _, r := range line {
		if unicode.IsSpace(r) {
			pendingSpace++
			continue
		}
		if pendingSpace > 0 {
			if pendingSpace >= 2 {
				flush()
			} else if field.Len() > 0 {
				field.WriteRune(' ')
			}
			pendingSpace = 0
		}
		field.WriteRune(r)
	}
	flush()
	return cols
}
