# Fix Report — Go Microservices Platform

**Date:** 2026-09-12
**Scope:** Get the existing docker-compose stack (`api-gateway`, `user-service`, `order-service`, `notification-service` + infra) building, starting, and serving traffic correctly. Verified live with Docker Desktop on this machine, not just by reading code.

---

## 1. Summary

The project's individual Go services all *compiled* fine, but the stack did not work correctly end-to-end for two concrete reasons: the API Gateway silently 404'd every request it proxied, and the newly-added CI/monitoring configuration pointed at metrics endpoints that didn't exist anywhere in the code. Both are now fixed and verified against a live `docker compose up` run. A Dockerfile inconsistency that surfaced only after the fix (see §2.3) was also corrected.

| # | Issue | Severity | Status |
|---|-------|----------|--------|
| 2.1 | API Gateway never strips `/api/v1` prefix → every proxied route 404s | Critical | Fixed |
| 2.2 | No service exposes `/metrics`, but CI + Prometheus config assume it exists | High | Fixed |
| 2.3 | `api-gateway/Dockerfile` pinned an older Go image than its `go.mod` needs | High (surfaced by fix 2.2) | Fixed |
| 2.4 | `api-gateway/Dockerfile` didn't copy `go.sum` before `go mod download` | Low | Fixed |
| 2.5 | Obsolete `version:` key in `docker-compose.yml` | Cosmetic | Fixed |
| 2.6 | No `.gitignore`, stray `.exe` binaries were present in the working tree | Low | Fixed |
| 3.1 | No `/register`, `/login`, or `/profile` endpoints in `user-service` | Design gap | Documented, not fixed |
| 3.2 | `notification-service` has no HTTP server; gateway's `/notifications` routes always fail | Design gap | Documented, not fixed |
| 3.3 | DB/Redis/RabbitMQ hosts are hardcoded, not env-configurable | Design gap | Documented, not fixed |
| 3.4 | `terraform/terraform.yml` is an empty placeholder | Informational | Documented, not fixed |

---

## 2. Bugs Fixed

### 2.1 API Gateway dropped every proxied request (Critical)

**File:** `api-gateway/main.go`

The gateway mounts all routes under `/api/v1` (e.g. `/api/v1/orders`), but `user-service` and `order-service` register their handlers without that prefix (`/orders`, `/users/`). The `ReverseProxy` helper forwarded the *original* incoming path unchanged, so every request that reached a backend arrived as `/api/v1/orders` instead of `/orders` and was met with Go's default `404 page not found`.

**Verified before fix:**
```
POST http://localhost:8000/api/v1/orders  (valid JWT)  -> 404 page not found
```

**Fix:** the proxy's `Director` now strips the `/api/v1` prefix before forwarding.

**Verified after fix:**
```
POST http://localhost:8000/api/v1/orders  (valid JWT)  -> 201 Created
{"order_id":"ORD-1005","customer_name":"Rahim","item":"Phone","amount":499.5,"status":"CONFIRMED"}
```

This was the single most impactful bug — with it in place, **no request routed through the gateway could ever reach a backend**, regardless of authentication or method.

### 2.2 No service exposed `/metrics` (High)

**Files:** `api-gateway/main.go`, `user-service/cmd/server/main.go`, `order-service/cmd/server/main.go`

The repo already had `prometheus/prometheus.yml` (scraping `api-gateway:8000`, `user-service:8001`, `order-service:8002` on the default `/metrics` path) and a CI step added to `.github/workflows/ci-cd.yml` that does:
```yaml
curl -f http://localhost:8000/metrics
curl -f http://localhost:8001/metrics
curl -f http://localhost:8002/metrics
```
None of the Go services imported a metrics library or registered a `/metrics` route — confirmed with a repo-wide search (no `prometheus`/`promhttp` references existed before this fix). Every one of those `curl -f` calls, and every Prometheus scrape, was failing with `404`.

**Fix:** added `github.com/prometheus/client_golang/prometheus/promhttp` to all three HTTP-serving services and registered a `/metrics` handler in each:
- `api-gateway`: `router.GET("/metrics", gin.WrapH(promhttp.Handler()))`
- `user-service`: `http.Handle("/metrics", promhttp.Handler())`
- `order-service`: `http.Handle("/metrics", promhttp.Handler())`

(`notification-service` was left alone — it's a pure RabbitMQ consumer with no HTTP server and isn't in the Prometheus target list.)

**Verified after fix — direct curl:**
```
:8000/metrics -> HTTP 200
:8001/metrics -> HTTP 200
:8002/metrics -> HTTP 200
```

**Verified after fix — live Prometheus targets** (`docker compose -f docker-compose.yml -f docker-compose.monitoring.yml up -d`, then `GET http://localhost:9090/api/v1/targets`):
```
api-gateway    -> up
order-service  -> up
user-service   -> up
prometheus     -> up
```

### 2.3 `api-gateway/Dockerfile` used an older Go image than its own `go.mod` required (High, surfaced by 2.2)

