package updater

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// commandResultStreamLimitBytes bounds the stdout/stderr retained in
// CommandResult. The Session Log still receives the complete stream as it is
// produced; this limit only caps per-result JSON/job retention.
const commandResultStreamLimitBytes = 2 * 1024 * 1024

type boundedOutputTail struct {
	limit   int
	buffer  []byte
	omitted int64
}

func newBoundedOutputTail(limit int) *boundedOutputTail {
	return &boundedOutputTail{limit: limit}
}

func (tail *boundedOutputTail) Write(data []byte) (int, error) {
	written := len(data)
	if written == 0 {
		return 0, nil
	}
	if tail.limit <= 0 {
		tail.omitted += int64(written)
		return written, nil
	}
	if len(data) >= tail.limit {
		tail.omitted += int64(len(tail.buffer) + len(data) - tail.limit)
		tail.buffer = append(tail.buffer[:0], data[len(data)-tail.limit:]...)
		return written, nil
	}
	overflow := len(tail.buffer) + len(data) - tail.limit
	if overflow > 0 {
		tail.omitted += int64(overflow)
		copy(tail.buffer, tail.buffer[overflow:])
		tail.buffer = tail.buffer[:len(tail.buffer)-overflow]
	}
	tail.buffer = append(tail.buffer, data...)
	return written, nil
}

func (tail *boundedOutputTail) String() string {
	output := validUTF8TailString(tail.buffer)
	if tail.omitted == 0 {
		return output
	}
	marker := fmt.Sprintf("[output truncated: omitted %d bytes]\n", tail.omitted)
	return marker + output
}

func validUTF8TailString(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	if utf8.Valid(data) {
		return string(data)
	}
	trimmed := data
	for len(trimmed) > 0 {
		r, size := utf8.DecodeRune(trimmed)
		if r != utf8.RuneError || size > 1 || trimmed[0] < utf8.RuneSelf {
			break
		}
		trimmed = trimmed[1:]
	}
	return strings.ToValidUTF8(string(trimmed), "")
}

func compactCommandResult(result CommandResult, streamLimit, commandLimit int) CommandResult {
	if streamLimit <= 0 {
		streamLimit = commandResultStreamLimitBytes
	}
	if commandLimit <= 0 {
		commandLimit = maxCommandResultCommandBytes
	}
	result.Command = truncateCommandField(result.Command, commandLimit)
	result.Stdout = boundCommandText(result.Stdout, streamLimit)
	result.Stderr = boundCommandText(result.Stderr, streamLimit)
	return result
}

func truncateCommandField(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	omitted := len(value) - limit
	marker := fmt.Sprintf("[command truncated: omitted %d bytes]\n", omitted)
	keep := limit - len(marker)
	if keep < 0 {
		keep = 0
	}
	return marker + validUTF8TailString([]byte(value[len(value)-keep:]))
}

func boundCommandText(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	tail := newBoundedOutputTail(limit)
	_, _ = tail.Write([]byte(value))
	return tail.String()
}
