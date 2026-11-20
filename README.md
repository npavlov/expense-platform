# expense-platform

A learning project: an expense approval service. An employee submits a claim ("12 EUR for a taxi"), a reviewer approves or rejects it, and the decision history is kept in a separate service.

## Architecture

```text
Employee or reviewer
        │
        │ HTTPS + user token
        ▼
    expenses ──────► PostgreSQL · schema expenses
                        claims, decisions, events to deliver
                                  │
                     delivery worker inside expenses
                                  │ HTTP over the Kubernetes internal network
                                  ▼
                      audit ───► PostgreSQL · schema audit
                                    decision history
```

- **expenses** — submit a claim, view your own claim, decide on a claim. The decision and the `ExpenseDecided` event are written in one transaction (transactional outbox); a background worker delivers the event to `audit`.
- **audit** — receives events and stores the decision history. A unique event ID protects against duplicate records on redelivery.

## Repository layout

```text
expense-platform/
├── go.work                    # joins Go modules for local development
├── services/
│   ├── expenses/              # go.mod, cmd/, internal/, migrations/, Dockerfile
│   └── audit/                 # (step 2)
├── contracts/                 # HTTP API and ExpenseDecided event format (step 2)
├── deploy/k8s/                # base/ plus overlays/local and overlays/aws
├── infra/terraform/aws/       # cloud environment and cloud-init for k3s (step 4)
├── tests/e2e/                 # end-to-end tests in Go
└── .github/workflows/         # pull request checks
```

Each service is a separate Go module, binary and container image.

## Quick start

Requires Go 1.26+.

```bash
make test           # run tests for all services
make run-expenses   # start expenses on :8080 (override with HTTP_ADDR)

curl -s localhost:8080/health
# {"status":"ok"}
```

## Roadmap

1. **First working result:** `expenses` stores a claim in PostgreSQL and returns it. First migration and integration test.
   - [x] `services/expenses` Go module, `GET /health`, `httptest` tests, CI
   - [ ] PostgreSQL, first migration, `POST /expenses` and `GET /expenses/{id}`
2. **Second service:** reviewer decisions, `outbox`, `audit`. Redelivery and recovery after an outage.
3. **Security and observability:** user sign-in (Cognito), authorization checks, metrics (including the age of the oldest undelivered event), tracing a decision through logs.
4. **Publishing:** cost estimate, AWS deployment with Terraform (k3s on EC2), demo, database restore from backup, `terraform destroy`.
