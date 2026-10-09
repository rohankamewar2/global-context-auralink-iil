package contextapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rohankamewar2/global-context-auralink-iil/internal/memory"
)

var ErrRefreshPending = errors.New("memory refresh pending")
var ErrChatSelection = errors.New("chat context selection unavailable")
var editCommand = regexp.MustCompile(`(?i)^(product|quantity|unit|delivery_city)\s*=\s*(.+)$`)

type ChatRequest struct {
	MessageID string `json:"message_id"`
	Message   string `json:"message"`
}
type ChatResponse struct {
	Mode      string         `json:"mode"`
	Reply     string         `json:"reply"`
	Candidate *UpdateRequest `json:"candidate,omitempty"`
	Version   int64          `json:"memory_version"`
	Hash      string         `json:"content_sha256"`
	Thread    string         `json:"active_thread_ref"`
}
type ChatTurn struct {
	MessageID string       `json:"message_id"`
	Message   string       `json:"message"`
	Response  ChatResponse `json:"response"`
	CreatedAt time.Time    `json:"created_at"`
}
type chatStore interface {
	Exchange(context.Context, Session, string, ChatRequest) (ChatResponse, error)
	ChatHistory(context.Context, Session) ([]ChatTurn, error)
}

func (a *API) chatSession(w http.ResponseWriter, r *http.Request) (Session, bool) {
	v, ok := a.boundSession(r, r.PathValue("id"))
	cap := r.Header.Get("X-Auralink-Session-Token")
	if !ok || !hexToken.MatchString(cap) || !equalSecret(digest(cap), v.CapabilityHash) {
		fail(w, 404, "session_unavailable")
		return Session{}, false
	}
	if v.Channel != "chat" {
		fail(w, 409, "chat_session_required")
		return Session{}, false
	}
	return v, true
}
func (a *API) chat(w http.ResponseWriter, r *http.Request) {
	v, ok := a.chatSession(w, r)
	if !ok {
		return
	}
	var req ChatRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if !updateKey.MatchString(req.MessageID) || !boundedUpdateText(req.Message, 2000) {
		fail(w, 400, "invalid_chat_message")
		return
	}
	store, ok := a.store.(chatStore)
	if !ok {
		fail(w, 503, "chat_store_unavailable")
		return
	}
	response, err := store.Exchange(r.Context(), v, r.Header.Get("X-Auralink-Session-Token"), req)
	switch {
	case errors.Is(err, ErrNotFound):
		fail(w, 404, "session_unavailable")
	case errors.Is(err, ErrUpdateConflict):
		fail(w, 409, "message_id_conflict")
	case errors.Is(err, ErrChatSelection):
		fail(w, 409, "selected_thread_requires_reselection")
	case errors.Is(err, ErrRefreshPending):
		fail(w, 503, "memory_refresh_pending")
	case err != nil:
		fail(w, 503, "chat_unavailable")
	default:
		write(w, 200, response)
	}
}
func (a *API) chatHistory(w http.ResponseWriter, r *http.Request) {
	v, ok := a.chatSession(w, r)
	if !ok {
		return
	}
	store, ok := a.store.(chatStore)
	if !ok {
		fail(w, 503, "chat_store_unavailable")
		return
	}
	turns, err := store.ChatHistory(r.Context(), v)
	if err != nil {
		fail(w, 503, "chat_history_unavailable")
		return
	}
	write(w, 200, map[string]any{"turns": turns, "limit": 30, "mode": "local_memory_chat"})
}

// localReply is an explicit deterministic demo assistant, not an LLM or general
// free-text extractor. Only a whole-message field=value command proposes an edit.
func localReply(req ChatRequest, c memory.Context, hash string) ChatResponse {
	result := ChatResponse{Mode: "local_memory_chat", Version: c.MemoryVersion, Hash: hash, Thread: c.ActiveThreadRef}
	if c.ActiveThreadRef == "" {
		result.Reply = c.NextQuestion
		return result
	}
	var summary []string
	conflict := false
	for _, f := range c.CurrentConstraints {
		if f.Field == "do_not_call" {
			continue
		}
		if f.Status == "conflict" {
			conflict = true
		}
		summary = append(summary, fmt.Sprintf("%s: %s (%s)", f.Field, f.Value, f.Status))
	}
	result.Reply = "Saved context for this requirement:\n" + strings.Join(summary, "\n")
	if len(summary) == 0 {
		result.Reply = "No structurally confirmed details are available for this requirement."
	}
	result.Reply += "\n" + c.NextQuestion
	if conflict {
		result.Reply += " Conflicting details need an explicit correction target."
	}
	command := editCommand.FindStringSubmatch(strings.TrimSpace(req.Message))
	if command == nil {
		result.Reply += "\nTo propose a change, use a single edit such as quantity = 300. Review it before saving."
		return result
	}
	field, value := strings.ToLower(command[1]), strings.TrimSpace(command[2])
	fields := map[string]string{field: value}
	if memory.ValidateLiveFields(fields) != nil {
		result.Reply = "That edit is invalid. Quantity must be a positive decimal; other fields must be nonempty bounded text."
		return result
	}
	// Prefer the field's actual evidence. If the field is new, anchor it to the
	// selected requirement's product evidence. Never resolve conflicts implicitly.
	var target int64
	for _, name := range []string{field, "product"} {
		for _, f := range c.CurrentConstraints {
			if f.Field != name {
				continue
			}
			if f.Status == "conflict" {
				result.Reply = "This field has conflicting evidence. Select the original event explicitly before correcting it."
				return result
			}
			if len(f.Evidence) > 0 {
				target, _ = strconv.ParseInt(f.Evidence[0].EventID, 10, 64)
			}
			if target > 0 {
				break
			}
		}
		if target > 0 {
			break
		}
	}
	if target <= 0 {
		result.Reply = "No eligible evidence target is available. Choose a supported requirement before saving an edit."
		return result
	}
	result.Candidate = &UpdateRequest{IdempotencyKey: "chat:" + req.MessageID, Kind: "correction", Fields: fields, Evidence: req.Message, SupersedesEventID: target}
	// Keep update keys within the existing bound even for maximum-size message IDs.
	if len(result.Candidate.IdempotencyKey) > 128 {
		result.Candidate.IdempotencyKey = "chat:" + digest(req.MessageID)
	}
	result.Reply = fmt.Sprintf("Proposed change: %s = %s. Nothing has been saved to memory yet. Confirm the edit to keep its evidence and refresh memory.", field, value)
	return result
}

