package authservice

import (
	"strings"
	"testing"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal"
)

// TestBuildClientsFormat pins the `clients` JWT-claim contract the Laravel SDK
// (FlowCatalystUser) depends on: "*" for anchor access, otherwise "id:identifier"
// pairs split on ":" to match a tenant code. A bare id (empty identifier map)
// is the regression that produced "No access to this tenant" — the identifier
// half is what the SDK matches, so it must be present.
func TestBuildClientsFormat(t *testing.T) {
	idPtr := func(s string) *string { return &s }

	t.Run("anchor is wildcard", func(t *testing.T) {
		p := &principal.Principal{Scope: principal.ScopeAnchor}
		got := buildClients(p)
		if len(got) != 1 || got[0] != "*" {
			t.Fatalf("anchor: got %v, want [*]", got)
		}
	})

	t.Run("client scope emits id:identifier for the home client", func(t *testing.T) {
		p := &principal.Principal{
			Scope:               principal.ScopeClient,
			ClientID:            idPtr("clt_spar"),
			ClientIdentifierMap: map[string]string{"clt_spar": "spar"},
		}
		got := buildClients(p)
		if len(got) != 1 || got[0] != "clt_spar:spar" {
			t.Fatalf("client: got %v, want [clt_spar:spar]", got)
		}
		// The SDK extracts the identifier as the part after ":".
		if _, ident, _ := strings.Cut(got[0], ":"); ident != "spar" {
			t.Fatalf("identifier half = %q, want spar", ident)
		}
	})

	t.Run("partner scope emits id:identifier per granted client", func(t *testing.T) {
		p := &principal.Principal{
			Scope:           principal.ScopePartner,
			AssignedClients: []string{"clt_a", "clt_b"},
			ClientIdentifierMap: map[string]string{
				"clt_a": "alpha",
				"clt_b": "bravo",
			},
		}
		got := buildClients(p)
		want := map[string]bool{"clt_a:alpha": true, "clt_b:bravo": true}
		if len(got) != 2 || !want[got[0]] || !want[got[1]] {
			t.Fatalf("partner: got %v, want clt_a:alpha + clt_b:bravo", got)
		}
	})

	t.Run("missing identifier map falls back to bare id (the bug shape)", func(t *testing.T) {
		// Documents the failure mode: with no identifier hydrated, the claim is a
		// bare id and the SDK can't match the tenant. hydrateClientAccess prevents
		// this for real loads.
		p := &principal.Principal{Scope: principal.ScopeClient, ClientID: idPtr("clt_spar")}
		got := buildClients(p)
		if len(got) != 1 || got[0] != "clt_spar" {
			t.Fatalf("fallback: got %v, want [clt_spar]", got)
		}
	})
}
