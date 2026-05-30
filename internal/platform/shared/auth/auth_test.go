package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPermissionMatches(t *testing.T) {
	const required = "platform:messaging:event-type:view"
	cases := []struct {
		held string
		want bool
		why  string
	}{
		{"platform:messaging:event-type:view", true, "exact"},
		{"platform:*:*:*", true, "super-admin wildcard"},
		{"platform:messaging:*:*", true, "domain wildcard"},
		{"platform:messaging:event-type:*", true, "action wildcard"},
		{"platform:iam:*:*", false, "different domain"},
		{"platform:messaging:subscription:view", false, "different resource"},
		{"platform:messaging:event-type", false, "segment-count mismatch"},
	}
	for _, c := range cases {
		assert.Equalf(t, c.want, permissionMatches(c.held, required), "held=%q (%s)", c.held, c.why)
	}
}

// TestNonAnchorWithSeededPermissionIsAllowed is the regression test for the
// V2 bug: a non-anchor principal holding the actual stored 4-segment
// permission must pass the typed check. (The old logical names never matched
// any stored permission, so every non-anchor principal was denied.)
func TestNonAnchorWithSeededPermissionIsAllowed(t *testing.T) {
	a := &AuthContext{
		Scope:       ScopeClient,
		Permissions: []string{"platform:messaging:event-type:view"},
	}
	assert.NoError(t, CanReadEventTypes(a), "exact stored permission must be allowed")
	assert.Error(t, CanCreateEventTypes(a), "a view permission must not grant create")
}

func TestDomainWildcardGrantsWholeDomain(t *testing.T) {
	a := &AuthContext{
		Scope:       ScopeClient,
		Permissions: []string{"platform:messaging:*:*"},
	}
	assert.NoError(t, CanReadEventTypes(a))
	assert.NoError(t, CanWriteSubscriptions(a))
	assert.NoError(t, CanUpdateDispatchPools(a))
	assert.NoError(t, CanFireScheduledJobs(a))
	// The wildcard is scoped to the messaging domain — admin/iam are excluded.
	assert.Error(t, CanReadApplications(a), "messaging wildcard must not grant admin domain")
	assert.Error(t, CanReadRoles(a), "messaging wildcard must not grant iam domain")
}

func TestSuperAdminWildcardGrantsEverything(t *testing.T) {
	a := &AuthContext{Scope: ScopeClient, Permissions: []string{"platform:*:*:*"}}
	assert.NoError(t, CanReadEventTypes(a))
	assert.NoError(t, CanWriteApplications(a))
	assert.NoError(t, CanDeleteRoles(a))
	assert.NoError(t, IsAdmin(a), "super-admin wildcard satisfies IsAdmin")
}

func TestAnchorBypassesPermissionChecks(t *testing.T) {
	a := &AuthContext{Scope: ScopeAnchor} // no explicit permissions
	assert.NoError(t, CanReadEventTypes(a))
	assert.NoError(t, CanWriteConnections(a))
	assert.NoError(t, IsAdmin(a))
	assert.NoError(t, RequireAnchor(a))
}

func TestNonAnchorWithoutPermissionDenied(t *testing.T) {
	a := &AuthContext{Scope: ScopeClient, Permissions: []string{"platform:messaging:event:view"}}
	assert.Error(t, CanReadEventTypes(a), "an unrelated permission must not grant event-type view")
	assert.Error(t, RequireAnchor(a), "non-anchor must fail RequireAnchor")
	assert.Error(t, IsAdmin(a))
}
