# Architecture

## Components

```text
                         ┌──────────────────────────── trust boundary: internet ────────────────────────────┐
  employee / reviewer ── HTTPS + access token ──► Traefik ingress (k3s, TLS)   │  local: kubectl port-forward on 127.0.0.1
                         └──────────────────────┬──────────────────────────────┘
                                                │ /v1/claims…            │ /v1/audit…
                                                ▼                        ▼
 Cognito (AWS) / devidp (local) ◄─JWKS─ ┌──────────────┐          ┌──────────────┐ ─JWKS─► Cognito / devidp
                                        │   expenses   │          │    audit     │
                                        │ :8080 public │  HMAC    │ :8080 public │
                                        │ :9090 metrics│─signed──►│ :8081 intern.│
                                        │ :9091 admin  │  HTTP    │ :9090 metrics│
                                        │  (loopback)  │ (outbox) └──────┬───────┘
                                        └──────┬───────┘                 │
                                   expenses_app│ DML only        audit_app│ INSERT+SELECT only
                                               ▼                          ▼
                                        ┌───────────────────────────────────────────┐
                                        │ PostgreSQL 17, one database expense_platform │
                                        │  schema expenses: claims, idempotency_keys,   │
                                        │                   outbox                      │
                                        │  schema audit:    decisions (append-only)     │
                                        └───────────────────────────────────────────┘
```

| Component | Owns | Code |
|---|---|---|
| `expenses` | claims, decisions, idempotency keys, the outbox; delivery worker | `services/expenses` |
| `audit` | decision history | `services/audit` |
| `libs/platform` | token verification, HTTP plumbing, HMAC signing, migrations runner | shared Go module |
| `devidp` | local stand-in for Cognito (tokens without passwords) | `tools/devidp`, local overlay only |
| PostgreSQL | one instance, one schema and two roles per service | `deploy/k8s/base/postgres` |

Each service is its own Go module, binary, image, Deployment and set of
database roles. They share no tables and no code paths at run time; the only
coupling is the `ExpenseDecided` contract (`contracts/events`).

## The two paths into `audit`

**A reviewer reads history** (user request, public listener):

1. `GET /v1/audit/claims/{id}/decisions` with a Cognito access token arrives at
   Traefik (AWS) or at the loopback port-forward (local) and is routed by path
   to `audit:8080`.
2. `audit` verifies the token itself (signature with the issuer's JWKS, RS256
   only, issuer, expiry, `token_use=access`, `client_id`), then requires the
   `reviewers` group. `expenses` is not involved, so history access is
   authorized separately from claim access.
3. `audit_app` reads `audit.decisions`. It has no privilege on `expenses.*`.

**`expenses` delivers an event** (service-to-service, internal listener):

1. The decision transaction writes the claim update and an outbox row.
2. The worker leases the row and `POST`s the stored bytes to
   `audit-internal:8081/internal/v1/events`, signed with HMAC-SHA256 and
   carrying the original `X-Request-ID`.
3. NetworkPolicy admits only pods labelled `app.kubernetes.io/name=expenses`
   on 8081; `audit` verifies the signature (constant-time, ±5 min window,
   rotating key set) before parsing.
4. `audit_app` inserts with `ON CONFLICT (event_id) DO NOTHING`; a duplicate
   gets `200`, a conflicting duplicate `409`.

Port 8081 has no ingress route and no host port; port 8080 does not serve
`/internal/...`. Tests check both (`TestHistoryAccess`,
`TestInternalIngestNotOnPublicPort`, `tests/manifests`).

## Consistency model

| Operation | Guarantee | Mechanism |
|---|---|---|
| Submit | at most one claim per (employee, `Idempotency-Key`) | key row inserted first in the transaction; concurrent duplicates wait on its primary key |
| Decide | exactly one decision per claim | `SELECT … FOR UPDATE`, status check, `claims_decision_consistent` constraint, `outbox_one_event_per_claim` unique constraint |
| Decision ⇄ event | both or neither | same transaction |
| Event delivery | at least once | outbox row marked delivered only after a 2xx |
| History | exactly one entry per event | `audit.decisions` primary key on `event_id` |
| History freshness | eventual; seconds normally, catches up after an outage | worker retries with capped backoff |

