package securedata

import "errors"

var (
	// ErrUnsupported is returned when the current platform has no secure data backend.
	ErrUnsupported = errors.New("secure data backend unsupported")

	errEmptyPlaintext  = errors.New("secure data plaintext is empty")
	errEmptyCiphertext = errors.New("secure data ciphertext is empty")
	errEmptyOutput     = errors.New("secure data backend returned empty output")
)
