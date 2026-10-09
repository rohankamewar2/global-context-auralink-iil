// Package refresh runs the demo's durable local PostgreSQL refresh tasks.
// No brokers, provider connections or extraction-model calls are involved.
package refresh

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rohankamewar2/global-context-auralink-iil/internal/memory"
)

type Worker struct {
	Pool       *pgxpool.Pool
	ExportRoot string
}
type Result struct {
	JobID            int64   `json:"job_id"`
	Version          int64   `json:"memory_version"`
	IncludedRevision int64   `json:"included_revision"`
	FreshnessMS      float64 `json:"acceptance_to_publication_ms"`
	FileDelayMS      float64 `json:"acceptance_to_file_ms"`
}

// Step claims one due task. Lease recovery and publication version reuse make
// retries safe after process interruption, including after DB publication.
func (w Worker) Step(ctx context.Context) (Result, bool, error) {
	var result Result
	// Repeated crashes must eventually stop reclaiming a poisoned task.
	if _, err := w.Pool.Exec(ctx, `UPDATE refresh_jobs SET state='dead',lease_token=NULL,lease_until=NULL,error_code='lease_expired'
 WHERE state='running' AND lease_until<clock_timestamp() AND attempts>=5`); err != nil {
		return result, false, errors.New("refresh recovery unavailable")
	}
	var scope memory.Scope
	var tokenBytes [16]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return result, false, errors.New("refresh lease unavailable")
	}
	token := hex.EncodeToString(tokenBytes[:])
	var received time.Time
	err := w.Pool.QueryRow(ctx, `WITH candidate AS (
 SELECT id FROM refresh_jobs WHERE (state='queued' AND next_attempt_at<=clock_timestamp())
 OR (state='running' AND lease_until<clock_timestamp()) ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1
 ), claimed AS (
 UPDATE refresh_jobs j SET state='running',attempts=attempts+1,lease_token=$1,lease_until=clock_timestamp()+interval '2 minutes'
 FROM candidate c WHERE j.id=c.id RETURNING j.id,j.scope_id,j.event_id
 ) SELECT j.id,u.tenant_id,u.namespace,u.glid,s.role,r.received_at
 FROM claimed j JOIN subject_scopes s ON s.id=j.scope_id JOIN users u ON u.id=s.user_id JOIN raw_events r ON r.id=j.event_id`, token).Scan(&result.JobID, &scope.Tenant, &scope.Namespace, &scope.GLID, &scope.Role, &received)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, false, nil
	}
	if err != nil {
		return result, false, errors.New("refresh claim unavailable")
	}
	err = w.process(ctx, &result, scope, received, token)
	if err != nil {
		// Preserve only a fixed error code; payloads, identifiers and credentials
		// must never become retry logs. Cancellation also leaves a durable retry.
		clean, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, retryErr := w.Pool.Exec(clean, `UPDATE refresh_jobs SET state=CASE WHEN attempts>=5 THEN 'dead' ELSE 'queued' END,
  next_attempt_at=clock_timestamp()+make_interval(secs=>LEAST(60,attempts*2)),lease_token=NULL,lease_until=NULL,error_code='refresh_failed'
  WHERE id=$1 AND lease_token=$2 AND state='running'`, result.JobID, token)
		if retryErr != nil {
			return result, true, errors.New("refresh retry recording unavailable")
		}
		return result, true, errors.New("refresh failed; retry or dead-letter recorded")
	}
	return result, true, nil
}

func (w Worker) process(ctx context.Context, result *Result, scope memory.Scope, received time.Time, token string) error {
	previous, _, err := memory.ReadPublished(ctx, w.Pool, scope)
	if err != nil {
		return err
	}
	policy := previous.Context.Policy
	// Reuse the original source lookback and historical reference. Live retention
	// advances only its separate receipt clock and never selects a thread.
	policy.ThreadRef = ""
	// Generate derives the live receipt reference under its revision lock.
	snapshot, err := memory.Generate(ctx, w.Pool, scope, policy)
	if err != nil {
		return err
	}
	if _, err = memory.ExportSnapshot(ctx, w.Pool, snapshot, w.ExportRoot); err != nil {
		return err
	}
	result.Version = snapshot.Version
	result.IncludedRevision = snapshot.IncludedRevision
	result.FreshnessMS = float64(snapshot.PublishedAt.Sub(received).Microseconds()) / 1000
	// Use the same database clock for acceptance, publication and file checkpoint.
	var fileReady time.Time
	err = w.Pool.QueryRow(ctx, `UPDATE refresh_jobs SET state='done',memory_version=$3,published_at=$4,file_ready_at=clock_timestamp(),
 freshness_ms=$5,lease_token=NULL,lease_until=NULL,error_code=NULL
 WHERE id=$1 AND lease_token=$2 AND state='running' RETURNING file_ready_at`, result.JobID, token, snapshot.Version, snapshot.PublishedAt, result.FreshnessMS).Scan(&fileReady)
	if err != nil {
		return err
	}
	result.FileDelayMS = float64(fileReady.Sub(received).Microseconds()) / 1000
	return nil
}

// Run polls immediately and then every second while idle. There are no
// fire-and-forget goroutines per HTTP request; tasks survive API restarts.
func (w Worker) Run(ctx context.Context) {
	for ctx.Err() == nil {
		taskCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		result, worked, err := w.Step(taskCtx)
		cancel()
		if err != nil {
			slog.Warn("memory refresh task delayed", "reason", err.Error())
		} else if worked {
			slog.Info("memory refresh complete", "memory_version", result.Version, "acceptance_to_publication_ms", result.FreshnessMS, "acceptance_to_file_ms", result.FileDelayMS)
		}
		if !worked || err != nil {
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}