### Idempotency key semantics

- Required on `POST /v1/claims`; 8-128 chars `[A-Za-z0-9._:-]`.
- Scope: the employee from the verified token. Alice and Bob can use the same
  key independently; Bob cannot replay Alice's key to read her claim.
- Same key, same parsed body (`amount_cents`, `description`): the original
  claim, `201` with `Idempotent-Replayed: true`.
- Same key, different body: `422 idempotency_key_reused`; nothing is stored.
- Retry while the first request is still running: it waits for the first
  transaction and then gets the replay.
- Retention: as long as the claim (rows are tiny; no expiry job in v1).
- Decisions are naturally idempotent: the same reviewer repeating the same
  decision gets `200` replayed; anything else after the first decision is `409`.

### Outbox delivery

| Situation | Worker behaviour |
|---|---|
| 2xx | mark delivered |
| timeout, connection error, 5xx, 408, 429 | retry: exponential backoff from 1 s, capped (30 s; 5 s locally), full jitter in [d/2, d] |
| other 4xx (bad signature, invalid event, conflict) | **park** immediately: retrying cannot help |
| 200 transient attempts | park (about 1.5 h at the cap) |
| worker crashes mid-delivery | the lease (30 s) expires; the event is leased again and redelivered with the same bytes |
| several replicas | `FOR UPDATE SKIP LOCKED` lease; each event is in flight at most once at a time |

Operators see the backlog in metrics (`outbox_events_pending`,
`outbox_events_parked`, `outbox_oldest_undelivered_age_seconds`), in alerts
(`OutboxDeliveryStalled`, `OutboxEventsParked`) and through the loopback-only
admin endpoint (`make outbox-status`, `scripts/outbox.sh requeue <id>`).
See [operations.md](operations.md).

## Database roles

| Role | Used by | Privileges |
|---|---|---|
| `postgres` | bootstrap job, backups | superuser; never used by a service |
| `expenses_migrator` | `expenses-migrate` job | owns schema `expenses`; DDL |
| `expenses_app` | `expenses` pods | `SELECT, INSERT` on its tables; `UPDATE` only on decision columns of `claims` and delivery columns of `outbox`; no `DELETE` |
| `audit_migrator` | `audit-migrate` job | owns schema `audit`; DDL |
| `audit_app` | `audit` pods | `SELECT, INSERT` on `audit.decisions`; triggers reject `UPDATE`/`DELETE`/`TRUNCATE` even for the owner |

Migrator credentials exist only in the migration Jobs' secrets.
`TestRuntimeRoleIsolation` (both services) and `TestDatabaseEnforcesRules`
prove the boundaries against a real PostgreSQL.

## Single points of failure (accepted)

The AWS stand is one EC2 instance running k3s, PostgreSQL and both services,
and the local stand is one Docker Desktop node. This is deliberate for cost.

| Fails | Effect | Recovery |
|---|---|---|
| `audit` | history unavailable; decisions continue, events queue in the outbox | automatic catch-up when audit returns |
| `expenses` | API unavailable | Kubernetes restarts the pod |
| PostgreSQL pod | both APIs fail readiness; no data loss (persistent volume) | pod restart; volume reattached |
| EC2 instance | full outage | replace the instance: the data volume (k3s state, database, certificates) is reattached and k3s resumes the same cluster (see [aws.md](aws.md#instance-replacement)) |
| data volume | data loss since the last snapshot/dump | restore a DLM snapshot or a `make backup` dump |
| Availability Zone / region | full outage | not covered: no multi-AZ, snapshots in the same region |

## What changes for production

Multi-AZ managed PostgreSQL (RDS) with point-in-time recovery; at least two
nodes per service across zones; a managed Kubernetes or ECS; workload
identity or mTLS instead of the shared HMAC key; External Secrets with
Secrets Manager; signed-image admission policy; central log storage with
retention locks; cross-account backups. See the residual risks in
[threat-model.md](threat-model.md#residual-risks).
