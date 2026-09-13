# Network & Container Security Audit — Go Microservices Platform

**Date:** 2026-09-13
**Scope:** `docker-compose.yml`, `api-gateway/Dockerfile`, `user-service/Dockerfile`, `order-service/Dockerfile`, `notification-service/Dockerfile`. Service source files were read only where needed to confirm how the containers talk to each other (connection strings, listeners, routes).
**Method:** Static review of the files. Nothing was run and no exploits were attempted. Anything that depends on the host (firewall, Docker Desktop vs Linux engine) is called out as such.

---

## 1. Executive Summary

The stack is set up for local development, and it is not safe to run anywhere reachable by other machines. The biggest problem is that **the API Gateway is the only place authentication happens, but nothing forces traffic to go through it.** Every backend service and every datastore publishes its port on all host interfaces. They all share one flat Docker network. Most of them use default or hardcoded credentials, and Redis has none at all.

In practice, anyone who can reach the host can do the following without a JWT:
- call `user-service` directly on `:8001` and list every user (`GET /users`)
- connect to Redis on `:6379`, which has no password, and read or overwrite the user cache
- log in to Postgres on `:5432`/`:5433` as superuser with the password `secret`
- log in to the RabbitMQ management UI on `:15672` with `guest/guest`, then read or inject order events

### Findings at a glance

| ID | Finding | Area | Severity |
|----|---------|------|----------|
| N-01 | Backend services publish ports on the host, so the gateway's JWT check can be skipped | Compose / network | **Critical** |
| N-02 | Redis is published on `0.0.0.0:6379` with no authentication | Compose / network | **Critical** |
| N-03 | Postgres, RabbitMQ AMQP and the RabbitMQ management UI are published on all interfaces | Compose / network | **High** |
| N-04 | One flat default network with no segmentation between tiers | Compose / network | **High** |
| N-05 | Hardcoded or default credentials (Postgres `secret`, JWT `super-secret-key`, RabbitMQ `guest:guest`) | Secrets | **High** |
| N-06 | Secrets compiled into service binaries, so they end up baked into images | Secrets / image | **High** |
| D-01 | Every container runs as `root` | Dockerfile | **High** |
| N-07 | All internal traffic is plaintext (`sslmode=disable`, `amqp://`, plain Redis, plain HTTP) | Transport | Medium |
| N-08 | Apps connect to Postgres as the `postgres` superuser | Least privilege | Medium |
| N-09 | Gateway passes client-supplied `X-User-ID` / `X-User-Role` headers through | Gateway trust boundary | Medium |
| N-10 | `/metrics` is served without authentication on the gateway and on the backend ports | Info disclosure | Medium |
| N-11 | No container hardening (`cap_drop`, `no-new-privileges`, `read_only`, resource limits) | Compose | Medium |
| D-02 | Base images use floating tags (`alpine:latest`, `redis:alpine`), not version or digest pins | Supply chain | Medium |
| D-03 | No `.dockerignore`, so `COPY . .` sends `.env`, `.git` and binaries into the build | Dockerfile | Medium |
| N-12 | CORS allows origin `*` together with `Allow-Credentials: true` | Gateway | Low |
| D-04 | Runtime images keep a shell and package manager (full Alpine) | Dockerfile | Low |
| D-05 | `ca-certificates` missing in 3 of 4 runtime images | Dockerfile | Low |
| D-06 | No `HEALTHCHECK` or restart policy on app services | Availability | Low |
| N-13 | More `depends_on` links than needed (`user-service` → `rabbitmq`) | Least connectivity | Info |

---

## 2. Current Network Topology

```
                              HOST (all interfaces 0.0.0.0)
 ┌───────────┬───────────┬───────────┬───────────┬───────────┬───────────┬───────────┐
 │  :8000    │  :8001    │  :8002    │  :5432    │  :5433    │  :6379    │ :5672     │ :15672
 ▼           ▼           ▼           ▼           ▼           ▼           ▼           ▼
┌──────────────────────────── default bridge network (flat) ─────────────────────────────┐
│ api-gateway   user-service   order-service   user-db   order-db   redis   rabbitmq    │
│  (JWT here)   (no auth)      (no auth)       (secret)  (secret)   (none)  (guest)     │
│                                                                                        │
│ notification-service  ── can reach every container above ──                           │
└────────────────────────────────────────────────────────────────────────────────────────┘
```

