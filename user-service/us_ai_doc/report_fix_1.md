# Fix Report #1 — User Service: "List All Users" Feature

> **Update (follow-up):** The service now has exactly two endpoints,
> `GET /users` and `GET /users/{id}`, using Go's built-in method + path
> patterns. The `/user` alias and the `/users/` → list behavior from Issue 4
> are **gone**. The manual 405 checks (Issue 5) are also gone, because Go's
> router now returns 405 itself. Everything else below still applies.
> Tested against the running Docker stack; see `test.md`.

| Item          | Detail                                                        |
|---------------|---------------------------------------------------------------|
| Service       | `user-service` (Go, port `8001`)                              |
| Date          | 2026-09-13                                                    |
| Scope         | Uncommitted "get all users" work + related error handling     |
| Build status  | `go build ./...` ✅ &nbsp; `go vet ./...` ✅                    |
| Runtime test  | ⚠️ Not run — needs `user-db` (Postgres) and `redis` from Docker Compose |

---

## 1. Summary

The working tree had a half-finished feature to return every user (`GetUsers`)
across all layers (repository → service → handler → route). It compiled, but it
had **7 issues**: a Redis cache that was written but never read, an odd route,
`null` returned for an empty list, every error reported as `404`, and a few
smaller robustness gaps.

All 7 are fixed in 5 files. The API contract did not change for existing
callers: `GET /users/{id}` works the same, and `GET /user` still works.

---

## 2. Architecture Context

```
HTTP request
   │
   ▼
cmd/server/main.go            → route registration
   │
   ▼
internal/handler              → HTTP parsing, status codes, JSON
   │
   ▼
internal/services             → cache-aside logic (Redis, TTL 10 min)
   │                 ┌───────────────┐
   ├────────────────►│ Redis         │  keys: user:{id}, users:all
   ▼                 └───────────────┘
internal/repository           → SQL via pgxpool
   │                 ┌───────────────┐
   └────────────────►│ PostgreSQL    │  table: users(id, name, email)
                     └───────────────┘
```

---

## 3. Issues Found & Fixes Applied

### Issue 1 — Redis cache for the user list was write-only 🔴 High

**File:** `internal/services/user_services.go` — `GetUsers`

**Problem:** The function started at comment `// 2. Cache MISS`. Step 1 (read
the cache) was missing. Every request hit PostgreSQL and then wrote to Redis
again, so the cache added a write and saved nothing. The log always said
`[CACHE MISS]`.

**Before:**
```go
cacheKey := "users:all"

// 2. Cache MISS -> Fetch from DB
fmt.Println("🐢 [CACHE MISS] Fetching all users from PostgreSQL...")
users, err := s.repo.GetUsers(ctx)
```

**After:**
```go
cacheKey := "users:all"

// 1. Redis Cache check
cachedUsers, err := s.redis.Get(ctx, cacheKey).Result()
if err == nil {
    var users []*model.User
    if err := json.Unmarshal([]byte(cachedUsers), &users); err == nil && users != nil {
        fmt.Println("⚡ [CACHE HIT] All users found in Redis!")
        return users, nil
    }
}

// 2. Cache MISS -> Fetch from DB
```

**Why it's correct:** This matches the cache-aside pattern `GetUserByID`
already uses. If Redis is down or the cached data is bad, the code falls
through to the database, so a Redis problem never breaks the endpoint. The
`users != nil` check makes sure a cached `null` counts as a miss.

---

### Issue 2 — Empty user list returned `null` instead of `[]` 🟠 Medium

**File:** `internal/repository/user_repository.go` — `GetUsers`

**Problem:** `var users []*model.User` is a nil slice. With no rows,
`json.Encode` writes `null`. Clients that expect an array (for example
`users.map(...)` in JS) would crash.

**Fix:**
```go
// Non-nil so an empty table encodes as [] instead of null
users := []*model.User{}
```

I also added `ORDER BY id` to the query. Without it, PostgreSQL can return rows
in any order, so responses (and cached copies) could differ between calls.

---

### Issue 3 — All `GetUserByID` errors returned `404 Not Found` 🟠 Medium

**Files:** `internal/repository/user_repository.go`, `internal/handler/user_handler.go`

**Problem:** The handler answered `404 User not found` for **any** error. A
database outage, a timeout, or a cancelled context all looked like "this user
does not exist". That hides real incidents from clients, the API gateway, and
Prometheus error-rate alerts.

**Fix — repository:** add a sentinel error and map `pgx.ErrNoRows` to it:
```go
var ErrUserNotFound = errors.New("user not found")

err := r.DB.QueryRow(ctx, query, id).Scan(&user.ID, &user.Name, &user.Email)
if errors.Is(err, pgx.ErrNoRows) {
    return nil, ErrUserNotFound
}
```

**Fix — handler:** tell the two cases apart:
```go
if errors.Is(err, repository.ErrUserNotFound) {
    writeJSON(w, http.StatusNotFound, map[string]string{"message": "User not found"})
    return
}
if err != nil {
    log.Printf("GetUserByID(%s) failed: %v\n", id, err)
    writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "Failed to fetch user"})
    return
}
```

The service passes the error through without wrapping it, so `errors.Is` works
across the layers.

---

### Issue 4 — Odd list route `/user` (singular) 🟡 Low–Medium

**File:** `cmd/server/main.go`

