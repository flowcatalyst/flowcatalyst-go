package stream

import (
	"testing"
	"time"
)

func TestParsePartitionEnd(t *testing.T) {
	cases := []struct {
		name   string
		parent string
		want   string // exclusive end (RFC3339 date), "" => ok=false
	}{
		{"msg_events_2026_03", "msg_events", "2026-04-01"},
		{"msg_events_2026_12", "msg_events", "2027-01-01"},
		{"msg_events_read_2026_01", "msg_events_read", "2026-02-01"},
		{"msg_events_default", "msg_events", ""},        // not YYYY_MM
		{"msg_events_2026_13", "msg_events", ""},        // bad month
		{"msg_dispatch_jobs_2026_06", "msg_events", ""}, // wrong parent prefix
		{"msg_events", "msg_events", ""},                // no suffix
	}
	for _, c := range cases {
		end, ok := parsePartitionEnd(c.name, c.parent)
		if c.want == "" {
			if ok {
				t.Errorf("parsePartitionEnd(%q,%q) = %s, want ok=false", c.name, c.parent, end)
			}
			continue
		}
		if !ok {
			t.Errorf("parsePartitionEnd(%q,%q) ok=false, want %s", c.name, c.parent, c.want)
			continue
		}
		want, _ := time.Parse("2006-01-02", c.want)
		if !end.Equal(want.UTC()) {
			t.Errorf("parsePartitionEnd(%q) = %s, want %s", c.name, end, want)
		}
	}
}

func TestParsePartitionEndDriveRetention(t *testing.T) {
	// A 2026-01 partition ends 2026-02-01; with a 90-day retention measured
	// from 2026-06-01 the cutoff is ~2026-03-03, so the partition is expired.
	end, ok := parsePartitionEnd("msg_events_2026_01", "msg_events")
	if !ok {
		t.Fatal("expected parse ok")
	}
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	cutoff := now.AddDate(0, 0, -90)
	if end.After(cutoff) {
		t.Errorf("expected 2026-01 partition (end %s) to be <= cutoff %s", end, cutoff)
	}
	// A 2026-05 partition ends 2026-06-01, which is after the cutoff → kept.
	end2, _ := parsePartitionEnd("msg_events_2026_05", "msg_events")
	if !end2.After(cutoff) {
		t.Errorf("expected 2026-05 partition (end %s) to be > cutoff %s", end2, cutoff)
	}
}
