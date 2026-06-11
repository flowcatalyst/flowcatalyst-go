package operations

import (
	"context"
	"crypto/rand"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/serviceaccount"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/httperror"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/commit"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

// RegenerateAuthTokenCommand rotates the service account's bearer token.
type RegenerateAuthTokenCommand struct {
	ServiceAccountID string `json:"serviceAccountId"`
}

// RegenerateAuthToken rotates the service account's bearer token. After
// the commit, the plaintext token lands in a process-local stash so the
// HTTP handler can return it once and only once.
func RegenerateAuthToken(
	ctx context.Context,
	repo *serviceaccount.Repository,
	uow *usecasepgx.UnitOfWork,
	cmd RegenerateAuthTokenCommand,
	ec usecase.ExecutionContext,
) (commit.Committed[ServiceAccountTokenRegenerated], error) {
	var zero commit.Committed[ServiceAccountTokenRegenerated]

	if strings.TrimSpace(cmd.ServiceAccountID) == "" {
		return zero, usecase.Validation("SERVICE_ACCOUNT_ID_REQUIRED", "Service account ID is required")
	}

	sa, err := repo.FindByID(ctx, cmd.ServiceAccountID)
	if err != nil {
		return zero, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if sa == nil {
		return zero, httperror.NotFound("ServiceAccount", cmd.ServiceAccountID)
	}

	token := generateAuthToken()
	sa.WebhookCredentials.Token = &token
	sa.WebhookCredentials.AuthType = serviceaccount.AuthBearer
	sa.UpdatedAt = time.Now().UTC()

	stashSecret(sa.ID, "token", token)

	event := ServiceAccountTokenRegenerated{
		Metadata:         usecase.NewEventMetadata(ec, ServiceAccountTokenRegeneratedType, Source, subjectFor(sa.ID)),
		ServiceAccountID: sa.ID,
		Code:             sa.Code,
	}
	return commit.Save(ctx, uow, sa, repo, event, cmd)
}

// generateAuthToken returns "fc_" + 32 lowercase-alphanumeric chars.
// Matches the Rust port byte-for-byte (length 35, prefix fc_).
func generateAuthToken() string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	max := big.NewInt(int64(len(alphabet)))
	var sb strings.Builder
	sb.WriteString("fc_")
	for i := 0; i < 32; i++ {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			// crypto/rand failures are catastrophic; the Rust source panics
			// in this codepath too via Result<…>. Fall through to a
			// deterministic char so the build path stays infallible.
			sb.WriteByte(alphabet[0])
			continue
		}
		sb.WriteByte(alphabet[n.Int64()])
	}
	return sb.String()
}

// stashSecret is a process-local one-shot stash keyed by
// (serviceAccountID, kind). The HTTP handler reads + removes the entry
// after the commit succeeds; the plaintext never persists.
var stash sync.Map

type stashKey struct {
	id   string
	kind string
}

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
	stash.Range(func(k, v any) bool {
		if e, ok := v.(stashEntry); ok && !stashFresh(e.storedAt, now) {
			stash.Delete(k)
		}
		return true
	})
}

func stashSecret(id, kind, value string) {
	now := time.Now()
	sweepStash(now)
	stash.Store(stashKey{id, kind}, stashEntry{plaintext: value, storedAt: now})
}

// PopStashedSecret retrieves and removes a stashed plaintext. Used by
// the HTTP handler to return the rotated token/secret in the response.
// Entries older than stashTTL are treated as absent: a stale entry means
// the owning request never read it, and the plaintext must not be handed
// to anyone else later.
func PopStashedSecret(id, kind string) (string, bool) {
	v, ok := stash.LoadAndDelete(stashKey{id, kind})
	if !ok {
		return "", false
	}
	e := v.(stashEntry)
	if !stashFresh(e.storedAt, time.Now()) {
		return "", false
	}
	return e.plaintext, true
}
