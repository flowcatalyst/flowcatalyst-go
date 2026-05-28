package oauthapi

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMatchesRedirectURI(t *testing.T) {
	cases := []struct {
		uri        string
		registered []string
		want       bool
	}{
		{"https://app.example.com/cb", []string{"https://app.example.com/cb"}, true}, // exact
		{"https://app.example.com/cb", []string{"https://other/cb"}, false},          // no match
		{"https://x.example.com/cb", []string{"https://*.example.com/cb"}, true},     // wildcard one segment
		{"https://x.y.example.com/cb", []string{"https://*.example.com/cb"}, false},  // dotted segment rejected
		{"https://.example.com/cb", []string{"https://*.example.com/cb"}, false},     // empty segment rejected
		{"https://app.example.com/foo", []string{"https://app.example.com/*"}, true}, // trailing wildcard
		{"https://evil.com/cb", []string{"https://app.example.com/cb", "https://*.x.com/cb"}, false},
	}
	for _, c := range cases {
		if got := matchesRedirectURI(c.uri, c.registered); got != c.want {
			t.Errorf("matchesRedirectURI(%q, %v) = %v, want %v", c.uri, c.registered, got, c.want)
		}
	}
}

func TestPctEncode(t *testing.T) {
	cases := map[string]string{
		"abc-_.~":          "abc-_.~",
		"a b":              "a%20b",
		"https://x/cb?a=1": "https%3A%2F%2Fx%2Fcb%3Fa%3D1",
		"openid profile":   "openid%20profile",
	}
	for in, want := range cases {
		if got := pctEncode(in); got != want {
			t.Errorf("pctEncode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInvalidScopes(t *testing.T) {
	if got := invalidScopes("openid profile email offline_access", nil); len(got) != 0 {
		t.Errorf("standard scopes should be valid, got %v", got)
	}
	if got := invalidScopes("openid custom", []string{"custom"}); len(got) != 0 {
		t.Errorf("client scope should be valid, got %v", got)
	}
	got := invalidScopes("openid bogus", nil)
	if len(got) != 1 || got[0] != "bogus" {
		t.Errorf("want [bogus], got %v", got)
	}
}

func TestMaxAgeExceeded(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name     string
		maxAge   string
		issuedAt time.Time
		want     bool
	}{
		{"absent max_age", "", now.Add(-time.Hour), false},
		{"zero issuedAt", "60", time.Time{}, false},
		{"within window", "3600", now.Add(-10 * time.Minute), false},
		{"exceeded", "60", now.Add(-10 * time.Minute), true},
		{"max_age=0 always re-auth", "0", now.Add(-time.Second), true},
		{"invalid max_age ignored", "abc", now.Add(-time.Hour), false},
		{"negative max_age ignored", "-5", now.Add(-time.Hour), false},
	}
	for _, c := range cases {
		if got := maxAgeExceeded(c.maxAge, c.issuedAt); got != c.want {
			t.Errorf("%s: maxAgeExceeded(%q, %v) = %v, want %v", c.name, c.maxAge, c.issuedAt, got, c.want)
		}
	}
}

func TestAuthorizeUnsupportedResponseType(t *testing.T) {
	s := &State{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/oauth/authorize?response_type=token&redirect_uri=https://app/cb&state=xyz", nil)
	s.Authorize(rec, req)

	if rec.Code != 307 {
		t.Fatalf("status = %d, want 307", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "https://app/cb?") {
		t.Fatalf("Location = %q", loc)
	}
	if !strings.Contains(loc, "error=unsupported_response_type") || !strings.Contains(loc, "state=xyz") {
		t.Errorf("Location missing error/state: %q", loc)
	}
}

func TestAuthorizeMissingState(t *testing.T) {
	s := &State{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/oauth/authorize?response_type=code&redirect_uri=https://app/cb", nil)
	s.Authorize(rec, req)

	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["error"] != "invalid_request" {
		t.Errorf("error = %v, want invalid_request", body["error"])
	}
}
