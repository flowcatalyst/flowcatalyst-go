package grantstore

import "context"

// RotationResult is Rotate's outcome. Stored is the consumed token (nil when
// the presented token was invalid/expired/revoked); NewRaw/New are the
// replacement (set only on a completed rotation).
type RotationResult struct {
	Stored *RefreshToken
	NewRaw string
	New    *RefreshToken
}

// Rotate consumes a presented refresh token and issues its replacement,
// implementing the full rotation contract in ONE place so the two refresh
// surfaces (/oauth/token refresh_token grant and /auth/refresh) can't drift:
//
//   - Reuse detection (OAuth 2.0 Security BCP §4.14.2): a token we don't
//     accept as valid might be one we already rotated out. If the hash
//     matches a previously-replaced token, the family is presumed
//     compromised and every token in it is revoked, so a stolen,
//     already-rotated token cannot keep the chain alive.
//   - Rotation: the presented token is revoked before the replacement is
//     inserted.
//   - Lineage: the replacement preserves Scopes, AccessibleClients and the
//     OAuth client binding, stays in the presented token's rotation family
//     (rooting a fresh family for legacy tokens that predate tracking), and
//     the rotated-out token is linked to its replacement so a later replay
//     trips the reuse detection above.
//
// authorize, when non-nil, runs between lookup and revocation — it's the
// caller's binding check (e.g. "issued to the authenticated OAuth client").
// On authorize failure nothing is rotated and (RotationResult{Stored}, err)
// is returned so the caller can shape its own response.
//
// A nil-Stored result with nil error means "invalid or expired token".
func Rotate(ctx context.Context, repo *RefreshTokenRepository, rawToken string, authorize func(*RefreshToken) error) (RotationResult, error) {
	tokenHash := HashToken(rawToken)
	stored, err := repo.FindValidByHash(ctx, tokenHash)
	if err != nil {
		return RotationResult{}, err
	}
	if stored == nil {
		if prior, ferr := repo.FindByHash(ctx, tokenHash); ferr == nil &&
			prior != nil && prior.ReplacedBy != nil && prior.TokenFamily != nil {
			_, _ = repo.RevokeAllInFamily(ctx, *prior.TokenFamily)
		}
		return RotationResult{}, nil
	}

	if authorize != nil {
		if aerr := authorize(stored); aerr != nil {
			return RotationResult{Stored: stored}, aerr
		}
	}

	// Rotate: revoke the presented token before issuing a replacement.
	if _, err := repo.RevokeByHash(ctx, tokenHash); err != nil {
		return RotationResult{Stored: stored}, err
	}

	raw, entity, err := GenerateTokenPair(stored.PrincipalID)
	if err != nil {
		return RotationResult{Stored: stored}, err
	}
	entity.Scopes = stored.Scopes
	entity.AccessibleClients = stored.AccessibleClients
	entity.OAuthClientID = stored.OAuthClientID
	if stored.TokenFamily != nil {
		entity.TokenFamily = stored.TokenFamily
	} else {
		fam := entity.ID
		entity.TokenFamily = &fam
	}
	if err := repo.Insert(ctx, entity); err != nil {
		return RotationResult{Stored: stored}, err
	}
	// Link the rotated-out token to its replacement so a later replay of it
	// trips the reuse-detection path above. Best-effort: the rotation itself
	// is already durable.
	_, _ = repo.MarkAsReplaced(ctx, tokenHash, entity.TokenHash)

	return RotationResult{Stored: stored, NewRaw: raw, New: entity}, nil
}
