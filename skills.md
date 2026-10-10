# AuraLink · Build Journey

**Voice AI Hackathon 2.0 · PS02: Global Context Memory Across Channels**  
**Stack:** Go, PostgreSQL, Sarvam Voice Agents, embedded HTML/CSS/JavaScript, Cloudflare Tunnel.  
**Development assistant:** Codex, used for implementation, debugging, tests and documentation alongside human-led agent configuration and live voice rehearsals.

This document records the engineering skills and lessons behind the working prototype. It is a project build log, not an executable agent skill or a claim that every available Codex skill was used.

## 1. Turn the problem into a testable journey

We focused on one concrete outcome: a buyer or seller changes a selected requirement in voice, continues in chat without repeating the details, and an executive sees that same updated memory before the next conversation.

We separated four states that are easy to confuse: a user requests a change, confirms it, the backend accepts it, and shared memory is published. Each state has different evidence. Calling a tool alone does not establish that a change was saved.

**Skill applied:** scope definition and acceptance criteria. We chose a local Go/PostgreSQL prototype and dropped Kafka/RabbitMQ to keep the complete flow practical within the hackathon.

## 2. Preserve provenance while importing context

Historical buyer and seller data was normalized into users, role scopes, threads, source events and projections. Dataset fields and source excerpts remain qualified by provenance and timestamp quality.

The memory compiler distinguishes historical information, client-reported structured details, unresolved dates and conflicts. It does not treat delivery receipts or call metadata as conversation transcripts. Customer demand is kept separate from seller inventory.

**Skill applied:** conservative data ingestion and evidence modeling. Free-text fact extraction remains pending; the implemented baseline uses structured fields and cited excerpts.

## 3. Build shared memory rather than separate channel copies

PostgreSQL is authoritative. The renderer publishes canonical Markdown, derived context, a memory version, included revision and SHA-256 hash. Exported `buyer.md` and `seller.md` are derived artifacts.

Sessions bind the user, tenant, namespace, role, channel and selected requirement. Chat resumes an actual voice session instead of trying to identify a customer again from their words. The executive preparation panel reads the same published context.

**Skill applied:** context modeling, deterministic rendering and cross-channel session design. A canonical Markdown snapshot may describe an unselected workspace; the session's selected thread remains authoritative for the conversation.

## 4. Connect Sarvam through a narrow tool gateway

Sarvam agents use an on-start Load Memory tool and a during-call Record User Update tool. The public tunnel exposes a scoped gateway; the private operator UI and administrative APIs remain on a separate loopback listener.

The tool contract requires a service bearer and session capability. Capability digests are stored server-side, and scope comes from the bound session rather than model-supplied user identifiers. Credentials stay outside conversational memory.

**Skill applied:** API integration and boundary design. Agent configuration and actual spoken behavior were checked through human-operated Sarvam tests. Provider signed-URL access is a separate check and does not prove an audio conversation succeeded.

## 5. Make confirmed corrections durable and retryable

Acceptance stores an immutable update event, revision, receipt and refresh task together. A PostgreSQL worker claims durable jobs, validates corrections, publishes memory and exports files, with leases and bounded retries.

An idempotency key identifies one edit. An identical retry returns the original receipt; a changed body under the same key conflicts. A failed later duplicate does not undo an earlier accepted edit.

**Skill applied:** transactional writes, idempotency and asynchronous publication. PostgreSQL publication and filesystem export cannot be one transaction, so the worker tracks and repairs file readiness separately.

## 6. Debug real tool serialization and correction targets

Live rehearsals exposed differences between the API contract and the visual tool editor: model arguments could arrive as JSON strings, booleans as text, and event IDs as quoted numbers or citation labels such as `E2`.

We added narrow normalization while retaining strict validation. Unknown fields, malformed JSON, invalid quantities and invented hash identifiers remain rejected. Evidence ownership and thread checks remain enforced in the backend.

