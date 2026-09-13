# Run Guide — Go Microservices Platform

Step-by-step instructions to run this project locally. Every command below was actually executed against this repo (post-fix) on Windows with Docker Desktop — see `ai_doc/report.md` for what was broken and what was fixed to get here.

---

## 1. Prerequisites

- **Docker Desktop** running (the daemon must be up — on Windows, launch the app and wait for the whale icon to say "running").
- That's it for the core stack — Go doesn't need to be installed locally since everything builds inside Docker. (Go 1.26+ is only needed if you want to build/run a service outside Docker.)
- A terminal that can run `curl`. On Windows, PowerShell's `curl` is aliased to `Invoke-WebRequest`; the examples below assume a POSIX shell (Git Bash, WSL) — swap in `Invoke-RestMethod` if you're in native PowerShell.

Check Docker is actually running before continuing:
```bash
docker info
```
If this errors with something like `failed to connect to the docker API`, start Docker Desktop and wait ~30-60s before retrying.

---

## 2. Start the core stack

From the repo root:
```bash
docker compose up -d --build
```

This builds and starts 8 containers:

| Container | Purpose | Port |
|---|---|---|
| `user-db` | Postgres for user-service | 5432 |
| `order-db` | Postgres for order-service | 5433 |
| `redis` | Cache for user-service | 6379 |
| `rabbitmq` | Event bus (+ management UI) | 5672, 15672 |
| `go-microservices-user-service-1` | User lookup + Redis cache | 8001 |
| `go-microservices-order-service-1` | Order creation + event publish | 8002 |
| `go-microservices-notification-service-1` | RabbitMQ consumer (console notifications) | — (no port) |
| `api-gateway` | Reverse proxy + JWT auth | 8000 |

The first build takes a few minutes (pulling Postgres/Redis/RabbitMQ images and compiling 4 Go binaries). Watch it come up:
```bash
docker compose ps
```
Wait until `user-db`, `order-db`, and `rabbitmq` show `(healthy)` — the Go services depend on that and will otherwise crash-loop.

---

## 3. Sanity-check the containers are healthy

```bash
docker compose ps --format "table {{.Name}}\t{{.Status}}"
```
All 8 rows should show `Up` (three of them `Up ... (healthy)`).

If something isn't running, check its logs, e.g.:
```bash
docker compose logs user-service --tail=50
```

---

## 4. Exercise the working flow directly (bypassing the gateway)

This is the simplest way to confirm the core business logic works, and doesn't require a JWT.

**Look up a seeded user** (the DB is seeded with users `101` "Rahim" and `102` "Karim" on first boot):
```bash
curl http://localhost:8001/users/101
# {"id":"101","name":"Rahim","email":"rahim@example.com"}
```

**Create an order** (this calls `user-service` internally to validate the user, saves to `order-db`, and publishes an event to RabbitMQ):
```bash
curl -X POST http://localhost:8002/orders -H "Content-Type: application/json" -d '{"user_id":"101","item":"Laptop","amount":999.99}'
# {"order_id":"ORD-1006","customer_name":"Rahim","item":"Laptop","amount":999.99,"status":"CONFIRMED"}
```

**Confirm the notification was consumed:**
```bash
docker compose logs notification-service --tail=10
```
You should see:
```
📧 [NOTIFICATION SENT] Customer: Rahim (rahim@example.com)
   Dear Rahim, your order ORD-1006 for 'Laptop' (Amount: $999.99) is CONFIRMED!
```

---

## 5. Exercise the flow through the API Gateway (JWT-protected)

The gateway requires a valid JWT on protected routes, but **`user-service` has no login/register endpoint yet** (see `ai_doc/report.md` §3.1) — there's no legitimate way to obtain a token. To still test the gateway's proxying and auth middleware, hand-craft a token signed with the same secret the gateway uses (`JWT_SECRET=super-secret-key`, set in `docker-compose.yml`):

