# Event Ticketing & Discovery Platform

A backend-heavy, low-fee event ticketing platform (think "Eventbrite for student
clubs and small venues") written in **Go**. Organizers create events and ticket
tiers; buyers pay via Stripe, receive a QR code, and check in at the door.

The technical story here is the **backend**: payment idempotency, distributed
lock correctness, and concurrency safety under contention — each proven with
tests that deliberately try to break them.

> **Deployment honesty:** this project is deployed and run locally via **Docker
> Compose**. Kubernetes and Terraform manifests are included and validated
> (`terraform validate`, `kubectl apply --dry-run=client`) but **not applied**
> to any live cluster in this build.

## Status by phase

| Phase | Feature | State |
|------:|---------|-------|
| 1 | Skeleton, Postgres schema, REST event CRUD + JWT, Docker Compose, CI | ✅ |
| 2 | Stripe idempotent webhooks (Redis keys) | ✅ |
| 3 | Inventory concurrency (`SELECT FOR UPDATE`) | ✅ |
| 4 | gRPC + Protobuf analytics service | ✅ |
| 5 | QR generation + Redis `SETNX` exactly-once entry | ✅ |
| 6 | Kafka analytics + WebSocket live dashboard | ✅ |
| 7 | GraphQL discovery + PostGIS geo search | ✅ |
| 8 | IaC (Terraform/K8s, unapplied) + rate limiter | ✅ |
| 9 | Thin React dashboard (sacrificial) | ⏳ |

## Quick start

```bash
docker compose up -d --build      # boots postgres (PostGIS), redis, and the API
curl localhost:8080/healthz       # -> ok
```

Register an organizer, create an event, publish it, and list it:

```bash
BASE=http://localhost:8080
TOK=$(curl -s -X POST $BASE/auth/register -H 'Content-Type: application/json' \
  -d '{"email":"org@demo.com","password":"hunter2","role":"organizer"}' | jq -r .token)

EID=$(curl -s -X POST $BASE/events -H "Authorization: Bearer $TOK" \
  -d '{"title":"Demo Concert","category":"music","venue":"CRC"}' | jq -r .id)

curl -s -X POST $BASE/events/$EID/tiers -H "Authorization: Bearer $TOK" \
  -d '{"name":"GA","price_cents":2500,"capacity":100}'

curl -s -X POST $BASE/events/$EID/publish -H "Authorization: Bearer $TOK"
curl -s $BASE/events            # the published event appears
```

## Architecture (target)

```
                    ┌──────────────┐   gRPC (stream)   ┌────────────────┐
   REST (writes) ──▶│              │ ─────────────────▶│   analytics    │
 GraphQL (reads) ──▶│   core API   │                   │   service      │
   WebSocket   ◀────│   (Go/chi)   │◀── Kafka ─────────│ (aggregations) │
                    └──────┬───────┘  purchase-events  └────────────────┘
                           │
              ┌────────────┼─────────────┐
              ▼            ▼              ▼
         PostgreSQL      Redis          Stripe
        (+PostGIS)   (locks/cache/    (test mode)
                      idempotency)
```

## Internal gRPC contract (core ↔ analytics)

The core API pushes committed purchases to a standalone **analytics service**
over gRPC, using a client-streaming RPC (`RecordStream`) plus a unary read
(`GetStats`). Full contract in [`proto/analytics.proto`](proto/analytics.proto):

```protobuf
message PurchaseEvent {
  string purchase_id = 1;
  string event_id = 2;
  string tier_id = 3;
  string buyer_id = 4;
  int64 amount_cents = 5;
  int32 quantity = 6;
  google.protobuf.Timestamp occurred_at = 7;
}

service AnalyticsService {
  // client-streaming: push a live feed of purchases without a round trip each
  rpc RecordStream(stream PurchaseEvent) returns (RecordAck);
  rpc GetStats(StatsRequest) returns (EventStats);
}
```

Regenerate stubs with `make proto` (requires `protoc` + the Go plugins).

## Tech stack

- **Go 1.25**, one module, multiple internal packages
- **PostgreSQL 16 + PostGIS**, integer-cents money, enum states
- **Redis 7** for idempotency keys, `SETNX` locks, inventory cache, rate limiting
- **Kafka** (single broker, one topic `purchase-events`) for the analytics pipeline
- **gRPC + Protobuf** between the core API and the analytics service
- **GraphQL** (`gqlgen`) for discovery reads; **REST** (`chi`) for writes
- **Docker Compose** for local deployment; **Terraform + K8s** manifests (unapplied)

## Development

```bash
# Bring up just the backing services for tests:
docker run -d --name pg  -e POSTGRES_USER=postgres -e POSTGRES_PASSWORD=postgres \
  -e POSTGRES_DB=ticketing -p 5432:5432 postgis/postgis:16-3.4
docker run -d --name rd  -p 6379:6379 redis:7-alpine

export DATABASE_URL="postgres://postgres:postgres@localhost:5432/ticketing?sslmode=disable"
export REDIS_ADDR="localhost:6379"
go test ./... -v
```

Test evidence for each phase's **PROVE IT** step is committed under
[`docs/evidence/`](docs/evidence/).

## Infrastructure (written & validated, deployed via Compose)

Deployment for this build is **Docker Compose**. The cloud infrastructure is
authored as code and validated in CI, but **not applied** to any live cluster:

- [`infra/terraform/`](infra/terraform) — AWS infra (VPC, EKS + node group, RDS
  PostgreSQL, ElastiCache Redis, MSK Kafka, S3). `terraform validate` passes.
- [`infra/k8s/`](infra/k8s) — Deployment + Service + **HPA** per service, plus
  ConfigMap/Secret. Validated against real Kubernetes schemas with `kubeconform`.
- [`infra/helm/event-ticketing/`](infra/helm) — parameterized Helm chart; passes
  `helm lint` and renders to schema-valid manifests.

Run it all locally with `make infra-validate`.

## Interview talking points

- **Idempotent Stripe webhooks:** the payment-intent ID is the idempotency key,
  stored in Redis with a TTL, checked before any side effect. Defended in depth
  by a `SELECT ... FOR UPDATE` on the purchase row, so even a 100×-concurrent
  replay creates exactly one ticket.
- **No overselling:** `SELECT ... FOR UPDATE` on the tier row serializes
  concurrent buyers; the Redis inventory cache only ever reflects committed state.
- **Exactly-once entry:** QR validation uses Redis `SETNX` as an atomic
  distributed lock — the first scanner wins, later scans see the key and are
  rejected as "already scanned." Cheaper than a DB lock held open during a scan.
- **Why gRPC internally vs REST?** Typed Protobuf contracts catch schema drift at
  compile time; a client-streaming RPC pushes purchase events without polling.
- **Why GraphQL for discovery but REST for writes?** GraphQL collapses the
  multi-dimensional discovery filter (location + date + category + price) into a
  single round trip; REST's simplicity + idempotency suits purchases/creation.
- **Why Kafka for analytics vs direct DB writes?** It decouples analytics from the
  purchase critical path — checkout commits fast, the pipeline can lag without
  hurting UX; the WebSocket dashboard still updates within milliseconds.
- **Rate limiting:** a Redis sliding-window log (one atomic Lua script) keyed per
  IP and per user, plus a per-event purchase cap.

## Resume bullets (each backed by committed evidence in `docs/evidence/`)

- Built a full-stack event ticketing platform in Go with Stripe payment
  processing; idempotent webhook delivery via Redis keys yields **zero duplicate
  tickets across 100 concurrent payment events** (`phase2_test.txt`).
- Implemented distributed QR ticket validation using Redis `SETNX` atomic
  locking, guaranteeing **exactly-once entry across 50 concurrent door scanners**
  (`phase5_test.txt`).
- Designed real-time inventory management with PostgreSQL pessimistic locking
  (`SELECT FOR UPDATE`) preventing overselling under **100 concurrent buyers** for
  the same tier (`phase3_test.txt`).
- Built a Kafka-driven sales analytics pipeline with WebSocket fan-out delivering
  live dashboard updates **within ~5ms of purchase events** (`phase6_test.txt`).
- Implemented gRPC + Protobuf internal service communication between the core API
  and an analytics service — strongly-typed contracts with streaming.
- Added a GraphQL API layer via gqlgen for flexible event discovery —
  multi-dimensional filtering (location + date + category + price) in one round trip.
- Authored reproducible infrastructure-as-code (Terraform + Kubernetes manifests
  with HPA) for AWS; validated in CI, deployed locally via Docker Compose.
- Implemented location-based discovery via PostGIS spatial queries serving
  **~40ms p99 across 300 events** with trending-score ranking (`phase7_test.txt`).
