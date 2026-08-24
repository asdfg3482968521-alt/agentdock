package redact

import (
	"strings"
	"testing"
)

func TestTextRedactsCredentialForms(t *testing.T) {
	secret := "D2_TEST_SECRET_7f2a"
	inputs := []string{
		`{"api_key":"` + secret + `"}`,
		"token=" + secret,
		"Authorization: Bearer " + secret,
		"Authorization: Basic " + secret,
		"https://user:" + secret + "@example.invalid/path",
	}
	for _, input := range inputs {
		if got := Text(input); strings.Contains(got, secret) {
			t.Fatalf("secret leaked for %q: %q", input, got)
		}
	}
}

func TestTextLeavesOrdinaryTextUnchanged(t *testing.T) {
	const input = "ordinary provider output without credential material"
	if got := Text(input); got != input {
		t.Fatalf("Text() = %q, want %q", got, input)
	}
}
