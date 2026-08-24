package credential

import (
	"context"
	"errors"
	"fmt"
	"regexp"
)

var credentialRefPattern = regexp.MustCompile(`^cred_[A-Za-z0-9][A-Za-z0-9._-]{2,127}$`)

var (
	ErrInvalidRef       = errors.New("invalid credential reference")
	ErrNotFound         = errors.New("credential not found")
	ErrInvalidRecord    = errors.New("invalid credential record")
	ErrProviderMismatch = errors.New("credential provider mismatch")
	ErrInvalidProvider  = errors.New("invalid credential provider")
	ErrEmptySecret      = errors.New("credential secret is empty")
)

type Secret []byte

type Resolver interface {
	Resolve(context.Context, string) (Secret, error)
}

func ValidateRef(ref string) error {
	if !credentialRefPattern.MatchString(ref) {
		return fmt.Errorf("%w: must match %s", ErrInvalidRef, credentialRefPattern.String())
	}
	return nil
}

func CloneSecret(secret Secret) Secret {
	return append(Secret(nil), secret...)
}

func Zero(secret Secret) {
	for i := range secret {
		secret[i] = 0
	}
}
