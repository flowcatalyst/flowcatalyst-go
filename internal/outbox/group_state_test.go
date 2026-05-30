package outbox

import "testing"

func TestGroupStateManager(t *testing.T) {
	m := NewGroupStateManager()

	// Default → Running/active.
	if !m.IsActive("g1") {
		t.Fatal("absent group must default to active (Running)")
	}

	// Pause → inactive; Resume → active.
	m.Pause("g1")
	if m.IsActive("g1") {
		t.Fatal("paused group must be inactive")
	}
	m.Resume("g1")
	if !m.IsActive("g1") {
		t.Fatal("resumed group must be active")
	}

	// Block → inactive; recorded in Snapshot + Blocked.
	m.Block("g1", "itm-poison", "boom")
	if m.IsActive("g1") {
		t.Fatal("blocked group must be inactive")
	}
	if b := m.Blocked(); len(b) != 1 || b[0].Group != "g1" || b[0].BlockedItemID != "itm-poison" || b[0].Error != "boom" {
		t.Fatalf("Blocked() = %+v, want g1/itm-poison/boom", b)
	}
	// Pause is a no-op on a Blocked group.
	m.Pause("g1")
	if b := m.Blocked(); len(b) != 1 {
		t.Fatal("Pause must not override Blocked")
	}

	// ClearBlock returns the poison id + transitions to Running.
	id, ok := m.ClearBlock("g1")
	if !ok || id != "itm-poison" {
		t.Fatalf("ClearBlock = (%q,%v), want (itm-poison,true)", id, ok)
	}
	if !m.IsActive("g1") {
		t.Fatal("group must be active after ClearBlock")
	}
	if _, ok := m.ClearBlock("g1"); ok {
		t.Fatal("ClearBlock on a non-blocked group must be ok=false")
	}
}