Adding `promhttp` (fix 2.2) pulled in transitive dependencies that require Go ≥ 1.25, so `go mod tidy` bumped `api-gateway/go.mod`'s `go` directive from `1.23` to `1.25.0`. But `api-gateway/Dockerfile` was still `FROM golang:1.23-alpine`, while the other three services already used `golang:1.26-alpine`. The build failed:
```
go: go.mod requires go >= 1.25.0 (running go 1.23.12; GOTOOLCHAIN=local)
```
**Fix:** changed `api-gateway/Dockerfile` to `FROM golang:1.26-alpine`, matching the other three services (and this repo's local toolchain, Go 1.26.3).

### 2.4 `api-gateway/Dockerfile` didn't copy `go.sum` before `go mod download`

The other three Dockerfiles do `COPY go.mod go.sum ./`; `api-gateway/Dockerfile` only copied `go.mod`. This didn't hard-fail the build, but it meant the module cache layer wasn't reproducible/verifiable against the lockfile and Docker layer caching would needlessly bust whenever `go.sum` alone changed. Fixed to match the other services.

### 2.5 Obsolete `version: "3.8"` key in `docker-compose.yml`

Modern Docker Compose (v2+) ignores the top-level `version:` key and prints a warning on every invocation:
```
the attribute `version` is obsolete, it will be ignored, please remove it to avoid potential confusion
```
Removed it. Purely cosmetic, no functional effect, but it was noise on every command.

### 2.6 No `.gitignore`; stray build binaries in the working tree

Running `go build ./...` locally while diagnosing the issues above produced `api-gateway/api-gateway.exe` and `notification-service/notification-service.exe` (Windows `go build` writes a binary named after the module when no `-o` is given). There was no `.gitignore` in the repo to keep these — or any other build output — out of version control. Removed the stray binaries and added a `.gitignore` covering compiled binaries, test artifacts, `.env` files, and editor directories.

---

## 3. Known Gaps (not fixed — out of scope for a "make it run" pass)

These aren't things that were "broken" by a code change; they're missing functionality relative to what `spec/project_details.md` describes as the target architecture (a much larger B2B2C SaaS platform). Flagging them so they aren't mistaken for bugs, and so the next work session has a starting point.

### 3.1 No auth endpoints in `user-service`
The API Gateway wires up `POST /api/v1/users/register`, `POST /api/v1/users/login`, and `GET|PUT /api/v1/users/profile` behind `JWTAuthMiddleware`, but `user-service` only implements one handler: `GET /users/{id}` (a plain lookup, no auth, no password, no JWT issuance). There is currently **no way to legitimately obtain a JWT** — the middleware exists, but nothing ever mints a token for it to validate. `run_guide.md` shows how to hand-craft a test token signed with the shared `JWT_SECRET` so the protected `/orders` route can still be exercised through the gateway.

### 3.2 `notification-service` has no HTTP surface
`docker-compose.yml` sets `NOTIFICATION_SERVICE_URL=http://notification-service:8003` and the gateway proxies `/api/v1/notifications*` to it, but `notification-service/main.go` is a RabbitMQ consumer only — it never opens port 8003 or any HTTP listener. Any request to the gateway's notification routes will fail with a connection error from the reverse proxy. Notifications currently only happen as **fire-and-forget console output** when `notification-service` consumes an `order_notifications` message (confirmed working, see run_guide.md §6).

### 3.3 Infrastructure hostnames are hardcoded, not configurable
`user-service/internal/config/config.go` and `order-service/internal/config/config.go` hardcode `user-db:5432`, `order-db:5432`, and `redis:6379` (and `order-service`/`notification-service` hardcode `rabbitmq:5672`) rather than reading them from environment variables. This works fine under `docker-compose` (those are the Compose service names), but means these two services **cannot be run outside the Compose network** (e.g. against a local Postgres, or partially containerized for debugging) without editing source. `api-gateway` is the only service that already reads its downstream URLs from environment variables.

### 3.4 `terraform/terraform.yml` is an empty file
Present in the repo tree but contains no content — a placeholder for future IaC work, not something used by the local Docker Compose flow.

---

## 4. Files Changed

```
.github/workflows/ci-cd.yml        (no change needed — see note)
api-gateway/Dockerfile             base image 1.23 -> 1.26, copy go.sum
api-gateway/go.mod / go.sum        added prometheus/client_golang
api-gateway/main.go                strip /api/v1 prefix in proxy; add /metrics
user-service/cmd/server/main.go    add /metrics
user-service/go.mod / go.sum       added prometheus/client_golang
order-service/cmd/server/main.go   add /metrics
order-service/go.mod / go.sum      added prometheus/client_golang
notification-service/go.mod/sum    go mod tidy (indirect -> direct dependency marking)
docker-compose.yml                 removed obsolete `version:` key
.gitignore                         new file
```

The CI workflow file itself needed no code change — its `/metrics` smoke-test step was correct all along; the services just didn't implement the endpoint it was checking for.

## 5. Verification Performed

All of the following were run live against this project on this machine (Docker Desktop, Go 1.26.3), not inferred from reading code:

- `go build ./...` — all 4 services compile cleanly.
- `docker compose up -d --build` — all 8 containers (4 services + user-db, order-db, redis, rabbitmq) start and reach `healthy`/`running`.
- `GET /users/101` direct to `user-service:8001` → `200`, returns seeded user.
- `POST /orders` direct to `order-service:8002` → `201 Created`.
- `notification-service` logs show the corresponding `[NOTIFICATION SENT]` line consumed from RabbitMQ.
- `GET /health` and `POST /api/v1/orders` (with a hand-signed JWT) through `api-gateway:8000` → `200` / `201` respectively (this route 404'd before the fix).
- `GET /metrics` on ports 8000, 8001, 8002 → `200` (all 404'd before the fix).
- `docker compose -f docker-compose.yml -f docker-compose.monitoring.yml up -d` → Prometheus (`:9090`) reports all 4 targets (`prometheus`, `api-gateway`, `user-service`, `order-service`) as `up`.

See `ai_doc/run_guide.md` for the exact commands to reproduce all of the above.