```bash
node -e "
const crypto = require('crypto');
function b64url(obj){return Buffer.from(JSON.stringify(obj)).toString('base64').replace(/\+/g,'-').replace(/\//g,'_').replace(/=+$/,'');}
const header = b64url({alg:'HS256',typ:'JWT'});
const payload = b64url({user_id:'101',role:'customer',exp: Math.floor(Date.now()/1000)+3600});
const data = header+'.'+payload;
const sig = crypto.createHmac('sha256','super-secret-key').update(data).digest('base64').replace(/\+/g,'-').replace(/\//g,'_').replace(/=+$/,'');
console.log(data+'.'+sig);
"
```
Copy the printed token into `TOKEN` below (valid for 1 hour):

```bash
TOKEN="<paste the token here>"

# Gateway health check (no auth needed)
curl http://localhost:8000/health

# Create an order through the gateway (auth required)
curl -X POST http://localhost:8000/api/v1/orders \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"user_id":"101","item":"Phone","amount":499.5}'
# {"order_id":"ORD-1005","customer_name":"Rahim","item":"Phone","amount":499.5,"status":"CONFIRMED"}
```

Routes that will **not** work yet, by design gap (not a bug in your setup):
- `POST /api/v1/users/register`, `POST /api/v1/users/login`, `GET|PUT /api/v1/users/profile` — no handlers exist in `user-service` for these.
- Anything under `/api/v1/notifications` — `notification-service` has no HTTP server (it's a RabbitMQ consumer only).

---

## 6. RabbitMQ management UI (optional)

```
http://localhost:15672
```
Login: `guest` / `guest`. Look at the `order_notifications` queue to watch messages flow when you create orders.

---

## 7. Monitoring stack (Prometheus + Grafana) — optional

The main `docker-compose.yml` and the monitoring stack are separate files by design; pass both together so they land on the same network:

```bash
docker compose -f docker-compose.yml -f docker-compose.monitoring.yml up -d --build
```

This adds:
| Container | Purpose | Port |
|---|---|---|
| `prometheus` | Scrapes `/metrics` from api-gateway, user-service, order-service every 15s | 9090 |
| `grafana` | Dashboards | 3000 (login: `admin` / `admin`) |

Check Prometheus sees all targets as up:
```
http://localhost:9090/targets
```
You should see `api-gateway`, `user-service`, `order-service`, and `prometheus` itself, all green/`UP`.

---

## 8. Stopping everything

Stop and remove the core stack:
```bash
docker compose down
```

If you also started monitoring, tear both down together (otherwise the monitoring containers will keep referencing a removed network):
```bash
docker compose -f docker-compose.yml -f docker-compose.monitoring.yml down
```

Add `-v` to also delete the Postgres/Grafana volumes (fresh database and dashboards on next start):
```bash
docker compose down -v
```

---

## 9. Rebuilding after a code change

Compose caches image layers, so a plain rebuild is fast unless you changed `go.mod`/`go.sum`:
```bash
docker compose up -d --build
```
To force a completely clean rebuild of one service (e.g. after editing `order-service`):
```bash
docker compose build --no-cache order-service
docker compose up -d order-service
```

---

## 10. Running a single service outside Docker (advanced)

Not recommended day-to-day (all four services hardcode Docker Compose service names like `user-db`, `redis`, `rabbitmq` as hostnames — see `ai_doc/report.md` §3.3), but if you need to, e.g., debug `user-service` with a local debugger while its dependencies stay in Docker:

1. Start everything except the service you're debugging: `docker compose up -d user-db redis rabbitmq order-db`
2. Add entries to your hosts file mapping `user-db`, `redis`, `rabbitmq`, `order-db` to `127.0.0.1` (Windows: `C:\Windows\System32\drivers\etc\hosts`, requires admin).
3. `cd user-service && go run ./cmd/server`

This only works because Docker Compose publishes each infra container's port to `localhost` already (`5432`, `6379`, `5672`, `5433`).
