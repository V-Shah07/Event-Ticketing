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
| 7 | GraphQL discovery + PostGIS geo search | ⏳ |
| 8 | IaC (Terraform/K8s, unapplied) + rate limiter | ⏳ |
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

## Interview talking points

_(expanded as later phases land — see `docs/evidence/` for the proofs)_

- **Idempotent Stripe webhooks:** the payment-intent ID is the idempotency key,
  stored in Redis with a TTL. Any retry (or a 100×-concurrent replay) is checked
  before side effects, so it returns success without creating a second ticket.
- **No overselling:** `SELECT ... FOR UPDATE` on the tier row serializes
  concurrent buyers; the Redis inventory cache only ever reflects committed state.
- **Exactly-once entry:** QR validation uses Redis `SETNX` as an atomic
  distributed lock — the first scanner wins, later scans see the key and are
  rejected as "already scanned."
