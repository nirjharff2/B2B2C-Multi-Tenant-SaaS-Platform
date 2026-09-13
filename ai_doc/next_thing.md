# Next Thing — Implement Real Authentication in `user-service`

**Date:** 2026-09-12
**Scope:** One thing. The single highest-value piece of work to do next on this project.
**Builds on:** the compose stack now builds, runs, and proxies correctly (see `ai_doc/report.md`). This document is about what to build on top of that, not about fixing it.

---

## The One Thing

**Implement `POST /users/register`, `POST /users/login`, and `GET|PUT /users/profile` in `user-service`, with password hashing and JWT issuance — making `user-service` the actual token issuer for the platform.**

The API Gateway already routes and protects these exact three paths (`api-gateway/main.go:152-172`). `user-service` implements exactly one handler — `GET /users/{id}` — and nothing anywhere in the repo mints a JWT.

---

## Why It Should Be Done

### 1. It is the keystone: nothing else in the platform is reachable without it

The gateway's protected group covers `/users/profile`, `/orders*`, and `/notifications*` — all of it behind `JWTAuthMiddleware`. That middleware validates tokens, but **nothing in the system creates one**. The result is a platform with a locked front door and no key cut:

- A real client (web app, mobile app, Postman collection, integration test) has no way to authenticate. Not "an awkward way" — no way.
- The only way to exercise the protected surface today is to hand-forge an HS256 token against the shared secret, which is exactly what `ai_doc/run_guide.md` §5 instructs a developer to do with a `node -e` HMAC one-liner. A run guide that tells you to forge credentials is a symptom, not a workflow.
- Every future feature that needs a caller identity — tenant scoping, ownership checks, per-user order history, notification preferences — is blocked behind this. It is not one feature among many; it is the prerequisite for most of the remaining roadmap in `spec/project_details.md`.

### 2. Identity is currently unenforced end-to-end, which is a live security hole

The gateway does the right thing: on a valid token it injects `X-User-ID` and `X-User-Role` for downstream services. But look at what downstream does with them:

```go
// order-service/internal/handler/order_handler.go
var req model.OrderRequest
if err := json.NewDecoder(r.Body).Decode(&req); err != nil { ... }
resp, err := h.service.CreateOrder(r.Context(), req)   // user_id comes from the BODY
```

`order-service` reads `user_id` from the **request body** and ignores `X-User-ID` entirely. So even once a token exists, any authenticated caller can create orders in another user's name by changing a JSON field. The combined current state is: nobody can log in, and anybody who gets in can act as anyone. Real auth is what makes the gateway's header injection mean something — and it forces the downstream fix (trust the header, not the body).

### 3. The spec treats identity as the foundation, and the data model can't support it yet

`spec/project_details.md` names the first service "**User & Auth Service** — Identity, authentication, tenant membership, roles, and permissions", states that "tenant isolation is treated as a fundamental security boundary rather than a simple filtering mechanism", and requires that security-sensitive context be "explicitly established and validated instead of being inferred."

The current `users` table is three columns — `id`, `name`, `email` — with no password, no role, no tenant. There is no `tenant_id` anywhere in the codebase. Every deferred day makes the eventual multi-tenancy migration more expensive, because orders, notifications, and cache keys keep accumulating without a tenant dimension. Adding `role` and `tenant_id` to the identity model *now*, while there are three rows of seed data, is nearly free; adding it after notification and analytics persistence exist is a migration project.

### 4. It is small, self-contained, and unblocks disproportionately

One service, one idempotent schema migration, three handlers, one new dependency (`bcrypt`) plus one already proven in this repo (`golang-jwt/jwt/v5`, used by `api-gateway`). No new infrastructure; no compose changes beyond a few environment variables. Roughly a day of work that removes the blocker from most of the backlog.

---

## How It Should Be Done

### Step 0 — Make `user-service` config environment-driven (prerequisite, ~20 min)

`user-service/internal/config/config.go` hardcodes `postgres://postgres:secret@user-db:5432/userdb` and `redis:6379`. Auth needs a `JWT_SECRET` from the environment anyway, so introduce the `getEnv(key, fallback string)` helper pattern `api-gateway/main.go:27-33` already uses and route `DATABASE_URL`, `REDIS_ADDR`, `JWT_SECRET`, and `PORT` through it. This also closes `report.md` §3.3 for this service as a side effect.

Issuer and validator must agree on the secret, so pass it explicitly in `docker-compose.yml`:

```yaml
  user-service:
    build: ./user-service
    environment:
      - JWT_SECRET=super-secret-key      # same value api-gateway uses
      - DATABASE_URL=postgres://postgres:secret@user-db:5432/userdb?sslmode=disable
      - REDIS_ADDR=redis:6379
```

Note for later: a shared symmetric secret between issuer and validator is acceptable for a local reference stack and deliberately not acceptable in production. Record the follow-up (RS256 + JWKS, so the gateway only ever holds a public key) — it is explicitly **not** part of this work.

