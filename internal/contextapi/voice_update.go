package contextapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
)

// The provider's confirmation is a client assertion, not independent proof of
// transcript origin. Scope, role and thread always come from the bound session.
type VoiceUpdateRequest struct {
	SessionID string        `json:"session_id"`
	Confirmed bool          `json:"confirmed"`
	Update    UpdateRequest `json:"update"`
}

// Sarvam's visual tool editor can serialize model arguments as strings.
// Normalize only these two documented fields, retaining strict JSON validation.
func (v *VoiceUpdateRequest) UnmarshalJSON(data []byte) error {
	var wire struct {
		SessionID string          `json:"session_id"`
		Confirmed json.RawMessage `json:"confirmed"`
		Update    json.RawMessage `json:"update"`
	}
	strict := func(data []byte, dst any) error {
		d := json.NewDecoder(bytes.NewReader(data))
		d.DisallowUnknownFields()
		if err := d.Decode(dst); err != nil {
			return err
		}
		var extra any
		if err := d.Decode(&extra); err != io.EOF {
			return errors.New("invalid JSON boundary")
		}
		return nil
	}
	if err := strict(data, &wire); err != nil {
		return err
	}
	normalize := func(raw json.RawMessage) ([]byte, error) {
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) > 0 && trimmed[0] == '"' {
			var value string
			if err := json.Unmarshal(trimmed, &value); err != nil {
				return nil, err
			}
			return []byte(value), nil
		}
		return trimmed, nil
	}
	confirmation, err := normalize(wire.Confirmed)
	if err != nil {
		return err
	}
	if !bytes.Equal(confirmation, []byte("true")) && !bytes.Equal(confirmation, []byte("false")) {
		return errors.New("invalid confirmation")
	}
	update, err := normalize(wire.Update)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(update)) == 0 || bytes.TrimSpace(update)[0] != '{' {
		return errors.New("update object required")
	}
	var payload struct {
		IdempotencyKey string            `json:"idempotency_key"`
		Kind           string            `json:"kind"`
		Fields         map[string]string `json:"fields"`
		Evidence       string            `json:"evidence"`
		Target         json.RawMessage   `json:"supersedes_event_id"`
	}
	if err := strict(update, &payload); err != nil {
		return err
	}
	var target int64
	if len(payload.Target) > 0 {
		number, err := normalize(payload.Target)
		if err != nil {
			return err
		}
		target, err = strconv.ParseInt(string(number), 10, 64)
		if err != nil || target <= 0 || strconv.FormatInt(target, 10) != string(number) {
			return errors.New("positive integer evidence ID required")
		}
	}
	parsed := UpdateRequest{IdempotencyKey: payload.IdempotencyKey, Kind: payload.Kind, Fields: payload.Fields, Evidence: payload.Evidence, SupersedesEventID: target}
	*v = VoiceUpdateRequest{SessionID: wire.SessionID, Confirmed: bytes.Equal(confirmation, []byte("true")), Update: parsed}
	return nil
}

func (a *API) voiceUpdate(w http.ResponseWriter, r *http.Request) {
	var req VoiceUpdateRequest
	if !decodeBody(w, r, &req) {
		return
	}
	v, ok := a.boundSession(r, req.SessionID)
	cap := r.Header.Get("X-Auralink-Session-Token")
	if !ok || !hexToken.MatchString(cap) || !equalSecret(digest(cap), v.CapabilityHash) {
		fail(w, 404, "session_unavailable")
		return
	}
	if v.Channel != "voice" {
		fail(w, 409, "voice_session_required")
		return
	}
	if !req.Confirmed || !req.Update.valid(v.Thread) {
		fail(w, 400, "confirmed_valid_update_required")
		return
	}
	store, ok := a.store.(UpdateStore)
	if !ok {
		fail(w, 503, "update_store_unavailable")
		return
	}
	receipt, err := store.AcceptUpdate(r.Context(), v, cap, req.Update)
	switch {
	case errors.Is(err, ErrNotFound):
		fail(w, 404, "session_unavailable")
	case errors.Is(err, ErrUpdateConflict):
		fail(w, 409, "update_conflict")
	case err != nil:
		fail(w, 503, "update_acceptance_unavailable")
	default:
		write(w, 202, map[string]any{"receipt": receipt, "processing_status": "accepted_unpublished", "agent_message": "The confirmed details were accepted. Shared memory is refreshing; do not claim publication yet."})
	}
}
