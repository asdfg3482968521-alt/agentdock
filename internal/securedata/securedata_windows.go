//go:build windows

package securedata

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Protect encrypts plaintext for the current Windows user with DPAPI.
func Protect(plaintext, entropy []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, errEmptyPlaintext
	}

	input := windows.DataBlob{Size: uint32(len(plaintext)), Data: &plaintext[0]}
	optionalEntropy := dataBlob(entropy)
	var output windows.DataBlob
	if err := windows.CryptProtectData(&input, nil, optionalEntropy, 0, nil, 0, &output); err != nil {
		return nil, fmt.Errorf("protect secure data: %w", err)
	}
	return copyOutput(&output)
}

// Unprotect decrypts DPAPI ciphertext for the current Windows user.
func Unprotect(ciphertext, entropy []byte) ([]byte, error) {
	if len(ciphertext) == 0 {
		return nil, errEmptyCiphertext
	}

	input := windows.DataBlob{Size: uint32(len(ciphertext)), Data: &ciphertext[0]}
	optionalEntropy := dataBlob(entropy)
	var output windows.DataBlob
	if err := windows.CryptUnprotectData(&input, nil, optionalEntropy, 0, nil, 0, &output); err != nil {
		return nil, fmt.Errorf("unprotect secure data: %w", err)
	}
	return copyOutput(&output)
}

func dataBlob(data []byte) *windows.DataBlob {
	if len(data) == 0 {
		return nil
	}
	return &windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
}

func copyOutput(output *windows.DataBlob) ([]byte, error) {
	if output.Data != nil {
		defer windows.LocalFree(windows.Handle(uintptr(unsafe.Pointer(output.Data))))
	}
	if output.Data == nil || output.Size == 0 {
		return nil, errEmptyOutput
	}
	return append([]byte(nil), unsafe.Slice(output.Data, int(output.Size))...), nil
}
