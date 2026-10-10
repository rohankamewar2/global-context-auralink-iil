package memory

import (
	"errors"
	"math/big"
	"regexp"
	"strconv"
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

// A correction chooses exactly one representation. A per-field map must name
// every changed field, and cannot silently add unrelated supersession edges.
func ValidateCorrectionTargets(fields map[string]string, single int64, targets map[string]int64) error {
	if len(targets) == 0 {
		if single > 0 {
			return nil
		}
		return errors.New("correction target required")
	}
	if single != 0 || len(targets) != len(fields) {
		return errors.New("ambiguous correction targets")
	}
	for field, id := range targets {
		if _, ok := fields[field]; !ok || id <= 0 {
			return errors.New("invalid field target")
		}
	}
	return nil
}

func correctionTargets(e Event) (map[string]int64, error) {
	fields := liveFields(e)
	single, _ := strconv.ParseInt(str(e.Payload["supersedes_event_id"]), 10, 64)
	targets := map[string]int64{}
	if raw, exists := e.Payload["supersedes_event_ids"]; exists {
		values, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("invalid target map")
		}
		for field, value := range values {
			id, err := strconv.ParseInt(str(value), 10, 64)
			if err != nil {
				return nil, err
			}
			targets[field] = id
		}
	}
	if err := ValidateCorrectionTargets(fields, single, targets); err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		for field := range fields {
			targets[field] = single
		}
	}
	return targets, nil
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
			targets, err := correctionTargets(e)
			if err != nil {
				return err
			}
			for _, id := range targets {
				target, found := byID[id]
				if !found || target.ID == e.ID || target.ThreadRef != e.ThreadRef || target.Revision >= e.Revision || !requirementEvent(target) {
					return errors.New("invalid correction ownership or order")
				}
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
			targets, _ := correctionTargets(e) // validated before reduction
			for field, target := range targets {
				id := strconv.FormatInt(target, 10)
				if suppressed[id] == nil {
					suppressed[id] = map[string]bool{}
				}
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
