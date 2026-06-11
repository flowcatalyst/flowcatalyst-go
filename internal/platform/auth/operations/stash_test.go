package operations

import (
	"testing"
	"time"
)

// Pure in-memory tests for the one-shot secret stash. Stale entries are
// seeded by storing stashEntry values directly with a back-dated
// storedAt, so no clock manipulation or signature changes are needed.

func TestPopStashedSecretOnceThenGone(t *testing.T) {
	stashSecret("client-once", "plain")

	got, ok := PopStashedSecret("client-once")
	if !ok || got != "plain" {
		t.Fatalf("first pop = (%q, %v), want (%q, true)", got, ok, "plain")
	}
	if got, ok := PopStashedSecret("client-once"); ok {
		t.Fatalf("second pop = (%q, true), want absent", got)
	}
}

func TestPopStashedSecretExpiredIsAbsent(t *testing.T) {
	secretStash.Store("client-expired", stashEntry{
		plaintext: "plain",
		storedAt:  time.Now().Add(-stashTTL - time.Second),
	})

	if got, ok := PopStashedSecret("client-expired"); ok {
		t.Fatalf("expired pop = (%q, true), want absent", got)
	}
	// The expired entry must also be gone from the map, not just hidden.
	if _, ok := secretStash.Load("client-expired"); ok {
		t.Fatal("expired entry still present after pop")
	}
}

func TestStashSecretSweepsStaleEntries(t *testing.T) {
	secretStash.Store("client-stale", stashEntry{
		plaintext: "old",
		storedAt:  time.Now().Add(-stashTTL - time.Second),
	})

	stashSecret("client-fresh", "new")

	if _, ok := secretStash.Load("client-stale"); ok {
		t.Fatal("stale entry survived the sweep on store")
	}
	got, ok := PopStashedSecret("client-fresh")
	if !ok || got != "new" {
		t.Fatalf("fresh pop = (%q, %v), want (%q, true)", got, ok, "new")
	}
}
