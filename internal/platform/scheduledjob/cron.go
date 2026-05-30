package scheduledjob

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// cronParser matches the Rust `cron` crate's accepted shape as closely as
// robfig/cron permits: seconds are REQUIRED, so the canonical firing form is
// the 6-field "sec min hour dom mon dow".
//
// The Rust crate additionally accepts an optional 7th *year* field, which
// robfig/cron does not model — a 7-field expression therefore fails to parse
// here and the slot is simply skipped. A bare 5-field POSIX expression also
// fails to parse (seconds required), which matches the Rust crate: such an
// expression is accepted at create time by ValidateCronShape but never fires.
var cronParser = cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// ValidateCronShape mirrors the Rust validate_cron_shape: a cron expression
// must have 5–7 whitespace-separated fields. This is a *shape* gate at
// create/update time; the firing parser (cronParser) is stricter (it needs
// the 6-field seconds-first form), exactly as in Rust where a 5-field
// expression passes validation but never produces a fire slot.
func ValidateCronShape(expr string) error {
	n := len(strings.Fields(strings.TrimSpace(expr)))
	if n < 5 || n > 7 {
		return fmt.Errorf("cron expression must have 5-7 whitespace-separated fields, got %d: '%s'",
			n, strings.TrimSpace(expr))
	}
	return nil
}

// LatestSlotInWindow returns the latest cron slot in the half-open window
// (after, upTo] across all of a job's cron expressions, evaluated in tzName.
//
// Mirrors the Rust latest_slot_in_window — "skip-missed" (AWS-style)
// semantics: when a long downtime means several slots elapsed, only the
// LATEST fires; older missed slots are dropped. Returns ok=false when no slot
// falls in the window (including after >= upTo). Unparseable expressions are
// skipped (a 5- or 7-field expression that passed ValidateCronShape simply
// yields no slot).
func LatestSlotInWindow(crons []string, tzName string, after, upTo time.Time) (time.Time, bool) {
	if !after.Before(upTo) {
		return time.Time{}, false
	}
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		loc = time.UTC
	}
	afterLoc := after.In(loc)
	upToLoc := upTo.In(loc)

	var best time.Time
	found := false
	for _, expr := range crons {
		sched, err := cronParser.Parse(expr)
		if err != nil {
			continue
		}
		// Walk forward from `after` (exclusive) collecting slots <= upTo,
		// keeping the latest. Next() is strictly increasing, so this
		// terminates at the window edge.
		for slot := sched.Next(afterLoc); !slot.IsZero() && !slot.After(upToLoc); slot = sched.Next(slot) {
			if !found || slot.After(best) {
				best = slot
				found = true
			}
		}
	}
	if !found {
		return time.Time{}, false
	}
	return best.UTC(), true
}
