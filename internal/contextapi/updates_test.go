package contextapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rohankamewar2/global-context-auralink-iil/internal/memory"
	"github.com/rohankamewar2/global-context-auralink-iil/internal/refresh"
)

func TestUpdateValidationAndAuthentication(t *testing.T) {
	h, _, c := fixtureAPI(t)
	v := create(t, h, c, `{"glid":"900000001","role":"buyer","channel":"chat"}`)
	path := "/v1/sessions/" + v.Session.ID + "/updates"
	body := `{"idempotency_key":"turn-1","kind":"new_requirement","fields":{"product":"steel"},"evidence":"I need steel"}`
	for _, tc := range []struct {
		body, token, cap string
		want             int
	}{
		{body, "", v.Token, 401}, {body, c.DemoToken, strings.Repeat("f", 64), 404},
		{strings.Replace(body, `"product"`, `"do_not_call"`, 1), c.DemoToken, v.Token, 400},
		{strings.Replace(body, `"new_requirement"`, `"correction"`, 1), c.DemoToken, v.Token, 400},
		{strings.Replace(body, `"turn-1"`, `""`, 1), c.DemoToken, v.Token, 400},
		{strings.TrimSuffix(body, "}") + `,"glid":"999"}`, c.DemoToken, v.Token, 400},
		{body, c.DemoToken, v.Token, 503},
	} {
		if w := request(h, "POST", path, tc.body, tc.token, tc.cap); w.Code != tc.want {
			t.Fatalf("got %d want %d", w.Code, tc.want)
		}
	}
	if (UpdateRequest{IdempotencyKey: "x", Kind: "new_requirement", Fields: map[string]string{"product": "steel"}, Evidence: "explicit request"}).valid("req:a") {
		t.Fatal("new requirement accepted in bound thread")
	}
	if boundedUpdateText("line\ncontrol", 100) {
		t.Fatal("control accepted")
	}
}

// Called from the isolated PostgreSQL/session integration fixture. All writes
// remain in that disposable synthetic schema, never the actual dataset.
func verifyLiveUpdates(t *testing.T, ctx context.Context, pool *pgxpool.Pool, h http.Handler, c Config, voice, chat created) {
	t.Helper()
	blank := create(t, h, c, `{"glid":"900000001","role":"buyer","channel":"app"}`)
	var target int64
	if err := pool.QueryRow(ctx, `SELECT event_id FROM event_projections WHERE thread_id IS NOT NULL LIMIT 1`).Scan(&target); err != nil {
		t.Fatal(err)
	}
	req := UpdateRequest{IdempotencyKey: "correction-1", Kind: "correction", Fields: map[string]string{"quantity": "250", "unit": "kg"}, Evidence: "Please change the quantity to 250 kg", SupersedesEventID: target}
	body, _ := json.Marshal(req)
	path := "/v1/sessions/" + voice.Session.ID + "/updates"
	if request(h, "POST", path, string(body), c.DemoToken, chat.Token).Code != 404 {
		t.Fatal("another session capability accepted")
	}
	if request(h, "POST", path, string(body), c.ToolToken, voice.Token).Code != 401 {
		t.Fatal("provider credential accepted on operator update route")
	}
	var wg sync.WaitGroup
	gateway, err := GatewayHandler(c, PGStore{Pool: pool})
	if err != nil {
		t.Fatal(err)
	}
	voiceBody, _ := json.Marshal(VoiceUpdateRequest{SessionID: voice.Session.ID, Confirmed: true, Update: req})
	results := make(chan UpdateReceipt, 8)
	failures := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := request(gateway, "POST", "/v1/sarvam/tools/record_user_update", string(voiceBody), c.ToolToken, voice.Token)
			var r UpdateReceipt
			var envelope struct {
				Receipt UpdateReceipt `json:"receipt"`
			}
			if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &envelope) != nil {
				failures <- w.Code
				return
			}
			r = envelope.Receipt
			results <- r
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for status := range failures {
		t.Fatalf("concurrent acceptance failed: %d", status)
	}
	var first UpdateReceipt
	unique := 0
	for r := range results {
		if !r.Duplicate {
			unique++
		}
		if first.EventID == 0 {
			first = r
		}
		if r.EventID != first.EventID || r.AcceptedRevision != first.AcceptedRevision || r.ThreadRef != voice.Session.Thread || r.ProcessingStatus != "accepted_unpublished" {
			t.Fatal("retry changed receipt")
		}
	}
	if unique != 1 {
		t.Fatal("concurrent retry did not create exactly one event")
	}
	req.Fields["quantity"] = "300"
	changed, _ := json.Marshal(req)
	if request(h, "POST", path, string(changed), c.DemoToken, voice.Token).Code != 409 {
		t.Fatal("changed retry accepted")
	}
	req.IdempotencyKey = "wrong-target"
	req.SupersedesEventID = 999999999
	changed, _ = json.Marshal(req)
	if request(h, "POST", path, string(changed), c.DemoToken, voice.Token).Code != 409 {
		t.Fatal("unowned correction target accepted")
	}
	newReq := `{"idempotency_key":"new-1","kind":"new_requirement","fields":{"product":"copper"},"evidence":"I have another requirement for copper"}`
	w := request(h, "POST", "/v1/sessions/"+blank.Session.ID+"/updates", newReq, c.DemoToken, blank.Token)
	var newer UpdateReceipt
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &newer) != nil || !strings.HasPrefix(newer.ThreadRef, "live:req:") || newer.AcceptedRevision != first.AcceptedRevision+1 {
		t.Fatal("new requirement not atomically attributed")
	}
	var events, receipts int
	var accepted, published int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM raw_events WHERE source='local.session_update'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM live_updates`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if events != 2 || receipts != 2 {
		t.Fatal("rejected update left partial state or duplicate events")
	}
	if err := pool.QueryRow(ctx, `SELECT accepted_revision,published_revision FROM publication_state LIMIT 1`).Scan(&accepted, &published); err != nil || accepted != newer.AcceptedRevision || published >= accepted {
		t.Fatal("acceptance did not mark pending refresh")
	}
	var originalExists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM raw_events WHERE id=$1)`, target).Scan(&originalExists); err != nil || !originalExists {
		t.Fatal("correction destroyed original evidence")
	}
	if request(h, "POST", "/v1/sarvam/on-start", `{"session_id":"`+voice.Session.ID+`"}`, c.ToolToken, voice.Token).Code != 503 {
		t.Fatal("stale memory served after accepted update")
	}
	verifyRefreshContinuation(t, ctx, pool, h, c, voice, newer, first)
	if _, err := pool.Exec(ctx, `UPDATE context_sessions SET created_at=clock_timestamp()-interval '2 hours',expires_at=clock_timestamp()-interval '1 hour' WHERE id=$1`, blank.Session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := (PGStore{pool}).AcceptUpdate(ctx, blank.Session, blank.Token, UpdateRequest{IdempotencyKey: "expired", Kind: "new_requirement", Fields: map[string]string{"product": "steel"}, Evidence: "steel"}); err != ErrNotFound {
		t.Fatal("store accepted expired session")
	}
}

