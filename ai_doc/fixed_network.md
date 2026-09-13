# Network Topology Fix — Go Microservices Platform

**Date:** 2026-09-13
**Follows:** `ai_doc/network_issues.md` (findings N-01, N-02, N-03, N-04)
**Changed file:** `docker-compose.yml` (only file modified)
**Verified:** `go build` + `go vet` on all four modules, `docker compose config`, `docker compose up -d --build`, then live probes from the host and between containers on Docker Engine 29.7.2 (Docker Desktop, Windows).

---

## 1. Summary

Before, the stack ran on one flat default network, and **8 host ports** were published on `0.0.0.0`. Among them were both Postgres databases, Redis with no password, and the two backend services, which have no authentication of their own. It now runs on **four purpose-built networks**, and only **2 host ports** are published: the API Gateway (`8000`) and the RabbitMQ Management UI (`15672`). Two of the new networks are `internal: true`, so their containers have no route to the internet and cannot be reached from the host.

A real order still goes through the whole chain: host → gateway → order-service → user-service / order-db → RabbitMQ → notification-service.

| | Before | After |
|---|---|---|
| Networks | 1 (`default`, flat) | 4 (`public-net`, `internal-net`, `db-net`, `mgmt-net`) |
| Host-published ports | 8000, 8001, 8002, 5432, 5433, 6379, 5672, 15672 | **8000, 15672** |
| Backends reachable without JWT | Yes (`:8001`, `:8002`) | **No** |
| Redis / Postgres reachable from host | Yes | **No** |
| Gateway → databases / Redis / RabbitMQ | Yes | **No** |
| App & data containers → internet | Yes | **No** (except `api-gateway` and `rabbitmq`, see §5) |

---

## 2. What Was Done

### 2.1 Network segmentation

I removed the implicit `default` network and declared four explicit bridge networks:

```yaml
networks:
  public-net:   { driver: bridge }                  # edge, host-facing
  internal-net: { driver: bridge, internal: true }  # gateway <-> microservices
  db-net:       { driver: bridge, internal: true }  # microservices <-> datastores/broker
  mgmt-net:     { driver: bridge }                  # rabbitmq only, to publish 15672
```

### 2.2 Service wiring

| Service | Networks | Host ports |
|---|---|---|
| `api-gateway` | `public-net`, `internal-net` | `8000:8000` |
| `user-service` | `internal-net`, `db-net` | — |
| `order-service` | `internal-net`, `db-net` | — |
| `notification-service` | `internal-net`, `db-net` | — |
| `user-db` | `db-net` | — |
| `order-db` | `db-net` | — |
| `redis` | `db-net` | — |
| `rabbitmq` | `db-net`, `mgmt-net` | `15672:15672` |

### 2.3 Port restrictions

I removed these published ports:

| Removed mapping | Service | Why it was dangerous |
|---|---|---|
| `8001:8001` | user-service | `GET /users` returned all users with no JWT (skipped the gateway) |
| `8002:8002` | order-service | `POST /orders` created orders with no JWT |
| `5432:5432` | user-db | Superuser login `postgres/secret` from outside |
| `5433:5432` | order-db | Same as above |
| `6379:6379` | redis | No password; cache read / poisoning |
| `5672:5672` | rabbitmq (AMQP) | `guest/guest` publish/consume, forged order events |

I kept `8000:8000` (gateway) and `15672:15672` (RabbitMQ Management UI), as requested.

### 2.3a Follow-up: monitoring override (`docker-compose.monitoring.yml`)

Added after the first pass. Once the `default` network was gone, Prometheus landed on an empty `default` network and every app target went `down` (`lookup user-service ... no such host`). The fix:

| Service | Networks | Host ports |
|---|---|---|
| `prometheus` | `internal-net` (to scrape), `monitoring-net` (to publish) | `9090:9090` |
| `grafana` | `monitoring-net` | `3000:3000` |

`monitoring-net` is a new non-internal bridge that only these two containers join. Result: all four Prometheus targets are `up`. Grafana can reach Prometheus but not the app services (`bad address 'user-service:8001'`). The leftover empty `go-microservices_default` network was removed.

### 2.4 Environment variables / code

**No changes were needed.** Every connection already used Compose service names, and the service names did not change:

| Consumer | Target | Source |
|---|---|---|
| api-gateway | `http://user-service:8001`, `http://order-service:8002`, `http://notification-service:8003` | `docker-compose.yml` env |
| user-service | `user-db:5432`, `redis:6379` | `user-service/internal/config/config.go` |
| order-service | `order-db:5432`, `rabbitmq:5672`, user-service | `order-service/internal/config/config.go`, `internal/messaging/rabbitmq.go`, `internal/client/user_client.go` |
| notification-service | `rabbitmq:5672` | `notification-service/main.go` |

Docker's embedded DNS resolves a service name on every network the caller shares with that service. Each consumer shares at least one network with each of its targets, so every name still resolves.

