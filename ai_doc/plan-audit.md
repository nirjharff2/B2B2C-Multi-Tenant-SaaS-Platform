# Plan Audit — Getting to a Secure, Self-Driving Pipeline

**Date:** 2026-09-14
**Goal:** You and a subordinate should eventually be able to just write code, push it, and trust that GitHub Actions will test it, build it, scan it, deploy it to somewhere real users can hit, and tell you (via Grafana/alerts) the moment something breaks — without either of you touching pipeline config again.
**Audience:** You (owns platform/pipeline/security) + one subordinate (owns internal service business logic). Written so both of you can read it without translating jargon.
**Builds on:** `ai_doc/audit_day_1.md` (reliability/correctness findings) and `ai_doc/network_issues.md` + `ai_doc/fixed_network.md` (security findings). This document doesn't repeat those findings — it tells you what order to fix them in and what to build around them.

---

## 1. The honest starting point

Right now you have:
- Four services that build and talk to each other locally via Docker Compose.
- A CI workflow that starts the stack, curls `/metrics`, and shuts it down. It does not run real tests (there aren't any yet), does not build a deployable image, and does not deploy anywhere.
- A security audit with a clear list of what's insecure (secrets in the repo, containers running as root, open ports) but most of it not yet fixed.
- An empty `terraform/terraform.yml` — no infrastructure actually exists outside your laptop's Docker Desktop.
- A plan for the next business-logic feature (`next_thing.md`) that a subordinate can pick up independently.

So "production-grade pipeline" today means: **nothing auto-deploys, nothing is scanned, and the secrets that exist would need to be rotated before this touches the internet.** That's normal for this stage — the point of this document is the path from here to there, not a judgment.

---

## 2. What "done" actually looks like

Concretely, when this is finished, your day-to-day should be:

1. You or your subordinate write code on a branch, open a PR.
2. GitHub Actions automatically: runs `go vet`/lint, runs unit tests, builds every service's Docker image, scans those images for known vulnerabilities, and reports pass/fail directly on the PR.
3. A PR can't merge to `main` unless all of that is green (branch protection enforces this — nobody can bypass it by accident, including you).
4. Merging to `main` automatically: builds tagged, versioned images, pushes them to a registry, and deploys them to production.
5. If the deploy fails a health check, it doesn't finish rolling out (no manual "did it work?" step).
6. Grafana shows live dashboards (request rates, error rates, latency, resource usage) and Alertmanager (or equivalent) pings you — Slack/email/whatever you wire up — if something is actually wrong, without you needing to go look.
7. Your subordinate never has to think about any of this. They write handler/service/repository code, push, and the pipeline is just... there, the same way `go build` is just there.

Everything below is the sequence of phases to get from §1 to this.

---

## 3. Ground rules while a subordinate is actively coding

Because someone else is in this codebase at the same time you're changing the pipeline:

- **Don't touch business-logic files while doing pipeline work**, and vice versa — ask them to avoid `docker-compose.yml`, `Dockerfile`s, `.github/workflows/`, and any new `ci/`/`deploy/` folders unless you're pairing on it. Those are the files most likely to conflict.
- **Land infrastructure changes in small, separate PRs** from feature work, so a broken pipeline change never blocks their unrelated feature PR (and vice versa).
- **Turn on branch protection early, but don't make it strict immediately.** Add required checks (build, lint) as soon as they exist and are reliable; add "tests must pass" as a required check only once there are real tests to require — otherwise you'll block your subordinate on a check that can never turn green.
- **Give them a fast local loop that matches CI.** If CI runs `go vet ./... && go build ./... && go test ./...`, make sure that's also exactly what running it locally does, so they're never surprised by a CI failure they can't reproduce.
- **They don't need production credentials.** Only you (or a small number of people) should hold deploy secrets. Their PRs trigger the pipeline; they don't need to be able to run it manually against production.

---

## 4. The phased roadmap

Each phase is scoped so it can be one PR (or a small handful), is independently useful even if you stop after it, and doesn't require the next phase to already exist.

### Phase 0 — Guardrails (do this first, before touching anything else)  -   DONEEEEEEEEEEEEEEEE....IT'S DONE!

**Why first:** everything after this assumes a repo where "merge to main" is a meaningful, protected gate. If that's not true yet, none of the automation you build later actually protects anything.

- Turn on GitHub branch protection on `main`: require a PR (no direct pushes), require at least one review, require status checks to pass once they exist.
- Set up **GitHub Environments** (`staging`, `production`) now, even before you have a deploy target — this is where deploy secrets will live later, scoped so only workflows targeting that environment can read them, and you can require manual approval on `production` deploys if you want a human in the loop initially.
- Decide your branch model now, in one sentence, and write it in the README: e.g. "feature branches → PR → `main` → auto-deploys to production." Simple beats clever here.

### Phase 1 — Get secrets out of the repository

**Why before CI/CD:** you don't want a pipeline that automatically deploys code containing a hardcoded JWT secret and DB password to the internet, faster than you used to be able to do it manually. This has to happen before Phase 4/5, not after.

- Move every hardcoded value currently in `docker-compose.yml` (Postgres passwords, `JWT_SECRET`, RabbitMQ creds) into environment variables sourced from a git-ignored `.env` for local dev, and **GitHub Actions Secrets** (repo or environment-scoped) for CI/CD.
- Generate new, real random secrets — the current ones (`secret`, `super-secret-key`, `guest`) are already committed to git history, so they must be treated as burned even after removed from the working tree. Rotate them.
- Remove the `JWT_SECRET` fallback default in `api-gateway/main.go` so the gateway refuses to start without one being explicitly provided — a missing secret should be a loud startup failure, not a silent security hole.
- This is a good task to split: you handle rotating/wiring the secrets themselves; your subordinate doesn't need to be involved unless a service they own reads one of these values, in which case just tell them the env var name to expect.

This phase is fully specified already in `network_issues.md` findings N-05/N-06 — use that document's fix list, this section is just telling you *when* to do it.

### Phase 2 — Harden the containers you're about to ship

**Why before shipping anywhere real:** once Phase 5 exists, whatever's in these Dockerfiles goes to production automatically. Fix them before that's true, not after.

- Add a non-root `USER` to every Dockerfile (currently all four run as root).
- Pin base images to specific versions (ideally with a digest), not `latest`/floating tags.
- Add a `.dockerignore` per service so local `.env` files, `.git`, and build artifacts never enter a Docker build context.
- Add `HEALTHCHECK`s so the orchestrator (whichever you pick in Phase 5) can tell a container is actually ready, not just running.

This is `network_issues.md` §4 (D-01 through D-06) — same relationship as Phase 1: already documented, this just says do it now, before Phase 4 starts building these images for real.

### Phase 3 — Make CI actually test something

**Why before CD:** deploying automatically is only safe if something automated actually checks the code first. Right now nothing does.

- Add a real CI job matrix (or simple sequential steps) that, for each service: `go vet ./...`, `go build ./...`, `go test ./...`. Empty test suites are fine at first — the job should still exist and be a required check, so tests your subordinate adds later are automatically enforced with zero pipeline changes.
- Add `golangci-lint` (or similar) as a required check — catches a large class of bugs (like the order-ID issue in `audit_day_1.md` R-01) before merge, cheaply.
- Keep the existing Compose-based integration/smoke test (`smoke_test.sh`) but wire it into this same workflow as a distinct job, so a regression in end-to-end behavior is also a required check, not something only caught by manually running the script.
- This is the phase where your subordinate benefits immediately and directly: from here on, their PRs get real automated feedback instead of a rubber-stamp.

### Phase 4 — Build and publish real, versioned images

**Why before deploy:** you can't auto-deploy what doesn't exist as a shippable artifact yet.

- On merge to `main` (or on tag, if you prefer release-tag-driven deploys — pick one and be consistent), build each service's Docker image and push it to a registry. **GitHub Container Registry (GHCR)** is the simplest choice here since it needs no separate account/billing and integrates with repo permissions directly.
- Tag images meaningfully: at minimum the git SHA, ideally also `latest` for the most recent `main` build. This gives you a precise rollback target later (§Phase 5) — "redeploy the image tagged `abc1234`" is a real, safe rollback story.
- Add image vulnerability scanning here (Trivy or Grype, both have ready-made GitHub Actions) as a required step before a tag is considered deployable — catches known CVEs in your base images and dependencies automatically, continuously, instead of only when someone happens to run a scanner manually.

### Phase 5 — Pick a deployment target and wire CD

**This is the one real decision in this whole plan — everything else is sequencing, this is a choice.** Three realistic options for a project at this stage, roughly in order of "fastest to get working" to "most control":

| Option | What it is | Good fit if | Tradeoff |
|---|---|---|---|
| **Managed platform (e.g. Railway, Render, Fly.io)** | You point it at your repo/images, it runs your containers, handles networking/TLS/scaling knobs for you | You want real users served this month with minimal ops work | Less control over networking internals; some vendor lock-in; cost scales with usage |
| **A single VPS running Docker Compose** | GitHub Actions SSHes in, pulls new images, runs `docker compose up -d` | You want to keep exactly the Compose setup you already have, cheaply | You own patching/uptime/scaling yourself; still needs a reverse proxy + TLS (e.g. Caddy/Traefik) in front |
| **Kubernetes (managed, e.g. EKS/GKE/DOKS)** | Full orchestration: rolling deploys, self-healing, autoscaling, `NetworkPolicy` for the segmentation `network_issues.md` recommends | You expect real scale/multi-region/strict compliance needs | Real added complexity and cost; overkill for four services and modest traffic today |

**Recommendation:** start with the managed-platform or single-VPS option. Both get you "push to `main` → real users see it" fast, and Kubernetes' main advantages (fine-grained network policy, autoscaling, self-healing across nodes) matter once traffic or team size actually demands it — not before. You can migrate later; nothing in Phases 0–4 is Kubernetes-specific, so this choice doesn't lock in your earlier work.

Whichever you pick:
- Wire the actual deploy step into the workflow from Phase 4, gated on the `production` GitHub Environment from Phase 0 (so the deploy credentials are scoped and, if you want, require manual approval).
- Deploy should include a post-deploy health check (hit `/health` on the new version) before considering the deploy successful — don't just fire-and-forget the deploy command.
- Write down (in this repo, in a short `DEPLOY.md`) exactly how to roll back: which image tag was previously live, and the one command to redeploy it. You want this written *before* you need it at 2am, not after.

### Phase 6 — Make monitoring actually watch, not just collect

You already have Prometheus + Grafana wired up (`docker-compose.monitoring.yml`) — that's data collection, not monitoring. "Monitoring tools fully figured out" means someone (or something) actually looks at the data and tells you when it's bad:

- Import or build real Grafana dashboards for each service (request rate, error rate, p95/p99 latency, container memory/CPU) instead of the current empty Grafana instance with only default credentials.
- Add **Alertmanager** (or your platform's built-in alerting, if you went the managed-platform route in Phase 5) with a small number of alerts that actually matter early on: error rate spike, a service down/unhealthy, disk/memory pressure. Resist the urge to configure dozens of alerts on day one — a handful of high-signal alerts you'll actually act on beats fifty you'll learn to ignore.
- Route alerts somewhere you'll actually see them (Slack webhook, email, PagerDuty free tier) — an alert nobody sees is the same as no alert.
- Once deployed (Phase 5) to something other than your laptop, also add basic **log aggregation** (even something simple like shipping container logs to a hosted log service, or your platform's built-in log viewer if you went the managed route) — `docker compose logs` doesn't exist once this isn't running on your machine.
- This connects directly to `audit_day_1.md` R-09 (no structured logging/correlation IDs) — worth doing that cleanup around the same time you set up log aggregation, since structured logs are what make aggregated logs actually searchable.

### Phase 7 — Ongoing hygiene (set up once, then it runs itself)

- Turn on **Dependabot** (or Renovate) for Go modules and Docker base images — automatic PRs when a dependency has a security fix, instead of finding out from a scanner months later.
- Revisit the image scan (Phase 4) and secret scan (GitHub has a built-in secret-scanning feature — enable it on the repo) periodically; both should already be blocking merges/deploys automatically once configured, so "ongoing" mostly means not turning them off.
- Once a month or so, glance at the Grafana dashboards even when nothing's alerting — this is how you catch slow-burning problems (a memory leak, a slowly rising error rate) that don't cross an alert threshold quickly enough to page you.

---

## 5. Where this leaves your subordinate

After Phase 3, their workflow is simply: branch → code → push → PR → automated checks run → you (or another reviewer) approve → merge → it ships itself. They don't need to know Docker registry tags exist, don't need production credentials, and don't need to touch any file under `.github/workflows/`. If a check fails on their PR, the failure output should be enough to fix it without asking you what the pipeline does — that's the bar for "fully figured out."

If they're the one building out `notification-service`'s HTTP surface or the auth endpoints from `next_thing.md`, none of that work is blocked by any phase above — it can proceed in parallel starting now. The only file overlap to watch for is `docker-compose.yml` (if their work needs new environment variables or a new port) and each service's `Dockerfile` (if their work changes build steps) — small, quick syncs, not blockers.

---

## 6. Quick reference — phase order and rough effort

| Phase | What | Blocks | Rough effort |
|---|---|---|---|
| 0 | Branch protection, GitHub Environments | Everything else | < 1 hour |
| 1 | Secrets out of git, rotated | Phase 4/5 (don't ship secrets automatically) | ~1–2 hours |
| 2 | Non-root containers, pinned images, `.dockerignore`, healthchecks | Phase 4 (don't ship what you'd have to fix later) | ~2–3 hours |
| 3 | Real CI: vet/build/test/lint as required checks | Phase 4/5 (need a real gate before auto-deploy) | ~2–4 hours (grows as tests are added) |
| 4 | Build, tag, scan, push images to a registry | Phase 5 | ~2–3 hours |
| 5 | Pick a target, wire CD, health-gated deploy, rollback doc | Real users being served | ~1 hour – 1 day, depending on target chosen |
| 6 | Dashboards, alerts routed somewhere, log aggregation | Knowing when it breaks | ~1 day |
| 7 | Dependabot, secret scanning, periodic review | Nothing — ongoing | ~1 hour to set up, then passive |

Do them roughly in this order. Phases 0–2 are pure risk-reduction and can start today without waiting on any decision. Phase 5 is the one place you need to make a real choice (§Phase 5 table) before continuing — everything before and after it is sequencing, not decisions.
