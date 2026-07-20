# Event Ticketing & Discovery Platform — Build Spec

> **For the autonomous coding session.** This is the single source of truth for this repo. Build in the phase order given. Every phase ends with a **PROVE IT** step — do not move on until it passes. The resume bullets at the bottom are the contract: every one must be literally true and demonstrable by an artifact (test output, benchmark log, or committed code) when you finish.

---

## 0. What you're building & the one thing that matters

A backend-heavy event ticketing platform (a low-fee Eventbrite for student clubs / small venues) written in **Go**. Organizers create events and ticket tiers; buyers pay via Stripe, get a QR code, check in at the door.

**The technical story is the backend, not the product.** Interviewers will not care that it's a ticketing app. They will care about: payment idempotency, distributed lock correctness, and concurrency safety under contention. Those three things are the whole point. Spend your correctness budget there and **prove them with tests that deliberately try to break them**.

### Scope decisions already made (do not re-expand these)
- **Language:** Go. One module, multiple internal packages. Do not split into microservices repos.
- **Infra:** Docker Compose is the real deployment target. K8s + Terraform manifests are written but **not applied** (they exist to back the IaC bullet and to be talked about — they are not run in this sprint). See Phase 8.
- **CUT entirely:** live EKS deploy, Helm, HPA autoscaling in practice, AWS SNS push, Prometheus/Grafana, CloudFront, Mapbox polish. Frontend is a thin dashboard only, built last, and is the first thing sacrificed if time runs short.
- **Priority if time runs out:** provable backend bullets > everything else. Cut the frontend before you cut a test.

---

## 1. Tech stack (final, simplified)

| Layer | Choice | Notes |
|---|---|---|
| Language | Go 1.22+ | |
| DB | PostgreSQL 16 + PostGIS | PostGIS only needed in Phase 7 |
| Cache / locks | Redis 7 | idempotency keys, SETNX locks, inventory cache |
| Events | Kafka (single broker, Compose) | analytics pipeline only — keep it one topic |
| Internal RPC | gRPC + Protobuf | between core API and one analytics service |
| Read API | GraphQL via `gqlgen` | discovery + dashboard reads |
| Write API | REST (`chi` or `gin`) | purchases, event CRUD |
| Payments | Stripe Go SDK | test mode only |
| Email | AWS SES **or** a logging stub | a stub that logs the QR is fine; SES optional |
| Frontend | React + TypeScript + Vite | thin, last, sacrificial |
| Local infra | Docker Compose | the real deploy |
| IaC (unapplied) | Terraform + K8s manifests | written Phase 8, never applied |
| CI | GitHub Actions | build + test on push, from Phase 1 |

**Simplifications from the original design that are allowed and encouraged:**
- One analytics service over gRPC, not three. Notification logic can live in the core API and just publish to Kafka.
- Kafka has exactly one topic (`purchase-events`). Don't build a notification topic; a consumer can react to the same stream.
- QR "email" can be a logged link + stored PNG. Real SES is a nice-to-have, not a blocker.
- Trending score = simple recency-weighted purchase count. Don't overthink it.

---

## 2. Build order (phases)

Each phase: build → **PROVE IT** → commit. CI must stay green.

### Phase 1 — Skeleton + CI (target: first few hours)
- Go module, package layout: `cmd/api`, `internal/{event,payment,ticket,inventory,analytics,discovery}`, `proto/`, `migrations/`.
- Postgres schema + migrations: `users`, `events`, `ticket_tiers`, `tickets`, `purchases`. Money in integer cents. Enums for event state (`draft|published|ended`) and role (`organizer|buyer|admin`).
- REST event CRUD + JWT auth (organizer vs buyer vs admin).
- Docker Compose: `api`, `postgres`, `redis`. (Add kafka in Phase 5.)
- GitHub Actions: `go build`, `go vet`, `go test ./...` on push.
- Define Protobuf schemas now even though gRPC lands in Phase 4.

