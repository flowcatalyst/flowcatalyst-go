package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/client"
)

// ─── M5: OAuth client-credentials token manager ──────────────────────────

func TestTokenManagerCachesAndRefreshesBeforeExpiry(t *testing.T) {
	var hits int
	var lastForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_ = r.ParseForm()
		lastForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		// Short TTL so the refresh-before-expiry path is exercised by the clock.
		_, _ = w.Write([]byte(`{"access_token":"tok-` + itoa(hits) + `","token_type":"Bearer","expires_in":3600}`))
	}))
	defer srv.Close()

	tm := NewTokenManager(srv.URL, "cid", "csecret", srv.Client())
	clock := time.Unix(1_700_000_000, 0)
	tm.now = func() time.Time { return clock }

	// First call fetches.
	got, err := tm.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got != "tok-1" || hits != 1 {
		t.Fatalf("first call: got=%q hits=%d, want tok-1/1", got, hits)
	}
	// The grant must be a form-encoded client_credentials request.
	if lastForm.Get("grant_type") != "client_credentials" || lastForm.Get("client_id") != "cid" || lastForm.Get("client_secret") != "csecret" {
		t.Fatalf("unexpected token request form: %v", lastForm)
	}

	// Second call within validity reuses the cache.
	got, _ = tm.Token(context.Background())
	if got != "tok-1" || hits != 1 {
		t.Fatalf("cached call: got=%q hits=%d, want tok-1/1 (no refetch)", got, hits)
	}

	// Advance to within the 60s refresh buffer of expiry → must refetch.
	clock = clock.Add(3600*time.Second - 30*time.Second)
	got, _ = tm.Token(context.Background())
	if got != "tok-2" || hits != 2 {
		t.Fatalf("refresh call: got=%q hits=%d, want tok-2/2", got, hits)
	}
}

func TestTokenManagerErrorsOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
	}))
	defer srv.Close()

	tm := NewTokenManager(srv.URL, "cid", "bad", srv.Client())
	if _, err := tm.Token(context.Background()); err == nil {
		t.Fatal("expected error on 401 token response, got nil")
	}
}

// ─── M7: config resolution (env → file → default) ────────────────────────

func TestLoadConfigEnvPrecedenceAndDefault(t *testing.T) {
	t.Setenv("FLOWCATALYST_URL", "https://example.test")
	t.Setenv("FLOWCATALYST_CLIENT_ID", "envid")
	t.Setenv("FLOWCATALYST_CLIENT_SECRET", "envsecret")
	cfg := LoadConfig()
	if cfg.BaseURL != "https://example.test" || cfg.ClientID != "envid" || cfg.ClientSecret != "envsecret" {
		t.Fatalf("env not honoured: %+v", cfg)
	}

	t.Setenv("FLOWCATALYST_URL", "")
	t.Setenv("FLOWCATALYST_CLIENT_ID", "")
	t.Setenv("FLOWCATALYST_CLIENT_SECRET", "")
	// With no env and (in CI) no credentials file, BaseURL falls back to the default.
	if cfg := LoadConfig(); cfg.BaseURL == "" {
		t.Fatalf("BaseURL must never be empty; got %+v", cfg)
	}
}

func TestCredentialsFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "mcp-credentials.json")
	if err := writeCredentialsFileAt(path, "cid", "csecret", "https://p.test"); err != nil {
		t.Fatalf("write: %v", err)
	}
	fc, ok := readCredentialsFileAt(path)
	if !ok {
		t.Fatal("read returned ok=false")
	}
	if fc.ClientID != "cid" || fc.ClientSecret != "csecret" || fc.BaseURL != "https://p.test" {
		t.Fatalf("round-trip mismatch: %+v", fc)
	}
}

func TestRequireCredentials(t *testing.T) {
	if err := RequireCredentials(Config{}); err == nil {
		t.Fatal("empty creds must error")
	}
	if err := RequireCredentials(Config{ClientSecret: "s"}); err != nil {
		t.Fatalf("static secret should satisfy RequireCredentials: %v", err)
	}
}

// ─── M7: tool JSON output (the fmt.Sprintf %v fix) ────────────────────────

func TestToolReturnsPrettyJSONNotGoMap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/event-types" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"code":"order.created","status":"CURRENT"}]}`))
	}))
	defer srv.Close()

	s := NewWithClient(client.New(srv.URL))
	res, _, err := s.listEventTypes(context.Background(), nil, listEventTypesArgs{})
	if err != nil {
		t.Fatalf("listEventTypes: %v", err)
	}
	text := toolText(t, res)
	if !json.Valid([]byte(text)) {
		t.Fatalf("tool output is not valid JSON:\n%s", text)
	}
	if strings.Contains(text, "map[") {
		t.Fatalf("tool output looks like Go %%v formatting, not JSON:\n%s", text)
	}
	if !strings.Contains(text, "order.created") || !strings.Contains(text, "\n  ") {
		t.Fatalf("expected pretty-printed JSON containing the payload, got:\n%s", text)
	}
}

func TestGetSchemaFallsBackCurrentToFinalising(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// No CURRENT version; only FINALISING carries a schema.
		_, _ = w.Write([]byte(`{"specVersions":[{"status":"FINALISING","schema":{"type":"object","title":"X"}}]}`))
	}))
	defer srv.Close()

	s := NewWithClient(client.New(srv.URL))
	res, _, err := s.getSchema(context.Background(), nil, getSchemaArgs{ID: "evt_1"})
	if err != nil {
		t.Fatalf("getSchema: %v", err)
	}
	text := toolText(t, res)
	if !strings.Contains(text, `"title": "X"`) {
		t.Fatalf("expected FINALISING schema via CURRENT fallback, got:\n%s", text)
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────

func toolText(t *testing.T, res *mcpsdk.CallToolResult) string {
	t.Helper()
	if res == nil || len(res.Content) == 0 {
		t.Fatal("empty tool result")
	}
	tc, ok := res.Content[0].(*mcpsdk.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", res.Content[0])
	}
	return tc.Text
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