func verifyRefreshContinuation(t *testing.T, ctx context.Context, pool *pgxpool.Pool, h http.Handler, c Config, voice created, newer, first UpdateReceipt) {
	t.Helper()
	worker := refresh.Worker{Pool: pool, ExportRoot: t.TempDir()}
	statusPath := "/v1/sessions/" + voice.Session.ID + "/updates/correction-1"
	if request(h, "GET", statusPath, "", c.DemoToken, strings.Repeat("d", 64)).Code != 404 {
		t.Fatal("status leaked without capability")
	}
	var status UpdateStatus
	w := request(h, "GET", statusPath, "", c.DemoToken, voice.Token)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &status) != nil || status.State != "queued" {
		t.Fatal("durable task missing after acceptance")
	}
	r, worked, err := worker.Step(ctx)
	if err != nil || !worked || r.Version != 2 || r.FreshnessMS < 0 || r.FileDelayMS < r.FreshnessMS {
		t.Fatalf("refresh failed: %+v %v", r, err)
	}
	second, worked, err := worker.Step(ctx)
	if err != nil || !worked || second.Version != r.Version {
		t.Fatal("coalesced refresh created unnecessary version")
	}
	if _, worked, err := worker.Step(ctx); err != nil || worked {
		t.Fatal("done jobs were processed again")
	}
	chat := create(t, h, c, `{"channel":"chat","resumed_from_id":"`+voice.Session.ID+`"}`)
	known := map[string]string{}
	for _, f := range chat.Context.Variables.CurrentConstraints {
		known[f.Field] = f.Value
	}
	if known["quantity"] != "250" || known["unit"] != "kg" || chat.Context.Variables.MemoryVersion != 2 || chat.Session.Thread != voice.Session.Thread {
		t.Fatal("resumed channel lost corrected requirement")
	}
	for _, f := range chat.Context.Variables.MissingFields {
		if f == "quantity" || f == "unit" {
			t.Fatal("known correction re-requested")
		}
	}
	copper := create(t, h, c, `{"glid":"900000001","role":"buyer","channel":"chat","active_thread_ref":"`+newer.ThreadRef+`"}`)
	if len(copper.Context.Variables.CurrentConstraints) == 0 || copper.Context.Variables.CurrentConstraints[0].Value != "copper" {
		t.Fatal("new requirement not selectable or mixed with corrected thread")
	}
	published, accepted, err := memory.ReadPublished(ctx, pool, voice.Session.Scope)
	if err != nil || accepted != published.IncludedRevision || published.Context.Policy.AsOfDate != "2026-10-09" {
		t.Fatal("publication checkpoint/reference incorrect")
	}
	md, err := os.ReadFile(filepath.Join(worker.ExportRoot, voice.Session.Scope.Namespace, voice.Session.Scope.GLID, "buyer.md"))
	if err != nil || string(md) != published.MD {
		t.Fatal("refreshed file differs from canonical DB")
	}
	w = request(h, "GET", statusPath, "", c.DemoToken, voice.Token)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &status) != nil || status.State != "done" || status.FreshnessMS == nil || status.FileDelayMS == nil {
		t.Fatal("freshness checkpoint unavailable")
	}
	t.Logf("synthetic local refresh: acceptance-to-publication %.2f ms, acceptance-to-file %.2f ms", *status.FreshnessMS, *status.FileDelayMS)
	// Export failure is durable and retries reuse the already-published version.
	req := UpdateRequest{IdempotencyKey: "retry-export", Kind: "correction", Fields: map[string]string{"quantity": "275"}, Evidence: "Change to 275 kg", SupersedesEventID: first.EventID}
	body, _ := json.Marshal(req)
	if request(h, "POST", "/v1/sessions/"+voice.Session.ID+"/updates", string(body), c.DemoToken, voice.Token).Code != 202 {
		t.Fatal("retry fixture acceptance failed")
	}
	badRoot := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(badRoot, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	broken := refresh.Worker{Pool: pool, ExportRoot: badRoot}
	failed, worked, err := broken.Step(ctx)
	if !worked || err == nil {
		t.Fatal("export failure not reported")
	}
	var state string
	var attempts int
	if err := pool.QueryRow(ctx, `SELECT state,attempts FROM refresh_jobs WHERE id=$1`, failed.JobID).Scan(&state, &attempts); err != nil || state != "queued" || attempts != 1 {
		t.Fatal("failed task not scheduled for retry")
	}
	// Simulate a crashed lease and exercise recovery without waiting two minutes.
	if _, err := pool.Exec(ctx, `UPDATE refresh_jobs SET state='running',lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, failed.JobID); err != nil {
		t.Fatal(err)
	}
	recovered, worked, err := worker.Step(ctx)
	if err != nil || !worked || recovered.Version != 3 {
		t.Fatal("expired lease did not recover idempotently")
	}
	final, _, err := memory.ReadPublished(ctx, pool, voice.Session.Scope)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := memory.WithThread(final.Context, voice.Session.Thread)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range selected.CurrentConstraints {
		if f.Field == "quantity" && f.Value != "275" {
			t.Fatal("correction chain lost on retry")
		}
	}
	// Exercise the actual automatic polling loop, not just explicit Step calls.
	var recoveredEvent int64
	if err := pool.QueryRow(ctx, `SELECT event_id FROM refresh_jobs WHERE id=$1`, recovered.JobID).Scan(&recoveredEvent); err != nil {
		t.Fatal(err)
	}
	req.IdempotencyKey = "automatic-loop"
	req.SupersedesEventID = recoveredEvent
	req.Fields["quantity"] = "325"
	req.Evidence = "Change to 325 kg"
	body, _ = json.Marshal(req)
	if request(h, "POST", "/v1/sessions/"+voice.Session.ID+"/updates", string(body), c.DemoToken, voice.Token).Code != 202 {
		t.Fatal("automatic fixture acceptance failed")
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); worker.Run(runCtx) }()
	storedVoice, err := (PGStore{pool}).Session(ctx, voice.Session.ID)
	if err != nil {
		stop()
		<-done
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		current, e := (PGStore{pool}).UpdateStatus(ctx, storedVoice, "automatic-loop")
		if e == nil && current.State == "done" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	<-done
	auto, e := (PGStore{pool}).UpdateStatus(ctx, storedVoice, "automatic-loop")
	if e != nil || auto.State != "done" || auto.Version == nil || *auto.Version != 4 {
		t.Fatal("automatic worker did not publish")
	}
	resumed := create(t, h, c, `{"channel":"chat","resumed_from_id":"`+voice.Session.ID+`"}`)
	quantity := ""
	for _, f := range resumed.Context.Variables.CurrentConstraints {
		if f.Field == "quantity" {
			quantity = f.Value
		}
	}
	if quantity != "325" {
		t.Fatal("automatic refresh not visible to resumed session")
	}
	verifyLocalChat(t, ctx, pool, h, c, voice, resumed, worker)
	// A persistent failure eventually stops retrying; fresh data still blocks
	// stale agent reads until a valid publication succeeds.
	// Corrupt only the isolated schema's first live event to model malformed evidence.
	if _, err := pool.Exec(ctx, `UPDATE raw_events SET payload=jsonb_set(payload,'{fields,quantity}','"NaN"') WHERE id=$1`, first.EventID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE refresh_jobs SET state='queued',attempts=4,next_attempt_at=clock_timestamp() WHERE id=$1`, failed.JobID); err != nil {
		t.Fatal(err)
	}
	// Force recompilation instead of the prior same-policy version reuse.
	if _, err := pool.Exec(ctx, `UPDATE memory_snapshots SET derived_context=jsonb_set(derived_context,'{policy_sha256}','"test"') WHERE memory_version=5`); err != nil {
		t.Fatal(err)
	}
	_, worked, err = worker.Step(ctx)
	if !worked || err == nil {
		t.Fatal("invalid evidence silently published")
	}
	if err := pool.QueryRow(ctx, `SELECT state FROM refresh_jobs WHERE id=$1`, failed.JobID).Scan(&state); err != nil || state != "dead" {
		t.Fatal("retry bound did not dead-letter")
	}
}