**PROVE IT:** `docker compose up` boots clean; can create an event and list it via REST with a valid JWT; CI green on a pushed commit.

### Phase 2 — Stripe idempotency ⭐ (this is a headline bullet)
- Stripe payment intent creation on checkout.
- Webhook handler. **Every** payment intent carries an idempotency key = Stripe payment intent ID, stored in Redis with a TTL.
- Handler checks the key **before** doing anything. If seen → return 200 success without side effects. If new → process, then record the key.
- Ticket creation + inventory decrement happen in **one** Postgres transaction.

**PROVE IT:** an automated test fires the **same webhook 100× concurrently** (goroutines) and asserts exactly one ticket is created and inventory decremented by exactly one. Commit the test output. This is the bullet.

### Phase 3 — Inventory concurrency ⭐ (headline bullet)
- Inventory decrement via `SELECT ... FOR UPDATE` on the tier row — concurrent buyers queue, not race.
- Redis caches the current count for fast reads; invalidate on every purchase.
- Overselling is impossible: last ticket + N concurrent buyers → exactly one wins.

**PROVE IT:** test spawns N=100 goroutines all buying the final ticket of a 1-capacity tier; assert exactly 1 success, 99 clean "sold out" failures, final inventory = 0, no negative inventory ever observed. Commit the output.

### Phase 4 — gRPC service layer ⭐
- Protobuf contracts for core↔analytics.
- Stand up one **analytics service** as a separate binary (`cmd/analytics`) in the same repo/module, talking gRPC + Protobuf.
- Core API calls it via gRPC. Use a streaming RPC for pushing purchase events so you can honestly say "bidirectional/server streaming."

**PROVE IT:** integration test starts both binaries, drives a purchase through core, asserts the analytics service received it over gRPC. Show the Protobuf `.proto` in the README.

### Phase 5 — QR generation + distributed validation ⭐ (headline bullet)
- On successful payment: generate a unique QR per ticket (encode a signed ticket token). Store PNG; "email" = log the link (SES optional).
- Validation endpoint uses **Redis SETNX** as a distributed lock: first scan sets the key atomically and admits; any later scan finds the key and returns "already scanned."

**PROVE IT:** test fires the **same QR at 2+ scanners concurrently**; assert exactly one "admitted," the rest "already scanned." Commit the output. This is the exactly-once-entry bullet.

### Phase 6 — Kafka analytics + WebSocket dashboard feed ⭐
- Add `kafka` to Compose (single broker, one topic `purchase-events`).
- Payment handler publishes a purchase event to Kafka after commit.
- Analytics service (or a dedicated consumer) aggregates running totals in Redis.
- WebSocket server pushes live updates to the organizer dashboard: revenue, sold vs remaining per tier, sales velocity.

**PROVE IT:** script drives 50 purchases; assert the WebSocket client receives matching live totals within a low latency bound (log the ms). Commit the log — that's the "within Xms" bullet.

### Phase 7 — GraphQL discovery + PostGIS ⭐
- Add PostGIS. Events carry lat/lng.
- `gqlgen` GraphQL server for reads: filter events by location + date + category + price **in one query**.
- PostGIS radius query for "events near me," ranked by distance + a simple trending score (recency-weighted purchase count cached in Redis).

**PROVE IT:** one GraphQL query filtering on all four dimensions returns correct results; a geo query returns events within radius ordered by distance. Log p99 over Z events for the bullet.

### Phase 8 — IaC manifests (written, NOT applied) + fraud limiter + README
- **Terraform** configs describing the AWS infra (EKS, RDS, ElastiCache, MSK, S3) — written, `terraform validate` passes, **never applied**.
- **K8s manifests**: Deployment + Service + HPA per service, plus a Helm chart skeleton. `kubectl apply --dry-run=client` passes. Never applied to a real cluster.
- Redis sliding-window rate limiter via a Lua script (per IP + per user); per-event purchase cap.
- README: architecture diagram, the interview talking points (below), and **explicit honesty**: "Deployed via Docker Compose; K8s/Terraform manifests included and validated but not applied in this build."