The only listener that checks credentials is `api-gateway`. Every other ingress point skips it.

### Actual communication paths (from source)

| From | To | Evidence |
|------|----|----------|
| api-gateway | user-service:8001, order-service:8002, notification-service:8003 | `docker-compose.yml:84-86` |
| user-service | user-db:5432, redis:6379 | `user-service/internal/config/config.go:18,50` |
| order-service | order-db:5432, rabbitmq:5672 | `order-service/internal/config/config.go:11`, `order-service/internal/messaging/rabbitmq.go:23` |
| notification-service | rabbitmq:5672 | `notification-service/main.go:22` |

No other paths are needed. Everything else the flat network allows is unnecessary attack surface.

---

## 3. Detailed Findings — docker-compose.yml

### N-01 · Backend services published on the host, so the gateway can be skipped — Critical

**Location:** `docker-compose.yml:48-49` (`8001:8001`), `docker-compose.yml:61-62` (`8002:8002`)

**Issue:** The gateway checks JWTs (`api-gateway/main.go:161`). `user-service` and `order-service` have no authentication of their own, and both are published directly on the host. The published port is a second way in that never passes through the gateway.

**Impact:**
- `GET http://<host>:8001/users` returns every user with no token (`user-service/cmd/server/main.go:27`).
- `GET http://<host>:8001/users/{id}` allows ID enumeration.
- `POST http://<host>:8002/orders` creates orders with no token (`order-service/cmd/server/main.go:33`).
- `/metrics` on both ports is exposed too (see N-10).

**Fix:** Remove the `ports:` blocks from `user-service` and `order-service`. If you need host access for debugging, use `expose:` for internal-only documentation, or bind to loopback in a dev-only override file (`127.0.0.1:8001:8001` in `docker-compose.override.yml`). Longer term, backends should also verify identity themselves (mTLS, or a signed internal token) and not rely only on network placement.

---

### N-02 · Redis on all interfaces with no authentication — Critical

**Location:** `docker-compose.yml:30-33`, `user-service/internal/config/config.go:50-51` (`Password: ""`)

**Issue:** `redis:alpine` is started with no config file, no `requirepass`, and no ACLs, and it is published as `6379:6379`. The official Redis image turns off `protected-mode`, so it accepts unauthenticated clients from any source.

**Impact:**
- Anyone who can reach the port can read the cached user objects that `user-service` stores (`user_services.go:51,85`).
- **Cache poisoning:** they can write fake `user:*` entries, and `user-service` will return them as real data for up to 10 minutes.
- Commands like `CONFIG SET`, `FLUSHALL`, `SLAVEOF`/`REPLICAOF` and `MODULE LOAD` are available. Unauthenticated Redis is a well-known way into a host.

**Fix:**
- Remove the `ports:` mapping.
- Enable auth: `command: ["redis-server", "--requirepass", "${REDIS_PASSWORD}"]`, or better, an ACL file with a dedicated user limited to `~user:*` and `+get +set +del`.
- Rename or disable dangerous commands (`rename-command CONFIG ""`, `FLUSHALL`, `DEBUG`).
- Pass the password to `user-service` through an environment variable or secret.

---

### N-03 · Postgres and RabbitMQ published on all interfaces — High

**Location:** `docker-compose.yml:8-9` (`5432`), `:22-23` (`5433`), `:37-39` (`5672`, `15672`)

**Issue:** A short-form mapping like `"5432:5432"` binds to `0.0.0.0`.
- On a **Linux** Docker Engine, Docker writes its own iptables rules, and those are evaluated **before** `ufw`/`firewalld` rules. A host firewall does not block these ports.
- On **Docker Desktop (Windows/macOS)**, the ports listen on every host interface. Whether other machines on the LAN can reach them depends on the Windows Defender Firewall rules Docker Desktop created.

