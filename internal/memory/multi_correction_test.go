package memory

import (
	"encoding/json"
	"testing"
)

func TestMultiFieldCorrectionAcrossEvidence(t *testing.T) {
	in := testInput("seller")
	in.Events = []Event{liveFixture(1, 0, "live:a", map[string]any{"product": "MCB", "quantity": "100", "unit": "pieces", "delivery_city": "Noida"}), liveFixture(2, 1, "live:a", map[string]any{"quantity": "150"})}
	edit := liveFixture(3, 1, "live:a", map[string]any{"product": "steel", "quantity": "500", "unit": "kg"})
	edit.Payload["supersedes_event_id"] = json.Number("0")
	edit.Payload["supersedes_event_ids"] = map[string]any{"product": json.Number("1"), "quantity": json.Number("2"), "unit": json.Number("1")}
	in.Events = append(in.Events, edit)
	p := testPolicy()
	p.LiveAsOfDate = "2026-10-09"
	p.ThreadRef = "live:a"
	c, err := Build(in, p, 1)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"product": "steel", "quantity": "500", "unit": "kg", "delivery_city": "Noida"}
	if len(c.CurrentConstraints) != 4 {
		t.Fatal("duplicate or missing fields")
	}
	for _, f := range c.CurrentConstraints {
		if f.Value != want[f.Field] || f.Status == "conflict" {
			t.Fatalf("incorrect correction: %+v", f)
		}
	}
	// Every field target must belong to this requirement and precede the edit.
	edit.Payload["supersedes_event_ids"].(map[string]any)["quantity"] = json.Number("3")
	if _, err = Build(in, p, 2); err == nil {
		t.Fatal("self correction accepted")
	}
}

func TestCorrectionTargetMapValidation(t *testing.T) {
	fields := map[string]string{"product": "steel", "quantity": "500"}
	for _, targets := range []map[string]int64{{"product": 1}, {"product": 1, "unit": 2}, {"product": 1, "quantity": 0}} {
		if ValidateCorrectionTargets(fields, 0, targets) == nil {
			t.Fatal("invalid target map accepted")
		}
	}
	if ValidateCorrectionTargets(fields, 1, map[string]int64{"product": 1, "quantity": 2}) == nil {
		t.Fatal("both target modes accepted")
	}
}
