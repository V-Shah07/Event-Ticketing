# Event Ticketing and Discovery Platform

A low-fee event ticketing platform written in Go, aimed at the "Eventbrite for student clubs and small venues" end of the market. Organizers create events and ticket tiers, buyers pay through Stripe and receive a QR code, and staff check people in at the door.

The interesting part is the backend, not the UI. The problems worth solving here are payment idempotency, inventory correctness under concurrent purchases, and exactly-once entry, and each of those is backed by tests that deliberately try to break it.

## Architecture

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

Writes go through a REST API; discovery reads go through GraphQL. Committed purchases are pushed to a standalone analytics service over gRPC and also published to Kafka, which feeds a live organizer dashboard over WebSocket. Postgres (with PostGIS for geo search) is the source of truth, and Redis handles idempotency keys, locks, the inventory cache, and rate limiting.

## Key decisions

* **Money is integer cents, and states are enums.** No floats for currency, and event and ticket lifecycles are explicit states rather than loose booleans.
* **Stripe webhooks are idempotent by construction.** Webhook processing keys off a Redis idempotency key so a redelivered event does not double-charge or double-issue a ticket.
* **Inventory is protected under contention.** Ticket purchases take a `SELECT FOR UPDATE` row lock so two buyers racing for the last seat cannot both win, and there is a test that fires concurrent purchases at a nearly-sold-out tier to prove it.
* **Entry is exactly-once.** Check-in uses a Redis `SETNX` on the ticket so a QR code cannot be used twice, even if two scanners hit it at the same moment.
* **Reads and writes use the tool that fits.** REST over chi for writes, GraphQL over gqlgen for discovery reads, and a client-streaming gRPC RPC for pushing purchase events to analytics without a round trip per event.

## Internal gRPC contract (core to analytics)

The core API pushes committed purchases to the analytics service over gRPC, using a client-streaming RPC (`RecordStream`) plus a unary read (`GetStats`). The full contract is in [`proto/analytics.proto`](proto/analytics.proto):

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

Regenerate the stubs with `make proto` (requires `protoc` and the Go plugins).

## Tech stack

* Go 1.25, one module with several internal packages
* PostgreSQL 16 with PostGIS, integer-cents money, enum states
* Redis 7 for idempotency keys, `SETNX` locks, the inventory cache, and rate limiting
* Kafka (single broker, one `purchase-events` topic) for the analytics pipeline
* gRPC and Protobuf between the core API and the analytics service
* GraphQL (gqlgen) for discovery reads, REST (chi) for writes
* Docker Compose for local deployment

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

## Frontend

A minimal React, TypeScript, and Vite dashboard lives in [`frontend/`](frontend): an event list, a checkout flow (against Stripe test mode or the mock provider), and a live organizer dashboard over WebSocket. It is intentionally thin, since the backend is the focus.

```bash
cd frontend && npm install && npm run dev   # proxies /api and /ws to :8080
```

## Infrastructure

Deployment for this build is Docker Compose. The cloud infrastructure is written as code and validated in CI, but not applied to any live cluster:

* [`infra/terraform/`](infra/terraform), AWS infrastructure (VPC, EKS with a node group, RDS PostgreSQL, ElastiCache Redis, MSK Kafka, S3). `terraform validate` passes.
* [`infra/k8s/`](infra/k8s), a Deployment, Service, and HPA per service, plus ConfigMap and Secret, validated against real Kubernetes schemas with `kubeconform`.
* [`infra/helm/event-ticketing/`](infra/helm), a parameterized Helm chart that passes `helm lint` and renders to schema-valid manifests.

Run all of the validation locally with `make infra-validate`.
