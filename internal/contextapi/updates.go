package contextapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/rohankamewar2/global-context-auralink-iil/internal/memory"
)

var ErrUpdateConflict = errors.New("update conflict")
var updateKey = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// UpdateRequest contains explicit client assertions, not provider-authenticated
// transcript evidence. Identity, role, channel and thread ownership come from
// the authenticated session. Contact-policy changes are outside this contract.
type UpdateRequest struct {
	IdempotencyKey     string            `json:"idempotency_key"`
	Kind               string            `json:"kind"`
	Fields             map[string]string `json:"fields"`
	Evidence           string            `json:"evidence"`
	SupersedesEventID  int64             `json:"supersedes_event_id,omitempty"`
	SupersedesEventIDs map[string]int64  `json:"supersedes_event_ids,omitempty"`
}
type UpdateReceipt struct {
	EventID          int64     `json:"event_id"`
	AcceptedRevision int64     `json:"accepted_revision"`
	ThreadRef        string    `json:"thread_ref"`
	ReceivedAt       time.Time `json:"received_at"`
	Duplicate        bool      `json:"duplicate"`
	ProcessingStatus string    `json:"processing_status"`
}
type UpdateStore interface {
	AcceptUpdate(context.Context, Session, string, UpdateRequest) (UpdateReceipt, error)
}

