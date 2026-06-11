package serviceaccount

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/repocommon"
	"github.com/flowcatalyst/flowcatalyst-go/internal/sqlc/dbq"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

// Repository is the Postgres-backed repo. Table: iam_service_accounts.
// Webhook credentials live as flat wh_* columns on the row (matches
// Rust schema); the entity's WebhookCredentials struct is reconstituted
// from those columns on read.
//
// Role assignments (RoleAssignment) live in iam_principal_roles and are
// owned by the principal subdomain — they are not persisted by this
// repo today. Callers set them in memory only; the principal port
// handles persistence.
type Repository struct{ q *dbq.Queries }

// NewRepository wires a repo.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{q: dbq.New(pool)}
}

// FindByID loads by id.
func (r *Repository) FindByID(ctx context.Context, id string) (*ServiceAccount, error) {
	res, err := r.q.ServiceAccountFindByID(ctx, id)
	row, err := repocommon.One(res, err, "service_account repo")
	if row == nil || err != nil {
		return nil, err
	}
	return rowToServiceAccount(*row), nil
}

// FindByCode loads by unique code.
func (r *Repository) FindByCode(ctx context.Context, code string) (*ServiceAccount, error) {
	res, err := r.q.ServiceAccountFindByCode(ctx, code)
	row, err := repocommon.One(res, err, "service_account repo")
	if row == nil || err != nil {
		return nil, err
	}
	return rowToServiceAccount(*row), nil
}

// FindAll returns every service account.
func (r *Repository) FindAll(ctx context.Context) ([]ServiceAccount, error) {
	rows, err := r.q.ServiceAccountFindAll(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ServiceAccount, 0, len(rows))
	for _, row := range rows {
		out = append(out, *rowToServiceAccount(row))
	}
	return out, nil
}

// Persist implements usecasepgx.Persist[ServiceAccount]. Maps the
// WebhookCredentials struct onto the flat wh_* schema columns.
// wh_credentials_created_at / wh_credentials_regenerated_at are derived
// from sa.CreatedAt / sa.UpdatedAt today — when the SA gains an
// explicit credentials-rotation timestamp, plumb it through here.
func (r *Repository) Persist(ctx context.Context, sa *ServiceAccount, tx *usecasepgx.DbTx) error {
	creds := sa.WebhookCredentials
	return r.q.WithTx(tx.Inner()).ServiceAccountUpsert(ctx, dbq.ServiceAccountUpsertParams{
		ID:                         sa.ID,
		Code:                       sa.Code,
		Name:                       sa.Name,
		Description:                sa.Description,
		ApplicationID:              sa.ApplicationID,
		Scope:                      sa.Scope,
		ClientIds:                  sa.ClientIDs,
		Active:                     sa.Active,
		WhAuthType:                 stringPtrOrNil(string(creds.AuthType)),
		WhAuthTokenRef:             creds.Token,
		WhSigningSecretRef:         creds.SigningSecret,
		WhSigningAlgorithm:         creds.SigningAlgorithm,
		WhCredentialsCreatedAt:     timePtr(sa.CreatedAt),
		WhCredentialsRegeneratedAt: nil,
		LastUsedAt:                 sa.LastUsedAt,
		CreatedAt:                  sa.CreatedAt,
		UpdatedAt:                  time.Now().UTC(),
	})
}

// Delete removes the row.
func (r *Repository) Delete(ctx context.Context, sa *ServiceAccount, tx *usecasepgx.DbTx) error {
	return r.q.WithTx(tx.Inner()).ServiceAccountDelete(ctx, sa.ID)
}

func rowToServiceAccount(row dbq.IamServiceAccount) *ServiceAccount {
	sa := &ServiceAccount{
		ID:            row.ID,
		Code:          row.Code,
		Name:          row.Name,
		Description:   row.Description,
		Active:        row.Active,
		ApplicationID: row.ApplicationID,
		Scope:         row.Scope,
		LastUsedAt:    row.LastUsedAt,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
		ClientIDs:     append([]string{}, row.ClientIds...),
		Roles:         []RoleAssignment{},
		WebhookCredentials: WebhookCredentials{
			AuthType:         WebhookAuthType(stringDerefOrEmpty(row.WhAuthType)),
			Token:            row.WhAuthTokenRef,
			SigningSecret:    row.WhSigningSecretRef,
			SigningAlgorithm: row.WhSigningAlgorithm,
		},
	}
	if sa.WebhookCredentials.AuthType == "" {
		sa.WebhookCredentials = NoCredentials()
	}
	return sa
}

func stringPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func stringDerefOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