**Impact:**
- **Postgres:** superuser login as `postgres/secret` from outside. That means full read/write on `userdb`/`orderdb`, plus possible command execution through `COPY ... FROM PROGRAM`, which a superuser is allowed to run.
- **RabbitMQ `15672`:** the management UI and HTTP API. The official image allows `guest` to log in remotely (it clears `loopback_users`), so `guest/guest` works from any host. An attacker can read queues, publish forged order events for `notification-service` to consume, and create admin users.
- **RabbitMQ `5672`:** direct AMQP publishing and consuming with `guest/guest`.

**Fix:** Remove all four mappings. For local tooling, move them into a `docker-compose.override.yml` bound to loopback (`"127.0.0.1:5432:5432"`) and keep that file out of any deployed environment. Use the non-management image `rabbitmq:3.13-alpine` unless the UI is actually needed.

---

### N-04 · Flat default network, no tier segmentation — High

**Location:** `docker-compose.yml`. No `networks:` key exists, so every service joins the project's default bridge network.

**Issue:** Every container can resolve and connect to every other container on every port. `notification-service` only needs RabbitMQ, yet it can reach both Postgres instances and Redis. The gateway can reach the databases. `user-service` can reach `order-db`.

**Impact:** One compromised container gives an attacker network access to everything. A bug in `notification-service`, for example, puts `user-db` within direct reach. Lateral movement takes no extra effort.

**Fix:** Split traffic into purpose-built networks, and mark every network except the edge as `internal: true` so it has no outbound route. See §5 for a full example.

---

### N-05 · Hardcoded or default credentials — High

| Secret | Where | Value |
|--------|-------|-------|
| Postgres password (both DBs) | `docker-compose.yml:6,20` | `secret` |
| Postgres DSN | `user-service/internal/config/config.go:18`, `order-service/internal/config/config.go:11` | `postgres:secret@...` |
| JWT signing key | `docker-compose.yml:83` **and** as the code fallback in `api-gateway/main.go:39` | `super-secret-key` |
| RabbitMQ | `order-service/internal/messaging/rabbitmq.go:23`, `notification-service/main.go:22` | `guest:guest` (image default) |
| Redis | `user-service/internal/config/config.go:51` | empty |

**Impact:**
- **JWT key:** `super-secret-key` is committed to git and is the fallback when `JWT_SECRET` is unset. Anyone who reads the repo can mint HS256 tokens with any `user_id`/`role`, which completely defeats gateway authentication. The key is also short and guessable, so it could be brute-forced offline from any captured token.
- Both databases share one password, so leaking one leaks both.
- Every value is in git history. Rotating them is required; deleting them from the files is not enough.

**Fix:**
- Load secrets from a git-ignored `.env` (already covered by `.gitignore:17-18`), or better, use Compose `secrets:` mounted at `/run/secrets/*`. Use `POSTGRES_PASSWORD_FILE` for Postgres.
- Generate a JWT key of at least 32 random bytes, and **remove the fallback default** in `api-gateway/main.go:39` so the gateway refuses to start without one.
- Give each database its own credentials.
- Set `RABBITMQ_DEFAULT_USER`/`RABBITMQ_DEFAULT_PASS` so the default `guest` user is never created.
- Rotate all of these values, because they are already in history.

---

### N-06 · Secrets compiled into binaries — High

**Location:** `user-service/internal/config/config.go:18`, `order-service/internal/config/config.go:11`, `order-service/internal/messaging/rabbitmq.go:23`, `notification-service/main.go:22`, `api-gateway/main.go:39`

**Issue:** The connection strings are Go string literals. `go build` stores them in the binary, and the binary is copied into the runtime image. Running `strings main | grep postgres://` on any pulled image reveals them.

**Impact:** Anyone with pull access to a registry, a CI artifact, or an exported image tarball gets the credentials. They cannot be rotated without a rebuild, and every environment uses the same values.

**Fix:** Read DSNs and credentials from environment variables or secret files at startup, and fail fast if they are missing. §3.3 of `ai_doc/report.md` already recorded the hardcoded hosts as a design gap. This finding is the security side of the same problem.

---

### N-07 · Plaintext internal transport — Medium

**Issue:**
- Postgres uses `sslmode=disable`, set explicitly in both DSNs.
- RabbitMQ uses `amqp://`, not `amqps://`.
- Redis uses no TLS.
- The gateway reaches backends over `http://` (`docker-compose.yml:84-86`), forwarding the user's `Authorization` bearer token and the injected identity headers in cleartext.

