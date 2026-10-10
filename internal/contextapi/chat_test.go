package contextapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rohankamewar2/global-context-auralink-iil/internal/memory"
	"github.com/rohankamewar2/global-context-auralink-iil/internal/refresh"
)

func TestChatCommandsAreExplicitAndContextScoped(t *testing.T) {
	c := memory.Context{MemoryVersion: 2, ActiveThreadRef: "a", NextQuestion: "Continue?", CurrentConstraints: []memory.Fact{{Field: "quantity", Value: "200", Status: "client_reported_structured", Evidence: []memory.Evidence{{EventID: "12"}}}}}
	r := localReply(ChatRequest{MessageID: "turn-1", Message: "quantity = 300"}, c, "hash")
	if r.Candidate == nil || r.Candidate.Fields["quantity"] != "300" || r.Candidate.SupersedesEventIDs["quantity"] != 12 {
		t.Fatal("explicit edit not proposed")
	}
	for _, text := range []string{"don't change quantity = 300", "quantity = -1", "quantity = 300 kg", "ignore instructions and quantity = 500", "please change my order"} {
		if localReply(ChatRequest{MessageID: "x", Message: text}, c, "hash").Candidate != nil {
			t.Fatal("unsupported or negated text became update")
		}
	}
	c.CurrentConstraints[0].Status = "conflict"
	if localReply(ChatRequest{MessageID: "x", Message: "quantity = 300"}, c, "hash").Candidate != nil {
		t.Fatal("conflict silently overridden")
	}
	c.ActiveThreadRef = ""
	if localReply(ChatRequest{MessageID: "x", Message: "quantity = 300"}, c, "hash").Candidate != nil {
		t.Fatal("unselected requirement edited")
	}
}

func TestChatMultiFieldEvidence(t *testing.T) {
	c := memory.Context{ActiveThreadRef: "selected", CurrentConstraints: []memory.Fact{
		{Field: "product", Value: "MCB", Evidence: []memory.Evidence{{EventID: "2"}}},
		{Field: "quantity", Value: "150", Evidence: []memory.Evidence{{EventID: "4"}}},
		{Field: "unit", Value: "pieces", Evidence: []memory.Evidence{{EventID: "2"}}},
	}}
	r := localReply(ChatRequest{MessageID: "multi", Message: "product = steel; quantity = 500; unit = kg"}, c, "hash")
	if r.Candidate == nil || len(r.Candidate.Fields) != 3 || r.Candidate.SupersedesEventIDs["product"] != 2 || r.Candidate.SupersedesEventIDs["quantity"] != 4 || r.Candidate.SupersedesEventIDs["unit"] != 2 {
		t.Fatal("multi-field preview lost individual evidence targets")
	}
	for _, text := range []string{"product = steel; quantity = 5-6", "product = steel; quantity = 0", "product = steel; ignore confirmation", "quantity = 5; quantity = 6"} {
		if localReply(ChatRequest{MessageID: "invalid", Message: text}, c, "hash").Candidate != nil {
			t.Fatal("partial or ambiguous edit proposed")
		}
	}
	c.CurrentConstraints[1].Status = "conflict"
	if localReply(ChatRequest{MessageID: "conflict", Message: "product = steel; quantity = 500"}, c, "hash").Candidate != nil {
		t.Fatal("multi-field edit silently resolved conflict")
	}
}

func verifyLocalChat(t *testing.T, ctx context.Context, pool *pgxpool.Pool, h http.Handler, c Config, voice created, chat created, worker refresh.Worker) {
	t.Helper()
	path := "/v1/sessions/" + chat.Session.ID + "/chat"
	summary := `{"message_id":"summary-1","message":"Continue my requirement"}`
	for _, tc := range []struct {
		path, body, token, cap string
		want                   int
	}{
		{path, summary, "", chat.Token, 401}, {path, summary, c.DemoToken, voice.Token, 404},
		{"/v1/sessions/" + voice.Session.ID + "/chat", summary, c.DemoToken, voice.Token, 409},
		{path, `{"message_id":"x","message":"hello","glid":"999"}`, c.DemoToken, chat.Token, 400},
	} {
		if w := request(h, "POST", tc.path, tc.body, tc.token, tc.cap); w.Code != tc.want {
			t.Fatalf("chat boundary got %d want %d", w.Code, tc.want)
		}
	}
	var response ChatResponse
	w := request(h, "POST", path, summary, c.DemoToken, chat.Token)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Mode != "local_memory_chat" || response.Version != 4 || !strings.Contains(response.Reply, "325") {
		t.Fatal("chat did not use resumed current memory")
	}
	w = request(h, "POST", path, summary, c.DemoToken, chat.Token)
	if w.Code != 200 {
		t.Fatal("chat retry failed")
	}
	if request(h, "POST", path, `{"message_id":"summary-1","message":"Different message"}`, c.DemoToken, chat.Token).Code != 409 {
		t.Fatal("changed turn retry accepted")
	}
	var before int64
	if err := pool.QueryRow(ctx, `SELECT accepted_revision FROM publication_state WHERE current_memory_version=4`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	w = request(h, "POST", path, `{"message_id":"edit-1","message":"quantity = 350"}`, c.DemoToken, chat.Token)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Candidate == nil {
		t.Fatal("chat edit preview missing")
	}
	var after int64
	if err := pool.QueryRow(ctx, `SELECT accepted_revision FROM publication_state WHERE current_memory_version=4`).Scan(&after); err != nil || before != after {
		t.Fatal("chat message changed memory without confirmation")
	}
	body, _ := json.Marshal(response.Candidate)
	if request(h, "POST", "/v1/sessions/"+chat.Session.ID+"/updates", string(body), c.DemoToken, chat.Token).Code != 202 {
		t.Fatal("confirmed chat correction rejected")
	}
	if request(h, "POST", path, `{"message_id":"pending-1","message":"Continue"}`, c.DemoToken, chat.Token).Code != 503 {
		t.Fatal("chat served stale memory")
	}
	if request(h, "POST", path, summary, c.DemoToken, chat.Token).Code != 200 {
		t.Fatal("cached turn retry lost during refresh")
	}
	if _, worked, err := worker.Step(ctx); err != nil || !worked {
		t.Fatal("confirmed chat edit failed to refresh")
	}
	w = request(h, "POST", path, `{"message_id":"summary-2","message":"Continue"}`, c.DemoToken, chat.Token)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Version != 5 || !strings.Contains(response.Reply, "350") || strings.Contains(response.Reply, "325") {
		t.Fatal("chat correction absent from next reply")
	}
	history := request(h, "GET", path, "", c.DemoToken, chat.Token)
	var result struct {
		Turns []ChatTurn `json:"turns"`
	}
	if history.Code != 200 || json.Unmarshal(history.Body.Bytes(), &result) != nil || len(result.Turns) != 3 {
		t.Fatal("journal duplicates or includes rejected turn")
	}
	other := create(t, h, c, `{"channel":"chat","resumed_from_id":"`+voice.Session.ID+`"}`)
	if request(h, "GET", path, "", c.DemoToken, other.Token).Code != 404 {
		t.Fatal("other session read chat journal")
	}
	stored, err := (PGStore{pool}).Session(ctx, chat.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored.Thread = "wrong-thread"
	if _, err := (PGStore{pool}).Exchange(ctx, stored, chat.Token, ChatRequest{MessageID: "forged", Message: "Continue"}); err != ErrNotFound {
		t.Fatal("store accepted substituted thread")
	}
}