One product/quantity/unit correction exposed another issue: different fields can originate from different events. A single target could not correctly represent that edit. We added `supersedes_event_ids`, an exact field-to-event map, with atomic acceptance and per-field supersession. The previous singular form remains supported for compatibility.

**Skill applied:** debugging from observed failures and evolving an API without weakening its boundaries.

## 7. Keep agent claims aligned with backend outcomes

Early tests showed misleading success statements after HTTP errors, duplicate submissions after repeated confirmation, and summaries that incorrectly described accepted edits as failed or quantities as ranges.

Instructions were revised to require explicit confirmation, receipt-based acceptance, unchanged retry arguments and no repeated submission after acceptance. Prompts distinguish publication from acceptance and ask for an exact quantity when a range is unsupported.

**Skill applied:** tool-use prompting and failure analysis. Prompts improve behavior but do not replace backend validation. The latest buyer v2 instructions still require a final voice smoke test.

## 8. Add a reviewable second channel

The embedded chat UI resumes a voice session with its original role and requirement, displays recalled facts, and proposes explicit corrections for review. Only confirmation sends a durable memory update.

Chat supports `quantity = 600` and combined commands such as `product = steel; quantity = 500; unit = kg`. The backend rejects invalid or conflicting proposals. The interface exposes memory publication, hash and refresh timing for inspection.

**Skill applied:** browser UI development and human review flows. This chat is deterministic; it is not an LLM assistant or WhatsApp integration.

## 9. Verify with tests and live rehearsals

Automated checks cover session boundaries, strict request parsing, correction ownership, atomic field maps, idempotency, conflict handling, refresh publication and chat continuity. Disposable PostgreSQL schemas keep integration tests and synthetic rehearsals separate from historical data.

Live synthetic journeys supplied additional evidence:

- **Buyer:** Steel rods, 200 kg, Noida → confirmed voice correction to 450 kg → chat recall from memory v3.
- **Seller:** MCB, 100 pieces, Noida → confirmed voice correction to steel and quantity 500 → chat recall from v3 → confirmed chat correction to quantity 600 → next reply and preparation panel from v4. The unit remained pieces because it was not changed.

Canonical memory copies and safe verification notes were preserved locally. Screenshots and tool responses complement those copies; they should never include credentials.

**Skill applied:** unit/integration testing, manual acceptance testing and evidence collection. Go tests and vet passed during development. Database checks depend on local connectivity; one later check could not connect even though the human-run rehearsal subsequently worked. The latest buyer prompt version has not yet been demonstrated in voice.

## Development checkpoints

- `d12f7db` — Go server and PostgreSQL health checks.
- `70852ad` — Dataset memory, context APIs and Sarvam connection bootstrap.
- `38b4cee` — Voice updates, memory refresh and voice-to-chat demo.
- `2c2ced0` — Seller rehearsals and atomic multi-field corrections.
- `c82982e` — Submission README.

## What remains

Automatic per-call secret binding, persistent rehearsal fixtures, stronger voice edit-key generation, conversational LLM chat, free-text extraction, callback recording and on-end persistence are not complete. Historical context covers three buyers and three sellers; the live journeys above demonstrate one synthetic subject per role, not six completed voice cases.

The current rehearsal uses manual session configuration and a disposable schema. Restarting creates fresh fixtures and credentials. This is a controlled local prototype, not a production deployment or a benchmarked freshness SLA.

## Reproduce and inspect

Start with [README.md](README.md), then consult [local chat](docs/local-chat-demo.md), [session updates](docs/session-updates.md), [multi-field corrections](docs/multi-field-corrections.md) and [memory refresh](docs/memory-refresh.md).

```powershell
go test ./...
go vet ./...
go run ./cmd/voice-demo -role buyer
# Use -role seller for the seller rehearsal instead.
```

Keep the rehearsal running while demonstrating the flow, and preserve sanitized evidence before shutdown.
