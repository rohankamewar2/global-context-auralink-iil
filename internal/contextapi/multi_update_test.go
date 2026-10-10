package contextapi

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rohankamewar2/global-context-auralink-iil/internal/refresh"
	"net/http"
	"testing"
)

func verifyMultiUpdates(t *testing.T, ctx context.Context, pool *pgxpool.Pool, h http.Handler, c Config, worker refresh.Worker) {
	t.Helper()
	store := PGStore{pool}
	blank := create(t, h, c, `{"glid":"900000001","role":"buyer","channel":"voice"}`)
	blank.Session.Principal = demoPrincipal
	seed := UpdateRequest{IdempotencyKey: "multi-seed", Kind: "new_requirement", Fields: map[string]string{"product": "MCB", "quantity": "100", "unit": "pieces", "delivery_city": "Noida"}, Evidence: "Synthetic customer requirement"}
	first, err := store.AcceptUpdate(ctx, blank.Session, blank.Token, seed)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = worker.Step(ctx); err != nil {
		t.Fatal(err)
	}
	bound := create(t, h, c, `{"glid":"900000001","role":"buyer","channel":"voice","active_thread_ref":"`+first.ThreadRef+`"}`)
	bound.Session.Principal = demoPrincipal
	next := UpdateRequest{IdempotencyKey: "multi-qty", Kind: "correction", Fields: map[string]string{"quantity": "150"}, Evidence: "Confirmed 150 pieces", SupersedesEventID: first.EventID}
	second, err := store.AcceptUpdate(ctx, bound.Session, bound.Token, next)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = worker.Step(ctx); err != nil {
		t.Fatal(err)
	}
	edit := UpdateRequest{IdempotencyKey: "multi-edit", Kind: "correction", Fields: map[string]string{"product": "steel", "quantity": "500", "unit": "kg"}, Evidence: "Confirmed steel, 500 kg", SupersedesEventIDs: map[string]int64{"product": first.EventID, "quantity": second.EventID, "unit": first.EventID}}
	before, revision, err := store.Published(ctx, bound.Session.Scope)
	if err != nil {
		t.Fatal(err)
	}
	edit.SupersedesEventIDs["quantity"] = 9223372036854775807
	if _, err = store.AcceptUpdate(ctx, bound.Session, bound.Token, edit); err != ErrUpdateConflict {
		t.Fatal("invalid target accepted")
	}
	_, after, _ := store.Published(ctx, bound.Session.Scope)
	if after != revision {
		t.Fatal("partial revision committed")
	}
	edit.SupersedesEventIDs["quantity"] = second.EventID
	var foreign int64
	if err = pool.QueryRow(ctx, `SELECT p.event_id FROM event_projections p JOIN conversation_threads t ON t.id=p.thread_id WHERE t.thread_ref<>$1 LIMIT 1`, first.ThreadRef).Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	edit.SupersedesEventIDs["product"] = foreign
	if _, err = store.AcceptUpdate(ctx, bound.Session, bound.Token, edit); err != ErrUpdateConflict {
		t.Fatal("cross-thread target accepted")
	}
	edit.SupersedesEventIDs["product"] = first.EventID
	gateway, err := GatewayHandler(c, store)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(VoiceUpdateRequest{SessionID: bound.Session.ID, Confirmed: true, Update: edit})
	response := request(gateway, "POST", "/v1/sarvam/tools/record_user_update", string(body), c.ToolToken, bound.Token)
	if response.Code != 202 {
		t.Fatalf("multi voice update failed: %d", response.Code)
	}
	duplicate, err := store.AcceptUpdate(ctx, bound.Session, bound.Token, edit)
	if err != nil || !duplicate.Duplicate || duplicate.AcceptedRevision != revision+1 {
		t.Fatal("duplicate multi edit changed")
	}
	if _, _, err = worker.Step(ctx); err != nil {
		t.Fatal(err)
	}
	resumed := create(t, h, c, `{"channel":"chat","resumed_from_id":"`+bound.Session.ID+`"}`)
	want := map[string]string{"product": "steel", "quantity": "500", "unit": "kg", "delivery_city": "Noida"}
	for _, fact := range resumed.Context.Variables.CurrentConstraints {
		if fact.Status == "conflict" || fact.Value != want[fact.Field] {
			t.Fatalf("incorrect multi field result %+v", fact)
		}
	}
	if len(resumed.Context.Variables.CurrentConstraints) != 4 || resumed.Context.Variables.MemoryVersion <= before.Version {
		t.Fatal("publication incomplete")
	}
}
