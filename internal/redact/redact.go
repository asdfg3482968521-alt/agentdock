package redact

import "regexp"

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)("(?:password|passwd|pwd|token|api[_-]?key|secret|credential)"\s*:\s*)("(?:\\.|[^"\\])*"|true|false|null|-?\d+(?:\.\d+)?)`),
	regexp.MustCompile(`(?i)((?:password|passwd|pwd|token|api[_-]?key|secret|credential)\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,;]+)`),
	regexp.MustCompile(`(?i)(authorization\s*:\s*bearer\s+)[^\s]+`),
	regexp.MustCompile(`(?i)(authorization\s*:\s*basic\s+)[^\s]+`),
	regexp.MustCompile(`(?i)(https?://[^:\s/@]+:)[^@\s]+@`),
}

// Text removes common credential material from text before it is logged or persisted.
func Text(text string) string {
	text = secretPatterns[0].ReplaceAllString(text, `${1}"[REDACTED]"`)
	for i := 1; i < len(secretPatterns); i++ {
		text = secretPatterns[i].ReplaceAllString(text, `${1}[REDACTED]`)
	}
	return text
}
