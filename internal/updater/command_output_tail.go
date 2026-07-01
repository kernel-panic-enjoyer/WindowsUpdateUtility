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

type boundedCommandOutputTail struct {
	limitBytes    int
	retainedBytes []byte
	omittedBytes  int64
}

func newBoundedOutputTail(limitBytes int) *boundedCommandOutputTail {
	return &boundedCommandOutputTail{limitBytes: limitBytes}
}

func (tail *boundedCommandOutputTail) Write(chunk []byte) (int, error) {
	chunkLen := len(chunk)
	if chunkLen == 0 {
		return 0, nil
	}
	if tail.limitBytes <= 0 {
		tail.omittedBytes += int64(chunkLen)
		return chunkLen, nil
	}
	if chunkLen >= tail.limitBytes {
		tail.omittedBytes += int64(len(tail.retainedBytes) + chunkLen - tail.limitBytes)
		tail.retainedBytes = append(tail.retainedBytes[:0], chunk[chunkLen-tail.limitBytes:]...)
		return chunkLen, nil
	}
	overflowBytes := len(tail.retainedBytes) + chunkLen - tail.limitBytes
	if overflowBytes > 0 {
		tail.omittedBytes += int64(overflowBytes)
		copy(tail.retainedBytes, tail.retainedBytes[overflowBytes:])
		tail.retainedBytes = tail.retainedBytes[:len(tail.retainedBytes)-overflowBytes]
	}
	tail.retainedBytes = append(tail.retainedBytes, chunk...)
	return chunkLen, nil
}

func (tail *boundedCommandOutputTail) String() string {
	retainedText := validUTF8TailString([]byte(decodeCommandOutputBytes(tail.retainedBytes)))
	if tail.omittedBytes == 0 {
		return retainedText
	}
	truncationNotice := fmt.Sprintf("[output truncated: omitted %d bytes]\n", tail.omittedBytes)
	return truncationNotice + retainedText
}

func validUTF8TailString(tailBytes []byte) string {
	if len(tailBytes) == 0 {
		return ""
	}
	if utf8.Valid(tailBytes) {
		return string(tailBytes)
	}
	utf8AlignedTail := tailBytes
	for len(utf8AlignedTail) > 0 {
		r, size := utf8.DecodeRune(utf8AlignedTail)
		startsWithInvalidUTF8Byte := r == utf8.RuneError && size == 1 && utf8AlignedTail[0] >= utf8.RuneSelf
		if !startsWithInvalidUTF8Byte {
			break
		}
		utf8AlignedTail = utf8AlignedTail[1:]
	}
	return strings.ToValidUTF8(string(utf8AlignedTail), "")
}

func compactCommandResult(result CommandResult, streamLimitBytes, commandLimitBytes int) CommandResult {
	if streamLimitBytes <= 0 {
		streamLimitBytes = commandResultStreamLimitBytes
	}
	if commandLimitBytes <= 0 {
		commandLimitBytes = maxCommandResultCommandBytes
	}
	result.Command = truncateCommandField(result.Command, commandLimitBytes)
	result.Stdout = boundCommandText(result.Stdout, streamLimitBytes)
	result.Stderr = boundCommandText(result.Stderr, streamLimitBytes)
	return result
}

func truncateCommandField(commandText string, limitBytes int) string {
	if limitBytes <= 0 || len(commandText) <= limitBytes {
		return commandText
	}
	omittedBytes := len(commandText) - limitBytes
	truncationNotice := fmt.Sprintf("[command truncated: omitted %d bytes]\n", omittedBytes)
	keepBytes := limitBytes - len(truncationNotice)
	if keepBytes < 0 {
		keepBytes = 0
	}
	return truncationNotice + validUTF8TailString([]byte(commandText[len(commandText)-keepBytes:]))
}

func boundCommandText(streamText string, limitBytes int) string {
	if limitBytes <= 0 || len(streamText) <= limitBytes {
		return streamText
	}
	retainedTail := newBoundedOutputTail(limitBytes)
	_, _ = retainedTail.Write([]byte(streamText))
	return retainedTail.String()
}
