package job

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"
)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)("(?:password|passwd|pwd|token|api[_-]?key|secret|credential)"\s*:\s*)("(?:\\.|[^"\\])*"|true|false|null|-?\d+(?:\.\d+)?)`),
	regexp.MustCompile(`(?i)((?:password|passwd|pwd|token|api[_-]?key|secret|credential)\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,;]+)`),
	regexp.MustCompile(`(?i)(authorization\s*:\s*bearer\s+)[^\s]+`),
	regexp.MustCompile(`(?i)(authorization\s*:\s*basic\s+)[^\s]+`),
	regexp.MustCompile(`(?i)(https?://[^:\s/@]+:)[^@\s]+@`),
}

func defaultRedactText(text string) string {
	text = secretPatterns[0].ReplaceAllString(text, `${1}"[REDACTED]"`)
	for i := 1; i < len(secretPatterns); i++ {
		text = secretPatterns[i].ReplaceAllString(text, `${1}[REDACTED]`)
	}
	return text
}

func summarize(value any, maxBytes int, redact func(string) string) string {
	body, err := json.Marshal(value)
	if err != nil {
		body = []byte(`"[unserializable]"`)
	}
	return truncateUTF8(redact(string(body)), maxBytes)
}

func truncateUTF8(text string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len([]byte(text)) <= maxBytes {
		return text
	}
	suffix := "…[truncated]"
	budget := maxBytes - len([]byte(suffix))
	if budget <= 0 {
		return truncateRawUTF8(suffix, maxBytes)
	}
	return truncateRawUTF8(text, budget) + suffix
}

func truncateRawUTF8(text string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(text) <= maxBytes {
		return text
	}
	end := maxBytes
	for end > 0 && !utf8.ValidString(text[:end]) {
		end--
	}
	return text[:end]
}

func splitUTF8(text string, maxBytes int) []string {
	if text == "" {
		return nil
	}
	if maxBytes <= 0 {
		maxBytes = 1
	}
	var parts []string
	for text != "" {
		if len(text) <= maxBytes {
			parts = append(parts, text)
			break
		}
		part := truncateRawUTF8(text, maxBytes)
		if part == "" {
			_, size := utf8.DecodeRuneInString(text)
			if size <= 0 {
				size = 1
			}
			part = text[:size]
		}
		parts = append(parts, part)
		text = strings.TrimPrefix(text, part)
	}
	return parts
}
