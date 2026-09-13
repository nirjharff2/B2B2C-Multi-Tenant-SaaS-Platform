# API & Network Test Guide — Go Microservices Platform

Copy-paste `curl` commands that check every endpoint and the network isolation added in `ai_doc/fixed_network.md`. Every command below was run against the live stack on **2026-09-13**, and each **Expected** block is the real output.

**Read this first: how the network works now**
- From the host, only **`8000`** (api-gateway) and **`15672`** (RabbitMQ UI) are published, plus `9090`/`3000` when monitoring is running.
- `user-service` (8001) and `order-service` (8002) **cannot be reached from `localhost`**. That is on purpose, so nobody can skip the gateway's JWT check.
- To test those services directly (Parts A and B), run `curl` from a throwaway container attached to the Docker `internal-net` network. This is the same path the gateway uses.

Commands are written for **bash**. On Windows, use Git Bash. Run the parts in order, because some tests depend on earlier ones (the token from D4, the order from B1).

---

## 0. Setup

### 0.1 Start the stack
```bash
docker compose up -d --build
docker compose ps --format "table {{.Service}}\t{{.Status}}\t{{.Ports}}"
```
**Expected:** everything is `Up`, `user-db`/`order-db`/`rabbitmq` show `(healthy)`, and **only** `api-gateway` and `rabbitmq` have a `0.0.0.0:` binding:
```
SERVICE                STATUS                    PORTS
api-gateway            Up 10 minutes             0.0.0.0:8000->8000/tcp, [::]:8000->8000/tcp
notification-service   Up 10 minutes
order-db               Up 11 minutes (healthy)   5432/tcp
order-service          Up 10 minutes             8002/tcp
rabbitmq               Up 11 minutes (healthy)   0.0.0.0:15672->15672/tcp, [::]:15672->15672/tcp
redis                  Up 11 minutes             6379/tcp
user-db                Up 11 minutes (healthy)   5432/tcp
user-service           Up 10 minutes             8001/tcp
```
If a container isn't healthy, see `ai_doc/run_guide.md`.

### 0.2 Define the in-network curl helper
```bash
incurl() { docker run --rm --network go-microservices_internal-net curlimages/curl:8.11.1 "$@"; }
```
`incurl` works like `curl`, but it runs **inside** `internal-net`, so service names like `user-service:8001` resolve. It needs to be defined again in every new shell. The first call pulls the image (~10 MB).

> The network name is `<project>_internal-net`, and the project name comes from the folder (`go-microservices`). If you cloned into a different folder, check the name with `docker network ls`.

### 0.3 Reset the orders table (required on every re-run)
```bash
docker compose up -d --force-recreate --renew-anon-volumes order-db order-service
docker compose exec -T order-db psql -U postgres -d orderdb -tAc "select count(*) from orders;"
```
**Expected:** `0`

**Why this step exists:** order IDs aren't unique. `order-service/internal/service/order_service.go:38` builds them as `ORD-<1000 + length of the item name>`, so `"Headphones"` is always `ORD-1010` and `"Keyboard"` is always `ORD-1008`. Placing the same order twice (or any two orders whose item names have the same length) fails with:
```
ERROR: duplicate key value violates unique constraint "orders_order_id_key" (SQLSTATE 23505)

HTTP:400
```
`--renew-anon-volumes` is required. The Postgres image keeps its data in an anonymous volume, and a plain `--force-recreate` reuses that volume, so the old orders survive. You can skip this step on a stack that was just brought up for the first time.

---

## Part N — Network exposure & isolation

These tests check that the network hardening is actually working. If any of them fail, the security fix has regressed, even if every API test still passes.

### N1. Backends are NOT reachable from the host
```bash
curl -s -m 3 -o /dev/null http://localhost:8001/users; echo "curl exit $?"
curl -s -m 3 -o /dev/null http://localhost:8002/orders; echo "curl exit $?"
```
**Expected:** a non-zero exit code: `7` (connection refused) or `28` (timeout). The exit code must **never** be `0`:
```
curl exit 7
curl exit 7
```

### N2. Datastores and broker are NOT reachable from the host
```bash
for p in 5433 6379 5672; do curl -s -m 2 -o /dev/null http://localhost:$p; echo "$p -> curl exit $?"; done
```
**Expected:** every port gives a non-zero exit code (`7` or `28`).

> **Port 5432:** if `localhost:5432` answers, check whether a **native** PostgreSQL is installed on your machine. On the test machine it was the Windows service `postgresql-x64-18`, not Docker. `docker compose ps` must show `user-db` as `5432/tcp` with no `0.0.0.0:` prefix.

