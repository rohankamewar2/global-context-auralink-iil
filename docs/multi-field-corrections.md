# Atomic multi-field corrections

A selected requirement can be corrected in one acceptance transaction even when its fields come from different evidence events. Each edited field supplies its own supersedes_event_ids entry. Do not include the older singular supersedes_event_id when using this map.

Example: product and unit came from event 2, while quantity was previously corrected by event 4:

```json
{
  "idempotency_key": "unique-confirmed-edit-id",
  "kind": "correction",
  "fields": {"product": "steel", "quantity": "500", "unit": "kg"},
  "evidence": "Seller explicitly confirmed changing this customer enquiry to steel, 500 kg.",
  "supersedes_event_ids": {"product": 2, "quantity": 4, "unit": 2}
}
```

These IDs are illustrative; use each selected field's actual evidence.event_id in current_constraints. Do not use thread hashes, content hashes, session IDs or source_entity_id. The target map must match edited field names exactly. Unchanged delivery_city remains intact. Each target must belong to the authenticated session's scope and selected thread. Acceptance appends one event, advances one revision, stores one receipt and enqueues one refresh task atomically. An invalid target rejects the entire request.

Both private updates and the Sarvam update adapter support this map. Sarvam may send numeric IDs, quoted numeric IDs or exact quoted Markdown citations such as E2. The adapter converts only these narrow forms; hashes, malformed IDs and ambiguous simultaneous singular/map targets are rejected. Existing single-target updates remain supported. Idempotency covers the complete normalized update, including every field target and evidence statement.

The renderer applies supersession separately to each field. It does not pick winners by time: independent branching corrections remain conflicts. Original raw evidence is retained. Renderer policy version structured-live-v6 prevents older compiled snapshots from being reused under the new policy. No SQL migration is needed because the target map is stored in the existing event JSON payload.

## Sarvam update argument description

Replace the update argument description with:

```text
Return a JSON object containing idempotency_key, kind="correction", fields, evidence and supersedes_event_ids. fields contains only the confirmed changed product, quantity, unit or delivery_city values as strings. supersedes_event_ids is an object with exactly the same keys as fields. For each key, use that field's numeric evidence.event_id from current_constraints. If adding a field with no evidence, use the selected requirement's product evidence. Never use a hash, thread_ref, session_id, source_entity_id or invent an ID. Do not include singular supersedes_event_id with the map. Evidence records the proposed change and explicit confirmation. Use a unique edit key and preserve the entire object unchanged on retries. Call only after explicit confirmation.
```

Use the same rule in the agent instructions. If evidence conflicts, ask for clarification rather than automatically superseding one claim. A receipt confirms acceptance; publication remains a separate refresh checkpoint.

## Checks

Reducer tests verify product, quantity and unit corrections across different evidence events, preserving delivery city without residual old-value conflicts. Disposable PostgreSQL integration tests verify invalid-target rollback, one event/revision, duplicate retries, worker publication and resumed chat. Existing single-target correction and branching-conflict checks remain enabled.
