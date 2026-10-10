# Local chat and shared memory demo

This demo uses the existing Go API and PostgreSQL refresh worker. No RabbitMQ, Kafka, LLM or audio service is required. Voice-labelled sessions demonstrate backend channel continuity; they do not place calls.

## Run

With the existing private `.env` configuration and subject manifest enabled, run from the repository:

```powershell
go run ./cmd/migrate
go run ./cmd/server
```

Open `http://127.0.0.1:8080/demo` (or the configured HTTP_PORT). Unlock with DEMO_API_BEARER_TOKEN from the private `.env`. Never enter a provider key. The browser retains the token and session capability only in memory; locking or reloading clears them.

Choose a user and explicitly select a requirement. Create a voice-labelled session, then continue in chat. The response summarizes the selected requirement's canonical saved context. The call-preparation pane reads the same shared memory and exposes its version, hash and Markdown.

For a new requirement, choose Start a new requirement, create the session, fill the four fields and save. The UI creates a fresh session selecting the returned requirement before continuing.

## Confirm an edit

Send explicit commands, for example `quantity = 300` or `product = steel; quantity = 500; unit = kg`. Supported fields are product, quantity, unit and delivery_city. Quantity must be a positive decimal. Multi-field changes are reviewed together and saved atomically with each field's evidence target. Duplicate fields, invalid values and conflicting evidence reject the entire preview. General natural-language messages summarize context; they do not extract new facts.

Review the proposed value and evidence, then confirm or discard it. A preview does not change memory. Confirmation uses the existing authenticated update API and durable refresh worker. The UI waits for publication/file readiness, displays timings and reloads shared memory. Subsequent replies use that published value. Conflicting fields cannot be automatically corrected through chat.

Selecting a historical subject and confirming an edit changes that subject's memory. Rehearse edits only against an explicitly configured synthetic subject; viewing historical context is read-only until an update is confirmed. Browser verification used a disposable synthetic database schema and did not alter historical facts.

## API and storage

- POST `/v1/sessions/{id}/chat`: `{ "message_id": "unique-turn-id", "message": "quantity = 300" }`.
- GET `/v1/sessions/{id}/chat`: the most recent 30 turns in that session.
- Both require the operator bearer and matching session capability, using the existing session header contract.
- Only chat sessions can send turns. Scope, role and selected thread are checked against the authoritative stored session.
- Identical retries return the original response; reusing an ID with different text returns a conflict. New turns wait while memory refresh is pending.
- Migration 000006 stores private chat journals. Journal entries are not automatically memory facts. Only separately confirmed updates enter the evidence/revision pipeline.

## Validation

Go tests and vet passed, including disposable PostgreSQL integration checks for authentication, channel/scope/thread binding, idempotency, preview without mutation, confirmation, pending refresh, refreshed replies and private history. Browser rehearsal verified a 200-to-300 quantity correction, publication timings and matching chat/call-preparation memory. JavaScript syntax and Go build checks passed.

Actual Sarvam audio, LLM dialogue and natural-language fact extraction are subsequent implementations.
