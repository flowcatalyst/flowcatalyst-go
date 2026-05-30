package webauthn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/flowcatalyst/flowcatalyst-go/internal/sqlc/dbq"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

// Repository is the Postgres-backed credential repo.
// Table: webauthn_credentials. JSONB column is passkey_data.
type Repository struct{ q *dbq.Queries }

// NewRepository wires a repo.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{q: dbq.New(pool)}
}

// FindByID loads by primary key.
func (r *Repository) FindByID(ctx context.Context, id string) (*Credential, error) {
	row, err := r.q.WebauthnCredentialFindByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("webauthn repo: %w", err)
	}
	return rowToCredential(row)
}

// FindByCredentialID loads by the authenticator-issued ID (BYTEA column).
func (r *Repository) FindByCredentialID(ctx context.Context, credID []byte) (*Credential, error) {
	row, err := r.q.WebauthnCredentialFindByCredentialID(ctx, credID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("webauthn repo: %w", err)
	}
	c, err := rowToCredential(row)
	if errors.Is(err, ErrLegacyPasskey) {
		// Treat a legacy passkey as not-found so an assertion cleanly rejects
		// it (the user re-registers) rather than erroring the auth flow.
		return nil, nil
	}
	return c, err
}

// FindByPrincipal returns all credentials for a principal.
func (r *Repository) FindByPrincipal(ctx context.Context, principalID string) ([]Credential, error) {
	rows, err := r.q.WebauthnCredentialFindByPrincipal(ctx, principalID)
	if err != nil {
		return nil, err
	}
	out := make([]Credential, 0, len(rows))
	for _, row := range rows {
		c, err := rowToCredential(row)
		if err != nil {
			// Skip (don't fail the whole list on) a legacy webauthn-rs passkey
			// so it can't block listing / fresh registration / authentication.
			// It stays invisible + harmless until the owner re-registers.
			if errors.Is(err, ErrLegacyPasskey) {
				slog.Warn("skipping legacy webauthn passkey", "credential", row.ID, "principal", principalID)
				continue
			}
			return nil, err
		}
		out = append(out, *c)
	}
	return out, nil
}

// LibraryCredentialsByPrincipal returns the credentials in the shape go-webauthn
// expects (for the WebAuthn.BeginLogin / FinishLogin API).
func (r *Repository) LibraryCredentialsByPrincipal(ctx context.Context, principalID string) ([]webauthn.Credential, error) {
	creds, err := r.FindByPrincipal(ctx, principalID)
	if err != nil {
		return nil, err
	}
	out := make([]webauthn.Credential, len(creds))
	for i, c := range creds {
		out[i] = c.Credential
	}
	return out, nil
}

// Persist implements usecasepgx.Persist[Credential].
func (r *Repository) Persist(ctx context.Context, c *Credential, tx *usecasepgx.DbTx) error {
	credJSON, err := json.Marshal(c.Credential)
	if err != nil {
		return fmt.Errorf("marshal credential: %w", err)
	}
	return r.q.WithTx(tx.Inner()).WebauthnCredentialUpsert(ctx, dbq.WebauthnCredentialUpsertParams{
		ID:           c.ID,
		PrincipalID:  c.PrincipalID,
		CredentialID: c.CredentialIDBytes(),
		PasskeyData:  credJSON,
		Name:         c.Name,
		CreatedAt:    c.CreatedAt,
		LastUsedAt:   c.LastUsedAt,
	})
}

// Delete removes the credential.
func (r *Repository) Delete(ctx context.Context, c *Credential, tx *usecasepgx.DbTx) error {
	return r.q.WithTx(tx.Inner()).WebauthnCredentialDelete(ctx, c.ID)
}

func rowToCredential(row dbq.WebauthnCredential) (*Credential, error) {
	c := Credential{
		ID:          row.ID,
		PrincipalID: row.PrincipalID,
		Name:        row.Name,
		CreatedAt:   row.CreatedAt,
		LastUsedAt:  row.LastUsedAt,
	}
	if err := json.Unmarshal(row.PasskeyData, &c.Credential); err != nil {
		return nil, fmt.Errorf("unmarshal credential: %w", err)
	}
	// A4b safety: a legacy webauthn-rs Passkey blob ({"cred":{...}}, written by
	// the Rust system) unmarshals into a go-webauthn Credential WITHOUT error but
	// yields an empty id/publicKey (incompatible schema) — a silently-broken
	// credential that would fail assertion opaquely. Detect it and fail loudly
	// with an actionable message instead. Full convert-on-read of legacy
	// passkeys (COSE-key re-encoding) is a tracked follow-up that needs a real
	// webauthn-rs sample to implement + validate safely; until then a user with
	// a legacy passkey must re-register it.
	if len(c.Credential.ID) == 0 && isLegacyRustPasskey(row.PasskeyData) {
		return nil, fmt.Errorf("credential %s is a legacy webauthn-rs passkey (re-register): %w", row.ID, ErrLegacyPasskey)
	}
	return &c, nil
}

// ErrLegacyPasskey marks a credential stored in the legacy webauthn-rs format
// (incompatible with go-webauthn — see isLegacyRustPasskey). Per the owner,
// legacy passkeys need not be converted; callers skip them in list/auth
// contexts so they never block fresh passkey registration/use, and the owner
// re-registers. Full convert-on-read is a tracked, deliberately-deferred item.
var ErrLegacyPasskey = errors.New("legacy webauthn-rs passkey; re-registration required")

// isLegacyRustPasskey reports whether a passkey_data blob is the webauthn-rs
// Passkey shape (a top-level "cred" object) rather than the go-webauthn
// Credential shape (top-level "id" + "publicKey"). Used to turn a silent schema
// mismatch into a clear error.
func isLegacyRustPasskey(raw []byte) bool {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	_, hasCred := probe["cred"]
	_, hasID := probe["id"]
	_, hasPublicKey := probe["publicKey"]
	return hasCred && !hasID && !hasPublicKey
}
