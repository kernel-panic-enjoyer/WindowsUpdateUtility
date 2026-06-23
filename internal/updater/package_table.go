package updater

import (
	"strings"
	"unicode"
)

func packageTableColumnStarts(header string) []int {
	var starts []int
	inField := false
	for index, r := range []rune(strings.TrimRight(header, "\r\n")) {
		if unicode.IsSpace(r) {
			inField = false
			continue
		}
		if !inField {
			starts = append(starts, index)
			inField = true
		}
	}
	return starts
}

func splitPackageTableColumnsAtStarts(line string, starts []int) []string {
	line = strings.TrimRight(line, "\r\n")
	if strings.TrimSpace(line) == "" || len(starts) < 2 {
		return nil
	}
	runes := []rune(line)
	var cols []string
	for i, start := range starts {
		if start >= len(runes) {
			continue
		}
		end := len(runes)
		if i+1 < len(starts) && starts[i+1] < end {
			end = starts[i+1]
		}
		value := strings.TrimSpace(string(runes[start:end]))
		if value != "" {
			cols = append(cols, value)
		}
	}
	return cols
}

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
