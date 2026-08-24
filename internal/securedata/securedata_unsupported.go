//go:build !windows

package securedata

func Protect(plaintext, entropy []byte) ([]byte, error) {
	return nil, ErrUnsupported
}

func Unprotect(ciphertext, entropy []byte) ([]byte, error) {
	return nil, ErrUnsupported
}