### N3. Gateway can reach backends, but NOT the data tier
```bash
docker compose exec -T api-gateway nc -z -w 3 user-service 8001; echo "user-service exit $?"
docker compose exec -T api-gateway nc -z -w 3 user-db 5432;      echo "user-db exit $?"
docker compose exec -T api-gateway nc -z -w 3 redis 6379;        echo "redis exit $?"
```
**Expected:**
```
user-service exit 0
nc: bad address 'user-db'
user-db exit 1
nc: bad address 'redis'
redis exit 1
```
`bad address` means the name doesn't even resolve, because the gateway shares no network with the datastores.

### N4. Internal services have no internet egress
```bash
docker compose exec -T user-service nc -z -w 3 1.1.1.1 443; echo "exit $?"
```
**Expected:** `exit 1`. `internal-net` and `db-net` are `internal: true`, so they have no outbound route.

---

## Part A — `user-service` (in-network, `user-service:8001`)

### A1. Get a seeded user
```bash
incurl -s -w "\nHTTP:%{http_code}\n" http://user-service:8001/users/101
```
**Expected:**
```
{"id":"101","name":"Rahim","email":"rahim@example.com"}

HTTP:200
```

### A2. Get another seeded user
```bash
incurl -s -w "\nHTTP:%{http_code}\n" http://user-service:8001/users/102
```
**Expected:**
```
{"id":"102","name":"Karim","email":"karim@example.com"}

HTTP:200
```
Getting a second row back confirms the Postgres seed worked, and that A1 wasn't a one-off cache hit.

### A3. List all users
```bash
incurl -s -w "\nHTTP:%{http_code}\n" http://user-service:8001/users
```
**Expected:**
```
[{"id":"101","name":"Rahim","email":"rahim@example.com"},{"id":"102","name":"Karim","email":"karim@example.com"},{"id":"103","name":"Babe","email":"babe@example.com"}]

HTTP:200
```
> This endpoint has no authentication and returns every user. The network change is the only thing keeping it off the host (see N1).

### A4. User that doesn't exist
```bash
incurl -s -w "\nHTTP:%{http_code}\n" http://user-service:8001/users/999
```
**Expected:**
```
{"message":"User not found"}

HTTP:404
```

### A5. Missing user ID
```bash
incurl -s -w "\nHTTP:%{http_code}\n" http://user-service:8001/users/
```
**Expected:**
```
404 page not found

HTTP:404
```
This comes from Go's router, not from the handler. `GET /users/{id}` doesn't match an empty `{id}`, so the request never reaches the handler's own "User ID is required" check.

### A6. Redis cache is used
Request a user **twice**, then read the logs:
```bash
incurl -s -o /dev/null http://user-service:8001/users/103
incurl -s -o /dev/null http://user-service:8001/users/103
docker compose logs --no-log-prefix user-service --tail=3
```
**Expected:** the first request misses the cache and stores the user, and the second one hits the cache:
```
🐢 [CACHE MISS] Fetching User 103 from PostgreSQL...
💾 User 103 saved to Redis Cache (Valid for 10 mins)
⚡ [CACHE HIT] User 103 found in Redis!
```
Entries expire after 10 minutes. If you see a `CACHE HIT` on the first request, the user was requested recently, so try again with another ID.

---

## Part B — `order-service` (in-network, `order-service:8002`)

### B1. Create an order for a valid user
```bash
incurl -s -w "\nHTTP:%{http_code}\n" -X POST http://order-service:8002/orders \
  -H "Content-Type: application/json" \
  -d '{"user_id":"102","item":"Headphones","amount":149.99}'
```
**Expected** (after step 0.3):
```
{"order_id":"ORD-1010","customer_name":"Karim","item":"Headphones","amount":149.99,"status":"CONFIRMED"}

HTTP:201
```
This shows order-service called `user-service` over `internal-net` to look up the customer name, then saved the order to `order-db` over `db-net`.

### B2. Order for a user that doesn't exist
```bash
incurl -s -w "\nHTTP:%{http_code}\n" -X POST http://order-service:8002/orders \
  -H "Content-Type: application/json" \
  -d '{"user_id":"999","item":"Ghost Item","amount":10}'
```
**Expected:**
```
user not found or service unavailable

HTTP:400
```

### B3. Malformed JSON body
```bash
incurl -s -w "\nHTTP:%{http_code}\n" -X POST http://order-service:8002/orders \
  -H "Content-Type: application/json" \
  -d 'not-json'
```
**Expected:**
```
Invalid Body

HTTP:400
```

### B4. Wrong HTTP method
```bash
incurl -s -w "\nHTTP:%{http_code}\n" http://order-service:8002/orders
```
**Expected:**
```
Only POST allowed

HTTP:405
```

---

## Part C — Event flow (RabbitMQ → `notification-service`)