---

## 3. Why

### 3.1 Why segment the network

On the old flat network, every container could open a connection to every other container on any port. Breaking into *any* container, even `notification-service`, which only needs RabbitMQ, gave an attacker a direct line to both Postgres instances and Redis. Segmentation limits a compromise to the networks that container is attached to. The gateway is the only container that handles untrusted internet traffic, so it matters most: it can no longer reach any datastore at all.

### 3.2 Why `internal: true`

A network marked `internal` gets no gateway or NAT rule. Containers attached only to internal networks:
- **cannot open outbound internet connections**, which blocks data exfiltration, reverse shells and downloading second-stage tools after an RCE;
- **cannot be published to the host.** Docker silently drops the `ports:` mapping (see §3.4), so the network layer enforces "no host exposure", not just the compose file.

`public-net` must stay non-internal so the gateway can be published. The Go services and datastores never need internet access at runtime (Go modules are downloaded at build time), so `internal-net` and `db-net` can both be internal.

### 3.3 Why remove the host ports

A short-form mapping like `"6379:6379"` binds `0.0.0.0`, which means every interface. On a Linux host, Docker's iptables rules are evaluated before `ufw`/`firewalld`, so a host firewall does **not** protect these ports. On Docker Desktop, LAN exposure depends on Windows Defender Firewall rules. The only reliable fix is not to publish the port. Services talk over the Docker networks by service name, and that path does not touch the host.

### 3.4 Why the extra `mgmt-net` (deviation from the requested spec)

The request asked for `rabbitmq` to be **strictly on `db-net`**, `db-net` to be **internal**, **and** `15672` to be published. Those three requirements can't all hold at once. I tested this with a throwaway stack before changing anything:

```
internal-only   6379/tcp                      -> connect to 127.0.0.1:16379: REFUSED
non-internal    0.0.0.0:16380->6379/tcp        -> PONG
```

Docker does not publish ports for a container whose only networks are internal, and it gives no error. There were three options:

| Option | Trade-off |
|---|---|
| **Add `mgmt-net` (chosen)** | `db-net` stays internal. `mgmt-net` is joined by `rabbitmq` only, so it opens **no new container-to-container path**. Cost: RabbitMQ gets outbound internet egress. |
| Make `db-net` non-internal | Every datastore and all three services get internet egress |
| Put `rabbitmq` on `public-net` | The gateway could reach RabbitMQ AMQP `5672` with `guest/guest` |

You picked the `mgmt-net` option when asked.

---

## 4. Impact

### 4.1 Security impact

| Finding (network_issues.md) | Status | Notes |
|---|---|---|
| **N-01** Backends published, gateway can be skipped — Critical | **Fixed** | `8001`/`8002` refuse connections from the host. Backends are reachable only from containers on `internal-net`/`db-net`. |
| **N-02** Redis open with no auth — Critical | **Partially fixed** | No longer reachable from host or gateway. Still has **no password**, so any container on `db-net` can use it. |
| **N-03** Postgres / AMQP / mgmt UI on all interfaces — High | **Mostly fixed** | Postgres and AMQP are closed. `15672` is **still public by request**, with `guest/guest`. |
| **N-04** Flat network — High | **Fixed (3-tier)** | See the reachability matrix in §4.3. `db-net` is still one shared segment (§5). |
| **N-13** `user-service` → `rabbitmq` dependency not needed | Unchanged | Harmless. `user-service` is on `db-net` anyway. |
| N-05 … N-12, D-01 … D-06 | Not in scope | Credentials, root users, TLS, hardening etc. are still open. |

### 4.2 Functional impact

**No regression.** The stack starts and works exactly as it did before:
- All containers start. The DB and RabbitMQ health checks go green, and every service logs a successful connection to its dependencies.
- `POST /api/v1/orders` through the gateway with a valid JWT → `HTTP 201 {"order_id":"ORD-1012","customer_name":"Rahim",...,"status":"CONFIRMED"}`. This one request exercised gateway → order-service → user-service (user lookup) → order-db (insert) → RabbitMQ (publish) → notification-service (consume). The notification was logged.
- The same request with no token → `HTTP 401`, so the gateway still enforces auth.

**Developer workflow changes:**
- You can no longer connect a local DB client (pgAdmin, DBeaver, `psql`) to `localhost:5432/5433`, or `redis-cli` to `localhost:6379`, or call backends directly on `8001/8002`. Use the following instead:
  - `docker compose exec user-db psql -U postgres -d userdb`
  - `docker compose exec redis redis-cli`
  - call the backends through the gateway at `http://localhost:8000/api/v1/...`
  - or, if you really need host access, add a **git-ignored** `docker-compose.override.yml` that binds to loopback only (`"127.0.0.1:5433:5432"`).
- App containers no longer have internet access at runtime. That's fine today. If a service later needs an external API (email/SMS provider, payments), it will need to go through an egress proxy or join a dedicated non-internal network. Do not remove `internal: true` to allow it.

