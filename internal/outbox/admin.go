package outbox

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// AdminHandler returns an HTTP handler exposing the operational state machine so
// an operator can inspect message-group states and pause / resume / unblock /
// skip a group. StartOutboxProcessor serves it on FC_OUTBOX_ADMIN_PORT when set
// (the Rust equivalent is the GroupDistributor's programmatic controls — no
// HTTP — so this is a Go convenience that makes them operable):
//
//	GET  /outbox/groups               — non-default (Paused/Blocked) group states
//	GET  /outbox/groups/blocked       — Blocked groups only
//	POST /outbox/groups/{group}/pause
//	POST /outbox/groups/{group}/resume
//	POST /outbox/groups/{group}/unblock  — clear + re-queue the poison (retry)
//	POST /outbox/groups/{group}/skip     — clear + leave the poison failed
func (p *Processor) AdminHandler() http.Handler {
	r := chi.NewRouter()
	r.Get("/outbox/groups", func(w http.ResponseWriter, _ *http.Request) {
		writeAdminJSON(w, http.StatusOK, map[string]any{"groups": p.GroupStates()})
	})
	r.Get("/outbox/groups/blocked", func(w http.ResponseWriter, _ *http.Request) {
		writeAdminJSON(w, http.StatusOK, map[string]any{"blocked": p.BlockedGroups()})
	})
	r.Post("/outbox/groups/{group}/pause", func(w http.ResponseWriter, req *http.Request) {
		p.PauseGroup(chi.URLParam(req, "group"))
		writeAdminJSON(w, http.StatusOK, map[string]string{"status": "PAUSED"})
	})
	r.Post("/outbox/groups/{group}/resume", func(w http.ResponseWriter, req *http.Request) {
		p.ResumeGroup(chi.URLParam(req, "group"))
		writeAdminJSON(w, http.StatusOK, map[string]string{"status": "RUNNING"})
	})
	r.Post("/outbox/groups/{group}/unblock", func(w http.ResponseWriter, req *http.Request) {
		if p.UnblockGroup(req.Context(), chi.URLParam(req, "group")) {
			writeAdminJSON(w, http.StatusOK, map[string]string{"status": "UNBLOCKED"})
			return
		}
		writeAdminJSON(w, http.StatusNotFound, map[string]string{"error": "group not blocked"})
	})
	r.Post("/outbox/groups/{group}/skip", func(w http.ResponseWriter, req *http.Request) {
		if p.SkipGroup(chi.URLParam(req, "group")) {
			writeAdminJSON(w, http.StatusOK, map[string]string{"status": "SKIPPED"})
			return
		}
		writeAdminJSON(w, http.StatusNotFound, map[string]string{"error": "group not blocked"})
	})
	return r
}

func writeAdminJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
