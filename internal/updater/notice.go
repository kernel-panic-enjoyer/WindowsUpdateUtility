package updater

import (
	"fmt"
	"path"
	"strings"
)

const (
	noticeReasonLimit = 140
	noticeTextLimit   = 230
)

func updateFailureNotice(result CommandResult) string {
	if result.OK {
		return ""
	}
	return commandFailureNotice("Update finished with errors", result)
}

func updateResultsFailureNotice(results []UpdateResult) string {
	var failed []UpdateResult
	for _, item := range results {
		if !item.Result.OK {
			failed = append(failed, item)
		}
	}
	if len(failed) == 0 {
		return ""
	}
	message := fmt.Sprintf("%d update command(s) finished with errors. %s", len(failed), commandFailureText(failed[0].Result))
	return limitNoticeText(message) + " See Session Log for full output."
}

func commandFailureNotice(prefix string, result CommandResult) string {
	if result.OK {
		return ""
	}
	return limitNoticeText(prefix+". "+commandFailureText(result)) + " See Session Log for full output."
}

func commandFailureText(result CommandResult) string {
	text := fmt.Sprintf("%s failed with code %d", commandNoticeLabel(result.Command), result.Code)
	if reason := firstNoticeOutputLine(result.Stderr); reason != "" {
		text += ": " + limitNoticeReason(reason)
	} else if reason := firstNoticeOutputLine(result.Stdout); reason != "" {
		text += ": " + limitNoticeReason(reason)
	}
	return text + "."
}

func commandNoticeLabel(command string) string {
	line := compactNoticeText(strings.Split(command, "\n")[0])
	if line == "" {
		return "command"
	}
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return "command"
	}
	exe := strings.TrimSuffix(path.Base(strings.ReplaceAll(parts[0], "\\", "/")), ".exe")
	label := []string{exe}
	for _, part := range parts[1:] {
		if strings.HasPrefix(part, "-") || strings.HasPrefix(part, "/") {
			continue
		}
		label = append(label, part)
		if len(label) >= 3 {
			break
		}
	}
	return compactNoticeText(strings.Join(label, " "))
}

func firstNoticeOutputLine(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = compactNoticeText(line)
		if line == "" || noticeLineIsNoise(line) {
			continue
		}
		return line
	}
	return ""
}

func noticeLineIsNoise(line string) bool {
	trimmed := strings.Trim(line, `\|/- `)
	if trimmed == "" {
		return true
	}
	lower := strings.ToLower(line)
	if lower == "all" {
		return true
	}
	for _, prefix := range []string{
		"chocolatey v",
		"upgrading the following packages:",
		"by upgrading, you accept licenses",
		"you have chocolatey",
		"downloading package from source",
		"[approved]",
		"progress:",
	} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	if strings.Contains(lower, " is the latest version available based on your source(s).") {
		return true
	}
	return false
}

func compactNoticeText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func limitNoticeReason(value string) string {
	return truncateNoticeText(value, noticeReasonLimit)
}

func limitNoticeText(value string) string {
	return truncateNoticeText(value, noticeTextLimit)
}

func truncateNoticeText(value string, maxLength int) string {
	value = compactNoticeText(value)
	if len(value) <= maxLength {
		return value
	}
	if maxLength <= 3 {
		return value[:maxLength]
	}
	return strings.TrimSpace(value[:maxLength-3]) + "..."
}
