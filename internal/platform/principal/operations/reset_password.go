package operations

import (
	"context"
	"fmt"
	"strings"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/auth/passwordhash"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/httperror"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/commit"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

type ResetPasswordCommand struct {
	ID          string `json:"id"`
	NewPassword string `json:"newPassword"`
	// EnforcePasswordComplexity defaults to true when nil. When false the caller
	// owns its own password policy, so we relax the minimum length (1:1 with
	// Rust's relaxed() policy). Go has no upper/lower/digit/special complexity
	// checks, so the only effect today is the minimum-length floor.
	EnforcePasswordComplexity *bool `json:"enforcePasswordComplexity,omitempty"`
}

func ResetPassword(
	ctx context.Context,
	repo *principal.Repository,
	uow *usecasepgx.UnitOfWork,
	cmd ResetPasswordCommand,
	ec usecase.ExecutionContext,
) (commit.Committed[UserPasswordReset], error) {
	var zero commit.Committed[UserPasswordReset]
	if strings.TrimSpace(cmd.ID) == "" {
		return zero, usecase.Validation("ID_REQUIRED", "id is required")
	}
	// Minimum length follows the complexity flag: the strict default requires 8
	// (Rust PasswordPolicy::default min_length), an opt-out relaxes to 2 (Rust
	// PasswordPolicy::relaxed). enforce defaults to true when the flag is absent.
	minLen := 8
	if cmd.EnforcePasswordComplexity != nil && !*cmd.EnforcePasswordComplexity {
		minLen = 2
	}
	if len(cmd.NewPassword) < minLen {
		return zero, usecase.Validation("PASSWORD_TOO_SHORT",
			fmt.Sprintf("newPassword must be at least %d characters", minLen))
	}
	p, err := repo.FindByID(ctx, cmd.ID)
	if err != nil {
		return zero, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if p == nil {
		return zero, httperror.NotFound("Principal", cmd.ID)
	}
	if p.Type != principal.TypeUser {
		return zero, usecase.Conflict("NOT_A_USER", "Password reset only applies to USER principals")
	}
	hash, err := passwordhash.Hash(cmd.NewPassword)
	if err != nil {
		return zero, usecase.Internal("HASH", "password hash failed", err)
	}
	p.SetPasswordHash(hash)

	event := UserPasswordReset{
		Metadata: usecase.NewEventMetadata(ec, UserPasswordResetType, Source, subjectFor(p.ID)),
		UserID:   p.ID,
	}
	return commit.Save(ctx, uow, p, repo, event, cmd)
}
