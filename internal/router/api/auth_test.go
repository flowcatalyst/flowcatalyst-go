package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	routerapi "github.com/flowcatalyst/flowcatalyst-go/internal/router/api"
)

func TestBasicAuth_DisabledWhenEmpty(t *testing.T) {
	r := chi.NewRouter()
	r.Use(routerapi.BasicAuthMiddleware(routerapi.BasicAuthConfig{})) // empty cfg
	r.Get("/anything", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/anything", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (auth disabled)", rec.Code)
	}
}

func TestBasicAuth_RejectsMissingCreds(t *testing.T) {
	r := chi.NewRouter()
	r.Use(routerapi.BasicAuthMiddleware(routerapi.BasicAuthConfig{Username: "u", Password: "p"}))
	r.Get("/secret", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/secret", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, "Basic ") {
		t.Errorf("WWW-Authenticate=%q want Basic realm=...", got)
	}
}

func TestBasicAuth_AcceptsValidCreds(t *testing.T) {
	r := chi.NewRouter()
	r.Use(routerapi.BasicAuthMiddleware(routerapi.BasicAuthConfig{Username: "u", Password: "p"}))
	r.Get("/secret", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/secret", nil)
	req.SetBasicAuth("u", "p")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
}

func TestBasicAuth_RejectsWrongCreds(t *testing.T) {
	r := chi.NewRouter()
	r.Use(routerapi.BasicAuthMiddleware(routerapi.BasicAuthConfig{Username: "u", Password: "p"}))
	r.Get("/secret", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/secret", nil)
	req.SetBasicAuth("u", "wrong")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status=%d want 401", rec.Code)
	}
}

func TestBasicAuth_PublicPathsBypass(t *testing.T) {
	r := chi.NewRouter()
	r.Use(routerapi.BasicAuthMiddleware(routerapi.BasicAuthConfig{Username: "u", Password: "p"}))
	for _, path := range []string{
		"/health", "/health/live", "/health/ready", "/metrics", "/openapi.json", "/docs", "/docs/", "/docs/foo",
	} {
		r.Get(path, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})

		req := httptest.NewRequest("GET", path, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("path=%q status=%d want 200 (public bypass)", path, rec.Code)
		}
	}
}

func TestIsPublicPath(t *testing.T) {
	cases := map[string]bool{
		"/health":              true,
		"/health/live":         true,
		"/metrics":             true,
		"/openapi.json":        true,
		"/docs":                true,
		"/docs/swagger-ui.css": true,
		"/messages":            false,
		"/monitoring/pools":    false,
		"/docsy":               false, // not a prefix match
	}
	for path, want := range cases {
		if got := routerapi.IsPublicPath(path); got != want {
			t.Errorf("IsPublicPath(%q)=%v want %v", path, got, want)
		}
	}
}