### 4.3 Verified results

**Host port exposure** (TCP connect to `127.0.0.1`):

| Port | Result |
|---|---|
| 8000 | OPEN (gateway) |
| 15672 | OPEN (RabbitMQ UI, `HTTP 200`) |
| 8001, 8002, 5433, 6379, 5672 | closed |
| 5432 | OPEN, but **not Docker**. See note below. |

> **Note — port 5432:** On this machine a native Windows service, `postgresql-x64-18` (PostgreSQL 18, pid 6368), listens on `0.0.0.0:5432`. It's unrelated to this project, and neither Compose database publishes a host port (`docker compose ps` shows `5432/tcp` with no binding). I left it untouched. Stop it or bind it to `127.0.0.1` if you don't use it.

**Container-to-container reachability** (probed with `nc -z` / bash `/dev/tcp` inside the containers):

| From ↓ / To → | gateway | user-svc | order-svc | user-db | order-db | redis | rabbitmq:5672 | internet |
|---|---|---|---|---|---|---|---|---|
| **host** | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | — |
| **api-gateway** | — | ✅ | ✅ | ❌ | ❌ | ❌ | ❌ | ✅ |
| **user-service** | | — | | ✅ | | ✅ | | ❌ |
| **order-service** | | ✅ | — | | ✅ | | ✅ | ❌ |
| **notification-service** | | | | ⚠️ ✅ | | ⚠️ ✅ | ✅ | ❌ |
| **redis** | ❌ | ⚠️ ✅ | | | | — | | ❌ |
| **rabbitmq** | ❌ | ⚠️ ✅ | ⚠️ ✅ | | | | — | ⚠️ ✅ |

✅ = reachable and required · ❌ = blocked, as intended · ⚠️ = reachable but **not required**, a residual risk of the requested design (§5) · blank = not probed

---

## 5. Residual Risks & Recommended Next Steps

This change fixes network *exposure*. It does not fix *credentials* or *container privileges*. The segmentation as specified also leaves some gaps:

1. **`db-net` is one shared segment.** Every service on it can reach every datastore on it. Verified: `notification-service` can reach `user-db:5432` and `redis:6379`, which it never uses. In the other direction, `redis` and `rabbitmq` can reach `user-service:8001`/`order-service:8002`, because those services are also on `db-net`. **Next step:** split `db-net` per owner (`user-data`: user-service + user-db + redis; `order-data`: order-service + order-db; `broker-net`: order-service + notification-service + rabbitmq), as proposed in `network_issues.md` §5.

2. **RabbitMQ Management UI is public with `guest/guest`.** `15672` is on `0.0.0.0`, and the official image lets `guest` log in remotely. Anyone who can reach the host can read queues, publish forged order events and create admin users. **Next step:** set `RABBITMQ_DEFAULT_USER`/`RABBITMQ_DEFAULT_PASS` from an untracked `.env`, and bind the UI to loopback (`"127.0.0.1:15672:15672"`) unless remote access is really needed.

3. **RabbitMQ has internet egress** through `mgmt-net` (verified: `rabbitmq → 1.1.1.1:443` reachable). This is the cost of the option chosen in §3.4. Binding `15672` to `127.0.0.1` doesn't remove the egress. Only dropping the host mapping (and `mgmt-net` with it) does.

4. **Redis still has no password.** It's isolated from the host and gateway, but any container on `db-net` can read or poison the user cache (N-02).

5. **Gateway still has internet egress**, which is needed because it's the published edge. It is now the most exposed container and still runs as root (D-01) with a hardcoded, publicly known JWT secret (N-05). Rotating `JWT_SECRET` and removing its code fallback is now the **highest-priority remaining item**: anyone who can forge a JWT can still reach every backend through the gateway.

6. Everything else in `network_issues.md` is untouched: N-05 to N-12 and D-01 to D-06, i.e. hardcoded credentials, secrets in binaries, root containers, plaintext transport, `/metrics` exposure, unpinned images, no `.dockerignore`.

---

## 6. How to Re-verify

```powershell
# Only 8000 and 15672 should show a host binding
docker compose ps --format 'table {{.Service}}\t{{.Ports}}'

# Network membership and internal flag
foreach ($n in 'public-net','internal-net','db-net','mgmt-net') {
  docker network inspect "go-microservices_$n" --format "$n internal={{.Internal}} {{range .Containers}}{{.Name}} {{end}}"
}

# Gateway must NOT reach the data tier (expect non-zero exit)
docker compose exec api-gateway nc -z -w 3 user-db 5432; $LASTEXITCODE

# Internal services must NOT reach the internet (expect non-zero exit)
docker compose exec user-service nc -z -w 3 1.1.1.1 443; $LASTEXITCODE

# Backends must NOT be reachable from host (expect connection refused)
curl.exe -m 3 http://localhost:8001/users
```
