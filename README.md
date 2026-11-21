# expense-platform

An expense approval system built as a portfolio project for backend and
platform engineering: two Go services, PostgreSQL, Kubernetes and an AWS stand
described in Terraform.

An employee submits a claim ("Taxi, 12 EUR"), a reviewer approves or rejects
it, the employee sees the result, and the reviewer can open the history to see
who decided and when. The decision and its audit event are written in one
transaction (transactional outbox), so the history catches up even when the
audit service was down at the time of the decision.

## Architecture

```text
 employee / reviewer
        │ HTTPS + OIDC access token (Cognito in AWS, tools/devidp locally)
        ▼
 Traefik ingress (AWS)  ·  127.0.0.1 port-forward (local)
        │ /v1/claims…                          │ /v1/audit/…
        ▼                                      ▼
 ┌─────────────┐  ExpenseDecided, HMAC-signed  ┌─────────────┐
 │  expenses   │──── outbox worker, HTTP ─────►│    audit    │
 │ claims API  │      internal port 8081       │ history API │
 └──────┬──────┘                               └──────┬──────┘
        │ role expenses_app (DML only)                │ role audit_app (INSERT, SELECT)
        ▼                                             ▼
 ┌───────────────────────── PostgreSQL 17 ─────────────────────────┐
 │ schema expenses: claims, idempotency_keys, outbox                │
 │ schema audit:    decisions (append-only)                         │
 └──────────────────────────────────────────────────────────────────┘
```

- **expenses** owns claims, decisions and the outbox. Rules: positive amount
  in euro cents, employees see only their own claims, reviewers cannot decide
  their own claims, a claim is decided once. Submissions are idempotent per
  employee and `Idempotency-Key`.
- **audit** owns the decision history. Events carry a stable ID and a schema
  version; a redelivered event is stored once. History requires the
  `reviewers` group; ingestion requires a valid HMAC signature and is reachable
  only from expenses pods.
- One PostgreSQL instance, one schema and two roles per service (migrator for
  DDL, app for DML). Neither service can read the other's schema.

Details: [docs/architecture.md](docs/architecture.md) ·
threat model: [docs/threat-model.md](docs/threat-model.md) ·
contracts: [contracts/](contracts/).

## Quick start (local)

Requirements: macOS or Linux with **Docker Desktop and its Kubernetes
enabled**, Go 1.26+ (the `go` command downloads the pinned 1.27.4 toolchain itself),
kubectl, jq, curl, openssl, and istioctl at the pinned version (`make istioctl`
installs it into `.local/bin`, checksum-verified). Everything else (Terraform,
scanners) runs from pinned container images or pinned `go run` versions.

```bash
make istioctl           # pinned istioctl into .local/bin (once)
make test               # unit tests, all modules
make test-integration   # real PostgreSQL in a throwaway container
make local-up           # build images, deploy to namespace expense-platform-local
make demo               # the five-minute demo below
make e2e                # end-to-end suite through the local APIs
```

`make local-up` deploys into the Docker Desktop context only, into its own
namespace, generates secrets outside Git and keeps data across redeploys.
It also installs Istio cluster-wide; expenses and audit get sidecars, and
audit's event port requires mTLS from the expenses identity
([architecture](docs/architecture.md#service-mesh-on-the-local-stand)). The
APIs are on `127.0.0.1:30080` (expenses), `:30081` (audit), `:30082` (local
tokens), loopback only. `make help` lists every target; stopping, restarting
and deleting the stand are in [docs/operations.md](docs/operations.md).

## Five-minute demo

`make demo` runs this from the terminal with fictional users:

1. Alice submits "Taxi, 12 EUR" and gets the claim ID.
2. Bob, with a valid token and the ID, gets `404` and does not see the claim in his list.
3. Rita (reviewer) approves; the history shows exactly one decision.
4. `audit` is scaled to zero. Alice's next claim is still approved immediately;
   the event waits in the outbox (`attempts`, `last_error` visible).
5. `audit` comes back; the history catches up within seconds.
6. One request ID is followed through the logs of both services.

With `make local-up OBSERVABILITY=1`, `make obs-demo` shows the backlog in
Prometheus and the `OutboxDeliveryStalled` alert going pending → firing →
resolved.

## Repository layout

```text
expense-platform/
├── go.work                     # local workspace of all Go modules
├── libs/platform/              # shared: OIDC verification, HTTP plumbing, HMAC, migrations runner
├── services/
│   ├── expenses/               # go.mod, cmd/{expenses,migrate}, internal/, migrations/, Dockerfile
│   └── audit/                  # go.mod, cmd/{audit,migrate}, internal/, migrations/, Dockerfile
├── tools/devidp/               # local Cognito stand-in (local overlay only)
├── contracts/                  # OpenAPI for both services, ExpenseDecided JSON Schema and examples
├── deploy/k8s/
│   ├── base/                   # Postgres, setup jobs, services, default-deny NetworkPolicies
│   └── overlays/               # local, local-observability (Prometheus), aws (Traefik + Cognito)
├── infra/terraform/aws/        # VPC, EC2 + k3s cloud-init, EBS, IAM, Cognito, snapshots
├── tests/                      # e2e (through the APIs) and manifest policy tests
├── scripts/                    # deploy, secrets, port-forward, demo, backup, verification, AWS
├── docs/                       # architecture, threat model, operations, AWS, cost, learning path
└── .github/                    # CI, release (signed images), Dependabot, CODEOWNERS
```

## What is verified

| Property | How |
|---|---|
| Claim rules, authorization matrix, BOLA, mass assignment, input bounds | unit tests (`make test`) |
| Idempotency under concurrency, exactly one decision under 40 racing requests, DB constraints, role isolation, append-only history | integration tests on PostgreSQL (`make test-integration`) |
| Event contract compatibility (producer vs. schema, consumer vs. examples), redelivery after a lost response | unit and integration tests |
| Demo scenario, forged tokens, audit outage recovery, racing reviewers over HTTP | `make e2e` against the deployed stand |
| Pod Security `restricted` enforced; NetworkPolicies enforced, with positive controls | `make verify-cluster` |
| Backup restores into a clean database with the same claim and history | `make backup restore-check` |
| No secrets in Git, no reachable known vulnerabilities, no fixable HIGH/CRITICAL in images, clean SAST, valid manifests, Terraform and contracts | `make check`, `make image-scan` |

Results of the last run, and what can only be verified in AWS:
[docs/verification.md](docs/verification.md). Remaining risks before a real
production use: [docs/threat-model.md#residual-risks](docs/threat-model.md#residual-risks).
This is a learning project; nothing here claims to be certified or free of
vulnerabilities.

## AWS

A temporary single-node stand: one `t4g.small` with k3s, Cognito, Let's
Encrypt, no NAT gateway or load balancer, about $0.23 for an 8-hour demo day or
$21 per month left running ([docs/cost.md](docs/cost.md)). The stand is a
single point of failure by design. Plan, apply, verify and teardown:
[docs/aws.md](docs/aws.md).

## Learning path

[docs/learning-path.md](docs/learning-path.md): ten stages from Go and
PostgreSQL through transactions, the second service, delivery failure,
Kubernetes, security and observability to Terraform and publishing, each with
files to read, a command to run and a question to answer.
