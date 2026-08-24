package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/uvwt/agentdock/internal/config"
	"github.com/uvwt/agentdock/internal/credential"
)

const maxCredentialInputBytes = 64 << 10

func runCredentialCommand(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("用法：agentdock credential <set|list|delete>")
	}
	store, err := credentialStoreFromEnv()
	if err != nil {
		return err
	}

	switch args[0] {
	case "set":
		flags := flag.NewFlagSet("agentdock credential set", flag.ContinueOnError)
		flags.SetOutput(stderr)
		ref := flags.String("ref", "", "opaque credential_ref")
		provider := flags.String("provider", "", "provider name")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || strings.TrimSpace(*ref) == "" || strings.TrimSpace(*provider) == "" {
			return errors.New("用法：agentdock credential set --ref <credential_ref> --provider <provider> < secret-from-stdin")
		}
		secret, err := readCredentialSecret(stdin)
		if err != nil {
			return err
		}
		defer credential.Zero(secret)
		metadata, err := store.Put(ctx, strings.TrimSpace(*ref), strings.TrimSpace(*provider), secret)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "credential stored: %s provider=%s version=%d\n", metadata.Ref, metadata.Provider, metadata.Version)
		return nil

	case "list":
		flags := flag.NewFlagSet("agentdock credential list", flag.ContinueOnError)
		flags.SetOutput(stderr)
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("用法：agentdock credential list")
		}
		items, err := store.List(ctx)
		if err != nil {
			return err
		}
		for _, item := range items {
			fmt.Fprintf(stdout, "%s\t%s\tv%d\t%s\n", item.Ref, item.Provider, item.Version, item.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"))
		}
		return nil

	case "delete":
		flags := flag.NewFlagSet("agentdock credential delete", flag.ContinueOnError)
		flags.SetOutput(stderr)
		ref := flags.String("ref", "", "opaque credential_ref")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || strings.TrimSpace(*ref) == "" {
			return errors.New("用法：agentdock credential delete --ref <credential_ref>")
		}
		refValue := strings.TrimSpace(*ref)
		removed, err := store.Delete(ctx, refValue)
		if err != nil {
			return err
		}
		if removed {
			fmt.Fprintf(stdout, "credential deleted: %s\n", refValue)
		} else {
			fmt.Fprintf(stdout, "credential not found: %s\n", refValue)
		}
		return nil

	default:
		return errors.New("用法：agentdock credential <set|list|delete>")
	}
}

func credentialStoreFromEnv() (*credential.Store, error) {
	cfg, err := config.FromEnv()
	if err != nil {
		return nil, err
	}
	if err := cfg.Normalize(); err != nil {
		return nil, err
	}
	return credential.NewStore(cfg.AgentDockHome)
}

func readCredentialSecret(reader io.Reader) (credential.Secret, error) {
	if reader == nil {
		return nil, errors.New("credential secret must be provided on stdin")
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxCredentialInputBytes+3))
	if err != nil {
		return nil, fmt.Errorf("read credential secret from stdin: %w", err)
	}
	if bytes.HasSuffix(data, []byte("\r\n")) {
		data = data[:len(data)-2]
	} else if bytes.HasSuffix(data, []byte("\n")) {
		data = data[:len(data)-1]
	}
	if len(data) == 0 {
		return nil, credential.ErrEmptySecret
	}
	if len(data) > maxCredentialInputBytes {
		credential.Zero(credential.Secret(data))
		return nil, fmt.Errorf("credential secret exceeds %d bytes", maxCredentialInputBytes)
	}
	return credential.Secret(data), nil
}
