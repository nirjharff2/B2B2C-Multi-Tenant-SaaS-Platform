# Audit Day 1 — Production-Grade Microservices Readiness

**Date:** 2026-09-14
**Scope:** The platform *as infrastructure* — service-to-service communication, data consistency, messaging, deployment, observability, testing, and CI/CD. This is **not** a review of business logic (orders/users/notifications semantics); per direction, that logic is intentionally placeholder and will be written later. The findings below are about the load-bearing plumbing that any business logic will sit on top of, and whether that plumbing would hold up in production regardless of what the handlers eventually do.
**Method:** Static read of every `.go` file in `api-gateway`, `user-service`, `order-service`, `notification-service`, plus `docker-compose*.yml`, `.github/workflows/ci-cd.yml`, `prometheus/prometheus.yml`, `terraform/terraform.yml`, and all prior reports in `ai_doc/` and `user-service/us_ai_doc/`. Nothing was executed.
**Relationship to existing docs:** `ai_doc/network_issues.md` (findings) and `ai_doc/fixed_network.md` (what's been remediated) already give a thorough security/network audit — that ground is **not** repeated here except in a short status recap (§2). This report focuses on reliability, consistency, observability, and delivery — the categories those two docs explicitly called out of scope.

---

## 1. Executive Summary

The stack currently proves that four Go binaries can be built, wired together with Docker Compose, and pushed one order through the full chain (`gateway → order-service → user-service → order-db → RabbitMQ → notification-service`). That is a working *demo topology*, not yet a production-grade architecture. Every layer that production traffic actually depends on — retries, timeouts, idempotency, transactional consistency between the database and the message broker, graceful shutdown, structured observability, and automated testing — is either missing or implemented in a way that only works because nothing has failed yet.

None of this is a criticism of the current milestone; it reflects that the project has so far optimized for "does a request make it end to end" (see `ai_doc/report.md`) and "is the network exposed" (see `network_issues.md`). This report is the next layer down: what breaks the first time a dependency is slow, a process restarts mid-request, or the same request is sent twice.

**The single highest-leverage finding is §3.1** (order ID collisions) — it is a correctness bug in code that already exists and will corrupt data under completely ordinary use, independent of whatever business logic is added later.

### Findings at a glance

| ID | Finding | Category | Severity |
|----|---------|----------|----------|
| R-01 | Order IDs are derived from `len(item string)`, not a counter or UUID — guaranteed collisions | Correctness | **Critical** |
| R-02 | No idempotency key on order creation — retries/double-clicks create duplicate orders | Correctness / Reliability | **High** |
| R-03 | DB write and event publish are not transactional; publish is fire-and-forget in a goroutine | Data consistency | **High** |
| R-04 | `order-service → user-service` HTTP call has no timeout, retry, or circuit breaker | Reliability | **High** |
| R-05 | RabbitMQ consumer uses auto-ack with no DLQ — a crash mid-processing silently drops the event | Messaging | **High** |
| R-06 | No reconnect/backoff on any RabbitMQ or DB connection — first failure is fatal (`log.Fatal`) | Reliability | **High** |
| R-07 | Schema managed by `CREATE TABLE`/`ALTER TABLE` on every process boot, no migration tool, no lock | Data consistency / Scalability | **High** |
| R-08 | Only `api-gateway` handles `SIGTERM`; the other three services die immediately on `docker stop`/rolling deploy | Reliability | Medium |
| R-09 | No structured logging, no request/correlation ID, no distributed tracing | Observability | Medium |
| R-10 | Zero automated tests anywhere; CI's "Run Tests" step is a no-op stub | Testing / CI | Medium |
| R-11 | No healthcheck on `user-service`, `order-service`, `notification-service`; `depends_on` uses `service_started`, not `service_healthy` | Reliability | Medium |
| R-12 | No API contract (OpenAPI/protobuf); gateway↔backend coupling is an implicit path convention | API design | Medium |
| R-13 | Event schema (`OrderCreatedEvent`) has no `event_id`, `version`, or timestamp — blocks dedup/idempotent consumers | Messaging / Extensibility | Medium |
| R-14 | CI pipeline builds and smoke-tests only; no image publishing, no deploy stage, `terraform/terraform.yml` is empty | CI/CD / IaC | Medium |
| R-15 | Horizontal scaling untested and likely unsafe (concurrent DDL at boot, no leader election) | Scalability | Medium |
| §2 | Network/secrets posture — see `network_issues.md` | Security | Recap only |

---

## 2. Security — Status Recap (not re-audited here)

`network_issues.md` already did a thorough pass (13 network findings + 6 Dockerfile findings) and `fixed_network.md` recorded what changed. Current state, for context:

- **Fixed:** network segmentation into `public-net`/`internal-net`/`db-net`/`mgmt-net`; backend services (`8001`, `8002`) and datastores no longer publish host ports.
- **Still open:** hardcoded credentials committed to `docker-compose.yml` (Postgres `secret`, JWT `super-secret-key`, RabbitMQ `guest:guest`), Redis with no auth, all containers running as root, unpinned `alpine:latest`/floating tags, plaintext internal transport, no `X-User-*` header stripping at the gateway, public RabbitMQ management UI on `guest/guest`.

These are tracked with a remediation plan in `network_issues.md` §6 already — no need to duplicate it here. Treat that document as the authoritative security backlog.

---

## 3. Data Consistency & Correctness

### R-01 · Order ID generation guarantees collisions — **Critical**

**Location:** `order-service/internal/service/order_service.go:38`
```go
orderID := fmt.Sprintf("ORD-%d", 1000+len(req.Item))
```
The order ID is derived from the **character length of the item name**, not a sequence, timestamp, or UUID. Any two orders for items whose names are the same length (`"Phone"` / `"Mouse"` / `"Chair"` — all 5 characters) generate the identical ID `ORD-1005`. `orders.order_id` has a `UNIQUE NOT NULL` constraint (`order-service/internal/config/config.go:20`), so the second such order fails the INSERT and the customer gets a generic `400` (`order_handler.go:33`) with a raw Postgres error string leaked into the response body.

This isn't a business-logic gap to defer — it's a structural bug in already-written plumbing that will surface under completely ordinary traffic (two customers ordering different 5-letter items the same day). Replace with a DB sequence, ULID/UUID, or `gen_random_uuid()`, before any real order volume runs through this.

### R-02 · No idempotency protection on order creation — **High**

`spec/project_details.md` explicitly lists "duplicate-request protection" as a required property of the transactional model. Today, `CreateOrderHandler` has no idempotency key, no request deduplication, and no unique constraint tied to caller intent (only to the buggy generated ID above). A network retry, a double-tapped "Place Order" button, or an at-least-once redelivery anywhere upstream will create a second, fully-charged order with a different ID. This needs to be solved at the plumbing layer (accept a client-supplied `Idempotency-Key`, store it with a unique index, short-circuit repeats) rather than left to whatever business logic lands in the handler later, because the retry sources (proxy timeouts, browser retries, RabbitMQ redelivery) are infrastructure-level concerns.

### R-03 · DB write and event publish form an unguarded dual-write — **High**

**Location:** `order-service/internal/service/order_service.go:41-54`
```go
err = s.repo.SaveOrder(ctx, orderID, req.UserID, user.Name, req.Item, req.Amount, "CONFIRMED")
if err != nil { return nil, err }

event := model.OrderCreatedEvent{ /* ... */ }
go s.rabbitClient.PublishNotification(event)   // fire-and-forget goroutine

return &model.OrderResponse{ /* ... */ }, nil
```
The order row commits to Postgres, then the notification event is published **in a detached goroutine** whose only failure handling is `log.Printf` (`rabbitmq.go:65-66`). Three concrete failure modes exist today, none of them handled:
1. `order-service` crashes/restarts between the DB commit and the goroutine executing → the order exists, no event is ever published, and `notification-service` never learns about it. Nothing retries this.
2. The publish fails (broker unreachable, channel closed) → same outcome, silently logged, HTTP response to the caller already said `201 Created`.
3. Because the publish is `go`-routined off the request path, `CreateOrderHandler`'s response doesn't reflect the actual delivery outcome, and there's no way to distinguish "order confirmed, notification will follow" from "order confirmed, notification silently lost" from the API.

The standard fix is a **transactional outbox**: write the event to an `outbox` table in the same DB transaction as the order, then a separate relay process/poller publishes to RabbitMQ and marks rows sent (with retry). This is infrastructure, not business logic — it's the same pattern every consumer of the order service (notifications now, analytics later per the spec) will need, so it's worth building once at the platform level rather than per-service later.

### R-07 · Schema managed by ad hoc DDL at every process boot — **High**

**Location:** `user-service/internal/config/config.go:25-34`, `order-service/internal/config/config.go:17-26`

Both services run `CREATE TABLE IF NOT EXISTS ...` (and, per `next_thing.md`'s proposed next step, future `ALTER TABLE IF NOT EXISTS ...`) directly in `InitDB()`, executed unconditionally on every container start. This works for a single replica against an empty-or-matching database, but it has no place in a production deployment story:
- **No migration history.** There's no record of what schema version is running where, no rollback path, and no way to review a schema change before it ships (it ships as part of a Go binary deploy).
- **No concurrency safety.** If `user-service` is ever scaled to 2+ replicas (the spec's stated scalability goal), every replica runs the same DDL against the same database concurrently on startup. Postgres DDL takes an `ACCESS EXCLUSIVE` lock on the table; concurrent `CREATE TABLE IF NOT EXISTS`/`ALTER TABLE` from multiple booting replicas is a known source of deadlocks and startup races that have nothing to do with load, only with replica count.
- **No down-migration / drift detection.** Nothing here would catch a diverged schema between environments.

Recommend a real migration tool (`golang-migrate`, `goose`, or `atlas`) run once as a release step (CI job or init container with a leader lock), not embedded in `main()`.

---

## 4. Reliability: Timeouts, Retries, Circuit Breaking

### R-04 · Synchronous inter-service call with no timeout, retry, or breaker — **High**

**Location:** `order-service/internal/client/user_client.go:23-37`
```go
resp, err := http.Get(userServiceURL)
```
`CreateOrder` calls `user-service` synchronously using the package-level `http.Get`, which uses `http.DefaultClient` — **no timeout configured anywhere**. If `user-service` (or the network path to it) hangs instead of failing fast, this call blocks indefinitely, and so does the entire `CreateOrderHandler` request, and so does the underlying HTTP connection, and eventually so does `order-service`'s ability to serve any request once enough goroutines are stuck. There is no retry-with-backoff for transient failures and no circuit breaker to stop hammering a downstream that's already unhealthy. This is the classic cascading-failure setup: one slow dependency degrades the entire call chain with no isolation.

Minimum fix: a `context`-scoped `http.Client` with an explicit timeout (e.g. 2–3s), passed through from the incoming request context so cancellation propagates. Beyond that, retries with jittered backoff for idempotent GETs, and a circuit breaker (even a simple one) once there's more than one synchronous downstream call in the codebase.

### R-06 · No reconnect/backoff on infrastructure connections — **High**

**Location:** `order-service/internal/messaging/rabbitmq.go:22-25`, `notification-service/main.go:22-24`, both `config.go` DB inits
```go
conn, err := amqp.Dial("amqp://guest:guest@rabbitmq:5672/")
if err != nil {
    log.Fatalf("RabbitMQ Connection Failed: %v", err)
}
```
Every connection to Postgres, Redis, and RabbitMQ is established once, at startup, with `log.Fatal` on failure. Today this is masked by Compose `depends_on: condition: service_healthy`, which delays container start until the dependency is up — but that only covers the *first* connection. None of these services handle:
- A RabbitMQ connection drop mid-runtime (network blip, broker restart, broker-side connection churn). `amqp091-go` connections do not auto-reconnect; once `conn`/`ch` die, `PublishNotification` and the consumer loop in `notification-service` fail silently forever with no recovery.
- A Postgres connection pool exhaustion or DB failover — `pgxpool` handles some of this internally, but there's no readiness signal exposed (see R-11) to tell orchestration "I lost my DB, stop routing to me."

For a system meant to run under an orchestrator that expects self-healing processes, "reconnect with backoff, or exit cleanly so the orchestrator restarts you" needs to be an explicit choice, not an accident of `log.Fatal` only covering the boot path.

### R-05 · RabbitMQ consumer auto-acks with no dead-letter path — **High**

**Location:** `notification-service/main.go:48-56`
```go
msgs, err := ch.Consume(
    q.Name, "", true, /* auto-ack */ false, false, false, nil,
)
```
Auto-ack means RabbitMQ marks the message delivered — and deletes it — the instant it's handed to the consumer, **before** any processing happens. If `notification-service` panics, is OOM-killed, or the process is SIGKILLed while `fmt.Printf`-ing the "notification," the message is already gone; there is no redelivery. Conversely, there's also no dead-letter exchange for messages that fail to unmarshal (`json.Unmarshal` errors are just logged and dropped, `main.go:67-70`) — a malformed message vanishes with no way to inspect or replay it.

For a notification pipeline (and later, analytics, which the spec explicitly wants to be "event-derived" and tolerant of "late-arriving data" — which presupposes messages aren't silently lost first), this should be manual ack after successful processing, with a DLX/DLQ for poison messages and a retry-with-backoff policy for transient failures.

### R-08 · No graceful shutdown outside the gateway — Medium

**Location:** `api-gateway/main.go:189-202` has a full `SIGTERM`/`SIGINT` → `srv.Shutdown(ctx)` sequence. `user-service/cmd/server/main.go`, `order-service/cmd/server/main.go`, and `notification-service/main.go` have **none** — they call `log.Fatal(http.ListenAndServe(...))` or block on an unbuffered channel with no signal handling at all.

In practice this means `docker compose down`, a rolling restart, or an orchestrator sending `SIGTERM` kills these three services immediately: in-flight HTTP requests are dropped mid-response, and `notification-service`'s auto-ack (R-05) makes this worse — messages being handled at the moment of a kill are already gone from the queue. This is a small, mechanical fix (the pattern already exists in `api-gateway/main.go` and can be copied), but it's currently inconsistent across the fleet.

### R-11 · Missing healthchecks and weak `depends_on` conditions — Medium

`docker-compose.yml` gives `user-db`, `order-db`, and `rabbitmq` real healthchecks, but **`user-service`, `order-service`, and `notification-service` have none** — neither a `HEALTHCHECK` in their Dockerfiles nor a `healthcheck:` block in Compose. As a direct consequence, `order-service`'s dependency on `user-service` is `condition: service_started` (process exists), not `service_healthy` (process is ready) — already flagged as a gap in `next_thing.md` §Step 4, worth restating here because it's an infrastructure reliability issue independent of the auth work that document is about. Under Compose this mostly gets lucky on timing; under a real orchestrator (k8s readiness probes, ECS health checks) there is currently nothing to probe.

---

## 5. Observability

### R-09 · No structured logging, correlation IDs, or tracing — Medium

Every service logs with `fmt.Println`/`log.Printf` using free-form strings — several of them in Bengali (`"Order Service 8002 পোর্টে চালু হচ্ছে..."`, `notification-service/main.go:80`), mixed with emoji-prefixed cache logs (`user_services.go:36,42,55`). This is fine for a solo dev loop; it is not something a log aggregator (Loki, ELK, CloudWatch Insights) can filter or alert on reliably, and it isn't queryable by field (no `level`, `service`, `trace_id`).

More importantly: **there is no request correlation across the chain.** A single logical request (`gateway → order-service → user-service → RabbitMQ → notification-service`) produces log lines in four different containers with nothing connecting them — no `X-Request-ID` generated at the gateway and propagated, no OpenTelemetry trace/span IDs. Prometheus (already wired up, see `report.md` §2.2) gives aggregate metrics, but debugging "what happened to order X" today means manually correlating timestamps across `docker compose logs` for four services. This directly undercuts the spec's stated "Observable" design goal (`project_details.md` — Design Goals) and will get materially harder once an analytics service and tenant scoping are added.

Minimum viable fix: structured JSON logging (`log/slog`, stdlib since Go 1.21, already available given `go 1.25/1.26` toolchains in use) with a consistent field set, plus a generated/propagated correlation ID header threaded through the gateway → services → the `OrderCreatedEvent` payload (see R-13).

### R-13 · Event payloads carry no identity, version, or timing metadata — Medium

**Location:** `order-service/internal/model/order.go:23-29`, duplicated independently in `notification-service/main.go:11-17`
```go
type OrderCreatedEvent struct {
    OrderID       string
    CustomerName  string
    CustomerEmail string
    Item          string
    Amount        float64
}
```
No `event_id` (for consumer-side dedup), no `event_version`/`schema_version` (for safe evolution once a second consumer, like the spec's analytics service, comes online), and no `occurred_at` timestamp (needed for the "late-arriving data" handling the spec calls out for analytics). The struct is also **copy-pasted independently** in both services rather than shared — any field change has to be made twice and can silently drift (there's no compile-time guarantee the two stay in sync). Worth fixing now, before a second and third consumer exist, because retrofitting an event ID into already-flowing traffic is harder than starting with one.

---

## 6. Testing & CI/CD

### R-10 · Zero automated tests — Medium

There is not a single `_test.go` file anywhere in the repository, and `package.json`'s `test` script is `"echo \"No tests specified\" && exit 0"` — it always exits `0`. The CI workflow's "Run Tests" step (`.github/workflows/ci-cd.yml:31-32`) therefore always passes regardless of what's in the codebase; it currently tests nothing. This matters more than usual here because the codebase is already written with interfaces at every layer that invite unit testing — `OrderRepository`, `UserClient`, `RabbitClient`, `OrderService` in `order-service`, and `UserRepository`/Redis in `user-service` — the seams exist, they're just unused. `smoke_test.sh`/`smoke_test.ps1` do real black-box HTTP checks against a live stack, which is valuable, but they aren't wired into CI (the CI workflow only checks `/metrics`) and they don't substitute for fast, isolated unit coverage of the bug in R-01 or the error paths in R-02–R-06.

### R-14 · CI pipeline builds and smoke-tests only; no deploy stage; empty IaC — Medium

`.github/workflows/ci-cd.yml` (named "Auto CI/CD Pipeline") does: checkout → `docker compose up` → install Node → run the no-op test script → `curl /metrics` → tear down. There is no image build-and-push to a registry, no tagging/versioning strategy, no deployment step to any environment, and no distinction between a PR check and a release. `terraform/terraform.yml` exists as a path but is a completely empty file — there is currently no infrastructure-as-code for wherever this is meant to eventually run (it's unclear if the target is Kubernetes, ECS, or VMs). None of this blocks the current dev-loop, but "CI/CD" in the workflow's name is aspirational relative to what it does today; worth renaming or scoping honestly until a CD stage exists.

### R-15 · Horizontal scaling is untested and likely unsafe today — Medium

Nothing in the current design has been verified to work with more than one replica of `user-service` or `order-service`:
- Concurrent DDL at boot (R-07) is a race the moment replica count > 1.
- `order-service`'s order ID scheme (R-01) getting worse, not better, under concurrent replicas — two different replicas independently computing the same `len(item)`-derived ID makes collisions more likely, not less, since there's no shared sequence.
- No distributed lock/leader election exists anywhere, and no service currently reads a `REPLICA_ID` or similar to disambiguate.

This is worth flagging even though nobody's scaled it yet, because the spec's stated scalability goal ("Independent service scaling") assumes multi-replica services work, and right now that assumption hasn't been tested and has at least two known correctness issues waiting in it.

---

## 7. API & Contract Design

### R-12 · No formal API contract; gateway↔backend coupling is implicit — Medium

The API Gateway strips `/api/v1` and reverse-proxies by path prefix (`api-gateway/main.go:60-71`) into whatever route each backend happens to register. There is no OpenAPI/Swagger spec, no protobuf/gRPC contract, and no shared request/response types between `order-service` and `api-gateway` (or `user-service` and its callers) — `order-service/internal/model/order.go` even redefines its own local `User` struct (lines 9-13) independently from `user-service/internal/model/user.go`, which is a second, silent duplication risk alongside the event struct in R-13. Today this "works" because everything is developed in lockstep in one repo, but it means:
- Nothing catches a backend route rename or field rename until a proxied request 404s or a JSON field silently comes back empty.
- Any future API consumer (a frontend, a third-party integrator, the notification service's own HTTP surface once it exists per `report.md` §3.2) has no machine-readable contract to generate a client against.

Recommend an OpenAPI spec per service (even a thin one) checked into the repo and, ideally, validated in CI against the actual routes.

---

## 8. Prioritized Punch List

Ordered by "how much a real deployment would suffer from this being wrong," not by effort:

| Priority | Item | Findings closed |
|---|---|---|
| **P0** | Fix order ID generation (sequence/ULID/UUID) | R-01 |
| **P0** | Add a timeout to the `order-service → user-service` HTTP call | R-04 |
| **P0** | Switch RabbitMQ consumer to manual ack + DLQ | R-05 |
| **P1** | Add idempotency keys to order creation | R-02 |
| **P1** | Move schema management to a real migration tool, out of `main()` | R-07 |
| **P1** | Add graceful shutdown (`SIGTERM` handling) to the three services missing it | R-08 |
| **P1** | Add `HEALTHCHECK`/`healthcheck:` to the three services missing it; fix `depends_on` conditions | R-11 |
| **P2** | Transactional outbox (or at minimum, synchronous-with-retry) for the order→event publish | R-03 |
| **P2** | Reconnect/backoff logic for RabbitMQ and DB connections at runtime, not just boot | R-06 |
| **P2** | Structured logging (`slog`) + request correlation ID propagation | R-09 |
| **P2** | Add `event_id`/`version`/`occurred_at` to event payloads; de-duplicate shared structs | R-13 |
| **P3** | Wire real unit tests into CI; stop the no-op `npm test` from masking coverage | R-10 |
| **P3** | OpenAPI spec per service | R-12 |
| **P3** | Decide and build out the CD stage + Terraform, or rename the workflow/folder to reflect current scope | R-14 |
| **P3** | Verify multi-replica behavior once R-01/R-07 are fixed, before relying on it | R-15 |
| — | Security backlog | see `network_issues.md` §6 (separately tracked, not duplicated here) |

---

## 9. Explicitly Out of Scope for This Audit

Per direction, the following were **not** evaluated because business logic inside each service is intentionally deferred:
- Whether the actual order/user/notification business rules are correct or complete.
- Tenant isolation, multi-tenancy data model, actor/role authorization logic, and the Analytics service — none of this exists yet (confirmed: no `tenant_id` anywhere in the schema or code), which matches `next_thing.md`'s own assessment. It's a large, known gap versus `spec/project_details.md`, not a new finding.
- Endpoint-level feature completeness (register/login/profile) — already tracked in detail in `ai_doc/next_thing.md`.

This report is specifically about whether the *scaffolding* those business rules will be written into is sound.
