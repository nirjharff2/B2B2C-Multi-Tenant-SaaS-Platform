# API Test Guide — Go Microservices Platform

A copy-pasteable set of `curl` commands to verify every endpoint in this project is actually working, plus the expected response for each. Every command below was run live against this stack (post-fix, see `ai_doc/report.md`) and the "Expected" blocks are the **actual real output**, not guesses.

Run these in order — some tests depend on state created by earlier ones (e.g. the seeded users).

---

## 0. Prerequisites

```bash
docker compose up -d --build
docker compose ps --format "table {{.Name}}\t{{.Status}}"
```
All containers should show `Up` (three as `Up ... (healthy)`) before continuing. See `ai_doc/run_guide.md` if anything isn't healthy.

---

## Part A — `user-service` (direct, port 8001)

### A1. Get an existing (seeded) user
```bash
curl -s -w "\nHTTP:%{http_code}\n" http://localhost:8001/users/101
```
**Expected:**
```json
{"id":"101","name":"Rahim","email":"rahim@example.com"}
HTTP:200
```

### A2. Get the other seeded user
```bash
curl -s -w "\nHTTP:%{http_code}\n" http://localhost:8001/users/102
```
**Expected:**
```json
{"id":"102","name":"Karim","email":"karim@example.com"}
HTTP:200
```
Confirms Postgres seeding *and* that both rows are independently reachable (not a fluke/cache artifact).

### A3. Get a user that doesn't exist
```bash
curl -s -w "\nHTTP:%{http_code}\n" http://localhost:8001/users/999
```
**Expected:**
```json
{"message":"User not found"}
HTTP:404
```

### A4. Missing user ID
```bash
curl -s -w "\nHTTP:%{http_code}\n" http://localhost:8001/users/
```
**Expected:**
```json
{"message":"User ID is required"}
HTTP:400
```

### A5. Redis cache is actually being used
```bash
docker compose logs user-service --tail=20
```
Request `101` twice (A1 again) and check the logs — the **first** request for a given ID should log a cache miss + a "saved to cache" line, and the **second** should log a cache hit:
```
🐢 [CACHE MISS] Fetching User 101 from PostgreSQL...
💾 User 101 saved to Redis Cache (Valid for 10 mins)
⚡ [CACHE HIT] User 101 found in Redis!
```

---

## Part B — `order-service` (direct, port 8002)

### B1. Create an order for a valid user
```bash
curl -s -w "\nHTTP:%{http_code}\n" -X POST http://localhost:8002/orders \
  -H "Content-Type: application/json" \
  -d '{"user_id":"102","item":"Headphones","amount":149.99}'
```
**Expected (order_id counter will differ per run):**
```json
{"order_id":"ORD-1010","customer_name":"Karim","item":"Headphones","amount":149.99,"status":"CONFIRMED"}
HTTP:201
```
Confirms order-service successfully called `user-service` internally to resolve the customer name, then persisted to `order-db`.

### B2. Create an order for a user that doesn't exist
```bash
curl -s -w "\nHTTP:%{http_code}\n" -X POST http://localhost:8002/orders \
  -H "Content-Type: application/json" \
  -d '{"user_id":"999","item":"Ghost Item","amount":10}'
```
**Expected:**
```
user not found or service unavailable
HTTP:400
```
Confirms the cross-service validation call actually rejects bad user IDs instead of silently creating orphan orders.

### B3. Malformed JSON body
```bash
curl -s -w "\nHTTP:%{http_code}\n" -X POST http://localhost:8002/orders \
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
curl -s -w "\nHTTP:%{http_code}\n" http://localhost:8002/orders
```
**Expected:**
```
Only POST allowed
HTTP:405
```

---

## Part C — End-to-end event flow (RabbitMQ → `notification-service`)

### C1. Confirm the order from B1 was consumed and "notified"
```bash
docker compose logs notification-service --tail=10
```
**Expected** — a block matching the order you just created:
```
📧 [NOTIFICATION SENT] Customer: Karim (karim@example.com)
   Dear Karim, your order ORD-1010 for 'Headphones' (Amount: $149.99) is CONFIRMED!
==================================================
```
If you don't see this, RabbitMQ delivery is broken — check `docker compose logs rabbitmq` and `docker compose logs order-service` for publish errors.

### C2. (Optional) Watch the queue directly in the RabbitMQ UI
```
http://localhost:15672   (login: guest / guest)
```
Or via API:
```bash
curl -s -u guest:guest http://localhost:15672/api/overview -o /dev/null -w "HTTP:%{http_code}\n"
```
**Expected:** `HTTP:200`. Then check **Queues → `order_notifications`** in the UI — message rates should tick up each time you POST an order.

---

## Part D — `api-gateway` (port 8000)

### D1. Gateway health check (no auth)
```bash
curl -s -w "\nHTTP:%{http_code}\n" http://localhost:8000/health
```
**Expected:**
```json
{"status":"API Gateway Healthy"}
HTTP:200
```

### D2. Protected route with no token — must be rejected
```bash
curl -s -w "\nHTTP:%{http_code}\n" -X POST http://localhost:8000/api/v1/orders \
  -H "Content-Type: application/json" \
  -d '{"user_id":"101","item":"X","amount":1}'
```
**Expected:**
```json
{"error":"Authorization header required"}
HTTP:401
```