**Impact:** On a single-host bridge network the risk is limited to someone who already has a foothold on that network. That is easy to get given N-04. Once the stack runs across multiple hosts (Swarm/K8s overlay, cloud VPC), credentials, JWTs and PII cross the wire unencrypted.

**Fix:** Network segmentation (§5) is enough for a single host. Before any multi-host deployment, enable TLS: `sslmode=verify-full`, `amqps` with certificates, Redis `--tls-port`, and mTLS or a service mesh for HTTP. This depends on D-05.

---

### N-08 · Applications use the Postgres superuser — Medium

**Location:** `POSTGRES_USER: postgres` in `docker-compose.yml:5,19`, and the same user in both DSNs.

**Impact:** A SQL injection bug or leaked DSN in either service gives full superuser power. That includes `COPY ... TO/FROM PROGRAM` (command execution inside the DB container), reading server files, and creating roles.

**Fix:** Keep `postgres` for the init step only. Use an init script (`/docker-entrypoint-initdb.d/`) to create a per-service role that owns only its schema and has `NOSUPERUSER NOCREATEDB NOCREATEROLE`, and connect the app with that role.

---

### N-09 · Client-supplied identity headers pass through the gateway — Medium

**Location:** `api-gateway/main.go:106-113`

**Issue:** The middleware sets `X-User-ID`/`X-User-Role` **only if** the claim is present in the token. It never deletes headers the client sent. A request with a valid token that has no `role` claim, sent with a hand-written `X-User-Role: admin`, reaches the backend with that header unchanged. Under N-01, a client can also send these headers straight to a backend with no token at all.

**Current exposure:** None of the backends read these headers today (grep finds no consumers). The problem is waiting to happen: the first time a backend makes an authorization decision from `X-User-Role`, it becomes a privilege-escalation bug.

**Fix:** At the start of `JWTAuthMiddleware`, and also for public routes, call `c.Request.Header.Del("X-User-ID")` and `Del("X-User-Role")`. Only set them from verified claims. Also validate claim types, and require `exp` with `jwt.WithExpirationRequired()`.

---

### N-10 · Unauthenticated `/metrics` — Medium

**Location:** `api-gateway/main.go:147` (on the public `:8000`), `user-service/cmd/server/main.go:29`, `order-service/cmd/server/main.go:34`

**Impact:** Go runtime and process metrics reveal the Go version, memory and goroutine counts, and request paths and rates. That helps an attacker fingerprint the stack and time a DoS. Scraping large metric sets over and over also costs CPU.

**Fix:** Serve metrics on a separate internal-only port (e.g. `:9100`) on a `monitoring` network that only Prometheus joins. At minimum, do not register `/metrics` on the public gateway router.

---

### N-11 · No container runtime hardening — Medium

**Issue:** None of the services set any of the following:
- `security_opt: ["no-new-privileges:true"]`
- `cap_drop: [ALL]`
- `read_only: true` with `tmpfs` for scratch space
- `deploy.resources.limits` (memory, CPU) / `pids_limit`
- `restart:` policy

**Impact:** A compromised container keeps Docker's default capability set (`NET_RAW`, which allows ARP/packet spoofing on the bridge, plus `CHOWN`, `SETUID`, etc.) and can write to its own filesystem. With no limits, one runaway or attacked service can use up all host memory and take the other containers down with it.

**Fix:** See the `x-hardening` anchor in §5.

---

### N-12 · CORS wildcard with credentials — Low

**Location:** `api-gateway/main.go:121-122`

**Issue:** `Access-Control-Allow-Origin: *` combined with `Access-Control-Allow-Credentials: true`. The spec forbids this pair, and browsers refuse credentialed requests under it. It is harmless today but it signals intent. The usual "fix" of reflecting the request `Origin` would let any website make credentialed calls to the API.

**Fix:** Keep an explicit allowlist of origins from configuration, and only send `Allow-Credentials` for origins on that list.

---

### N-13 · More service dependencies than needed — Info

