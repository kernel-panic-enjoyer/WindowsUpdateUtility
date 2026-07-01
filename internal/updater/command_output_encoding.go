package updater

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	utf8MojibakeMarkerRunes   = "\u00c3\u00c2\u00e2"
	mojibakeRepairScoreMargin = 2
)

func decodeCommandOutputBytes(outputBytes []byte) string {
	if len(outputBytes) == 0 {
		return ""
	}
	if utf8.Valid(outputBytes) {
		return repairCommandOutputMojibake(string(outputBytes))
	}
	return repairCommandOutputMojibake(decodeWindows1252Bytes(outputBytes))
}

func repairCommandOutputMojibake(commandOutputText string) string {
	if commandOutputText == "" || !strings.ContainsAny(commandOutputText, utf8MojibakeMarkerRunes) {
		return commandOutputText
	}
	var repairedOutput strings.Builder
	var currentToken strings.Builder
	repairedAnyToken := false
	flushToken := func() {
		if currentToken.Len() == 0 {
			return
		}
		tokenText := currentToken.String()
		repairedToken, ok := repairMojibakeToken(tokenText)
		if ok {
			repairedOutput.WriteString(repairedToken)
			repairedAnyToken = true
		} else {
			repairedOutput.WriteString(tokenText)
		}
		currentToken.Reset()
	}
	for _, character := range commandOutputText {
		if unicode.IsSpace(character) {
			flushToken()
			repairedOutput.WriteRune(character)
			continue
		}
		currentToken.WriteRune(character)
	}
	flushToken()
	if !repairedAnyToken {
		return commandOutputText
	}
	return repairedOutput.String()
}

func repairMojibakeToken(tokenText string) (string, bool) {
	if tokenText == "" || !strings.ContainsAny(tokenText, utf8MojibakeMarkerRunes) {
		return "", false
	}
	candidateUTF8Bytes := make([]byte, 0, len(tokenText))
	for _, character := range tokenText {
		encodedByte, ok := encodeRuneAsWindows1252Byte(character)
		if !ok {
			return "", false
		}
		candidateUTF8Bytes = append(candidateUTF8Bytes, encodedByte)
	}
	if !utf8.Valid(candidateUTF8Bytes) {
		return "", false
	}
	repairedToken := string(candidateUTF8Bytes)
	originalScore := scoreCommandOutputText(tokenText)
	repairedScore := scoreCommandOutputText(repairedToken)
	if repairedScore <= originalScore+mojibakeRepairScoreMargin {
		return "", false
	}
	return repairedToken, true
}

func decodeWindows1252Bytes(encodedBytes []byte) string {
	var decodedText strings.Builder
	decodedText.Grow(len(encodedBytes))
	for _, encodedByte := range encodedBytes {
		decodedText.WriteRune(decodeWindows1252Byte(encodedByte))
	}
	return decodedText.String()
}

func decodeWindows1252Byte(encodedByte byte) rune {
	if encodedByte < 0x80 || encodedByte >= 0xa0 {
		return rune(encodedByte)
	}
	switch encodedByte {
	case 0x80:
		return '€'
	case 0x82:
		return '‚'
	case 0x83:
		return 'ƒ'
	case 0x84:
		return '„'
	case 0x85:
		return '…'
	case 0x86:
		return '†'
	case 0x87:
		return '‡'
	case 0x88:
		return 'ˆ'
	case 0x89:
		return '‰'
	case 0x8a:
		return 'Š'
	case 0x8b:
		return '‹'
	case 0x8c:
		return 'Œ'
	case 0x8e:
		return 'Ž'
	case 0x91:
		return '‘'
	case 0x92:
		return '’'
	case 0x93:
		return '“'
	case 0x94:
		return '”'
	case 0x95:
		return '•'
	case 0x96:
		return '–'
	case 0x97:
		return '—'
	case 0x98:
		return '˜'
	case 0x99:
		return '™'
	case 0x9a:
		return 'š'
	case 0x9b:
		return '›'
	case 0x9c:
		return 'œ'
	case 0x9e:
		return 'ž'
	case 0x9f:
		return 'Ÿ'
	default:
		return utf8.RuneError
	}
}

func encodeRuneAsWindows1252Byte(character rune) (byte, bool) {
	if character < 0x80 || (character >= 0xa0 && character <= 0xff) {
		return byte(character), true
	}
	switch character {
	case '€':
		return 0x80, true
	case '‚':
		return 0x82, true
	case 'ƒ':
		return 0x83, true
	case '„':
		return 0x84, true
	case '…':
		return 0x85, true
	case '†':
		return 0x86, true
	case '‡':
		return 0x87, true
	case 'ˆ':
		return 0x88, true
	case '‰':
		return 0x89, true
	case 'Š':
		return 0x8a, true
	case '‹':
		return 0x8b, true
	case 'Œ':
		return 0x8c, true
	case 'Ž':
		return 0x8e, true
	case '‘':
		return 0x91, true
	case '’':
		return 0x92, true
	case '“':
		return 0x93, true
	case '”':
		return 0x94, true
	case '•':
		return 0x95, true
	case '–':
		return 0x96, true
	case '—':
		return 0x97, true
	case '˜':
		return 0x98, true
	case '™':
		return 0x99, true
	case 'š':
		return 0x9a, true
	case '›':
		return 0x9b, true
	case 'œ':
		return 0x9c, true
	case 'ž':
		return 0x9e, true
	case 'Ÿ':
		return 0x9f, true
	default:
		return 0, false
	}
}

func scoreCommandOutputText(commandOutputText string) int {
	score := 0
	for _, character := range commandOutputText {
		switch {
		case character == utf8.RuneError:
			score -= 8
		case character == '\n' || character == '\r' || character == '\t':
			score += 1
		case unicode.IsControl(character):
			score -= 6
		case unicode.IsLetter(character) || unicode.IsDigit(character):
			score += 3
		case unicode.IsSpace(character) || unicode.IsPunct(character) || unicode.IsSymbol(character):
			score += 1
		default:
			score -= 1
		}
		switch character {
		case 'Ã', 'Â':
			score -= 8
		case '�':
			score -= 12
		case 'Ä', 'Ö', 'Ü', 'ä', 'ö', 'ü', 'ß':
			score += 3
		}
	}
	return score
}
