package contextapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

type UpdateStatus struct {
	EventID     int64      `json:"event_id"`
	State       string     `json:"state"`
	Attempts    int        `json:"attempts"`
	ErrorCode   *string    `json:"error_code,omitempty"`
	Version     *int64     `json:"memory_version,omitempty"`
	ReceivedAt  time.Time  `json:"received_at"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
	FileReadyAt *time.Time `json:"file_ready_at,omitempty"`
	FreshnessMS *float64   `json:"acceptance_to_publication_ms,omitempty"`
	FileDelayMS *float64   `json:"acceptance_to_file_ms,omitempty"`
}
type statusStore interface {
	UpdateStatus(context.Context, Session, string) (UpdateStatus, error)
}

func (s PGStore) UpdateStatus(ctx context.Context, v Session, key string) (UpdateStatus, error) {
	var result UpdateStatus
	err := s.Pool.QueryRow(ctx, `SELECT j.event_id,j.state,j.attempts,j.error_code,j.memory_version,r.received_at,j.published_at,j.file_ready_at,j.freshness_ms,
 EXTRACT(EPOCH FROM (j.file_ready_at-r.received_at))*1000
 FROM live_updates l JOIN refresh_jobs j ON j.event_id=l.event_id AND j.scope_id=l.scope_id
 JOIN raw_events r ON r.id=l.event_id
 WHERE l.session_id=$1 AND l.principal_id=$2 AND l.idempotency_key=$3`, v.ID, v.Principal, key).Scan(&result.EventID, &result.State, &result.Attempts, &result.ErrorCode, &result.Version, &result.ReceivedAt, &result.PublishedAt, &result.FileReadyAt, &result.FreshnessMS, &result.FileDelayMS)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, ErrNotFound
	}
	return result, err
}
func (a *API) updateStatus(w http.ResponseWriter, r *http.Request) {
	v, ok := a.boundSession(r, r.PathValue("id"))
	cap := r.Header.Get("X-Auralink-Session-Token")
	if !ok || !hexToken.MatchString(cap) || !equalSecret(digest(cap), v.CapabilityHash) {
		fail(w, 404, "session_unavailable")
		return
	}
	key := r.PathValue("key")
	if !updateKey.MatchString(key) {
		fail(w, 400, "invalid_update_key")
		return
	}
	store, ok := a.store.(statusStore)
	if !ok {
		fail(w, 503, "update_status_unavailable")
		return
	}
	status, err := store.UpdateStatus(r.Context(), v, key)
	if errors.Is(err, ErrNotFound) {
		fail(w, 404, "update_unavailable")
	} else if err != nil {
		fail(w, 503, "update_status_unavailable")
	} else {
		write(w, 200, status)
	}
}
