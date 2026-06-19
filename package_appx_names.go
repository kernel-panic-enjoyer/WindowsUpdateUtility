package main

import (
	"strings"
)

func friendlyAppxName(name, displayName string, preferred ...string) string {
	for _, value := range preferred {
		if clean := cleanManifestDisplayName(value); clean != "" {
			return clean
		}
	}
	if clean := cleanManifestDisplayName(displayName); clean != "" {
		return clean
	}
	candidate := strings.TrimSpace(name)
	if candidate == "" {
		return candidate
	}
	if strings.Contains(candidate, ".") {
		candidate = friendlyDottedPackageIdentity(candidate)
	}
	candidate = trimLeadingDigits(candidate)
	candidate = strings.Trim(candidate, " ._-")
	candidate = splitJoinedWords(candidate)
	if candidate == "" {
		return strings.TrimSpace(name)
	}
	return candidate
}

func trimLeadingDigits(value string) string {
	value = strings.TrimSpace(value)
	for index, r := range value {
		if r < '0' || r > '9' {
			return value[index:]
		}
	}
	return ""
}

func friendlyDottedPackageIdentity(name string) string {
	parts := strings.Split(strings.Trim(name, " ._"), ".")
	if len(parts) > 1 {
		parts = parts[1:]
	}
	if len(parts) >= 2 && numericString(parts[len(parts)-1]) && numericString(parts[len(parts)-2]) {
		version := parts[len(parts)-2] + "." + parts[len(parts)-1]
		parts = append(parts[:len(parts)-2], version)
	}
	return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
}

func numericString(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func cleanManifestDisplayName(displayName string) string {
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		return ""
	}
	lower := strings.ToLower(displayName)
	if strings.HasPrefix(lower, "ms-resource:") || strings.HasPrefix(lower, "@{") || strings.Contains(displayName, "\\") {
		return ""
	}
	if looksLikeManifestPackageIdentity(displayName) {
		return ""
	}
	return displayName
}

func looksLikeManifestPackageIdentity(displayName string) bool {
	displayName = strings.TrimSpace(displayName)
	return strings.Contains(displayName, ".") && !strings.Contains(displayName, " ")
}

func splitJoinedWords(value string) string {
	value = strings.ReplaceAll(value, "_", " ")
	value = strings.ReplaceAll(value, "-", " ")
	runes := []rune(value)
	var out []rune
	for i, r := range runes {
		if i > 0 && shouldInsertWordSpace(runes, i) {
			out = append(out, ' ')
		}
		out = append(out, r)
	}
	return strings.Join(strings.Fields(string(out)), " ")
}

func shouldInsertWordSpace(runes []rune, index int) bool {
	prev := runes[index-1]
	current := runes[index]
	var next rune
	if index+1 < len(runes) {
		next = runes[index+1]
	}
	if isLowerASCII(prev) && isUpperASCII(current) {
		return index+1 < len(runes)
	}
	if isUpperASCII(prev) && isUpperASCII(current) && isLowerASCII(next) {
		return true
	}
	if isDigitASCII(prev) && (isUpperASCII(current) || isLowerASCII(current)) {
		return true
	}
	return false
}

func isLowerASCII(r rune) bool { return r >= 'a' && r <= 'z' }

func isUpperASCII(r rune) bool { return r >= 'A' && r <= 'Z' }

func isDigitASCII(r rune) bool { return r >= '0' && r <= '9' }