// Exchange serializes with update acceptance/publication. No provider/model
// call runs under the transaction. A retried turn preserves its original reply
// and memory version; new turns reject pending refresh rather than using stale data.
func (s PGStore) Exchange(ctx context.Context, v Session, cap string, req ChatRequest) (ChatResponse, error) {
	var response ChatResponse
	if !updateKey.MatchString(req.MessageID) || !boundedUpdateText(req.Message, 2000) {
		return response, ErrChatSelection
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return response, err
	}
	defer tx.Rollback(context.Background())
	var scopeID int64
	var storedHash string
	err = tx.QueryRow(ctx, `SELECT x.scope_id,x.capability_sha256 FROM context_sessions x
 JOIN subject_scopes s ON s.id=x.scope_id JOIN users u ON u.id=s.user_id
 LEFT JOIN conversation_threads t ON t.id=x.thread_id AND t.scope_id=x.scope_id
 WHERE x.id=$1 AND x.principal_id=$2 AND x.channel='chat' AND x.expires_at>clock_timestamp()
 AND u.tenant_id=$3 AND u.namespace=$4 AND u.glid=$5 AND s.role=$6 AND COALESCE(t.thread_ref,'')=$7
 FOR UPDATE OF x`, v.ID, v.Principal, v.Scope.Tenant, v.Scope.Namespace, v.Scope.GLID, v.Scope.Role, v.Thread).Scan(&scopeID, &storedHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return response, ErrNotFound
	}
	if err != nil {
		return response, err
	}
	if !hexToken.MatchString(cap) || !equalSecret(digest(cap), storedHash) {
		return response, ErrNotFound
	}
	var priorHash string
	var priorBody []byte
	err = tx.QueryRow(ctx, `SELECT request_sha256,response FROM chat_turns WHERE session_id=$1 AND message_id=$2`, v.ID, req.MessageID).Scan(&priorHash, &priorBody)
	if err == nil {
		if priorHash != digest(req.Message) {
			return response, ErrUpdateConflict
		}
		if json.Unmarshal(priorBody, &response) != nil {
			return response, ErrChatSelection
		}
		return response, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return response, err
	}
	var accepted, published, version int64
	err = tx.QueryRow(ctx, `SELECT accepted_revision,published_revision,current_memory_version FROM publication_state WHERE scope_id=$1 FOR UPDATE`, scopeID).Scan(&accepted, &published, &version)
	if err != nil {
		return response, err
	}
	if accepted > published {
		return response, ErrRefreshPending
	}
	var md, hash string
	var body []byte
	err = tx.QueryRow(ctx, `SELECT memory_md,content_sha256,derived_context FROM memory_snapshots WHERE scope_id=$1 AND memory_version=$2`, scopeID, version).Scan(&md, &hash, &body)
	if err != nil {
		return response, err
	}
	var c memory.Context
	if json.Unmarshal(body, &c) != nil || c.MemoryMD != md || digest(md) != hash || c.MemoryVersion != version || c.Scope != v.Scope {
		return response, ErrChatSelection
	}
	c, err = memory.WithThread(c, v.Thread)
	if err != nil || !contextBounded(c) {
		return response, ErrChatSelection
	}
	response = localReply(req, c, hash)
	output, _ := json.Marshal(response)
	_, err = tx.Exec(ctx, `INSERT INTO chat_turns(session_id,scope_id,principal_id,message_id,request_sha256,message,response,memory_version) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, v.ID, scopeID, v.Principal, req.MessageID, digest(req.Message), req.Message, output, version)
	if err != nil {
		return response, err
	}
	return response, tx.Commit(ctx)
}
func (s PGStore) ChatHistory(ctx context.Context, v Session) ([]ChatTurn, error) {
	result := []ChatTurn{}
	rows, err := s.Pool.Query(ctx, `SELECT message_id,message,response,created_at FROM (SELECT id,message_id,message,response,created_at FROM chat_turns WHERE session_id=$1 AND principal_id=$2 ORDER BY id DESC LIMIT 30) recent ORDER BY id`, v.ID, v.Principal)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var turn ChatTurn
		var body []byte
		if err := rows.Scan(&turn.MessageID, &turn.Message, &body, &turn.CreatedAt); err != nil {
			return result, err
		}
		if json.Unmarshal(body, &turn.Response) != nil {
			return result, ErrChatSelection
		}
		result = append(result, turn)
	}
	return result, rows.Err()
}