**PROVE IT:** `terraform validate` + `kubectl apply --dry-run=client` both pass in CI. Rate-limit test shows the (N+1)th request in a window is rejected.

### Phase 9 — Thin frontend (sacrificial)
- Minimal React + TS dashboard: event list, a checkout flow against Stripe test mode, and the live organizer dashboard over WebSocket.
- **If time is short, cut this entirely.** The backend bullets do not depend on it.

---

## 3. Non-negotiable correctness tests (the interview gold)

These are not optional. They ARE the resume bullets. Keep them in `internal/.../*_concurrency_test.go` and make CI run them.

1. **Idempotency:** same webhook ×100 concurrent → exactly 1 ticket.
2. **No oversell:** N buyers, 1 ticket → exactly 1 success, inventory never negative.
3. **Exactly-once entry:** same QR at K scanners → exactly 1 admit.
4. **Rate limit:** (limit+1) requests in window → rejection.

Each test must print a clear PASS line with the observed counts. Those printouts are your evidence.

---

## 4. Interview talking points (put in README)

- **Why gRPC internally vs REST?** Typed Protobuf contracts catch schema drift at compile time; streaming pushes events without polling; lower serialization cost than JSON.
- **Why GraphQL for discovery but REST for writes?** GraphQL suits multi-dimensional read filters in one round trip; REST's simplicity + idempotency suits purchases/creation.
- **Why Redis SETNX for QR validation vs a DB lock?** Redis ops are atomic and microsecond-latency; a Postgres row lock is slower and holds a DB connection open during the scan.
- **How do you handle Stripe webhook retries?** Idempotency key = payment intent ID in Redis w/ TTL; any retry returns success without reprocessing.
- **Why Kafka for analytics vs direct DB writes?** Decouples the purchase critical path from analytics; purchase completes fast, analytics can lag without hurting UX.
- **How do you prevent overselling?** `SELECT FOR UPDATE` row lock on tier inventory; concurrent txns queue; Redis cache reflects only committed inventory.

---

## 5. Resume bullets (the contract — every one must end up TRUE)

Fill X/Y/Z from your own test/benchmark logs. Do **not** invent numbers — read them off the committed outputs.

- Built full-stack event ticketing platform in Go with Stripe payment processing, handling idempotent webhook delivery via Redis idempotency keys — **zero duplicate tickets across X simulated concurrent payment events** (X = your Phase 2 test's concurrency count).
- Implemented distributed QR ticket validation using Redis SETNX atomic locking, guaranteeing **exactly-once entry across Y concurrent door scanners** (Y = Phase 5 scanner count).
- Designed real-time inventory management with PostgreSQL pessimistic locking (SELECT FOR UPDATE) preventing overselling under **Z concurrent buyers** for the same tier (Z = Phase 3 count).
- Built Kafka-driven sales analytics pipeline with WebSocket fan-out delivering live dashboard updates **within Xms of purchase events** (X = Phase 6 measured latency).
- Implemented gRPC + Protobuf internal service communication between core API and analytics service — strongly-typed contracts with streaming, replacing REST.
- Added GraphQL API layer via gqlgen for flexible event discovery — multi-dimensional filtering (location + date + category + price) in a single round trip.
- Authored reproducible infrastructure-as-code (Terraform + Kubernetes manifests with HPA) for the platform's AWS deployment; ran locally via Docker Compose. *(Honest phrasing: manifests written & validated, deploy is Compose.)*
- Implemented location-based discovery via PostGIS spatial queries serving **Yms p99 across Z events** with trending-score ranking (from Phase 7 log).

> **Bullet honesty rule:** the IaC bullet must not claim a live EKS deploy. "Authored / validated / designed" is true; "deployed to production Kubernetes" is not. Keep it defensible.
