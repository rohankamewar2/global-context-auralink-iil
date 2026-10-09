# Local session update acceptance

POST /v1/sessions/{session_id}/updates on the private loopback API (8080).

Headers: Content-Type: application/json; Authorization: Bearer <DEMO_API_BEARER_TOKEN>; X-Auralink-Session-Token: <capability returned when creating that session>. The provider service bearer does not authorize this route. The restricted read gateway does not expose it.

New requirement example (use a session created without active_thread_ref):

```json
{
  "idempotency_key": "turn-001",
  "kind": "new_requirement",
  "fields": {"product": "Steel", "quantity": "250", "unit": "kg", "delivery_city": "Noida"},
  "evidence": "I need 250 kg of steel delivered to Noida."
}
```

Correction example (use a session explicitly bound to the existing requirement):

```json
{
  "idempotency_key": "turn-002",
  "kind": "correction",
  "fields": {"quantity": "300"},
  "evidence": "Please change the quantity to 300 kg.",
  "supersedes_event_id": 123
}
```

The superseded event must belong to the session's same scope and selected thread. Its ID is an internal evidence ID, not a provider turn number; it can come from an earlier update receipt or authorized backend evidence lookup. Historical evidence IDs are not currently exposed through a user-facing selector.

Only product, quantity, unit and delivery_city are accepted; values are nonempty strings up to 300 UTF-8 bytes. Quantity must be a positive decimal (up to 16 integer and six fractional digits); the API does not validate commercial accuracy or convert units. Evidence is a nonempty string up to 2000 bytes. Control characters are rejected. Values remain client-reported claims. Contact restrictions, callback scheduling, identity changes and arbitrary profile fields are unsupported. Requests cannot supply identity, role, channel or thread overrides. Unknown fields are rejected.

202 response: event_id, accepted_revision, thread_ref, received_at, duplicate and processing_status=accepted_unpublished. A new requirement creates a server-generated live:req: thread; it does not silently rebind the source session. Evidence origin is authenticated_client_assertion, claim status is submitted_unverified. No provider transcript authenticity or factual verification is implied. Source occurrence time is unresolved; received_at records server receipt time.

The database transaction rechecks session/capability/expiry, locks scope publication state, appends an immutable raw event and scoped projection, advances accepted_revision and stores a receipt. Identical retries using the same session/key return the same event, revision, thread and receipt time. Reusing the key with a changed body returns 409. Different sessions have independent keys. Rejected/failed requests roll back. Original correction evidence is retained.

Receipt status describes acceptance, not a refreshed memory checkpoint. Acceptance now atomically enqueues a durable refresh task. The local API's built-in worker validates structured values, applies explicit field-level corrections, publishes a snapshot and exports canonical files. Read [memory-refresh.md](memory-refresh.md) for the pipeline and limits. Agent reads and new session creation fail closed while accepted_revision is ahead of the published snapshot. Tests use disposable synthetic schemas rather than changing real sample facts.

GET /v1/sessions/{session_id}/updates/{idempotency_key} uses the same operator bearer and session capability to report queued/running/done/dead, attempts, safe error_code, memory_version, received_at, published_at, file_ready_at, acceptance_to_publication_ms and acceptance_to_file_ms. Other sessions cannot read the receipt. A done receipt records completion of that update's task; a later update may still make the scope pending, so check the session checkpoint before continuation.

Errors: 401 wrong operator bearer; 404 absent/expired/unowned session or incorrect capability; 400 invalid body/update; 409 changed retry or unowned correction target; 503 database/store unavailable.

Migrations: 000004_live_updates.sql adds receipt ownership; 000005_refresh_pipeline.sql adds durable tasks and backfills existing receipts. Both are applied to the local project database. Sarvam record_user_update now uses this pipeline through the separately authenticated scoped gateway; see [buyer agent setup](sarvam-buyer-agent.md). Callback and on-end persistence remain unimplemented.

Verification: go test ./..., go vet ./..., isolated PostgreSQL/session and migration tests, and ingestion/publication compatibility tests passed. Integration coverage includes eight concurrent identical submissions, changed retries, original evidence preservation, revision/pending state, new-thread attribution, authentication, wrong capability, expiry and rollback after invalid correction target.