`user-service` has `depends_on: rabbitmq` (`docker-compose.yml:53`) but never connects to RabbitMQ. It is harmless on its own, but once networks are split (§5) it would wrongly suggest `user-service` belongs on the broker network. Remove it.

---

## 4. Detailed Findings — Dockerfiles

All four Dockerfiles follow the same template. The findings apply to every service unless a line says otherwise.

| File | Build target | Runtime base | USER | EXPOSE | ca-certs |
|------|-------------|--------------|------|--------|----------|
| `api-gateway/Dockerfile` | `main.go` | `alpine:latest` | root | 8000 | yes |
| `user-service/Dockerfile` | `./cmd/server` | `alpine:latest` | root | 8001 | **no** |
| `order-service/Dockerfile` | `./cmd/server` | `alpine:latest` | root | 8002 | **no** |
| `notification-service/Dockerfile` | `.` | `alpine:latest` | root | — | **no** |

### D-01 · Containers run as root — High

**Location:** No `USER` instruction in any file. `WORKDIR /root/` makes the root context explicit (e.g. `user-service/Dockerfile:9`).

**Impact:** An RCE in any service runs as UID 0 inside the container. Combined with the default capabilities (N-11) and a writable filesystem, that makes container escape and host-level impact much easier, especially if a Docker socket or host path is ever mounted.

**Fix:**
```dockerfile
FROM alpine:3.20
RUN addgroup -S app && adduser -S -G app -H -s /sbin/nologin app
WORKDIR /app
COPY --from=builder --chown=app:app /app/main .
USER app:app
```
Alternatively, use `gcr.io/distroless/static-debian12:nonroot`, which runs as UID 65532 by default.

### D-02 · Base images not pinned — Medium

**Location:** `alpine:latest` in all four Dockerfiles, `redis:alpine` at `docker-compose.yml:31`, `golang:1.26-alpine` (patch version floats), and `postgres:16-alpine` / `rabbitmq:3.13-management` (minor versions float).

**Impact:** Builds are not reproducible, so a new upstream image can change behavior or add vulnerabilities silently. `redis:alpine` can jump a **major** version. There is no integrity guarantee against a tampered tag.

**Fix:** Pin to a specific version **and** digest, e.g. `alpine:3.20.3@sha256:...`. Let Dependabot or Renovate propose updates, and add image scanning (Trivy or Grype) to CI.

### D-03 · No `.dockerignore` — Medium

**Location:** `COPY . .` in every builder stage (e.g. `user-service/Dockerfile:5`). No `.dockerignore` exists anywhere in the repo.

**Impact:**
- Local `.env` files, editor configs, compiled `.exe` files, and any secrets in the service folder are sent to the Docker daemon and stored in the builder stage's layers and build cache. The builder stage is not shipped in the final image, but it stays in local and CI build caches, and `--target builder` or cache export would expose it.
- The build context gets larger, and any file change invalidates the cache.

**Fix:** Add a `.dockerignore` in each service directory:
```
.git
.env
.env.*
*.exe
*.test
*.out
.vscode/
.idea/
Dockerfile
```

### D-04 · Runtime image larger than needed — Low

**Issue:** `alpine` ships `/bin/sh`, busybox and `apk`. The Go binaries are static (`CGO_ENABLED=0`) and need none of them.

**Impact:** An attacker who gets code execution has a shell, `wget` and a package manager ready to use. The image also contains more CVE-bearing packages than necessary.

**Fix:** Use `gcr.io/distroless/static-debian12:nonroot` or `scratch` with copied CA certs. Build with `-trimpath -ldflags="-s -w"` to strip local paths and debug symbols.

### D-05 · `ca-certificates` missing in user, order and notification services — Low

**Location:** Only `api-gateway/Dockerfile:16` installs it.

**Impact:** None today, because everything is plaintext. But once TLS is enabled (N-07), these services will fail certificate verification. The common shortcut then is `InsecureSkipVerify: true` / `sslmode=require` without verification, which quietly re-opens MITM risk.

**Fix:** Install `ca-certificates` in every runtime image. Distroless `static` already includes them.

### D-06 · No HEALTHCHECK / restart policy — Low

