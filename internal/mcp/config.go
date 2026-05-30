package mcp

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/client"
)

// defaultBaseURL is the platform URL assumed when none is configured — the
// canonical local dev listener. 1:1 with Rust config.rs.
const defaultBaseURL = "http://localhost:8080"

// Config is the resolved MCP server configuration: where the platform lives
// and the credentials used to mint API tokens.
type Config struct {
	// BaseURL is the platform API root (no trailing /api).
	BaseURL string
	// ClientID / ClientSecret are the OAuth2 client_credentials grant
	// credentials. When both are set the server mints + caches tokens; when
	// only ClientSecret is set it is used as a static bearer token
	// (back-compat); when neither is set the server calls the platform
	// unauthenticated (local dev only).
	ClientID     string
	ClientSecret string
}

// credentialsFile is the on-disk shape fc-dev's MCP bootstrap writes and the
// standalone server reads as a fallback. Matches the Rust mcp-credentials.json.
type credentialsFile struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	BaseURL      string `json:"base_url"`
}

// CredentialsPath is where fc-dev writes, and the MCP server reads, the
// bootstrapped local credentials: <user-cache>/flowcatalyst-dev/mcp-credentials.json.
// Mirrors the Rust dirs::cache_dir() location.
func CredentialsPath() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "flowcatalyst-dev", "mcp-credentials.json"), nil
}

// LoadConfig resolves the MCP config with env vars taking precedence over the
// credentials file, falling back to defaults. Resolution order (1:1 with Rust
// config.rs): FLOWCATALYST_URL / FLOWCATALYST_CLIENT_ID /
// FLOWCATALYST_CLIENT_SECRET → mcp-credentials.json → defaultBaseURL. Any field
// left unset by the env is filled from the credentials file when present.
func LoadConfig() Config {
	cfg := Config{
		BaseURL:      os.Getenv("FLOWCATALYST_URL"),
		ClientID:     os.Getenv("FLOWCATALYST_CLIENT_ID"),
		ClientSecret: os.Getenv("FLOWCATALYST_CLIENT_SECRET"),
	}

	if cfg.BaseURL == "" || cfg.ClientID == "" || cfg.ClientSecret == "" {
		if fc, ok := readCredentialsFile(); ok {
			if cfg.BaseURL == "" {
				cfg.BaseURL = fc.BaseURL
			}
			if cfg.ClientID == "" {
				cfg.ClientID = fc.ClientID
			}
			if cfg.ClientSecret == "" {
				cfg.ClientSecret = fc.ClientSecret
			}
		}
	}

	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	return cfg
}

func readCredentialsFile() (credentialsFile, bool) {
	path, err := CredentialsPath()
	if err != nil {
		return credentialsFile{}, false
	}
	return readCredentialsFileAt(path)
}

func readCredentialsFileAt(path string) (credentialsFile, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return credentialsFile{}, false
	}
	var fc credentialsFile
	if err := json.Unmarshal(data, &fc); err != nil {
		return credentialsFile{}, false
	}
	return fc, true
}

// WriteCredentialsFile persists local MCP credentials to CredentialsPath with
// 0600 permissions, creating the parent directory. Used by fc-dev's bootstrap.
func WriteCredentialsFile(clientID, clientSecret, baseURL string) error {
	path, err := CredentialsPath()
	if err != nil {
		return err
	}
	return writeCredentialsFileAt(path, clientID, clientSecret, baseURL)
}

func writeCredentialsFileAt(path, clientID, clientSecret, baseURL string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(credentialsFile{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		BaseURL:      baseURL,
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// newPlatformClient builds the platform API client for the given config,
// choosing the auth mode from which credentials are present (see Config).
func newPlatformClient(cfg Config) *client.FlowCatalystClient {
	opts := []client.Option{client.WithTimeout(15 * time.Second)}
	switch {
	case cfg.ClientID != "" && cfg.ClientSecret != "":
		tm := NewTokenManager(cfg.BaseURL, cfg.ClientID, cfg.ClientSecret, nil)
		opts = append(opts, client.WithTokenProvider(tm.Token))
	case cfg.ClientSecret != "":
		opts = append(opts, client.WithToken(cfg.ClientSecret))
	}
	return client.New(cfg.BaseURL, opts...)
}

// errNoCredentials is returned by RequireCredentials when neither env nor the
// credentials file supplied a client_id/secret — surfaced to the user as a
// "start fc-dev first" hint, matching Rust.
var errNoCredentials = errors.New(
	"no MCP credentials: set FLOWCATALYST_CLIENT_ID/FLOWCATALYST_CLIENT_SECRET, " +
		"or run `fc-dev start` to bootstrap the mcp-credentials.json in the OS cache dir " +
		"(macOS ~/Library/Caches, Linux ~/.cache; see CredentialsPath)")

// RequireCredentials returns errNoCredentials when the config has neither a
// client_id/secret pair nor a static secret. The standalone server uses this
// to fail fast; the in-process launcher tolerates empty creds (localhost).
func RequireCredentials(cfg Config) error {
	if cfg.ClientID == "" && cfg.ClientSecret == "" {
		return errNoCredentials
	}
	return nil
}