### D3. Protected route with a garbage token — must be rejected
```bash
curl -s -w "\nHTTP:%{http_code}\n" -X POST http://localhost:8000/api/v1/orders \
  -H "Authorization: Bearer not-a-real-token" \
  -H "Content-Type: application/json" \
  -d '{"user_id":"101","item":"X","amount":1}'
```
**Expected:**
```json
{"error":"Invalid or expired token"}
HTTP:401
```

### D4. Generate a valid test JWT

There is currently no `/login` endpoint in this project (see `ai_doc/report.md` §3.1), so to test JWT-protected routes you sign a token yourself using the same secret the gateway uses (`JWT_SECRET=super-secret-key`, from `docker-compose.yml`):

```bash
TOKEN=$(node -e "
const crypto = require('crypto');
function b64url(obj){return Buffer.from(JSON.stringify(obj)).toString('base64').replace(/\+/g,'-').replace(/\//g,'_').replace(/=+\$/,'');}
const header = b64url({alg:'HS256',typ:'JWT'});
const payload = b64url({user_id:'101',role:'customer',exp: Math.floor(Date.now()/1000)+3600});
const data = header+'.'+payload;
const sig = crypto.createHmac('sha256','super-secret-key').update(data).digest('base64').replace(/\+/g,'-').replace(/\//g,'_').replace(/=+\$/,'');
console.log(data+'.'+sig);
")
echo $TOKEN
```
Valid for 1 hour from generation.

### D5. Protected route with a valid token — should succeed end-to-end
```bash
curl -s -w "\nHTTP:%{http_code}\n" -X POST http://localhost:8000/api/v1/orders \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"user_id":"101","item":"Keyboard","amount":79.5}'
```
**Expected:**
```json
{"order_id":"ORD-1008","customer_name":"Rahim","item":"Keyboard","amount":79.5,"status":"CONFIRMED"}
HTTP:201
```
This is the most important gateway test: it proves the JWT middleware, the reverse proxy, and the `/api/v1` → downstream path rewrite are all wired correctly (this exact call used to 404 before the routing fix in `ai_doc/report.md` §2.1).

### D6. Routes that are known NOT to work yet (expected failures, not bugs in your setup)
```bash
curl -s -w "\nHTTP:%{http_code}\n" -X POST http://localhost:8000/api/v1/users/login -d '{}'
curl -s -w "\nHTTP:%{http_code}\n" -X GET http://localhost:8000/api/v1/notifications -H "Authorization: Bearer $TOKEN"
```
**Expected (verified):**
```
{"message":"User not found"}
HTTP:404

HTTP:502
```
- `/api/v1/users/login` reaches `user-service` but there is no login handler there, so it falls through to the generic `/users/{id}` lookup handler, treats `login` as the ID, and returns `404`.
- `/api/v1/notifications` gets a `502` because `notification-service` has no HTTP server at all — the gateway's reverse proxy can't even connect.

Both are documented gaps (`ai_doc/report.md` §3.1, §3.2), not something to debug in your environment.

---

## Part E — Observability

### E1. Metrics endpoints exist on all HTTP services
```bash
for p in 8000 8001 8002; do echo ":$p/metrics ->"; curl -s -o /dev/null -w "HTTP:%{http_code}\n" http://localhost:$p/metrics; done
```
**Expected:**
```
:8000/metrics ->
HTTP:200
:8001/metrics ->
HTTP:200
:8002/metrics ->
HTTP:200
```

### E2. Prometheus is actually scraping them
```bash
docker compose -f docker-compose.yml -f docker-compose.monitoring.yml up -d --build
# wait ~15-20s for the first scrape interval, then:
curl -s "http://localhost:9090/api/v1/targets" | node -e "
let data='';process.stdin.on('data',d=>data+=d);
process.stdin.on('end',()=>{const j=JSON.parse(data);j.data.activeTargets.forEach(t=>console.log(t.labels.job,'->',t.health));});
"
```
**Expected:**
```
api-gateway -> up
order-service -> up
prometheus -> up
user-service -> up
```
You can also just open `http://localhost:9090/targets` in a browser.

### E3. Grafana is reachable
```bash
curl -s -o /dev/null -w "HTTP:%{http_code}\n" http://localhost:3000/login
```
**Expected:** `HTTP:200`. Login at `http://localhost:3000` with `admin` / `admin`.

---

## Quick pass/fail checklist

Run through these in sequence; every line should match:

- [ ] A1 → `200`, returns Rahim
- [ ] A2 → `200`, returns Karim
- [ ] A3 → `404`, "User not found"
- [ ] A4 → `400`, "User ID is required"
- [ ] B1 → `201`, order created with correct customer name
- [ ] B2 → `400`, "user not found or service unavailable"
- [ ] B3 → `400`, "Invalid Body"
- [ ] B4 → `405`, "Only POST allowed"
- [ ] C1 → notification-service logs show the matching order
- [ ] D1 → `200`, gateway healthy
- [ ] D2 → `401`, no auth header
- [ ] D3 → `401`, invalid token
- [ ] D5 → `201`, order created **through the gateway**
- [ ] E1 → all three `/metrics` return `200`
- [ ] E2 → all four Prometheus targets report `up`

If every box checks out, the stack is fully functional for everything currently implemented in this codebase.
