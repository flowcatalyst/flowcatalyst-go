package scheduledjob

import (
	"testing"
	"time"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts.UTC()
}

func TestValidateCronShape(t *testing.T) {
	ok := []string{
		"* * * * *",        // 5-field
		"0 * * * * *",      // 6-field (sec-first)
		"0 0 0 * * * 2030", // 7-field (with year)
		"  0 0 * * * *  ",  // surrounding whitespace
	}
	for _, c := range ok {
		if err := ValidateCronShape(c); err != nil {
			t.Errorf("ValidateCronShape(%q) = %v, want nil", c, err)
		}
	}
	bad := []string{"", "* * *", "* * * *", "a b c d e f g h"}
	for _, c := range bad {
		if err := ValidateCronShape(c); err == nil {
			t.Errorf("ValidateCronShape(%q) = nil, want error", c)
		}
	}
}

func TestLatestSlotInWindow_SkipsMissedToLatest(t *testing.T) {
	// Every-minute (6-field sec-first: at second 0 of every minute). Window
	// spans ~3.5 minutes — several slots elapsed; only the LATEST must fire.
	after := mustTime(t, "2026-05-29T10:00:30Z")
	upTo := mustTime(t, "2026-05-29T10:03:45Z")
	slot, ok := LatestSlotInWindow([]string{"0 * * * * *"}, "UTC", after, upTo)
	if !ok {
		t.Fatal("expected a slot in the window")
	}
	want := mustTime(t, "2026-05-29T10:03:00Z")
	if !slot.Equal(want) {
		t.Errorf("latest slot = %s, want %s (skip-to-latest)", slot, want)
	}
}

func TestLatestSlotInWindow_NoSlotInWindow(t *testing.T) {
	// Daily-at-midnight; a mid-morning window contains no slot.
	after := mustTime(t, "2026-05-29T10:00:00Z")
	upTo := mustTime(t, "2026-05-29T11:00:00Z")
	if _, ok := LatestSlotInWindow([]string{"0 0 0 * * *"}, "UTC", after, upTo); ok {
		t.Error("expected no slot in a window with no midnight")
	}
}

func TestLatestSlotInWindow_EmptyWindow(t *testing.T) {
	ts := mustTime(t, "2026-05-29T10:00:00Z")
	if _, ok := LatestSlotInWindow([]string{"0 * * * * *"}, "UTC", ts, ts); ok {
		t.Error("expected ok=false when after == upTo")
	}
}

func TestLatestSlotInWindow_MultipleCronsTakesLatest(t *testing.T) {
	// Two crons; the later of the two matching slots within the window wins.
	after := mustTime(t, "2026-05-29T10:00:00Z")
	upTo := mustTime(t, "2026-05-29T10:30:00Z")
	slot, ok := LatestSlotInWindow([]string{"0 15 * * * *", "0 25 * * * *"}, "UTC", after, upTo)
	if !ok {
		t.Fatal("expected a slot")
	}
	want := mustTime(t, "2026-05-29T10:25:00Z")
	if !slot.Equal(want) {
		t.Errorf("latest across crons = %s, want %s", slot, want)
	}
}

func TestLatestSlotInWindow_FiveFieldNeverFires(t *testing.T) {
	// A 5-field POSIX expression passes ValidateCronShape but the firing
	// parser requires seconds, so it yields no slot — matching Rust.
	if err := ValidateCronShape("* * * * *"); err != nil {
		t.Fatalf("5-field should pass shape validation: %v", err)
	}
	after := mustTime(t, "2026-05-29T10:00:00Z")
	upTo := mustTime(t, "2026-05-29T10:05:00Z")
	if _, ok := LatestSlotInWindow([]string{"* * * * *"}, "UTC", after, upTo); ok {
		t.Error("5-field expression must not produce a fire slot")
	}
}