**Issue:** The app containers define no `HEALTHCHECK` (Dockerfile) or `healthcheck:` (Compose), and no `restart:` policy. `api-gateway` has `depends_on` with no condition (`docker-compose.yml:87-90`).

**Impact:** A crashed or hung service stays down with no alert. A process that is up but broken keeps receiving traffic. This is availability, not confidentiality.

**Fix:** Add a `healthcheck:` that probes `/health` (the gateway already serves one at `main.go:142`), set `restart: unless-stopped`, and use `condition: service_healthy`. Distroless images have no `wget`/`curl`, so either add a small `-healthcheck` flag to each binary or use a static probe binary.

---

## 5. Recommended Target Configuration

### 5.1 Target topology

```
                    HOST
                     │ 127.0.0.1:8000 (or 0.0.0.0 behind a TLS reverse proxy)
                     ▼
         ┌────────── edge ──────────┐
         │       api-gateway        │
         └────────────┬─────────────┘
         ┌────────── app (internal) ───────────────────────────┐
         │ user-service   order-service   notification-service │
         └──────┬───────────────┬────────────────┬─────────────┘
   ┌── user-data (internal) ┐ ┌ order-data (int.) ┐ ┌── broker (internal) ─────────┐
   │ user-db   redis        │ │ order-db          │ │ rabbitmq                     │
   │ + user-service         │ │ + order-service   │ │ + order-svc, notification-svc│
   └────────────────────────┘ └───────────────────┘ └──────────────────────────────┘
```

**Reachability matrix (target):**

| ↓ can reach → | gateway | user-svc | order-svc | notif-svc | user-db | redis | order-db | rabbitmq |
|---|---|---|---|---|---|---|---|---|
| **host / LAN** | ✅ 8000 | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| **api-gateway** | — | ✅ | ✅ | ✅ | ❌ | ❌ | ❌ | ❌ |
| **user-service** | ❌ | — | ✅* | ✅* | ✅ | ✅ | ❌ | ❌ |
| **order-service** | ❌ | ✅* | — | ✅* | ❌ | ❌ | ✅ | ✅ |
| **notification-service** | ❌ | ✅* | ✅* | — | ❌ | ❌ | ❌ | ✅ |

\* East-west traffic on `app` remains possible. Plain Compose networks cannot restrict it further. If that matters, give each backend its own gateway↔service network, or move to Kubernetes `NetworkPolicy`.

### 5.2 Example `docker-compose.yml`

```yaml
x-hardening: &hardening
  security_opt:
    - no-new-privileges:true
  cap_drop:
    - ALL
  read_only: true
  tmpfs:
    - /tmp
  restart: unless-stopped
  pids_limit: 200
  deploy:
    resources:
      limits:
        memory: 256M
        cpus: "0.50"

networks:
  edge: {}                     # only network with host port publishing
  app:        { internal: true }
  user-data:  { internal: true }
  order-data: { internal: true }
  broker:     { internal: true }

secrets:
  user_db_password:  { file: ./secrets/user_db_password.txt }
  order_db_password: { file: ./secrets/order_db_password.txt }
  jwt_secret:        { file: ./secrets/jwt_secret.txt }
  redis_password:    { file: ./secrets/redis_password.txt }
  rabbitmq_password: { file: ./secrets/rabbitmq_password.txt }

services:
  user-db:
    image: postgres:16.4-alpine   # pin + @sha256 digest
    environment:
      POSTGRES_USER: postgres
      POSTGRES_PASSWORD_FILE: /run/secrets/user_db_password
      POSTGRES_DB: userdb
    secrets: [user_db_password]
    networks: [user-data]
    volumes: [user-db-data:/var/lib/postgresql/data]
    # no ports:

  redis:
    image: redis:7.4-alpine
    command: ["sh", "-c", "exec redis-server --requirepass \"$$(cat /run/secrets/redis_password)\" --rename-command CONFIG '' --rename-command FLUSHALL ''"]
    secrets: [redis_password]
    networks: [user-data]

  rabbitmq:
    image: rabbitmq:3.13-alpine   # drop -management unless required
    environment:
      RABBITMQ_DEFAULT_USER: app
      # RabbitMQ has no *_FILE support; inject via env from an untracked .env
      RABBITMQ_DEFAULT_PASS: ${RABBITMQ_PASSWORD:?set in .env}
    networks: [broker]

  user-service:
    <<: *hardening
    build: ./user-service
    networks: [app, user-data]
    secrets: [user_db_password, redis_password]
    depends_on:
      user-db: { condition: service_healthy }
      redis:   { condition: service_started }

  order-service:
    <<: *hardening
    build: ./order-service
    networks: [app, order-data, broker]
    secrets: [order_db_password]

  notification-service:
    <<: *hardening
    build: ./notification-service
    networks: [app, broker]

  api-gateway:
    <<: *hardening
    build: ./api-gateway
    networks: [edge, app]
    ports:
      - "127.0.0.1:8000:8000"   # put a TLS-terminating proxy in front for external exposure
    secrets: [jwt_secret]

volumes:
  user-db-data: {}
```

