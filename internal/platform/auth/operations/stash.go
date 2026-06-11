package operations

import (
	"sync"
	"time"
)

// secretStash is a process-local map for the create/rotate handlers to
// recover the plaintext after the UoW commit. See note in oauth_client.go.
var secretStash sync.Map

// stashTTL bounds how long an un-popped plaintext may sit in process
// memory. The legitimate pop happens microseconds after the stash, in
// the same request, so anything older means the handler never collected
// it (e.g. it died between commit and response). The plaintext must not
// outlive its single response, so such entries are discarded.
const stashTTL = 2 * time.Minute

// stashEntry pairs the plaintext with its stash time so PopStashedSecret
// and sweepStash can reject entries older than stashTTL.
type stashEntry struct {
	plaintext string
	storedAt  time.Time
}

// stashFresh reports whether an entry stored at storedAt is still within
// stashTTL as of now. Factored out so tests can exercise the expiry rule
// without manipulating the clock.
func stashFresh(storedAt, now time.Time) bool {
	return now.Sub(storedAt) < stashTTL
}

// sweepStash deletes entries that outlived stashTTL. Called on every
// store: the map holds at most a few in-flight entries, so a Range here
// is cheap and avoids a background goroutine.
func sweepStash(now time.Time) {
	secretStash.Range(func(k, v any) bool {
		if e, ok := v.(stashEntry); ok && !stashFresh(e.storedAt, now) {
			secretStash.Delete(k)
		}
		return true
	})
}

func stashSecret(clientID, plaintext string) {
	now := time.Now()
	sweepStash(now)
	secretStash.Store(clientID, stashEntry{plaintext: plaintext, storedAt: now})
}

// PopStashedSecret returns the once-readable plaintext for clientID.
// Called by the HTTP handler immediately after the use case succeeds.
// Entries older than stashTTL are treated as absent: a stale entry means
// the owning request never read it, and the plaintext must not be handed
// to anyone else later.
func PopStashedSecret(clientID string) (string, bool) {
	v, ok := secretStash.LoadAndDelete(clientID)
	if !ok {
		return "", false
	}
	e := v.(stashEntry)
	if !stashFresh(e.storedAt, time.Now()) {
		return "", false
	}
	return e.plaintext, true
}
