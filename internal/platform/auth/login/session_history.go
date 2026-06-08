package login

import (
	"net/http"
	"strings"
	"time"
)

// handleLoginHistory returns the authenticated user's recent sign-in attempts
// (success and failure) so they can review activity on their account from the
// Profile screen. Sessions are stateless JWTs, so this is visibility rather than
// per-session revocation — it answers "where/when was my account signed in?".
func (e *Endpoint) handleLoginHistory(w http.ResponseWriter, r *http.Request) {
	p := e.principalFromSession(w, r)
	if p == nil {
		return
	}
	if e.cfg.LoginAttempts == nil {
		writeJSON(w, http.StatusOK, loginHistoryResponse{Attempts: []loginHistoryItem{}})
		return
	}
	email := strings.ToLower(strings.TrimSpace(emailOf(p)))
	if email == "" {
		writeJSON(w, http.StatusOK, loginHistoryResponse{Attempts: []loginHistoryItem{}})
		return
	}
	rows, err := e.cfg.LoginAttempts.FindRecentByIdentifier(r.Context(), email, 20)
	if err != nil {
		writeServerError(w, "HISTORY_FAILED", "could not load sign-in history")
		return
	}
	items := make([]loginHistoryItem, 0, len(rows))
	for i := range rows {
		a := rows[i]
		items = append(items, loginHistoryItem{
			AttemptType:   string(a.AttemptType),
			Outcome:       string(a.Outcome),
			FailureReason: strDeref(a.FailureReason),
			IPAddress:     strDeref(a.IPAddress),
			UserAgent:     strDeref(a.UserAgent),
			AttemptedAt:   a.AttemptedAt,
		})
	}
	writeJSON(w, http.StatusOK, loginHistoryResponse{Attempts: items})
}

type loginHistoryResponse struct {
	Attempts []loginHistoryItem `json:"attempts"`
}

type loginHistoryItem struct {
	AttemptType   string    `json:"attemptType"`
	Outcome       string    `json:"outcome"`
	FailureReason string    `json:"failureReason,omitempty"`
	IPAddress     string    `json:"ipAddress,omitempty"`
	UserAgent     string    `json:"userAgent,omitempty"`
	AttemptedAt   time.Time `json:"attemptedAt"`
}

func strDeref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
