package api

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// BasicAuthConfig configures the optional HTTP BasicAuth middleware.
// Empty Username disables auth entirely (matches Rust AuthMode::None).
type BasicAuthConfig struct {
	Username string
	Password string
	// Realm is the WWW-Authenticate realm shown by browsers in the
	// password prompt. Defaults to "FlowCatalyst Router" when empty.
	Realm string
}

// publicPaths are the URLs that bypass authentication. Mirrors Rust's
// is_public_path: probes + Prometheus + the OpenAPI surface must be
// reachable by orchestration tooling without credentials.
var publicPaths = map[string]struct{}{
	"/health":           {},
	"/q/health":         {},
	"/health/live":      {},
	"/health/ready":     {},
	"/health/startup":   {},
	"/q/health/live":    {},
	"/q/health/ready":   {},
	"/metrics":          {},
	"/q/metrics":        {},
	"/ready":            {},
	"/openapi.json":     {},
	"/openapi.yaml":     {},
	"/openapi-3.0.json": {},
	"/openapi-3.0.yaml": {},
	"/openapi-3.1.json": {},
	"/openapi-3.1.yaml": {},
}

// publicPrefixes covers paths where any subroute is public — the docs
// renderer serves arbitrary asset paths under /docs.
var publicPrefixes = []string{"/docs"}

// IsPublicPath reports whether path is exempt from auth.
func IsPublicPath(path string) bool {
	if _, ok := publicPaths[path]; ok {
		return true
	}
	for _, p := range publicPrefixes {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

// BasicAuthMiddleware returns a chi-compatible middleware that enforces
// HTTP BasicAuth on every non-public route. A zero Config disables auth
// (returns the identity middleware) so callers can wire it
// unconditionally and let env config decide.
func BasicAuthMiddleware(cfg BasicAuthConfig) func(http.Handler) http.Handler {
	if cfg.Username == "" {
		// No-op when not configured.
		return func(next http.Handler) http.Handler { return next }
	}
	realm := cfg.Realm
	if realm == "" {
		realm = "FlowCatalyst Router"
	}
	expectedUser := []byte(cfg.Username)
	expectedPass := []byte(cfg.Password)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if IsPublicPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			user, pass, ok := r.BasicAuth()
			if !ok ||
				subtle.ConstantTimeCompare([]byte(user), expectedUser) != 1 ||
				subtle.ConstantTimeCompare([]byte(pass), expectedPass) != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="`+realm+`", charset="UTF-8"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
