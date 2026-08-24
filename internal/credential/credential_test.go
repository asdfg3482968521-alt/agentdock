package credential

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateRefMatchesD2ContractGrammar(t *testing.T) {
	valid := []string{
		"cred_abc",
		"cred_anthropic_default",
		"cred_DeepSeek.01-prod",
	}
	for _, ref := range valid {
		if err := ValidateRef(ref); err != nil {
			t.Fatalf("ValidateRef(%q) error = %v", ref, err)
		}
	}

	invalid := []string{
		"",
		"cred_ab",
		"cred__abc",
		"cred_-abc",
		"cred_a/b",
		"credential_abc",
		"cred_" + strings.Repeat("a", 129),
	}
	for _, ref := range invalid {
		if err := ValidateRef(ref); !errors.Is(err, ErrInvalidRef) {
			t.Fatalf("ValidateRef(%q) error = %v, want ErrInvalidRef", ref, err)
		}
	}
}

func TestCloneSecretAndZero(t *testing.T) {
	original := Secret("D2_TEST_SECRET_clone_zero")
	clone := CloneSecret(original)
	if string(clone) != string(original) {
		t.Fatalf("CloneSecret() = %q, want %q", clone, original)
	}
	clone[0] ^= 0xff
	if clone[0] == original[0] {
		t.Fatal("CloneSecret() shares backing storage")
	}
	Zero(original)
	for i, value := range original {
		if value != 0 {
			t.Fatalf("Zero() byte %d = %d, want 0", i, value)
		}
	}
}