### Step 1 — Extend the identity schema

In `config.InitDB()`, evolve the `CREATE TABLE IF NOT EXISTS users` statement and add idempotent `ALTER TABLE` statements so existing volumes migrate cleanly instead of requiring `docker compose down -v`:

```sql
CREATE TABLE IF NOT EXISTS users (
    id            VARCHAR(50) PRIMARY KEY,
    name          VARCHAR(100) NOT NULL,
    email         VARCHAR(100) NOT NULL,
    password_hash TEXT         NOT NULL,
    role          VARCHAR(20)  NOT NULL DEFAULT 'customer',
    tenant_id     VARCHAR(50)  NOT NULL DEFAULT 'default',
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now()
);
ALTER TABLE users ADD COLUMN IF NOT EXISTS password_hash TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS role      VARCHAR(20) NOT NULL DEFAULT 'customer';
ALTER TABLE users ADD COLUMN IF NOT EXISTS tenant_id VARCHAR(50) NOT NULL DEFAULT 'default';
CREATE UNIQUE INDEX IF NOT EXISTS users_tenant_email_uniq ON users (tenant_id, lower(email));
```

The unique index is on `(tenant_id, lower(email))`, not `email` alone — that is the shape the spec's identity model requires ("the same person to interact with multiple businesses"), and getting it right costs nothing today.

Keep the existing seed rows usable by giving them a known bcrypt hash (a documented dev password such as `password123`) so `run_guide.md` can log in as Rahim immediately instead of registering first.

### Step 2 — Model and repository

`user-service/internal/model/user.go`: add `PasswordHash string` tagged `json:"-"`, plus `Role` and `TenantID`. The `json:"-"` tag is not optional — without it the hash leaks through the existing `GET /users/{id}` response *and* into the Redis cache payload written by `UserService.GetUserByID`.

Add explicit request/response types (`RegisterRequest`, `LoginRequest`, `LoginResponse`, `UpdateProfileRequest`) so handlers aren't decoding into anonymous maps.

`user-service/internal/repository/user_repository.go`: add `Create(ctx, *model.User) error`, `GetByEmail(ctx, tenantID, email string) (*model.User, error)`, and `UpdateProfile(ctx, id, name, email string) error`, following the existing `pgxpool` + `QueryRow/Scan` style. Map a `23505` unique-violation `*pgconn.PgError` to a sentinel `ErrEmailTaken` so the handler can answer `409` rather than `500`.

### Step 3 — Service layer: hashing and token issuance

In `user-service/internal/services/user_services.go`, alongside the existing `GetUserByID`:

- `Register(ctx, RegisterRequest) (*model.User, error)` — validate email shape and a minimum password length, `bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)`, generate the ID, insert.
- `Login(ctx, LoginRequest) (string, *model.User, error)` — `GetByEmail`, then `bcrypt.CompareHashAndPassword`. On **either** a missing user or a wrong password, return the *same* generic error, so the endpoint is not an account-enumeration oracle. On success, sign a token.
- `UpdateProfile(ctx, userID string, req UpdateProfileRequest) (*model.User, error)` — and **invalidate the Redis key** `fmt.Sprintf("user:%s", id)` afterwards. `GetUserByID` caches for 10 minutes with no invalidation path today; adding a write endpoint without busting that cache introduces a stale-read bug on day one.

Token claims must match what the gateway's middleware already reads (`user_id`, `role`) plus what the spec needs:

```go
claims := jwt.MapClaims{
    "user_id":   user.ID,
    "role":      user.Role,
    "tenant_id": user.TenantID,
    "iat":       now.Unix(),
    "exp":       now.Add(ttl).Unix(),   // ttl from config, default 1h
}
signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.jwtSecret)
```

Use `github.com/golang-jwt/jwt/v5` — the same library and major version `api-gateway` uses, so the two can't drift on signing-method handling.

### Step 4 — Handlers and routing

`user-service/cmd/server/main.go` currently registers one trailing-slash prefix route (`http.HandleFunc("/users/", ...)`). Two options, in order of preference:

1. **Preferred:** move to Go 1.22+ method-and-pattern routing, fully supported by this module's `go 1.26.3`:
   ```go
   mux.HandleFunc("POST /users/register", userHandler.Register)
   mux.HandleFunc("POST /users/login",    userHandler.Login)
   mux.HandleFunc("GET /users/profile",   userHandler.GetProfile)
   mux.HandleFunc("PUT /users/profile",   userHandler.UpdateProfile)
   mux.HandleFunc("GET /users/{id}",      userHandler.GetUser)
   ```
   This also removes the hand-rolled `strings.TrimPrefix(r.URL.Path, "/users/")` parsing and the latent conflict where `/users/profile` would otherwise be read as a lookup of a user whose ID is literally `profile`.
2. Fallback: keep the single prefix handler and branch on path + method internally. Cheaper diff, worse to extend — and the `profile`-as-an-ID conflict becomes a permanent manual concern.