**Problem:** The list endpoint was only at `GET /user`, while single-user
lookup is at `GET /users/{id}`. REST convention and the existing prefix point
to `GET /users`. Also, `GET /users/` (trailing slash, empty ID) returned
`400 User ID is required` instead of the list.

**Fix:**
```go
http.HandleFunc("/users", userHandler.GetUsers)
http.HandleFunc("/user", userHandler.GetUsers) // kept as an alias for existing callers
http.HandleFunc("/users/", userHandler.GetUserByID)
```
The handler now sends an empty ID (`/users/`) to `GetUsers`, and trims a
trailing `/` from the ID, so `/users/101/` also works.

| Request              | Before                  | After                 |
|----------------------|-------------------------|-----------------------|
| `GET /users`         | 404 (mux default)       | 200 — user list       |
| `GET /users/`        | 400 "ID required"       | 200 — user list       |
| `GET /user`          | 200 — user list         | 200 — user list (alias) |
| `GET /users/101`     | 200                     | 200                   |
| `GET /users/101/`    | 404 (ID = `101/`)       | 200                   |
| `GET /users/999`     | 404                     | 404                   |
| DB down, `/users/101`| 404 (wrong)             | 500                   |

> ⚠️ **Check the API gateway:** If it forwards to user-service with a specific
> path (e.g. `/user`), both forms still work. I could not read gateway code
> outside `user-service/` because of the current read restriction, so please
> check it when you have a moment.

---

### Issue 5 — No HTTP method check 🟡 Low

**File:** `internal/handler/user_handler.go`

**Problem:** `POST`, `PUT`, and `DELETE` to these read-only endpoints were
treated like `GET`.

**Fix:** Both handlers now return `405 Method Not Allowed` with an
`Allow: GET` header for any method other than GET.

---

### Issue 6 — Errors from the list endpoint were never logged 🟡 Low

**File:** `internal/handler/user_handler.go`

**Problem:** `GetUsers` sent a generic 500 response and threw away the real
error, so there was nothing in the container logs to debug with.

**Fix:** `log.Printf("GetUsers failed: %v\n", err)` before responding. The
client still gets the generic message, so internal details are not exposed.

I also pulled the repeated "set header → write status → encode" code into one
`writeJSON` helper. `GetUserByID` now sets `200 OK` explicitly, like
`GetUsers`.

---

### Issue 7 — Seed query error silently ignored 🟡 Low

**File:** `internal/config/config.go`

**Problem:** The return value of `dbPool.Exec(seedQuery)` was thrown away. If
seeding failed (for example, the new user `103 / Babe` in this change), nothing
was logged.

**Fix:**
```go
if _, err = dbPool.Exec(context.Background(), seedQuery); err != nil {
    log.Printf("Failed to seed users: %v\n", err)
}
```
This logs instead of calling `log.Fatal`, because seed data is not needed for
the service to start.

---

## 4. Files Changed

| File                                      | Change                                                   |
|-------------------------------------------|----------------------------------------------------------|
| `cmd/server/main.go`                      | Added `/users` route; kept `/user` alias                 |
| `internal/handler/user_handler.go`        | Method checks, 404 vs 500, error logging, `writeJSON` helper, `/users/` → list |
| `internal/services/user_services.go`      | Added missing Redis cache read in `GetUsers`             |
| `internal/repository/user_repository.go`  | `ErrUserNotFound`, `pgx.ErrNoRows` mapping, non-nil slice, `ORDER BY id` |
| `internal/config/config.go`               | Seed query error now logged                              |

---

## 5. Verification

### 5.1 Done
```
go build ./...   → OK
go vet ./...     → OK
```

### 5.2 To do — runtime checks (need Docker stack)
```bash
docker compose up --build user-service

# List (first call = MISS, second = HIT in logs)
curl -i http://localhost:8001/users
curl -i http://localhost:8001/users
curl -i http://localhost:8001/user            # alias

# Single user
curl -i http://localhost:8001/users/103       # expect 200, "Babe"
curl -i http://localhost:8001/users/999       # expect 404

# Method check
curl -i -X POST http://localhost:8001/users   # expect 405, Allow: GET
```
Expected logs:
```
🐢 [CACHE MISS] Fetching all users from PostgreSQL...
💾 All users saved to Redis Cache (Valid for 10 mins)
⚡ [CACHE HIT] All users found in Redis!
```

> **Note on existing cache data:** A `users:all` key written by the old code is
> still valid JSON and will be served until its 10-minute TTL ends. To start
> clean: `docker compose exec redis redis-cli DEL users:all`.

---

## 6. Known Limitations / Recommended Next Steps

1. **No cache invalidation.** The service has no create/update/delete endpoints
   yet, so a 10-minute TTL is fine. Once writes are added, they **must** delete
   `users:all` and `user:{id}`, or clients will see stale data for up to
   10 minutes.
2. **No pagination.** `SELECT ... FROM users` returns every row, and the whole
   list is cached as one Redis value. Add `?limit=&offset=` (or cursor-based
   paging) before the table grows.
3. **Hardcoded config.** The DB connection string (including password `secret`)
   and the Redis address are hardcoded in `config.go`. Move them to environment
   variables.
4. **No tests.** Put interfaces in front of the repository and the Redis client
   so the handler/service can be unit-tested with `httptest` and fakes.
5. **Formatting.** `gofmt -l` lists several files, including ones this fix did
   not touch (e.g. `internal/model/user.go`). This is most likely Windows CRLF
   line endings. Consider `gofmt -w .` plus a `.gitattributes` rule
   (`*.go text eol=lf`).
