package secrets

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// EnvProvider reads secrets from environment variables.
//
//	ref: env://VAR_NAME
type EnvProvider struct{}

// NewEnvProvider constructs an env provider.
func NewEnvProvider() *EnvProvider { return &EnvProvider{} }

// Name returns the scheme.
func (*EnvProvider) Name() string { return "env" }

// Get reads VAR_NAME from os.Getenv.
func (*EnvProvider) Get(_ context.Context, key string) (string, error) {
	v := os.Getenv(key)
	if v == "" {
		return "", ErrNotFound
	}
	return v, nil
}

// Set is unsupported for env (env vars can be set in tests via os.Setenv;
// here we error so callers don't quietly drop writes in prod).
func (*EnvProvider) Set(_ context.Context, _, _ string) error {
	return errors.New("env provider is read-only")
}

// Delete is unsupported for env.
func (*EnvProvider) Delete(_ context.Context, key string) error {
	return fmt.Errorf("env provider is read-only (cannot delete %q)", key)
}
