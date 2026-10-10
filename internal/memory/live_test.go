package memory

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func liveFixture(id, target int64, thread string, fields map[string]any) Event {
	kind := "requirement.new_requirement"
	if target > 0 {
		kind = "requirement.correction"
	}
	return Event{ID: id, Revision: id, Source: "local.session_update", Kind: kind, ThreadRef: thread, Channel: "chat", TimeQuality: "unresolved", ReceivedAt: time.Date(2026, 10, 9, 10, 0, int(id), 0, time.UTC), Payload: map[string]any{"fields": fields, "evidence": "Explicitly submitted details", "evidence_origin": "authenticated_client_assertion", "supersedes_event_id": json.Number(fmtID(target))}}
}

func TestLiveCorrectionChainsAndConflicts(t *testing.T) {
	in := testInput("buyer")
	in.Events = []Event{liveFixture(1, 0, "live:a", map[string]any{"product": "Steel", "quantity": "200", "unit": "kg", "delivery_city": "Noida"}), liveFixture(2, 1, "live:a", map[string]any{"quantity": "250"}), liveFixture(3, 0, "live:b", map[string]any{"product": "Copper"})}
	p := testPolicy()
	p.LiveAsOfDate = "2026-10-09"
	p.ThreadRef = "live:a"
	c, err := Build(in, p, 1)
	if err != nil {
		t.Fatal(err)
	}
	assertField := func(c Context, field, value string, status string) {
		t.Helper()
		n := 0
		for _, f := range c.CurrentConstraints {
			if f.Field == field {
				n++
				if f.Value != value || f.Status != status {
					t.Fatalf("wrong %s: %+v", field, f)
				}
			}
		}
		if n != 1 {
			t.Fatalf("expected one %s, got %d", field, n)
		}
	}
	assertField(c, "quantity", "250", "client_reported_structured")
	assertField(c, "product", "Steel", "client_reported_structured")
	if len(c.MissingFields) != 0 || strings.Contains(c.MemoryMD, "quantity=200") || !strings.Contains(c.MemoryMD, "quantity=250") || c.Policy.AsOfDate != "2026-10-02" {
		t.Fatal("correction, missing slots or historical clock incorrect")
	}
	// Siblings are unresolved conflicts; changing one does not discard the other.
	in.Events = append(in.Events, liveFixture(4, 1, "live:a", map[string]any{"quantity": "300"}))
	c, err = Build(in, p, 2)
	if err != nil {
		t.Fatal(err)
	}
	conflicts := 0
	for _, f := range c.CurrentConstraints {
		if f.Field == "quantity" && f.Status == "conflict" {
			conflicts++
		}
	}
	if conflicts != 2 || !strings.Contains(c.NextQuestion, "conflicting") {
		t.Fatal("branching corrections silently resolved")
	}
	in.Events = append(in.Events, liveFixture(5, 4, "live:a", map[string]any{"quantity": "250"}))
	c, err = Build(in, p, 3)
	if err != nil {
		t.Fatal(err)
	}
	assertField(c, "quantity", "250", "client_reported_structured")
	// The original evidence remains in input; unknown times are not fabricated.
	if c.CurrentConstraints[0].Evidence[0].OccurredAt != nil {
		t.Fatal("receipt became occurrence time")
	}
}

func TestLiveEvidenceValidationAndRetention(t *testing.T) {
	for _, q := range []string{"0", "-1", "NaN", "Infinity", "200 kg", "1e6"} {
		if ValidateLiveFields(map[string]string{"quantity": q}) == nil {
			t.Fatalf("invalid quantity accepted %s", q)
		}
	}
	in := testInput("seller")
	in.Events = []Event{liveFixture(1, 0, "live:a", map[string]any{"product": "Steel"}), liveFixture(2, 1, "other", map[string]any{"quantity": "20"})}
	p := testPolicy()
	p.LiveAsOfDate = "2026-10-09"
	if _, err := Build(in, p, 1); err == nil {
		t.Fatal("cross-thread correction accepted")
	}
	in.Events = in.Events[:1]
	p.LiveAsOfDate = "2027-01-09"
	c, err := Build(in, p, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Requirements) != 0 {
		t.Fatal("expired live requirement retained")
	}
	in.Events[0].Payload["fields"] = map[string]any{"product": 17}
	p.LiveAsOfDate = "2026-10-09"
	if _, err := Build(in, p, 1); err == nil {
		t.Fatal("malformed live assertion silently published")
	}
}
