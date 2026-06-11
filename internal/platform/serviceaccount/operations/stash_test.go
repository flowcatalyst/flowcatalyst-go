package operations

import (
	"testing"
	"time"
)

// Pure in-memory tests for the one-shot secret stash. Stale entries are
// seeded by storing stashEntry values directly with a back-dated
// storedAt, so no clock manipulation or signature changes are needed.

func TestPopStashedSecretOnceThenGone(t *testing.T) {
	stashSecret("sa-once", "token", "plain")

	got, ok := PopStashedSecret("sa-once", "token")
	if !ok || got != "plain" {
		t.Fatalf("first pop = (%q, %v), want (%q, true)", got, ok, "plain")
	}
	if got, ok := PopStashedSecret("sa-once", "token"); ok {
		t.Fatalf("second pop = (%q, true), want absent", got)
	}
}

func TestPopStashedSecretExpiredIsAbsent(t *testing.T) {
	stash.Store(stashKey{"sa-expired", "token"}, stashEntry{
		plaintext: "plain",
		storedAt:  time.Now().Add(-stashTTL - time.Second),
	})

	if got, ok := PopStashedSecret("sa-expired", "token"); ok {
		t.Fatalf("expired pop = (%q, true), want absent", got)
	}
	// The expired entry must also be gone from the map, not just hidden.
	if _, ok := stash.Load(stashKey{"sa-expired", "token"}); ok {
		t.Fatal("expired entry still present after pop")
	}
}

func TestStashSecretSweepsStaleEntries(t *testing.T) {
	stash.Store(stashKey{"sa-stale", "signing_secret"}, stashEntry{
		plaintext: "old",
		storedAt:  time.Now().Add(-stashTTL - time.Second),
	})

	stashSecret("sa-fresh", "token", "new")

	if _, ok := stash.Load(stashKey{"sa-stale", "signing_secret"}); ok {
		t.Fatal("stale entry survived the sweep on store")
	}
	got, ok := PopStashedSecret("sa-fresh", "token")
	if !ok || got != "new" {
		t.Fatalf("fresh pop = (%q, %v), want (%q, true)", got, ok, "new")
	}
}
