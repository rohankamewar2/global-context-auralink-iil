package memory

import (
	"errors"
	"math/big"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var positiveDecimal = regexp.MustCompile(`^[0-9]{1,16}(\.[0-9]{1,6})?$`)

// ValidateLiveFields validates structure, not factual truth or transcript
// authenticity. Values remain explicitly client-reported in rendered memory.
func ValidateLiveFields(fields map[string]string) error {
	if len(fields) == 0 || len(fields) > 4 {
		return errors.New("invalid live fields")
	}
	for k, v := range fields {
		if k != "product" && k != "quantity" && k != "unit" && k != "delivery_city" {
			return errors.New("unsupported live field")
		}
		if !utf8.ValidString(v) || strings.TrimSpace(v) == "" || len(v) > 300 {
			return errors.New("invalid live value")
		}
		for _, r := range v {
			if unicode.IsControl(r) {
				return errors.New("invalid live value")
			}
		}
		if k == "quantity" {
			n, ok := new(big.Rat).SetString(v)
			if !positiveDecimal.MatchString(v) || !ok || n.Sign() <= 0 {
				return errors.New("quantity must be a positive decimal")
			}
		}
	}
	return nil
}

func liveEvent(e Event) bool {
	return e.Source == "local.session_update" && (e.Kind == "requirement.new_requirement" || e.Kind == "requirement.correction")
}
func liveFields(e Event) map[string]string {
	result := map[string]string{}
	for k, v := range obj(e.Payload["fields"]) {
		if s, ok := v.(string); ok {
			result[k] = s
		}
	}
	return result
}
func validateLiveEvents(events []Event) error {
	byID := map[int64]Event{}
	for _, e := range events {
		byID[e.ID] = e
	}
	for _, e := range events {
		if !liveEvent(e) {
			continue
		}
		fields := liveFields(e)
		if len(fields) != len(obj(e.Payload["fields"])) || ValidateLiveFields(fields) != nil || e.ThreadRef == "" || e.ReceivedAt.IsZero() || str(e.Payload["evidence_origin"]) != "authenticated_client_assertion" || plain(e.Payload["evidence"]) == "" {
			return errors.New("invalid live requirement evidence")
		}
		if e.Kind == "requirement.new_requirement" && fields["product"] == "" {
			return errors.New("new requirement needs product")
		}
		if e.Kind == "requirement.correction" {
			id, ok := new(big.Int).SetString(str(e.Payload["supersedes_event_id"]), 10)
			if !ok || !id.IsInt64() {
				return errors.New("invalid correction target")
			}
			target, found := byID[id.Int64()]
			if !found || target.ID == e.ID || target.ThreadRef != e.ThreadRef || target.Revision >= e.Revision || !requirementEvent(target) {
				return errors.New("invalid correction ownership or order")
			}
		}
	}
	return nil
}

// Apply only explicit field-level supersession edges. Two corrections to the
// same original event remain conflicting until one explicitly corrects the
// other. Unrelated historical values are never discarded by receipt order.
func applyLiveWork(w Work, events []Event) Work {
	suppressed := map[string]map[string]bool{}
	for _, e := range events {
		if liveEvent(e) && e.Kind == "requirement.correction" {
			id := str(e.Payload["supersedes_event_id"])
			if suppressed[id] == nil {
				suppressed[id] = map[string]bool{}
			}
			for field := range liveFields(e) {
				suppressed[id][field] = true
			}
		}
	}
	type value struct {
		evidence []Evidence
		live     bool
	}
	values := map[string]map[string]*value{}
	add := func(field, text string, ev Evidence, isLive bool) {
		if text == "" || suppressed[ev.EventID][field] {
			return
		}
		if values[field] == nil {
			values[field] = map[string]*value{}
		}
		if values[field][text] == nil {
			values[field][text] = &value{}
		}
		v := values[field][text]
		v.evidence = append(v.evidence, ev)
		v.live = v.live || isLive
	}
	for _, f := range w.Known {
		for _, ev := range f.Evidence {
			add(f.Field, f.Value, ev, false)
		}
	}
	hasLive := false
	for _, e := range events {
		if !liveEvent(e) {
			continue
		}
		hasLive = true
		for field, text := range liveFields(e) {
			add(field, cleanText(text), evidence(e, "fields."+field), true)
		}
	}
	if !hasLive {
		return w
	}
	w.Status = "client_reported; not independently verified"
	w.Known = []Fact{}
	for field, variants := range values {
		for text, v := range variants {
			status := "historical_source_field"
			if v.live {
				status = "client_reported_structured"
			}
			if len(variants) > 1 {
				status = "conflict"
			}
			w.Known = append(w.Known, Fact{Field: field, Value: text, Status: status, ThreadRef: w.ThreadRef, Evidence: v.evidence})
		}
	}
	// A corrected product also changes the short title; conflicting products
	// have a neutral title instead of implying that one won.
	if products := values["product"]; len(products) == 1 {
		for text, v := range products {
			w.Title = text
			w.Evidence = v.evidence[0]
		}
	} else if len(products) > 1 {
		w.Title = "Conflicting product details"
	}
	return w
}
