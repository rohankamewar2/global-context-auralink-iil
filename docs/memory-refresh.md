# Simple local memory refresh

The demo uses Go and the existing personal PostgreSQL database. RabbitMQ, Kafka and additional dependencies are not required, following the user's simplified hackathon scope.

## Run

From the repository:

```powershell
go run ./cmd/migrate
go run ./cmd/server
```

The private API starts one local refresh worker automatically. It polls durable refresh_jobs once per second while idle; it processes queued tasks immediately when already busy. The gateway is a separate read-only service and does not run the worker. No provider request is made by the worker.

## Flow

1. POST a structured update using the [session update contract](session-updates.md). Acceptance commits evidence, revision, receipt and refresh task together.
2. Worker claims a task with a two-minute lease. Parallel claimers use SKIP LOCKED; a worker task has a sixty-second deadline.
3. Validate typed/allowed fields and correction ownership/order. A quantity must be a positive decimal. Claims remain labelled client_reported_structured; structure validation does not establish factual truth or authenticated provider transcript origin.
4. Explicit corrections supersede only named fields on the cited event. Original events remain intact. Independent or branching conflicting values remain conflicts; there is no latest-value-wins rule.
5. Publish canonical Markdown/context/hash/version using the existing locked publication transaction. Multiple tasks may coalesce into one snapshot; unchanged revision/policy reuses that version.
6. Export canonical buyer.md/seller.md and companion JSON under artifacts/exports/<namespace>/<GLID>/. Record task completion and freshness timings.
7. Poll GET /v1/sessions/{id}/updates/{key} using the operator bearer and original session capability. Resume the bound session in chat after its scope checkpoint is current. To use a new requirement, create a new session explicitly selecting the thread_ref returned in its receipt; the source session is not silently rebound.

The worker retains the snapshot's historical source-date/lookback/budget policy and clears per-thread rendering selection for canonical shared memory. A separate live receipt-date reference is derived under the publication revision lock, preventing a midnight acceptance race from omitting a newly accepted update. This is a replay/reference-date policy, not a wall-clock expiry scheduler: no new task means no daily regeneration. Receipt time does not become occurrence time. Historical requirements remain qualified; live requirements remain client-reported.

Source evidence is still untrusted data. Free-text LLM extraction, semantic verification, callback scheduling/contact-policy mutation and Sarvam transcript binding remain outside this step.

## Failure and recovery

Tasks survive API restarts. Expired leases are reclaimed. Failed attempts retry with a short bounded delay; five failed attempts move a task to dead state. Logs/error_code contain fixed safe messages, not submitted text, credentials or customer identifiers. The status endpoint exposes a failed task for diagnosis; operator replay tooling is not yet included.

Publication and filesystem export cannot form one atomic transaction. PostgreSQL remains authoritative: if export fails after publication, API reads can already use the new DB memory, while task/file readiness stays pending. Retry reuses the published version and repairs files. New updates arriving during export can cause a version mismatch; retry loads the newer canonical version. The task only reports done after export succeeds.

## Verification

- go test ./..., go vet ./..., go build ./... and diff checks.
- Opt-in isolated PG tests: migration repeatability, update idempotency, stale-read rejection, actual automatic worker loop, correction chains/conflicts, refreshed file/DB equality, resumed chat context, new-thread selection, file failure retries, expired-lease recovery and five-attempt dead-letter behavior.
- Existing six-subject imported API reads still pass; real source data and sample facts were not altered for tests.

Reproduce the synthetic refresh demonstration without changing real data:

```powershell
$env:AURALINK_RUN_DB_TESTS='1'
go test ./internal/contextapi -run TestPostgresSessionBinding -count=1 -v
Remove-Item Env:AURALINK_RUN_DB_TESTS
```

One observed isolated synthetic run on 9 October 2026 measured 82.07 ms acceptance-to-publication and 88.97 ms acceptance-to-file. A subsequent automatic-loop task measured 9.933 ms and 17.594 ms. These are individual local test observations with an immediately available worker, not percentiles, a freshness SLA or provider/voice latency. An idle production demo worker can add up to roughly one polling interval before starting work. Both metrics use database timestamps; the file metric includes the checkpoint recorded after export.
