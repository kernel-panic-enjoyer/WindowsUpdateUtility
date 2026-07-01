package updater

import (
	"strings"
	"unicode"
)

func packageTableColumnStarts(headerLine string) []int {
	var columnStarts []int
	insideColumn := false
	headerRunes := []rune(strings.TrimRight(headerLine, "\r\n"))
	for runeIndex, char := range headerRunes {
		if unicode.IsSpace(char) {
			insideColumn = false
			continue
		}
		if !insideColumn {
			columnStarts = append(columnStarts, runeIndex)
			insideColumn = true
		}
	}
	return columnStarts
}

func splitPackageTableColumnsAtStarts(rowLine string, columnStarts []int) []string {
	rowLine = strings.TrimRight(rowLine, "\r\n")
	if strings.TrimSpace(rowLine) == "" || len(columnStarts) < 2 {
		return nil
	}
	lineRunes := []rune(rowLine)
	var columns []string
	for columnIndex, startIndex := range columnStarts {
		if startIndex >= len(lineRunes) {
			continue
		}
		endIndex := len(lineRunes)
		if columnIndex+1 < len(columnStarts) && columnStarts[columnIndex+1] < endIndex {
			endIndex = columnStarts[columnIndex+1]
		}
		value := strings.TrimSpace(string(lineRunes[startIndex:endIndex]))
		if value != "" {
			columns = append(columns, value)
		}
	}
	return columns
}

func splitPackageTableColumns(rowLine string) []string {
	rowLine = strings.TrimSpace(rowLine)
	if rowLine == "" {
		return nil
	}
	var columns []string
	var currentColumn strings.Builder
	pendingSpaceCount := 0
	flushCurrentColumn := func() {
		if currentColumn.Len() > 0 {
			columns = append(columns, currentColumn.String())
		}
		currentColumn.Reset()
	}
	for _, char := range rowLine {
		if unicode.IsSpace(char) {
			pendingSpaceCount++
			continue
		}
		if pendingSpaceCount > 0 {
			if pendingSpaceCount >= 2 {
				flushCurrentColumn()
			} else if currentColumn.Len() > 0 {
				currentColumn.WriteRune(' ')
			}
			pendingSpaceCount = 0
		}
		currentColumn.WriteRune(char)
	}
	flushCurrentColumn()
	return columns
}