### C1. The order from B1 was consumed
```bash
docker compose logs --no-log-prefix notification-service --tail=4
```
**Expected** — a block matching the B1 order:
```
==================================================
📧 [NOTIFICATION SENT] Customer: Karim (karim@example.com)
   Dear Karim, your order ORD-1010 for 'Headphones' (Amount: $149.99) is CONFIRMED!
==================================================
```
If this block is missing, check `docker compose logs order-service` and `docker compose logs rabbitmq` for publish errors.

### C2. Queue has a consumer and no backlog
```bash
curl -s -u guest:guest http://localhost:15672/api/queues | node -e "
let d='';process.stdin.on('data',c=>d+=c).on('end',()=>
  JSON.parse(d).forEach(q=>console.log(q.name,'consumers='+q.consumers,'messages='+q.messages)))"
```
**Expected:**
```
order_notifications consumers=1 messages=0
```
`consumers=0` means notification-service isn't connected. A growing `messages` count means events are published but never consumed. You can also open `http://localhost:15672` (login `guest` / `guest`) → **Queues → `order_notifications`**.

> The management UI is published on `0.0.0.0` with default credentials. See `ai_doc/fixed_network.md` §5.

---

## Part D — `api-gateway` (host, `localhost:8000`)

### D1. Health check (no auth)
```bash
curl -s -w "\nHTTP:%{http_code}\n" http://localhost:8000/health
```
**Expected:**
```
{"status":"API Gateway Healthy"}
HTTP:200
```

### D2. Protected route, no token → rejected
```bash
curl -s -w "\nHTTP:%{http_code}\n" -X POST http://localhost:8000/api/v1/orders \
  -H "Content-Type: application/json" \
  -d '{"user_id":"101","item":"X","amount":1}'
```
**Expected:**
```
{"error":"Authorization header required"}
HTTP:401
```

### D3. Protected route, garbage token → rejected
```bash
curl -s -w "\nHTTP:%{http_code}\n" -X POST http://localhost:8000/api/v1/orders \
  -H "Authorization: Bearer not-a-real-token" \
  -H "Content-Type: application/json" \
  -d '{"user_id":"101","item":"X","amount":1}'
```
**Expected:**
```
{"error":"Invalid or expired token"}
HTTP:401
```

### D4. Generate test JWTs
There is no `/login` endpoint yet (see D7), so sign the tokens yourself with the gateway's secret (`JWT_SECRET` in `docker-compose.yml`):
```bash
jwt() { node -e "
const c=require('crypto'), b=s=>Buffer.from(s).toString('base64url');
const h=b(JSON.stringify({alg:'HS256',typ:'JWT'}));
const p=b(JSON.stringify({user_id:'101',role:'customer',exp:Math.floor(Date.now()/1000)+Number(process.argv[1])}));
console.log(h+'.'+p+'.'+c.createHmac('sha256','super-secret-key').update(h+'.'+p).digest('base64url'));
" -- "$1"; }

TOKEN=$(jwt 3600)     # valid for 1 hour
EXPIRED=$(jwt -60)    # expired 1 minute ago
echo "$TOKEN"
```
If `JWT_SECRET` in `docker-compose.yml` changes, replace `super-secret-key` above with the new value.

### D5. Valid token → order created end-to-end
```bash
curl -s -w "\nHTTP:%{http_code}\n" -X POST http://localhost:8000/api/v1/orders \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"user_id":"101","item":"Keyboard","amount":79.5}'
```
**Expected** (after step 0.3; running it a second time gives the duplicate-key `400` described there):
```
{"order_id":"ORD-1008","customer_name":"Rahim","item":"Keyboard","amount":79.5,"status":"CONFIRMED"}

HTTP:201
```
**This is the most important test in the guide.** In a single request it covers the JWT check, the reverse proxy, the `/api/v1` path rewrite, and every network hop: `public-net` → gateway → `internal-net` → order-service → user-service, then `db-net` → order-db and RabbitMQ.

### D6. Expired token → rejected
```bash
curl -s -w "\nHTTP:%{http_code}\n" -X POST http://localhost:8000/api/v1/orders \
  -H "Authorization: Bearer $EXPIRED" \
  -H "Content-Type: application/json" \
  -d '{"user_id":"101","item":"X","amount":1}'
```
**Expected:**
```
{"error":"Invalid or expired token"}
HTTP:401
```

### D7. Routes known NOT to work yet
These failures are expected. They are gaps in the code, not problems with your setup.
```bash
curl -s -w "\nHTTP:%{http_code}\n" -X POST http://localhost:8000/api/v1/users/login -d '{}'
curl -s -w "\nHTTP:%{http_code}\n" http://localhost:8000/api/v1/notifications -H "Authorization: Bearer $TOKEN"
curl -s -w "\nHTTP:%{http_code}\n" http://localhost:8000/api/v1/users/101 -H "Authorization: Bearer $TOKEN"
```
**Expected:**
```
Method Not Allowed

HTTP:405

HTTP:502
404 page not found
HTTP:404
```
| Route | Result | Why |
|---|---|---|
| `POST /api/v1/users/login` | `405` from user-service | The gateway forwards the request, but user-service only registers `GET /users` and `GET /users/{id}`. There is no login handler (`ai_doc/report.md` §3.1). |
| `GET /api/v1/notifications` | `502` from gateway | notification-service has no HTTP server, so nothing listens on `:8003` (`ai_doc/report.md` §3.2). |
| `GET /api/v1/users/101` | `404` from gateway | The gateway only routes `/users/register`, `/users/login` and `/users/profile`. User lookups aren't exposed through the gateway, so the A-tests use `incurl`. |

