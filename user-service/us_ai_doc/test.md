# User Service — Test Guide

The user-service has two endpoints:

| Method | Path          | Description             | Success | Errors                    |
|--------|---------------|-------------------------|---------|---------------------------|
| GET    | `/users`      | List all users          | `200`   | `500` DB failure          |
| GET    | `/users/{id}` | Get a user by ID        | `200`   | `404` not found, `500` DB failure |

Any other method (POST, PUT, DELETE…) on these paths returns `405 Method Not Allowed`.
Any other path returns `404 page not found`.

Service port: **8001** (direct). Redis cache TTL: **10 minutes**.

---

## 1. Prerequisites

- Docker Desktop running
- `curl` (Git Bash, WSL, or PowerShell's `curl.exe`)
- Optional: `jq` for pretty JSON

---

## 2. Start the Stack

Run from the project root (`go-microservices/`):

```bash
docker compose up -d --build user-db redis user-service
```

Check it started:

```bash
docker compose ps
docker compose logs user-service --tail 5
```

Expected logs:
```
User Service: PostgreSQL Connected!
User Service: Redis Cache Connected!
User Service 8001 পোর্টে চালু হচ্ছে...
```

> **Code changed?** Always rebuild with `--build`. Without it, Docker runs the
> old image and your changes won't show up. This is the most common reason
> something "doesn't work".

---

## 3. Seed Data

Created automatically on startup:

| id  | name  | email             |
|-----|-------|-------------------|
| 101 | Rahim | rahim@example.com |
| 102 | Karim | karim@example.com |
| 103 | Babe  | babe@example.com  |

---

## 4. Test Cases

Open a second terminal with `docker compose logs -f user-service` to watch the
cache logs while you test.

### TC-1 — List all users
```bash
curl -i http://localhost:8001/users
```
**Expect:** `200 OK`, `Content-Type: application/json`
```json
[{"id":"101","name":"Rahim","email":"rahim@example.com"},{"id":"102","name":"Karim","email":"karim@example.com"},{"id":"103","name":"Babe","email":"babe@example.com"}]
```
Log (first call):
```
🐢 [CACHE MISS] Fetching all users from PostgreSQL...
💾 All users saved to Redis Cache (Valid for 10 mins)
```

### TC-2 — List all users, served from cache
Run the same request again:
```bash
curl -i http://localhost:8001/users
```
**Expect:** same `200` body. Log:
```
⚡ [CACHE HIT] All users found in Redis!
```

### TC-3 — Get one user
```bash
curl -i http://localhost:8001/users/101
```
**Expect:** `200 OK`
```json
{"id":"101","name":"Rahim","email":"rahim@example.com"}
```
Log: `[CACHE MISS]` on the first call, `[CACHE HIT]` on the next.

### TC-4 — User does not exist
```bash
curl -i http://localhost:8001/users/999
```
**Expect:** `404 Not Found`
```json
{"message":"User not found"}
```

### TC-5 — Wrong HTTP method
```bash
curl -i -X POST http://localhost:8001/users
curl -i -X DELETE http://localhost:8001/users/101
```
**Expect:** `405 Method Not Allowed`, header `Allow: GET, HEAD`

### TC-6 — Unknown path
```bash
curl -i http://localhost:8001/users/
curl -i http://localhost:8001/user
```
**Expect:** `404 page not found` (only `/users` and `/users/{id}` exist)

### TC-7 — Database down → 500 (not 404)
Clear the cache first, or Redis will still serve the data:
```bash
docker compose exec redis redis-cli FLUSHALL
docker compose stop user-db

curl -i http://localhost:8001/users        # expect 500 {"message":"Failed to fetch users"}
curl -i http://localhost:8001/users/102    # expect 500 {"message":"Failed to fetch user"}

docker compose start user-db
```
The service log should show the real error, for example `GetUsers failed: ...`.

### TC-8 — Redis down → still works (no cache)
```bash
docker compose stop redis
curl -i http://localhost:8001/users        # expect 200, logs show CACHE MISS + "Failed to cache"
docker compose start redis
```

---

## 5. Quick Smoke Test (all at once)

**Git Bash / WSL:**
```bash
for p in /users /users/101 /users/999; do
  printf "GET %-12s -> " "$p"
  curl -s -o /dev/null -w "%{http_code}\n" "http://localhost:8001$p"
done
printf "POST /users       -> "; curl -s -o /dev/null -w "%{http_code}\n" -X POST http://localhost:8001/users
```

**PowerShell:**
```powershell
foreach ($p in "/users", "/users/101", "/users/999") {
  $code = curl.exe -s -o NUL -w "%{http_code}" "http://localhost:8001$p"
  "GET $p -> $code"
}
```

Expected:
```
GET /users       -> 200
GET /users/101   -> 200
GET /users/999   -> 404
POST /users      -> 405
```

---

## 6. Inspect Cache & Database

```bash
# Redis
docker compose exec redis redis-cli KEYS 'user*'
docker compose exec redis redis-cli GET users:all
docker compose exec redis redis-cli TTL users:all
docker compose exec redis redis-cli DEL users:all user:101   # force a cache miss

# PostgreSQL
docker compose exec user-db psql -U postgres -d userdb -c "SELECT * FROM users ORDER BY id;"
```

> If you add or edit rows directly in the DB, the API returns the old cached
> data until the TTL ends (10 min) or you `DEL` the key.

---

## 7. Via the API Gateway (port 8000) — ⚠️ not routed yet

Tested on 2026-09-13: the gateway only registers `GET /api/v1/users/profile`
for users. So these currently return **404 from the gateway itself** and never
reach user-service:

```bash
curl -i http://localhost:8000/api/v1/users        # 404 (gateway)
curl -i http://localhost:8000/api/v1/users/102    # 404 (gateway)
```

To make them work, `api-gateway` needs `/api/v1/users` and `/api/v1/users/*path`
routes that forward to `http://user-service:8001/users[/{id}]`. Those routes
also go through the gateway's JWT middleware, so send
`Authorization: Bearer <token>`.

---

## 8. Troubleshooting

| Symptom                                   | Cause / Fix |
|-------------------------------------------|-------------|
| Old behavior after code change            | Rebuild: `docker compose up -d --build user-service` |
| `404 page not found` (plain text)         | Wrong path — use `/users` or `/users/{id}` (no trailing slash) |
| `{"message":"User not found"}`            | ID doesn't exist in DB |
| `curl: (7) Failed to connect`             | Container not running — `docker compose ps`, check logs |
| Service exits at startup                  | `user-db`/`redis` not ready — `docker compose logs user-service` |
| Data doesn't match DB                     | Stale Redis cache — `redis-cli DEL users:all user:<id>` |
| 404 on port 8000                          | Gateway has no route — see section 7 |

---

## 9. Cleanup

```bash
docker compose down        # stop containers
docker compose down -v     # also delete DB volumes (seed data is recreated on next start)
```
