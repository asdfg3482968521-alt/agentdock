//go:build windows

package securedata

import (
	"bytes"
	"strings"
	"testing"
)

func TestProtectUnprotectRoundTrip(t *testing.T) {
	plain := []byte("D2_TEST_SECRET_7f2a")
	entropy := []byte("agentdock.d2.securedata.test.v1")

	ciphertext, err := Protect(plain, entropy)
	if err != nil {
		t.Fatalf("Protect() error = %v", err)
	}
	if bytes.Contains(ciphertext, plain) {
		t.Fatalf("ciphertext contains plaintext marker")
	}

	got, err := Unprotect(ciphertext, entropy)
	if err != nil {
		t.Fatalf("Unprotect() error = %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("Unprotect() = %q, want %q", got, plain)
	}
}

func TestUnprotectRejectsWrongEntropy(t *testing.T) {
	plain := []byte("D2_TEST_SECRET_7f2a")
	ciphertext, err := Protect(plain, []byte("entropy-a"))
	if err != nil {
		t.Fatalf("Protect() error = %v", err)
	}
	got, err := Unprotect(ciphertext, []byte("entropy-b"))
	if err == nil && bytes.Equal(got, plain) {
		t.Fatalf("Unprotect() unexpectedly returned plaintext with wrong entropy")
	}
}

func TestProtectRejectsEmptyPlaintext(t *testing.T) {
	if _, err := Protect(nil, []byte("entropy")); err == nil {
		t.Fatal("Protect() error = nil, want rejection for empty plaintext")
	}
}

func TestUnprotectRejectsEmptyCiphertext(t *testing.T) {
	if _, err := Unprotect(nil, []byte("entropy")); err == nil {
		t.Fatal("Unprotect() error = nil, want rejection for empty ciphertext")
	}
}

func TestUnprotectRejectsMalformedCiphertext(t *testing.T) {
	if _, err := Unprotect([]byte("not-a-dpapi-blob"), []byte("entropy")); err == nil {
		t.Fatal("Unprotect() error = nil, want rejection for malformed ciphertext")
	}
}

func TestUnprotectErrorDoesNotLeakPlaintext(t *testing.T) {
	plain := []byte("D2_TEST_SECRET_error_leak")
	ciphertext, err := Protect(plain, []byte("entropy-a"))
	if err != nil {
		t.Fatalf("Protect() error = %v", err)
	}
	if _, err := Unprotect(ciphertext, []byte("entropy-b")); err == nil {
		t.Fatal("Unprotect() error = nil, want wrong-entropy failure")
	} else if strings.Contains(err.Error(), string(plain)) {
		t.Fatalf("Unprotect() error leaked plaintext marker")
	}
}