For local debugging with DB and broker ports, keep a **separate** `docker-compose.override.yml` that only binds to `127.0.0.1`. Do not use it outside a developer machine.

### 5.3 Example hardened Dockerfile (user-service)

```dockerfile
FROM golang:1.26.1-alpine@sha256:<digest> AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/main ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot@sha256:<digest>
WORKDIR /app
COPY --from=builder /out/main /app/main
EXPOSE 8001
USER nonroot:nonroot
ENTRYPOINT ["/app/main"]
```

---

## 6. Remediation Plan

| Priority | Action | Findings closed | Effort |
|----------|--------|-----------------|--------|
| **P0 — now** | Remove `ports:` from user-db, order-db, redis, rabbitmq, user-service and order-service | N-01, N-02, N-03 | Minutes |
| **P0 — now** | Rotate the JWT key to ≥32 random bytes, and remove the code fallback in `api-gateway/main.go:39` | N-05 (JWT) | Minutes |
| **P1** | Enable Redis `requirepass`/ACL, set non-default RabbitMQ creds, give each DB its own password | N-02, N-05 | < 1 hour |
| **P1** | Move DSNs and credentials out of Go source into env or secret files | N-05, N-06 | ~1–2 hours |
| **P1** | Add `USER` non-root to all four Dockerfiles, and add a `.dockerignore` | D-01, D-03 | < 1 hour |
| **P1** | Strip inbound `X-User-*` headers in the gateway | N-09 | Minutes |
| **P2** | Split into segmented `internal` networks (§5) | N-04, N-13 | ~1 hour |
| **P2** | Add a hardening anchor (`cap_drop`, `no-new-privileges`, `read_only`, limits) | N-11 | ~1 hour (test `read_only`) |
| **P2** | Pin image versions and digests, and add Trivy scanning in CI | D-02 | ~1 hour |
| **P2** | Move `/metrics` to an internal port or network | N-10 | ~1 hour |
| **P2** | Create least-privilege DB roles | N-08 | ~1 hour |
| **P3** | Switch to distroless, add CA certs, health checks and restart policy | D-04, D-05, D-06 | ~1–2 hours |
| **P3** | CORS origin allowlist | N-12 | Minutes |
| **P3 — before multi-host** | TLS for Postgres, AMQP, Redis and internal HTTP | N-07 | Days |

---

## 7. Verification Checklist (after remediation)

- [ ] `docker compose ps` shows **only** `api-gateway` with a published port.
- [ ] From the host: `curl localhost:8001/users` and `redis-cli -h localhost ping` both fail to connect.
- [ ] `docker compose exec notification-service` cannot resolve or connect to `user-db` (with distroless, test from a temporary debug container attached to the same networks).
- [ ] `docker compose exec user-service id` (or `docker inspect --format '{{.Config.User}}'`) shows a non-root user.
- [ ] `docker run --rm <image> strings /app/main | grep -i 'postgres://\|amqp://'` finds no credentials.
- [ ] `grep -rn 'secret\|guest\|super-secret' docker-compose.yml */internal */main.go` finds no credential literals.
- [ ] A request with a forged `X-User-Role: admin` header reaches the backend without that header.
- [ ] `trivy image` shows no HIGH or CRITICAL vulnerabilities in any runtime image.
- [ ] Old values (`secret`, `super-secret-key`, `guest`) are rotated everywhere they were ever deployed.
