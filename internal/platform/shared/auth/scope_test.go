package auth

import "testing"

func scopePtr(s string) *string { return &s }

func TestCheckScopeAccessAndCanAccessScope(t *testing.T) {
	clientA := scopePtr("clt_A")
	clientB := scopePtr("clt_B")

	anchor := &AuthContext{Scope: ScopeAnchor}
	tenant := &AuthContext{Scope: ScopeClient, Clients: []string{"clt_A"}}
	superAdmin := &AuthContext{Scope: ScopeClient, Permissions: []string{"platform:*:*:*"}}

	cases := []struct {
		name    string
		ac      *AuthContext
		client  *string
		wantErr bool
	}{
		{"anchor accesses any client", anchor, clientB, false},
		{"anchor accesses platform-level", anchor, nil, false},
		{"tenant accesses own client", tenant, clientA, false},
		{"tenant denied another client", tenant, clientB, true},
		{"tenant denied platform-level", tenant, nil, true},
		{"super-admin allowed platform-level (None bypass)", superAdmin, nil, false},
		{"client-scoped super-admin denied arbitrary client (matches Rust Some branch)", superAdmin, clientA, true},
		{"nil context denied", nil, clientA, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotErr := CheckScopeAccess(tc.ac, tc.client) != nil
			if gotErr != tc.wantErr {
				t.Fatalf("CheckScopeAccess err=%v, wantErr=%v", gotErr, tc.wantErr)
			}
			// The bool form must agree (allowed == no error).
			if allowed := CanAccessScope(tc.ac, tc.client); allowed == tc.wantErr {
				t.Fatalf("CanAccessScope=%v disagrees with CheckScopeAccess (wantErr=%v)", allowed, tc.wantErr)
			}
		})
	}
}