`GetProfile`/`UpdateProfile` must take the caller's identity from the **`X-User-ID` header the gateway injects** — never from the URL or body — and return `401` when it is absent. That is the spec's "explicit over implicit" principle, and it is the pattern every later service will copy.

Add a `GET /health` endpoint while in here: `docker-compose.yml` gives `user-service` no healthcheck, so `order-service`'s `depends_on: user-service: condition: service_started` currently waits only for the process to exist, not to be ready.

### Step 5 — Close the loop in `order-service`

Change `order-service/internal/handler/order_handler.go` to read `X-User-ID` (and later `X-Tenant-ID`) from the request headers and **ignore any `user_id` in the body**, rejecting with `401` when the header is missing. Without this step the new auth is decorative and §2 above stays true.

### Step 6 — Verify live, the way `report.md` verifies

Run it; don't infer it. The end-to-end check that should pass when this is done:

```bash
docker compose up -d --build

# register
curl -s -X POST http://localhost:8000/api/v1/users/register \
  -H "Content-Type: application/json" \
  -d '{"name":"Nabil","email":"nabil@example.com","password":"password123"}'
# -> 201, body has id/name/email/role, and NO password field

# log in and capture a real token — no more node -e forging
TOKEN=$(curl -s -X POST http://localhost:8000/api/v1/users/login \
  -H "Content-Type: application/json" \
  -d '{"email":"nabil@example.com","password":"password123"}' | jq -r .token)

# protected routes now work with a legitimately issued token
curl -s http://localhost:8000/api/v1/users/profile -H "Authorization: Bearer $TOKEN"
curl -s -X PUT http://localhost:8000/api/v1/users/profile -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" -d '{"name":"Nabil Ahmed"}'
curl -s http://localhost:8000/api/v1/users/profile -H "Authorization: Bearer $TOKEN"
# ^ reflects the update immediately, not a stale 10-minute cache

curl -s -X POST http://localhost:8000/api/v1/orders -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" -d '{"item":"Laptop","amount":999.99}'
# ^ succeeds with NO user_id in the body; identity came from the token
```

Negative cases that must also hold:

| Request | Expected |
|---|---|
| Login, correct email, wrong password | `401`, generic message |
| Login, unknown email | `401`, **identical** message to the line above |
| `GET /api/v1/users/profile` with no token | `401` from the gateway |
| Register with an email already in the tenant | `409`, not `500` |
| `GET /users/101` direct to `:8001` | `200`, and `password_hash` absent from the JSON |

Then update `ai_doc/run_guide.md` §5 to replace the hand-forged-token instructions with the real login flow, and strike §3.1 from the gap list in `ai_doc/report.md`.

### Explicitly not part of this

Refresh tokens and rotation, revocation/blocklists, RS256 + JWKS, rate limiting on `/login`, password reset, email verification, full tenant CRUD and membership tables, and the `notification-service` HTTP surface (`report.md` §3.2). Each is real work; bundling any of them turns a one-day keystone into a two-week epic and delays the unblocking that is the entire point. Land the keystone, then pick the next one.

---

## The Impact It Would Make

| Dimension | Before | After |
|---|---|---|
| **Obtaining a token** | Impossible; the run guide instructs forging one with a `node` HMAC one-liner | `POST /api/v1/users/login` returns a real JWT |
| **Protected surface** (`/orders`, `/notifications`, `/users/profile`) | Unreachable by any legitimate client | Reachable by any authenticated client |
| **Identity enforcement** | `order-service` trusts `user_id` from the body; anyone can act as anyone | Identity flows token → gateway → `X-User-ID` → service, and the body can't override it |
| **Credential storage** | No passwords exist at all | bcrypt hashes, never serialized (`json:"-"`) |
| **Tenant model** | `tenant_id` appears nowhere in the codebase | Present in the identity model and in every token, ready for scoping |
| **Cache correctness** | 10-minute user cache with no invalidation path | Invalidated on profile write |
| **Frontend / client work** | Blocked — nothing to integrate against | Unblocked — a standard register/login/profile flow |
| **Integration tests & CI** | Can only smoke-test unauthenticated direct calls and `/metrics` | Can drive the real gateway flow end to end |
| **Config portability** | `user-service` hardcodes DB and Redis hosts; runs only inside Compose | Env-driven (Step 0), runnable against a local Postgres |
| **Spec alignment** | "User & Auth Service" implements neither auth nor identity | The service starts matching the name the spec gives it |

The compounding effect is the real argument. Every subsequent item on the roadmap — tenant scoping, per-user order history, notification preferences, tenant-scoped analytics, the Super Admin / Tenant Admin / Customer actor model — takes a trustworthy caller identity as its input. Right now each of those would have to invent its own stand-in for identity, and every one of those stand-ins would later have to be torn out. Doing this one thing first means they all get to assume it.

It also changes what the project *is*: from four services that start cleanly, to a platform you can actually log into — the difference between a compose file that comes up green and a system someone else can build against.