func (u UpdateRequest) valid(thread string) bool {
	if memory.ValidateLiveFields(u.Fields) != nil {
		return false
	}
	if !updateKey.MatchString(u.IdempotencyKey) || len(u.Fields) == 0 || len(u.Fields) > 4 || !boundedUpdateText(u.Evidence, 2000) {
		return false
	}
	if u.Kind != "new_requirement" && u.Kind != "correction" {
		return false
	}
	if u.Kind == "new_requirement" && (thread != "" || u.SupersedesEventID != 0 || len(u.SupersedesEventIDs) > 0) {
		return false
	}
	if u.Kind == "correction" && (thread == "" || memory.ValidateCorrectionTargets(u.Fields, u.SupersedesEventID, u.SupersedesEventIDs) != nil) {
		return false
	}
	for k, v := range u.Fields {
		if k != "product" && k != "quantity" && k != "unit" && k != "delivery_city" {
			return false
		}
		if !boundedUpdateText(v, 300) {
			return false
		}
	}
	return u.Kind != "new_requirement" || u.Fields["product"] != ""
}
func boundedUpdateText(s string, limit int) bool {
	if !utf8.ValidString(s) || strings.TrimSpace(s) == "" || len(s) > limit {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func (a *API) recordUpdate(w http.ResponseWriter, r *http.Request) {
	var req UpdateRequest
	if !decodeBody(w, r, &req) {
		return
	}
	v, ok := a.boundSession(r, r.PathValue("id"))
	cap := r.Header.Get("X-Auralink-Session-Token")
	if !ok || !hexToken.MatchString(cap) || !equalSecret(digest(cap), v.CapabilityHash) {
		fail(w, 404, "session_unavailable")
		return
	}
	if !req.valid(v.Thread) {
		fail(w, 400, "invalid_update")
		return
	}
	store, ok := a.store.(UpdateStore)
	if !ok {
		fail(w, 503, "update_store_unavailable")
		return
	}
	receipt, err := store.AcceptUpdate(r.Context(), v, cap, req)
	switch {
	case errors.Is(err, ErrNotFound):
		fail(w, 404, "session_unavailable")
	case errors.Is(err, ErrUpdateConflict):
		fail(w, 409, "update_conflict")
	case err != nil:
		fail(w, 503, "update_acceptance_unavailable")
	default:
		write(w, 202, receipt)
	}
}

// AcceptUpdate rechecks session credentials under lock, serializes revision
// allocation with import/publication and commits event/projection/receipt
// together. It does not publish memory or call an extraction model.
func (s PGStore) AcceptUpdate(ctx context.Context, v Session, cap string, u UpdateRequest) (UpdateReceipt, error) {
	var out UpdateReceipt
	if !u.valid(v.Thread) {
		return out, ErrUpdateConflict
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(context.Background())
	var scopeID, userID int64
	var threadID *int64
	var channel, tenant, namespace, storedHash string
	var synthetic bool
	err = tx.QueryRow(ctx, `SELECT x.scope_id,s.user_id,x.thread_id,x.channel,u.tenant_id,u.namespace,u.is_synthetic,x.capability_sha256
 FROM context_sessions x JOIN subject_scopes s ON s.id=x.scope_id JOIN users u ON u.id=s.user_id
 LEFT JOIN conversation_threads t ON t.id=x.thread_id AND t.scope_id=x.scope_id
 WHERE x.id=$1 AND x.principal_id=$2 AND x.expires_at>clock_timestamp()
 AND u.tenant_id=$3 AND u.namespace=$4 AND u.glid=$5 AND s.role=$6 AND COALESCE(t.thread_ref,'')=$7
 FOR UPDATE OF x`, v.ID, v.Principal, v.Scope.Tenant, v.Scope.Namespace, v.Scope.GLID, v.Scope.Role, v.Thread).Scan(&scopeID, &userID, &threadID, &channel, &tenant, &namespace, &synthetic, &storedHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if !hexToken.MatchString(cap) || !equalSecret(digest(cap), storedHash) {
		return out, ErrNotFound
	}
	body, _ := json.Marshal(u)
	requestHash := digest(string(body))
	var revision int64
	err = tx.QueryRow(ctx, `SELECT accepted_revision FROM publication_state WHERE scope_id=$1 FOR UPDATE`, scopeID).Scan(&revision)
	if err != nil {
		return out, err
	}
	var oldHash string
	err = tx.QueryRow(ctx, `SELECT l.request_sha256,l.event_id,p.scope_revision,COALESCE(t.thread_ref,''),r.received_at
 FROM live_updates l JOIN event_projections p ON p.event_id=l.event_id AND p.scope_id=l.scope_id
 JOIN raw_events r ON r.id=l.event_id LEFT JOIN conversation_threads t ON t.id=p.thread_id AND t.scope_id=p.scope_id
 WHERE l.session_id=$1 AND l.idempotency_key=$2`, v.ID, u.IdempotencyKey).Scan(&oldHash, &out.EventID, &out.AcceptedRevision, &out.ThreadRef, &out.ReceivedAt)
	if err == nil {
		if oldHash != requestHash {
			return UpdateReceipt{}, ErrUpdateConflict
		}
		out.Duplicate = true
		out.ProcessingStatus = "accepted_unpublished"
		return out, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	if u.Kind == "correction" {
		targets := u.SupersedesEventIDs
		if len(targets) == 0 {
			targets = map[string]int64{"": u.SupersedesEventID}
		}
		for _, target := range targets {
			var owned bool
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM event_projections p JOIN raw_events r ON r.id=p.event_id
 WHERE p.event_id=$1 AND p.scope_id=$2 AND p.thread_id=$3 AND
 (r.event_kind='buyer.requirement_snapshot' OR r.source IN ('seller.gc_enquiries_received.csv','seller.gc_enquiry_messages.csv') OR
 (r.source='local.session_update' AND r.event_kind IN ('requirement.new_requirement','requirement.correction'))))`, target, scopeID, threadID).Scan(&owned)
			if err != nil {
				return out, err
			}
			if !owned {
				return out, ErrUpdateConflict
			}
		}
		out.ThreadRef = v.Thread
	} else {
		token, e := randomToken()
		if e != nil {
			return out, e
		}
		out.ThreadRef = "live:req:" + token
		var id int64
		err = tx.QueryRow(ctx, `INSERT INTO conversation_threads(scope_id,thread_ref) VALUES($1,$2) RETURNING id`, scopeID, out.ThreadRef).Scan(&id)
		if err != nil {
			return out, err
		}
		threadID = &id
	}
	payloadData := map[string]any{"fields": u.Fields, "evidence": u.Evidence, "supersedes_event_id": u.SupersedesEventID, "evidence_origin": "authenticated_client_assertion", "claim_status": "submitted_unverified", "session_id": v.ID}
	if len(u.SupersedesEventIDs) > 0 {
		payloadData["supersedes_event_ids"] = u.SupersedesEventIDs
	}
	payload, _ := json.Marshal(payloadData)
	err = tx.QueryRow(ctx, `INSERT INTO raw_events(tenant_id,namespace,source,source_entity_id,source_revision,event_kind,channel,payload,content_sha256,time_quality,is_synthetic)
 VALUES($1,$2,'local.session_update',$3,'1',$4,$5,$6,$7,'unresolved',$8) RETURNING id,received_at`, tenant, namespace, v.ID+":"+u.IdempotencyKey, "requirement."+u.Kind, channel, payload, digest(string(payload)), synthetic).Scan(&out.EventID, &out.ReceivedAt)
	if err != nil {
		return out, err
	}
	out.AcceptedRevision = revision + 1
	_, err = tx.Exec(ctx, `INSERT INTO event_projections(event_id,scope_id,user_id,tenant_id,namespace,scope_revision,thread_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, out.EventID, scopeID, userID, tenant, namespace, out.AcceptedRevision, threadID)
	if err != nil {
		return out, err
	}
	_, err = tx.Exec(ctx, `UPDATE publication_state SET accepted_revision=$2,updated_at=clock_timestamp() WHERE scope_id=$1`, scopeID, out.AcceptedRevision)
	if err != nil {
		return out, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO live_updates(session_id,scope_id,principal_id,idempotency_key,request_sha256,event_id) VALUES($1,$2,$3,$4,$5,$6)`, v.ID, scopeID, v.Principal, u.IdempotencyKey, requestHash, out.EventID)
	if err != nil {
		return out, err
	}
	// Durable enqueue shares the evidence transaction; an API/worker restart
	// cannot lose the refresh intent after acknowledging acceptance.
	_, err = tx.Exec(ctx, `INSERT INTO refresh_jobs(event_id,scope_id) VALUES($1,$2)`, out.EventID, scopeID)
	if err != nil {
		return out, err
	}
	out.ProcessingStatus = "accepted_unpublished"
	return out, tx.Commit(ctx)
}