---

## Part E — Observability (optional)

### E1. `/metrics` on every HTTP service
```bash
curl -s -o /dev/null -w "api-gateway    HTTP:%{http_code}\n" http://localhost:8000/metrics
incurl -s -o /dev/null -w "user-service   HTTP:%{http_code}\n" http://user-service:8001/metrics
incurl -s -o /dev/null -w "order-service  HTTP:%{http_code}\n" http://order-service:8002/metrics
```
**Expected:**
```
api-gateway    HTTP:200
user-service   HTTP:200
order-service  HTTP:200
```
Backend metrics are only served on `internal-net`. `curl localhost:8001/metrics` from the host should fail (see N1).

### E2. Prometheus scrapes all targets
```bash
docker compose -f docker-compose.yml -f docker-compose.monitoring.yml up -d
# wait ~20s for the first 15s scrape interval, then:
curl -s http://localhost:9090/api/v1/targets | node -e "
let d='';process.stdin.on('data',c=>d+=c).on('end',()=>
  JSON.parse(d).data.activeTargets.forEach(t=>console.log(t.labels.job,'->',t.health,t.lastError||'')))"
```
**Expected:**
```
api-gateway -> up
order-service -> up
prometheus -> up
user-service -> up
```
Prometheus joins `internal-net` (to scrape) and `monitoring-net` (to publish `:9090`). If targets show `down` with `no such host`, Prometheus isn't on `internal-net`. Check with `docker inspect prometheus --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}'`. You can also open `http://localhost:9090/targets`.

### E3. Grafana is reachable
```bash
curl -s -o /dev/null -w "HTTP:%{http_code}\n" http://localhost:3000/login
```
**Expected:** `HTTP:200`. Log in at `http://localhost:3000` with `admin` / `admin`.

### E4. Grafana can reach Prometheus, but NOT the app services
```bash
docker compose -f docker-compose.yml -f docker-compose.monitoring.yml exec -T grafana sh -c '
  wget -q -T 3 -O /dev/null http://prometheus:9090/-/ready;     echo "prometheus exit $?"
  wget -q -T 3 -O /dev/null http://user-service:8001/metrics;  echo "user-service exit $?"'
```
**Expected:**
```
prometheus exit 0
wget: bad address 'user-service:8001'
user-service exit 1
```

---

## Quick pass/fail checklist

**Network**
- [ ] 0.1 → only `api-gateway` and `rabbitmq` show `0.0.0.0:` ports
- [ ] 0.3 → `orders` count is `0` before starting
- [ ] N1 → `localhost:8001` / `8002` fail to connect (exit `7`/`28`)
- [ ] N2 → `localhost:5433` / `6379` / `5672` fail to connect
- [ ] N3 → gateway reaches `user-service`, gets `bad address` for `user-db` and `redis`
- [ ] N4 → `user-service` has no internet (exit `1`)

**user-service**
- [ ] A1 → `200`, Rahim
- [ ] A2 → `200`, Karim
- [ ] A3 → `200`, 3 users
- [ ] A4 → `404`, "User not found"
- [ ] A5 → `404`, "404 page not found"
- [ ] A6 → cache MISS then HIT

**order-service & events**
- [ ] B1 → `201`, `ORD-1010`, Karim
- [ ] B2 → `400`, "user not found or service unavailable"
- [ ] B3 → `400`, "Invalid Body"
- [ ] B4 → `405`, "Only POST allowed"
- [ ] C1 → notification log matches B1
- [ ] C2 → `order_notifications consumers=1 messages=0`

**api-gateway**
- [ ] D1 → `200`, healthy
- [ ] D2 → `401`, no auth header
- [ ] D3 → `401`, garbage token
- [ ] D5 → `201`, `ORD-1008`, order created **through the gateway**
- [ ] D6 → `401`, expired token
- [ ] D7 → `405` / `502` / `404` (known gaps)

**Observability (optional)**
- [ ] E1 → all three `/metrics` return `200`
- [ ] E2 → all four Prometheus targets `up`
- [ ] E3 → Grafana `200`
- [ ] E4 → Grafana reaches Prometheus, not `user-service`

If every box is checked, everything currently implemented works, and the network isolation is in place.
